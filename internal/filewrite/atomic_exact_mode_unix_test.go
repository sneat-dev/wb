//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package filewrite

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

//nolint:paralleltest // umask is process-wide; restore it before parallel tests run.
func TestWriteBytesAtomicAtExactModeRetainsPermissionsUnderUmask(t *testing.T) {
	directory := openTestDir(t)
	target := filepath.Join(directory.Name(), "existing")
	if err := os.WriteFile(target, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	oldUmask := syscall.Umask(0o077)
	defer syscall.Umask(oldUmask)
	if err := WriteBytesAtomicAtExactMode(directory, "existing", []byte("new"), 0o640); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "new" {
		t.Fatalf("exact-mode target = %q, %v", got, err)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("exact-mode target permission = %v, %v", info, err)
	}
}
