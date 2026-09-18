package gitrepo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDQCovAcquireCloneLockReportsUncreatableLockDirectory proves a clone path
// whose parent cannot be created as a directory fails acquisition with the
// lock-directory error rather than panicking or silently proceeding unlocked.
func TestDQCovAcquireCloneLockReportsUncreatableLockDirectory(t *testing.T) {
	t.Parallel()
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	clonePath := filepath.Join(blocker, "wb-state")

	lock, err := acquireCloneLock(clonePath)
	if err == nil {
		_ = lock.release()
		t.Fatal("acquireCloneLock succeeded with an uncreatable lock directory")
	}
	if !strings.Contains(err.Error(), "create wb-state lock directory") {
		t.Fatalf("error = %v, want the lock-directory cause", err)
	}
}

// TestDQCovAcquireCloneLockReportsUnopenableLockFile proves a lock path that
// exists as a directory cannot be opened as the lock file, and the failure is
// reported with the open error.
func TestDQCovAcquireCloneLockReportsUnopenableLockFile(t *testing.T) {
	t.Parallel()
	clonePath := filepath.Join(t.TempDir(), "team", "wb-state")
	if err := os.MkdirAll(clonePath+cloneLockSuffix, 0o755); err != nil {
		t.Fatal(err)
	}

	lock, err := acquireCloneLock(clonePath)
	if err == nil {
		_ = lock.release()
		t.Fatal("acquireCloneLock succeeded with a directory as the lock file")
	}
	if !strings.Contains(err.Error(), "open wb-state clone lock") {
		t.Fatalf("error = %v, want the open-lock cause", err)
	}
}

// TestDQCovAcquireCloneLockTimesOutAfterDeadline proves a held lock makes a
// second acquirer wait for the configured deadline and then fail with the
// held-lock message instead of blocking forever.
func TestDQCovAcquireCloneLockTimesOutAfterDeadline(t *testing.T) {
	restore := cloneLockTimeout
	cloneLockTimeout = 30 * time.Millisecond
	t.Cleanup(func() { cloneLockTimeout = restore })

	clonePath := filepath.Join(t.TempDir(), "team", "wb-state")
	first, err := acquireCloneLock(clonePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := first.release(); err != nil {
			t.Errorf("release first lock: %v", err)
		}
	}()

	start := time.Now()
	second, err := acquireCloneLock(clonePath)
	if err == nil {
		_ = second.release()
		t.Fatal("second acquireCloneLock succeeded while the first was held")
	}
	if !strings.Contains(err.Error(), "held the wb-state clone lock") {
		t.Fatalf("error = %v, want the held-lock message", err)
	}
	if time.Since(start) < 30*time.Millisecond {
		t.Fatalf("second acquire returned after %s, want it to wait out the deadline", time.Since(start))
	}
}

// TestDQCovCloneLockReleaseNilAndEmptyIsNoop proves release is safe on a nil
// lock and on a lock whose file was never set, returning nil both times.
func TestDQCovCloneLockReleaseNilAndEmptyIsNoop(t *testing.T) {
	t.Parallel()
	var nilLock *cloneLock
	if err := nilLock.release(); err != nil {
		t.Fatalf("nil lock release = %v, want nil", err)
	}
	empty := &cloneLock{}
	if err := empty.release(); err != nil {
		t.Fatalf("empty lock release = %v, want nil", err)
	}
}

// TestDQCovCloneLockReleaseReportsUnlockFailure proves release surfaces a
// failing unlock: after the underlying descriptor is closed out from under it,
// Flock can no longer succeed and release returns that error.
func TestDQCovCloneLockReleaseReportsUnlockFailure(t *testing.T) {
	t.Parallel()
	clonePath := filepath.Join(t.TempDir(), "team", "wb-state")
	lock, err := acquireCloneLock(clonePath)
	if err != nil {
		t.Fatal(err)
	}
	if lock.file == nil {
		t.Fatal("acquired lock has no file descriptor")
	}
	if err := lock.file.Close(); err != nil {
		t.Fatal(err)
	}

	if err := lock.release(); err == nil {
		t.Fatal("release on a closed descriptor succeeded, want an unlock error")
	}
	if lock.file != nil {
		t.Fatal("release left the lock's file field set")
	}
}
