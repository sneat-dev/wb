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

	lock, err := acquireCloneLock(clonePath, time.Now, time.Sleep)
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

	lock, err := acquireCloneLock(clonePath, time.Now, time.Sleep)
	if err == nil {
		_ = lock.release()
		t.Fatal("acquireCloneLock succeeded with a directory as the lock file")
	}
	if !strings.Contains(err.Error(), "open wb-state clone lock") {
		t.Fatalf("error = %v, want the open-lock cause", err)
	}
}

// TestDQCovAcquireCloneLockTimesOutAfterDeadline proves a held lock makes a
// second acquirer retry until the configured deadline and then fail with
// the held-lock message instead of blocking forever. The second acquirer
// gets a fake clock (now/sleep both driven off a virtual clock that sleep
// advances instantly) so the deadline elapses at full test speed while
// still exercising the real flock retry loop against the first, real lock;
// only cloneLockTimeout is shrunk (30ms, in 20ms poll steps) to keep the
// expected sleep count small and exact.
func TestDQCovAcquireCloneLockTimesOutAfterDeadline(t *testing.T) {
	restore := cloneLockTimeout
	cloneLockTimeout = 60 * time.Millisecond
	t.Cleanup(func() { cloneLockTimeout = restore })

	clonePath := filepath.Join(t.TempDir(), "team", "wb-state")
	first, err := acquireCloneLock(clonePath, time.Now, time.Sleep)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := first.release(); err != nil {
			t.Errorf("release first lock: %v", err)
		}
	}()

	virtual := time.Now()
	var slept []time.Duration
	fakeNow := func() time.Time { return virtual }
	fakeSleep := func(d time.Duration) {
		slept = append(slept, d)
		virtual = virtual.Add(d)
	}

	second, err := acquireCloneLock(clonePath, fakeNow, fakeSleep)
	if err == nil {
		_ = second.release()
		t.Fatal("second acquireCloneLock succeeded while the first was held")
	}
	if !strings.Contains(err.Error(), "held the wb-state clone lock") {
		t.Fatalf("error = %v, want the held-lock message", err)
	}
	wantSleeps := int(cloneLockTimeout/(20*time.Millisecond)) + 1
	if len(slept) != wantSleeps {
		t.Fatalf("acquireCloneLock slept %d times, want exactly %d (cloneLockTimeout %s in 20ms steps)", len(slept), wantSleeps, cloneLockTimeout)
	}
	for _, d := range slept {
		if d != 20*time.Millisecond {
			t.Fatalf("acquireCloneLock slept %v, want every wait to be 20ms", slept)
		}
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
	lock, err := acquireCloneLock(clonePath, time.Now, time.Sleep)
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
