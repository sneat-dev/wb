package sessionmove

import (
	"os"
	"testing"
)

// TestOpenSecureDirectoryAtReportsMkdirFailure drives the create-time
// Mkdirat error branch that is not EEXIST: a parent directory with no write
// permission makes creating "child" fail with EACCES.
func TestOpenSecureDirectoryAtReportsMkdirFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission checks")
	}
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
