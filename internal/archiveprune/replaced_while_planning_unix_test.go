//go:build !windows

package archiveprune

// openDirectoryAt and hashFileAt's replaced-while-planning guards: both
// re-stat the entry after opening it and refuse when the identity no longer
// matches what planning observed. golang.org/x/sys/unix has no Windows
// implementation, so this coverage stays unix-only rather than routing
// through internal/unixcompat, which the production code under test already
// does cross-platform.

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOpenDirectoryAtRejectsInodeMismatch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sub := filepath.Join(root, "child")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	parent, err := os.Open(root)
	if err != nil {
		t.Fatalf("open parent: %v", err)
	}
	t.Cleanup(func() { _ = parent.Close() })

	var real unix.Stat_t
	if err := unix.Stat(sub, &real); err != nil {
		t.Fatalf("stat: %v", err)
	}
	mismatched := real
	mismatched.Ino = real.Ino + 1

	if _, err := openDirectoryAt(parent, sub, "child", mismatched); err == nil {
		t.Fatalf("expected mismatch error when the inode changed")
	}
}

func TestHashFileAtRejectsSizeMismatch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	filePath := filepath.Join(root, "file.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	parent, err := os.Open(root)
	if err != nil {
		t.Fatalf("open parent: %v", err)
	}
	t.Cleanup(func() { _ = parent.Close() })

	var real unix.Stat_t
	if err := unix.Stat(filePath, &real); err != nil {
		t.Fatalf("stat: %v", err)
	}
	mismatched := real
	mismatched.Size = real.Size + 1

	if _, err := hashFileAt(parent, "file.txt", "file.txt", mismatched); err == nil {
		t.Fatalf("expected size-mismatch error")
	}
}
