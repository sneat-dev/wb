package deps

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// errBoomPR8 is task-9 PR-8's sentinel injected failure, distinct from any
// other package's or task's sentinel so errors.Is never accidentally
// matches a different test's error by coincidence.
var errBoomPR8 = errors.New("pr8 boom")

func TestWriteAtomicInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{
		filewrite.StepOpenOrCreate, filewrite.StepWrite, filewrite.StepChmod,
		filewrite.StepClose, filewrite.StepRename,
	} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "file.txt")
			inj := &filewrite.Injector{Step: step, Err: errBoomPR8}
			if err := writeAtomicInjected(path, []byte("hi"), 0o644, inj); !errors.Is(err, errBoomPR8) {
				t.Fatalf("writeAtomicInjected(%s failure) = %v, want errBoomPR8", step, err)
			}
			matches, globErr := filepath.Glob(filepath.Join(dir, ".wb-deps-*"))
			if globErr != nil {
				t.Fatal(globErr)
			}
			if len(matches) != 0 {
				t.Fatalf("leftover temp file(s) after injected failure: %v", matches)
			}
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("failed write published a visible file: %v", statErr)
			}
		})
	}
}

// TestWriteAtomicInjectedPublishesAtGivenMode uses a mode (0644) that
// differs from os.CreateTemp's own 0600 default, so a deleted chmod call
// is directly, umask-independently detectable without needing a
// chmod-preset Hook.
func TestWriteAtomicInjectedPublishesAtGivenMode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := writeAtomicInjected(path, []byte("hi"), 0o644, nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Fatalf("published file mode = %o, want 0644", perm)
	}
}
