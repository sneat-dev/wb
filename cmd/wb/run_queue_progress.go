package main

import (
	"fmt"
	"io"
	"time"

	"github.com/sneat-dev/wb/internal/hostload"
	"github.com/sneat-dev/wb/internal/runqueue"
)

// runQueueProgress renders `wb run`'s CPU-lease queue visibility on stderr:
// where a command sits in WB's machine-wide CPU budget queue, a heartbeat at
// most every 10s while it waits, and a receipt line once it is admitted or
// done. stdout, and the JSON receipts other flags produce, stay untouched.
//
// Unlike liveProgress's other callers, these lines are not gated on a
// terminal: three agents ran `wb run -- go test` concurrently on 2026-09-07
// and each saw nothing on stderr for 10-25 minutes while queued behind the
// others and a saturated host, so each could not tell "queued" from "hung"
// (lesson l152-a; lesson
// long-running-wb-operations-must-report-progress-within-ten-seconds). An
// agent redirecting stderr to a log file needs these lines exactly as much
// as a human at a terminal does. --quiet is the only way to silence them.
type runQueueProgress struct {
	live           *liveProgress
	enabled        bool
	configPath     string
	heartbeatEvery time.Duration
}

func newRunQueueProgress(out io.Writer, enabled bool, configPath string) *runQueueProgress {
	return newRunQueueProgressWithHeartbeat(out, enabled, configPath, universalProgressHeartbeat)
}

func newRunQueueProgressWithHeartbeat(out io.Writer, enabled bool, configPath string, heartbeat time.Duration) *runQueueProgress {
	return &runQueueProgress{
		live:           newLiveProgress(out, enabled),
		enabled:        enabled,
		configPath:     configPath,
		heartbeatEvery: heartbeat,
	}
}

// admittedImmediately reports a command that never had to wait.
func (progress *runQueueProgress) admittedImmediately() {
	progress.live.printLine("wb run: admitted (queue empty)")
}

// queued reports a command that must wait for CPU capacity, at the moment it
// starts waiting.
func (progress *runQueueProgress) queued(summary string, state runqueue.State) {
	progress.live.printLine(fmt.Sprintf(
		"wb run: queued %s (position %d of %d, waiting on: %s)",
		summary, state.Position, state.Total, progress.waitingOn(state),
	))
}

// heartbeat reports a still-waiting command; call at most every 10s.
func (progress *runQueueProgress) heartbeat(waited time.Duration, state runqueue.State) {
	progress.live.printLine(fmt.Sprintf(
		"wb run: still queued %s (position %d of %d; running: %s)",
		waited.Round(time.Second), state.Position, state.Total, progress.runningOn(state),
	))
}

// admittedAfterWait reports a command admitted after it had to wait.
func (progress *runQueueProgress) admittedAfterWait(waited time.Duration) {
	progress.live.printLine(fmt.Sprintf("wb run: admitted after %s", waited.Round(time.Second)))
}

// done reports the wrapped command's terminal result.
func (progress *runQueueProgress) done(elapsed time.Duration, exitCode int) {
	progress.live.printLine(fmt.Sprintf("wb run: done in %s (exit %d)", elapsed.Round(time.Second), exitCode))
}

// waitingOn names what a freshly queued command is waiting on: the oldest
// announced slot holder, or — when no holder announced itself, which the
// budget-exhausted-but-anonymous case and the host-load-refusal case both
// look like from here — the host's current load average against its
// admission floor.
func (progress *runQueueProgress) waitingOn(state runqueue.State) string {
	if len(state.Holders) > 0 {
		holder := state.Holders[0]
		return fmt.Sprintf("%d %s", holder.PID, holder.Summary)
	}
	return progress.hostLoadHint()
}

// runningOn names what a still-queued command is waiting on, with the
// holder's running duration — the heartbeat's "running: ..." clause.
func (progress *runQueueProgress) runningOn(state runqueue.State) string {
	if len(state.Holders) > 0 {
		holder := state.Holders[0]
		return fmt.Sprintf("%d %s %s", holder.PID, holder.Summary, time.Since(holder.StartedAt).Round(time.Second))
	}
	return progress.hostLoadHint()
}

func (progress *runQueueProgress) hostLoadHint() string {
	floor, reason := hostload.Resolve(progress.configPath)
	if reason != "" || floor <= 0 {
		return "capacity contention"
	}
	load, err := hostload.System()
	if err != nil {
		return "capacity contention"
	}
	return fmt.Sprintf("host load %.1f>%.1f", load, floor)
}
