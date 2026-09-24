//go:build !windows

package main

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// TestDaemonStateLockWaitsForContendedLockThenAcquires exercises the polling
// branch of daemonController.stateLock deterministically: a second
// controller contends for the real flock the first controller holds, and the
// fake sleep hook (rather than a wall-clock delay) is what releases it, so
// the retry loop always takes its contention path without depending on OS
// scheduling or real timing (sneat-dev/wb coverage-to-100: cmd/wb/daemon.go
// stateLock's contention check and its 20ms retry sleep).
func TestDaemonStateLockWaitsForContendedLockThenAcquires(t *testing.T) {
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

// TestDaemonStateLockReportsHolderAfterDeadline exercises the deadline
// branch of daemonController.stateLock deterministically: the fake lockNow
// clock jumps past daemonReadyTimeout on the first simulated sleep, so the
// loop reports the "another process held..." error on its very next check,
// without a real wall-clock wait.
func TestDaemonStateLockReportsHolderAfterDeadline(t *testing.T) {
	root, holder, _ := cwWtLockFixture(t)

	holderRelease, err := holder.stateLock()
	if err != nil {
		t.Fatalf("holder stateLock: %v", err)
	}
	t.Cleanup(holderRelease)

	start := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	current := start
	waiterDeps := daemonTestDependencies(t, root)
	waiterDeps.lockNow = func() time.Time { return current }
	waiterDeps.sleep = func(time.Duration) { current = current.Add(2 * daemonReadyTimeout) }
	waiter := newDaemonController(waiterDeps, root)

	if _, err := waiter.stateLock(); err == nil || !strings.Contains(err.Error(), "another process held the daemon state lock for") {
		t.Fatalf("stateLock past the deadline = %v", err)
	}
}

// TestDaemonStateLockDefaultClockIsMonotonic proves that the production
// default of deps.lockNow, unlike deps.now, keeps Go's monotonic clock
// reading. deps.now defaults to time.Now().UTC(), and UTC() strips the
// monotonic reading (see time.Time.UTC and time.Time.stripMono in GOROOT's
// time package): a stateLock deadline measured on that wall clock would move
// under an NTP step or a VM resume. deps.lockNow must default to plain
// time.Now so the 5s deadline is immune to wall-clock adjustments. A
// monotonic time.Time's String() includes an "m=" component; UTC() drops it.
func TestDaemonStateLockDefaultClockIsMonotonic(t *testing.T) {
	now := defaultDaemonDependencies().lockNow()
	if !strings.Contains(now.String(), "m=") {
		t.Fatalf("default lockNow() = %s, want a monotonic reading (\"m=...\")", now)
	}
}
