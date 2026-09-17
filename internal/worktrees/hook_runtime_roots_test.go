package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// TestSecureHookCapabilityRootsAuthorizeTheCallersHookRuntime pins the
// invariant the Linux filesystem capability depends on: for an explicit
// projects root, the capability roots computed for a secure Git helper contain
// the hook runtime root that a hook process resolving the same projects root
// writes to, and do not silently authorize the ambient default root instead.
//
// Before the projects root was threaded through, the helper passed "" and
// resolved $HOME/projects on a machine with no WB_PROJECTS_ROOT: macOS does not
// enforce the capability and stayed green, while Linux denied the hook's write
// to the real root's hook-runtime directory.
func TestSecureHookCapabilityRootsAuthorizeTheCallersHookRuntime(t *testing.T) {
	ambientRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(ambientRoot, "home"))
	t.Setenv(wbhome.EnvOverride, filepath.Join(ambientRoot, "ambient-projects"))

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repoPath := filepath.Join(root, "acme", "app")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}

	roots, handles, err := appendSecureHookExecutionCapabilityRoots(repoPath, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSecureHookRootHandles(handles)

	want, err := hooks.ResolveExecutionLayout(repoPath, root)
	if err != nil {
		t.Fatal(err)
	}
	if !capabilityRootsContain(roots, want.Root) {
		t.Fatalf("capability roots %v do not authorize the caller's hook runtime root %q", capabilityRootPaths(roots), want.Root)
	}

	ambient, err := hooks.ResolveExecutionLayout(repoPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(ambient.Root) == filepath.Clean(want.Root) {
		t.Fatalf("fixture is degenerate: ambient and explicit hook runtime roots coincide at %q", want.Root)
	}
	if capabilityRootsContain(roots, ambient.Root) {
		t.Fatalf("capability roots %v authorized the ambient default hook runtime root %q", capabilityRootPaths(roots), ambient.Root)
	}
}

// TestSecureHelperEnvironmentCarriesTheProjectsRoot pins the parent-to-child
// half: the helper environment exports the context's projects root, replaces an
// ambient value rather than duplicating it, and leaves the environment alone
// when no root was recorded.
func TestSecureHelperEnvironmentCarriesTheProjectsRoot(t *testing.T) {
	t.Setenv(wbhome.EnvOverride, "/ambient/projects")

	carried := secureHelperEnvironment(withProjectsRoot(context.Background(), "/real/projects"))
	value, count := environmentEntry(carried, wbhome.EnvOverride)
	if value != "/real/projects" || count != 1 {
		t.Fatalf("%s entries in helper environment = %q (appearing %d times), want exactly one /real/projects", wbhome.EnvOverride, value, count)
	}

	untouched := secureHelperEnvironment(context.Background())
	if value, _ := environmentEntry(untouched, wbhome.EnvOverride); value != "/ambient/projects" {
		t.Fatalf("helper environment without a recorded root = %q, want the ambient value preserved", value)
	}
}

func capabilityRootsContain(roots []gitFilesystemCapabilityRoot, path string) bool {
	path = filepath.Clean(path)
	for _, root := range roots {
		if filepath.Clean(root.path) == path {
			return true
		}
	}
	return false
}

func capabilityRootPaths(roots []gitFilesystemCapabilityRoot) []string {
	paths := make([]string, 0, len(roots))
	for _, root := range roots {
		paths = append(paths, root.path)
	}
	return paths
}

func environmentEntry(environment []string, name string) (value string, count int) {
	prefix := name + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			value = strings.TrimPrefix(entry, prefix)
			count++
		}
	}
	return value, count
}
