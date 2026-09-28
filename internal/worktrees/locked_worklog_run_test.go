package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

func TestLockedWorkLogRunReleasesFenceAndDirectory(t *testing.T) {
	t.Parallel()
	claimID := strings.Repeat("c", 64)
	locked, err := openLockedWorkLogRun(t.TempDir(), "effort", "run", claimID, true)
	if err != nil {
		t.Fatal(err)
	}
	lockFile, err := os.OpenFile(filepath.Join(locked.path, "locks", claimID+".lock"), os.O_RDWR, 0)
	if err != nil {
		locked.close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lockFile.Close() })
	locked.close()
	if _, err := locked.directory.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("locked run directory remains open: %v", err)
	}
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("claim fence remains locked after owner close: %v", err)
	}
	_ = unix.Flock(int(lockFile.Fd()), unix.LOCK_UN)
}

func TestOpenLockedWorkLogRunReturnsNoOwnerOnFailure(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	claimID := strings.Repeat("c", 64)
	locked, err := openLockedWorkLogRun(home, "effort", "missing", claimID, false)
	if locked != nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing run: owner=%v, error=%v", locked, err)
	}
	run, runPath, err := openWorkLogRun(home, "effort", "run", true)
	if err != nil {
		t.Fatal(err)
	}
	_ = run.Close()
	if err := os.WriteFile(filepath.Join(runPath, "locks"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked, err = openLockedWorkLogRun(home, "effort", "run", claimID, false)
	if locked != nil || err == nil || !strings.Contains(err.Error(), "open claim-lock directory") {
		t.Fatalf("obstructed claim lock: owner=%v, error=%v", locked, err)
	}
}

func TestLockedWorkLogRunClosesAfterUnlock(t *testing.T) {
	t.Parallel()
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	unlocked := false
	locked := &lockedWorkLogRun{directory: directory, unlock: func() {
		if _, err := directory.Stat(); err != nil {
			t.Errorf("directory closed before claim fence released: %v", err)
		}
		unlocked = true
	}}
	locked.close()
	if !unlocked {
		t.Error("claim fence was not released")
	}
	if _, err := directory.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Errorf("directory remains open after fence release: %v", err)
	}
}
