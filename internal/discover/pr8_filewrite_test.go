package discover

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

func TestWriteLocalIndexInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{
		filewrite.StepOpenOrCreate, filewrite.StepChmod, filewrite.StepWrite,
		filewrite.StepClose, filewrite.StepRename,
	} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "index.json")
			inj := &filewrite.Injector{Step: step, Err: errBoomPR8}
			if err := writeLocalIndexInjected(path, persistedLocalIndex{}, inj); !errors.Is(err, errBoomPR8) {
				t.Fatalf("writeLocalIndexInjected(%s failure) = %v, want errBoomPR8", step, err)
			}
			matches, globErr := filepath.Glob(filepath.Join(dir, ".fleet-inventory-*"))
			if globErr != nil {
				t.Fatal(globErr)
			}
			if len(matches) != 0 {
				t.Fatalf("leftover temp file(s) after injected failure: %v", matches)
			}
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("failed write published a visible index: %v", statErr)
			}
		})
	}
}

// TestWriteLocalIndexInjectedPublishesAt0600 is review-763's
// chmod-preset-Hook lesson applied to this site: os.CreateTemp already
// creates its temp file at 0600, the same as this site's own final
// published mode, so a plain end-to-end 0600 assertion cannot tell a real
// chmod call from a deleted one.
func TestWriteLocalIndexInjectedPublishesAt0600(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	hookRan := false
	inj := &filewrite.Injector{Step: filewrite.StepChmod, Hook: func() {
		hookRan = true
		matches, err := filepath.Glob(filepath.Join(dir, ".fleet-inventory-*"))
		if err != nil || len(matches) != 1 {
			t.Fatalf("locate temporary file before chmod: matches=%v err=%v", matches, err)
		}
		if err := os.Chmod(matches[0], 0o644); err != nil {
			t.Fatal(err)
		}
	}}
	if err := writeLocalIndexInjected(path, persistedLocalIndex{}, inj); err != nil {
		t.Fatal(err)
	}
	if !hookRan {
		t.Fatal("Hook did not run before the real chmod")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("published index mode = %o, want 0600", perm)
	}
}
