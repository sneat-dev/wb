//go:build !windows

package daemonruntime

// TestDaemonStateLockWaitsForContendedLockThenAcquires exercises the polling
// branch of daemonController.stateLock deterministically: a second
// controller contends for the real flock the first controller holds, and the
// fake sleep hook (rather than a wall-clock delay) is what releases it, so
// the retry loop always takes its contention path without depending on OS
// scheduling or real timing (sneat-dev/wb coverage-to-100: cmd/wb/daemon.go
// stateLock's contention check and its 20ms retry sleep).

// TestDaemonStateLockReportsHolderAfterDeadline exercises the deadline
// branch of daemonController.stateLock deterministically: the fake lockNow
// clock jumps past daemonReadyTimeout on the first simulated sleep, so the
// loop reports the "another process held..." error on its very next check,
// without a real wall-clock wait.

// TestDaemonStateLockDefaultClockIsMonotonic proves that the production
// default of deps.LockNow, unlike deps.Now, keeps Go's monotonic clock
// reading. deps.Now defaults to time.Now().UTC(), and UTC() strips the
// monotonic reading (see time.Time.UTC and time.Time.stripMono in GOROOT's
// time package): a stateLock deadline measured on that wall clock would move
// under an NTP step or a VM resume. deps.LockNow must default to plain
// time.Now so the 5s deadline is immune to wall-clock adjustments. A
// monotonic time.Time's String() includes an "m=" component; UTC() drops it.
