package repositoryevents

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

func TestCursorStoreSaveStateInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{
		filewrite.StepOpenOrCreate, filewrite.StepChmod, filewrite.StepWrite,
		filewrite.StepSync, filewrite.StepClose, filewrite.StepRename,
		filewrite.StepDirSync,
	} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "cursor.json")
			store := CursorStore{Path: path}
			inj := &filewrite.Injector{Step: step, Err: errBoomPR8}
			err := store.saveStateInjected(cursorState{Cursor: "cursor-1"}, inj)
			if !errors.Is(err, errBoomPR8) {
				t.Fatalf("saveStateInjected(%s failure) = %v, want errBoomPR8", step, err)
			}
			matches, globErr := filepath.Glob(filepath.Join(dir, ".cursor-*"))
			if globErr != nil {
				t.Fatal(globErr)
			}
			if step != filewrite.StepDirSync {
				if len(matches) != 0 {
					t.Fatalf("leftover temp file(s) after injected failure: %v", matches)
				}
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Fatalf("failed write published a visible cursor file: %v", statErr)
				}
				return
			}
			if _, statErr := os.Stat(path); statErr != nil {
				t.Fatalf("dir-sync failure unexpectedly lost the published cursor file: %v", statErr)
			}
		})
	}
}

// TestCursorStoreSaveStateInjectedPublishesAt0600 is review-763's
// chmod-preset-Hook lesson applied to this site: os.CreateTemp already
// creates its temp file at 0600, the same as this site's own final
// published mode, so a plain end-to-end 0600 assertion cannot tell a real
// chmod call from a deleted one.
func TestCursorStoreSaveStateInjectedPublishesAt0600(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "cursor.json")
	store := CursorStore{Path: path}
	hookRan := false
	inj := &filewrite.Injector{Step: filewrite.StepChmod, Hook: func() {
		hookRan = true
		matches, err := filepath.Glob(filepath.Join(dir, ".cursor-*"))
		if err != nil || len(matches) != 1 {
			t.Fatalf("locate temporary file before chmod: matches=%v err=%v", matches, err)
		}
		if err := os.Chmod(matches[0], 0o644); err != nil {
			t.Fatal(err)
		}
	}}
	if err := store.saveStateInjected(cursorState{Cursor: "cursor-1"}, inj); err != nil {
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
		t.Fatalf("published cursor file mode = %o, want 0600", perm)
	}
}

func TestCursorStoreSaveStateInjectedRejectsInvalidCursor(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "cursor.json")
	store := CursorStore{Path: path}
	if err := store.saveStateInjected(cursorState{Cursor: ""}, nil); err == nil {
		t.Fatal("saveStateInjected(empty cursor, no pending ack) = nil, want an error")
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".cursor-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("invalid cursor still staged a temp file: %v", matches)
	}
}
