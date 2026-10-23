package runqueue

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// errBoomPR8 is task-9 PR-8's sentinel injected failure, distinct from any
// other package's or task's sentinel so errors.Is never accidentally matches
// a different test's error by coincidence.
var errBoomPR8 = errors.New("pr8 boom")

func TestAtomicWriteFileInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{
		filewrite.StepOpenOrCreate, filewrite.StepWrite, filewrite.StepClose,
		filewrite.StepChmod, filewrite.StepRename,
	} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "visibility.json")
			inj := &filewrite.Injector{Step: step, Err: errBoomPR8}
			if err := atomicWriteFileInjected(path, []byte("data"), 0o644, inj); !errors.Is(err, errBoomPR8) {
				t.Fatalf("atomicWriteFileInjected(%s failure) = %v, want errBoomPR8", step, err)
			}
			matches, globErr := filepath.Glob(filepath.Join(dir, ".tmp-*"))
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

func TestAtomicWriteFileInjectedPublishesAtRequestedMode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "visibility.json")
	if err := atomicWriteFileInjected(path, []byte("data"), 0o640, nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Fatalf("published file mode = %o, want 0640", perm)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "data" {
		t.Fatalf("published contents = %q", contents)
	}
}
