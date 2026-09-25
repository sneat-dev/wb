//go:build !windows

package archiveprune

// Pack unit p02 coverage: openDirectoryAt and hashFileAt replaced-while-planning
// guards. Unit tier only: real temp-dir filesystem, no real git.
//
// golang.org/x/sys/unix (Stat/Stat_t below) does not build on Windows, and
// this file's premise -- proving a directory or file was swapped out from
// under a planned prune by comparing device+inode -- is itself POSIX-only,
// so it is excluded from the Windows build entirely rather than adapted.

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOpenDirectoryAtMismatch(t *testing.T) {
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

func TestHashFileAtMismatch(t *testing.T) {
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
