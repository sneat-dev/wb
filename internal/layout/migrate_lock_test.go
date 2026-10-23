package layout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// TestAcquireMigrationLockFailsWhenLockDirectoryIsBlocked covers the
// MkdirAll failure branch: something other than a directory already
// occupies the migration-lock directory's path.
func TestAcquireMigrationLockFailsWhenLockDirectoryIsBlocked(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	home, err := wbhome.EnsureRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(home, migrationsDirName)
	if err := os.WriteFile(blocked, []byte("occupied"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireMigrationLock(root); err == nil || !strings.Contains(err.Error(), "prepare migration lock directory") {
		t.Fatalf("acquireMigrationLock(blocked directory) error = %v, want a prepare-directory refusal", err)
	}
}

// TestAcquireMigrationLockFailsWhenLockFilePathIsADirectory covers the
// os.OpenFile failure branch: the lock file's own path is occupied by a
// directory instead of a file.
func TestAcquireMigrationLockFailsWhenLockFilePathIsADirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	home, err := wbhome.EnsureRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(home, migrationsDirName)
	if err := os.MkdirAll(filepath.Join(directory, migrationLockName), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireMigrationLock(root); err == nil || !strings.Contains(err.Error(), "open migration lock") {
		t.Fatalf("acquireMigrationLock(lock path is a directory) error = %v, want an open-migration-lock refusal", err)
	}
}

// TestMigrationLockReleaseIsSafeOnNilOrEmptyLock covers release's guard
// clause: releasing a nil *migrationLock, or one whose file was never set,
// must be a safe no-op rather than a nil-pointer dereference. A caller that
// held no lock (e.g. an earlier acquire failed) must still be able to defer
// release unconditionally.
func TestMigrationLockReleaseIsSafeOnNilOrEmptyLock(t *testing.T) {
	t.Parallel()
	var nilLock *migrationLock
	nilLock.release()

	empty := &migrationLock{}
	empty.release()
}
