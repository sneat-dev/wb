//go:build !windows

package worktrees

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestWTCoreCovDirtyCaptureRejectsUnsupportedType asserts the per-path reader
// refuses a FIFO by name rather than capturing it as an ordinary file. Git's
// own untracked listing never reports a FIFO, so this defensive branch is
// reachable only through the reader that decides how a changed path is stored.
func TestWTCoreCovDirtyCaptureRejectsUnsupportedType(t *testing.T) {
	t.Parallel()
	repository := newJournalWorktree(t)
	fifo := filepath.Join(repository, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	root, err := os.OpenRoot(repository)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	if _, _, err := readDirtyCaptureEntry(root, "pipe", 0, nil); err == nil {
		t.Fatal("a FIFO dirty path was accepted")
	} else if !strings.Contains(err.Error(), "unsupported dirty path type") {
		t.Fatalf("error %q does not name the unsupported type", err)
	}
}
