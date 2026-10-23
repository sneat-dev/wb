package waitregistry

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

func TestRegisterInjectedHonoursAWriteFailure(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	inj := &filewrite.Injector{Step: filewrite.StepWrite, Err: errBoomPR8}
	if _, err := registerInjected(home, Record{ID: "wait-1"}, inj); !errors.Is(err, errBoomPR8) {
		t.Fatalf("registerInjected(write failure) = %v, want errBoomPR8", err)
	}
	path := filepath.Join(dir(home), "wait-1.json")
	if _, statErr := os.Stat(path + ".tmp"); !os.IsNotExist(statErr) {
		t.Fatalf("write failure left a temp file: %v", statErr)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("failed write published a visible record: %v", statErr)
	}
}

func TestRegisterInjectedHonoursARenameFailure(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	inj := &filewrite.Injector{Step: filewrite.StepRename, Err: errBoomPR8}
	if _, err := registerInjected(home, Record{ID: "wait-1"}, inj); !errors.Is(err, errBoomPR8) {
		t.Fatalf("registerInjected(rename failure) = %v, want errBoomPR8", err)
	}
	path := filepath.Join(dir(home), "wait-1.json")
	// The original never cleaned up the staged temp file on a rename
	// failure, so preserve that: the temp file remains, but nothing was
	// published at path.
	if _, statErr := os.Stat(path + ".tmp"); statErr != nil {
		t.Fatalf("rename failure unexpectedly removed the staged temp file: %v", statErr)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("failed rename published a visible record: %v", statErr)
	}
}

func TestRegisterInjectedPublishesAt0600(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	release, err := registerInjected(home, Record{ID: "wait-1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	path := filepath.Join(dir(home), "wait-1.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("published record mode = %o, want 0600", perm)
	}
}

func TestRegisterInjectedRejectsAnEmptyID(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if _, err := registerInjected(home, Record{}, nil); err == nil {
		t.Fatal("registerInjected(empty id) = nil, want an error")
	}
}
