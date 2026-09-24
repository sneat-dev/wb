package sessionmove

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOpenSecureDirectoryAtReportsMkdirFailure drives the create-time
// Mkdirat error branch that is not EEXIST: a parent directory with no write
// permission makes creating "child" fail with EACCES.
func TestOpenSecureDirectoryAtReportsMkdirFailure(t *testing.T) {
	t.Parallel()
	parentPath := t.TempDir()
	if err := os.Chmod(parentPath, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parentPath, 0o700) })
	parent := openTestDirectory(t, parentPath)
	if _, err := openSecureDirectoryAt(parent, "child", true, "test"); err == nil {
		t.Fatal("openSecureDirectoryAt created a directory under a read-only parent, want error")
	}
}

// TestOpenSecureDirectoryAtRejectsWrongMode drives the mode-mismatch branch:
// the directory exists (create=false skips creation) but was not left at
// the required 0700.
func TestOpenSecureDirectoryAtRejectsWrongMode(t *testing.T) {
	t.Parallel()
	parentPath := t.TempDir()
	if err := os.Mkdir(filepath.Join(parentPath, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	parent := openTestDirectory(t, parentPath)
	if _, err := openSecureDirectoryAt(parent, "child", false, "test"); err == nil {
		t.Fatal("openSecureDirectoryAt accepted a 0755 directory, want a mode-mismatch error")
	}
}
