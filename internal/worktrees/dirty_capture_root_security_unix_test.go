//go:build !windows

package worktrees

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestDirtyCaptureRefusesRegularToFIFOAndSameSizeReplacement(t *testing.T) {
	t.Parallel()
	for _, replacement := range []string{"fifo", "different inode", "symlink"} {
		t.Run(replacement, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			path := filepath.Join(directory, "file")
			if err := os.WriteFile(path, []byte("old!"), 0o600); err != nil {
				t.Fatal(err)
			}
			initial, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			switch replacement {
			case "fifo":
				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "different inode":
				if err := os.WriteFile(path, []byte("new!"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.WriteFile(filepath.Join(directory, "target"), []byte("new!"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("target", path); err != nil {
					t.Fatal(err)
				}
			}
			root, err := os.OpenRoot(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			if _, err := readDirtyCaptureRegular(root, "file", initial, nil); err == nil || !strings.Contains(err.Error(), "changed before") {
				t.Fatalf("%s replacement accepted: %v", replacement, err)
			}
		})
	}
}
