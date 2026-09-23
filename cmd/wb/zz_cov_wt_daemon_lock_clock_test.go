//go:build !windows

package main

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// TestCwWtDaemonStateLockContendedThenSucceeds exercises the polling branch
// of daemonController.stateLock deterministically: a second controller
// contends for the real flock the first controller holds, and the fake sleep
// hook (rather than a wall-clock delay) is what releases it, so the retry
// loop always takes its contention path without depending on OS scheduling
// or real timing (sneat-dev/wb coverage-to-100: cmd/wb/daemon.go:993,:997).
func TestCwWtDaemonStateLockContendedThenSucceeds(t *testing.T) {
	root, holder, _ := cwWtLockFixture(t)

	holderRelease, err := holder.stateLock()
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
	waiterDeps.sleep = func(time.Duration) {
		releaseOnce.Do(func() {
			releasedHolder = true
			holderRelease()
		})
	}
	waiter := newDaemonController(waiterDeps, root)

	release, err := waiter.stateLock()
	if err != nil {
		t.Fatalf("waiter stateLock: %v", err)
	}
	release()
}

// TestCwWtDaemonStateLockContendedUntilDeadline exercises the deadline branch
// of daemonController.stateLock deterministically: the fake clock jumps past
// daemonReadyTimeout on the first simulated sleep, so the loop reports the
// "another process held..." error on its very next check, without a real
// wall-clock wait (sneat-dev/wb coverage-to-100: cmd/wb/daemon.go:993).
func TestCwWtDaemonStateLockContendedUntilDeadline(t *testing.T) {
	root, holder, _ := cwWtLockFixture(t)

	holderRelease, err := holder.stateLock()
	if err != nil {
		t.Fatalf("holder stateLock: %v", err)
	}
	t.Cleanup(holderRelease)

	start := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	current := start
	waiterDeps := daemonTestDependencies(t, root)
	waiterDeps.now = func() time.Time { return current }
	waiterDeps.sleep = func(time.Duration) { current = current.Add(2 * daemonReadyTimeout) }
	waiter := newDaemonController(waiterDeps, root)

	if _, err := waiter.stateLock(); err == nil || !strings.Contains(err.Error(), "another process held the daemon state lock for") {
		t.Fatalf("stateLock past the deadline = %v", err)
	}
}
