//go:build !windows

package daemonruntime

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDaemonStateLockWaitsForContendedLockThenAcquires(t *testing.T) {
	root, holder, _ := cwWtLockFixture(t)

	holderRelease, err := holder.AcquireStateLock()
	if err != nil {
		t.Fatalf("holder stateLock: %v", err)
	}
	releasedHolder := false
	t.Cleanup(func() {
		if !releasedHolder {
			holderRelease()
		}
	})

	waiterDeps := daemonTestDependencies(t, root)
	var releaseOnce sync.Once
	waiterDeps.Sleep = func(time.Duration) {
		releaseOnce.Do(func() {
			releasedHolder = true
			holderRelease()
		})
	}
	waiter := NewController(waiterDeps, root)

	release, err := waiter.AcquireStateLock()
	if err != nil {
		t.Fatalf("waiter stateLock: %v", err)
	}
	release()
}

func TestDaemonStateLockReportsHolderAfterDeadline(t *testing.T) {
	root, holder, _ := cwWtLockFixture(t)

	holderRelease, err := holder.AcquireStateLock()
	if err != nil {
		t.Fatalf("holder stateLock: %v", err)
	}
	t.Cleanup(holderRelease)

	start := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	current := start
	waiterDeps := daemonTestDependencies(t, root)
	waiterDeps.LockNow = func() time.Time { return current }
	waiterDeps.Sleep = func(time.Duration) { current = current.Add(2 * daemonReadyTimeout) }
	waiter := NewController(waiterDeps, root)

	if _, err := waiter.AcquireStateLock(); err == nil || !strings.Contains(err.Error(), "another process held the daemon state lock for") {
		t.Fatalf("stateLock past the deadline = %v", err)
	}
}

func TestDaemonStateLockDefaultClockIsMonotonic(t *testing.T) {
	now := DefaultDependencies(func(message string) error { return fmt.Errorf("%s", message) }).LockNow()
	if !strings.Contains(now.String(), "m=") {
		t.Fatalf("default lockNow() = %s, want a monotonic reading (\"m=...\")", now)
	}
}
