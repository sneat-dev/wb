//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package worktrees

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

//nolint:paralleltest // umask is process-wide and must be restored before parallel tests run.
func TestArchiveCopyRespectsUmaskForNewFilesAndPreservesExistingModes(t *testing.T) {
	root := t.TempDir()
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "new", "destination")
	if err := os.MkdirAll(filepath.Join(source, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	newSource := filepath.Join(source, "new-record")
	if err := os.WriteFile(newSource, []byte("new bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(newSource, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "existing-record"), []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(destination, "existing-record")
	if err := os.WriteFile(existing, []byte("old bytes"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(existing, 0o640); err != nil {
		t.Fatal(err)
	}
	oldUmask := syscall.Umask(0o077)
	defer syscall.Umask(oldUmask)
	if err := copyDir(source, destination); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		mode os.FileMode
		text string
	}{
		{filepath.Join(destination, "new-record"), 0o600, "new bytes"},
		{existing, 0o640, "replacement"},
	} {
		got, err := os.ReadFile(tc.path)
		if err != nil || string(got) != tc.text {
			t.Fatalf("%s contents = %q, %v", tc.path, got, err)
		}
		info, err := os.Stat(tc.path)
		if err != nil || info.Mode().Perm() != tc.mode {
			t.Fatalf("%s mode = %v, %v; want %04o", tc.path, info, err, tc.mode)
		}
	}
	if info, err := os.Stat(filepath.Join(destination, "empty")); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("new archive directory mode = %v, %v", info, err)
	}
}
