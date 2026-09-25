package landinglane

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

func TestWriteRecordInjectedHonoursAWriteFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "lane.json")
	inj := &filewrite.Injector{Step: filewrite.StepWrite, Err: errBoomPR8}
	if err := writeRecordInjected(path, Record{}, inj); !errors.Is(err, errBoomPR8) {
		t.Fatalf("writeRecordInjected(write failure) = %v, want errBoomPR8", err)
	}
	// The injected failure fires before os.WriteFile ever opens the temp
	// file, matching the original: a write failure leaves nothing behind.
	if _, statErr := os.Stat(path + ".tmp"); !os.IsNotExist(statErr) {
		t.Fatalf("write failure left a temp file: %v", statErr)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("failed write published a visible record: %v", statErr)
	}
}

func TestWriteRecordInjectedHonoursARenameFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "lane.json")
	inj := &filewrite.Injector{Step: filewrite.StepRename, Err: errBoomPR8}
	if err := writeRecordInjected(path, Record{}, inj); !errors.Is(err, errBoomPR8) {
		t.Fatalf("writeRecordInjected(rename failure) = %v, want errBoomPR8", err)
	}
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

func TestWriteRecordInjectedPublishesAt0600(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "lane.json")
	if err := writeRecordInjected(path, Record{}, nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("published record mode = %o, want 0600", perm)
	}
}
