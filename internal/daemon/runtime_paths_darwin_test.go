//go:build darwin

package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// Darwin can still name an unlinked current-directory vnode. Keep this
// platform's actual relative-root behavior covered without assuming Linux's
// getcwd(2) ENOENT result. t.Chdir changes process-wide state, so this test
// cannot run in parallel.
//
//nolint:paralleltest // t.Chdir and t.Setenv mutate process-wide state.
func TestRuntimePathHelpersResolveFromRemovedCurrentDirectoryOnDarwin(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(gone, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(gone)
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil || !filepath.IsAbs(cwd) {
		t.Fatalf("Darwin Getwd after removing cwd = %q, %v", cwd, err)
	}

	// A nonempty explicit root wins over WB_PROJECTS_ROOT, even after cwd
	// removal. The helpers must use that resolved home without creating it.
	override := t.TempDir()
	t.Setenv(wbhome.EnvOverride, override)
	const relativeRoot = "relative/projects/root"
	expectedHome := filepath.Join(cwd, relativeRoot, ".wb")
	home, err := wbhome.Root(relativeRoot)
	if err != nil || home != expectedHome {
		t.Fatalf("explicit relative root resolved home = %q, %v; want %q", home, err, expectedHome)
	}
	runtimeDir := filepath.Join(expectedHome, RuntimeDirName)
	for _, test := range []struct {
		name string
		path func(string) (string, error)
		want string
	}{
		{"state", StatePath, filepath.Join(runtimeDir, StateFileName)},
		{"operations", OperationsDir, filepath.Join(runtimeDir, "daemon", "operations")},
		{"socket", SocketPath, filepath.Join(runtimeDir, SocketFileName)},
	} {
		got, err := test.path(relativeRoot)
		if err != nil || got != test.want {
			t.Errorf("%s relative root from removed cwd = %q, %v; want %q", test.name, got, err, test.want)
		}
	}
}
