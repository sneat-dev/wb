package runqueue

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTailCovAcquireWithNoUnitsTakesNothing pins the free fast path: zero
// units returns an empty lease without creating the queue directory at all,
// so a caller that classified a command as light never touches the projects
// root.
func TestTailCovAcquireWithNoUnitsTakesNothing(t *testing.T) {
	root := t.TempDir()
	lease, waited, err := Acquire(context.Background(), root, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if lease == nil {
		t.Fatal("Acquire returned a nil lease for zero units")
	}
	if waited != 0 {
		t.Fatalf("Acquire waited %s for zero units, want 0", waited)
	}
	if len(lease.files) != 0 {
		t.Fatalf("zero-unit lease holds %d slots, want 0", len(lease.files))
	}
	lease.Release() // must be safe on an empty lease
	if _, err := os.Stat(filepath.Join(root, ".wb")); !os.IsNotExist(err) {
		t.Fatalf("zero-unit Acquire created %s, want nothing written: err=%v", filepath.Join(root, ".wb"), err)
	}
}

// TestTailCovAcquireNormalisesBudgetAndUnits checks the two documented
// corrections: a non-positive budget still grants one slot, and a request
// larger than the budget is clamped to the budget rather than over-admitting.
func TestTailCovAcquireNormalisesBudgetAndUnits(t *testing.T) {
	t.Run("non-positive budget becomes one", func(t *testing.T) {
		root := t.TempDir()
		lease, _, err := Acquire(context.Background(), root, 1, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer lease.Release()
		if len(lease.files) != 1 {
			t.Fatalf("lease holds %d slots, want the single granted slot", len(lease.files))
		}
		if got := filepath.Base(lease.files[0].Name()); got != "slot-00.lock" {
			t.Fatalf("granted slot = %q, want slot-00.lock", got)
		}
	})

	t.Run("units larger than budget clamp", func(t *testing.T) {
		root := t.TempDir()
		lease, _, err := Acquire(context.Background(), root, 9, 2)
		if err != nil {
			t.Fatal(err)
		}
		defer lease.Release()
		if len(lease.files) != 2 {
			t.Fatalf("lease holds %d slots, want the budget of 2", len(lease.files))
		}
	})
}

// TestTailCovAcquireReportsQueueDirectoryFailure proves a projects root that
// cannot host the lease directory is a hard, wrapped error rather than a
// silent zero-unit admission.
func TestTailCovAcquireReportsQueueDirectoryFailure(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := Acquire(context.Background(), blocker, 1, 1)
	if err == nil || !strings.Contains(err.Error(), "create WB CPU lease directory") {
		t.Fatalf("Acquire error = %v, want a queue-directory creation failure", err)
	}
}

// TestTailCovAcquireReportsUnopenableSlotFile proves that a slot path that
// exists but cannot be opened (here, a directory standing in for the lock
// file) is reported with its own wrapper instead of looping until the context
// expires.
func TestTailCovAcquireReportsUnopenableSlotFile(t *testing.T) {
	root := t.TempDir()
	slotAsDirectory := filepath.Join(queueRoot(root), "slot-00.lock")
	if err := os.MkdirAll(slotAsDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	_, _, err := Acquire(context.Background(), root, 1, 1)
	if err == nil || !strings.Contains(err.Error(), "open WB CPU slot") {
		t.Fatalf("Acquire error = %v, want a slot-open failure", err)
	}
}

// TestTailCovAcquireRetriesUntilASlotFreesUp exercises the retry timer: the
// only way to succeed is to lose the first attempt, sleep, and try again
// after the holder releases.
func TestTailCovAcquireRetriesUntilASlotFreesUp(t *testing.T) {
	root := t.TempDir()
	held, _, err := Acquire(context.Background(), root, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		defer close(released)
		time.Sleep(3 * retryInterval)
		held.Release()
	}()

	lease, waited, err := Acquire(context.Background(), root, 1, 1)
	if err != nil {
		t.Fatalf("Acquire after the holder released = %v", err)
	}
	defer lease.Release()
	<-released
	if waited < retryInterval {
		t.Fatalf("Acquire waited %s, want at least one %s retry interval", waited, retryInterval)
	}
	if len(lease.files) != 1 {
		t.Fatalf("lease holds %d slots after the retry, want 1", len(lease.files))
	}
}
