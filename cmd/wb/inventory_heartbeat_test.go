package main

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// signallingWriter records writes and announces each one, so a test can wait
// for the heartbeat goroutine to have written instead of sleeping.
type signallingWriter struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	writes chan struct{}
}

func newSignallingWriter() *signallingWriter {
	return &signallingWriter{writes: make(chan struct{}, 16)}
}

func (w *signallingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.buffer.Write(p)
	w.mu.Unlock()
	w.writes <- struct{}{}
	return n, err
}

func (w *signallingWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

func (w *signallingWriter) awaitWrite(t *testing.T) {
	t.Helper()
	select {
	case <-w.writes:
	case <-time.After(5 * time.Second):
		t.Fatal("heartbeat was not written")
	}
}

// fakeClock and fakeTicker make the heartbeat's interval a test input.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type fakeTicker struct {
	ticks    chan time.Time
	stopped  bool
	interval time.Duration
}

func (f *fakeTicker) start(interval time.Duration) (<-chan time.Time, func()) {
	f.interval = interval
	return f.ticks, func() { f.stopped = true }
}

func newHeartbeatFixture() (*inventoryProgress, *signallingWriter, *fakeClock, *fakeTicker) {
	out := newSignallingWriter()
	clock := &fakeClock{now: time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)}
	ticker := &fakeTicker{ticks: make(chan time.Time)}
	progress := newInventoryProgressWith(out, "wb clean", false, true, heartbeatInterval, ticker.start, clock.Now)
	return progress, out, clock, ticker
}

// A piped, non-verbose sweep must prove it is alive at every tick, naming what
// is in flight and which step it is in, and must never present the running
// count as a total.
func TestPipedSweepWritesAHeartbeatAtEveryTick(t *testing.T) {
	t.Parallel()
	progress, out, clock, ticker := newHeartbeatFixture()

	progress.detail(worktrees.ListProgressDetail{Found: 40})
	progress.report(worktrees.ListProgress{Index: 1, Task: "task-x", Path: "/p/sneat-co/sneat-go"})
	progress.detail(worktrees.ListProgressDetail{Found: 40, Index: 1, Task: "task-x", Path: "/p/sneat-co/sneat-go", Step: "pull requests"})
	progress.report(worktrees.ListProgress{Index: 2, Task: "task-y", Path: "/p/acme/app"})
	progress.report(worktrees.ListProgress{Index: 2, Task: "task-y", Path: "/p/acme/app", Done: true})
	progress.report(worktrees.ListProgress{Index: 3, Task: "task-z", Path: "/p/acme/z"})
	if ticker.interval != heartbeatInterval {
		t.Fatalf("ticker interval = %s, want %s", ticker.interval, heartbeatInterval)
	}

	clock.advance(70 * time.Second)
	ticker.ticks <- clock.Now()
	out.awaitWrite(t)
	line := out.String()
	for _, want := range []string{
		"wb clean: inspected 1, 40 found so far, 1m10s",
		"in flight: task-x sneat-co/sneat-go (pull requests), task-z acme/z (starting)",
		"(heartbeat every 10s)",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("heartbeat %q lacks %q", line, want)
		}
	}
	if strings.Contains(line, "total") {
		t.Fatalf("heartbeat calls a running count a total: %q", line)
	}

	progress.detail(worktrees.ListProgressDetail{Phase: "work log and size", PhaseDone: 3, PhaseTotal: 12})
	progress.report(worktrees.ListProgress{Index: 1, Task: "task-x", Path: "/p/sneat-co/sneat-go", Done: true})
	progress.report(worktrees.ListProgress{Index: 3, Task: "task-z", Path: "/p/acme/z", Done: true})
	ticker.ticks <- clock.Now()
	out.awaitWrite(t)
	if second := strings.SplitAfter(out.String(), "\n")[1]; strings.Contains(second, "heartbeat every") || !strings.Contains(second, "inspected 3, 40 found so far, 1m10s; work log and size 3/12\n") {
		t.Fatalf("second heartbeat = %q", out.String())
	}

	progress.finish()
	out.awaitWrite(t)
	if !ticker.stopped {
		t.Fatal("finish did not stop the ticker")
	}
	if !strings.HasSuffix(out.String(), "wb clean: done; inspected 3 candidates in 1m10s\n") {
		t.Fatalf("closing line missing: %q", out.String())
	}
	// finish runs both deferred and explicitly; the second call is inert.
	progress.finish()
}

// A sweep that ends before its first tick stays as quiet as before, and a
// heartbeat that races finish writes nothing after it.
func TestShortPipedSweepWritesNoHeartbeat(t *testing.T) {
	t.Parallel()
	progress, out, _, _ := newHeartbeatFixture()
	progress.report(worktrees.ListProgress{Index: 1, Task: "t", Path: "/p/acme/app"})
	progress.report(worktrees.ListProgress{Index: 1, Task: "t", Path: "/p/acme/app", Done: true})
	progress.finish()
	progress.beat()
	if got := out.String(); got != "" {
		t.Fatalf("short sweep wrote %q", got)
	}
}

// Detail events for an unknown candidate, a smaller found count, or a
// renderer that is not in heartbeat mode change nothing.
func TestHeartbeatIgnoresStaleDetail(t *testing.T) {
	t.Parallel()
	progress, _, _, _ := newHeartbeatFixture()
	progress.detail(worktrees.ListProgressDetail{Found: 5})
	progress.detail(worktrees.ListProgressDetail{Found: 3, Index: 9, Step: "git"})
	if progress.found != 5 || len(progress.inFlight) != 0 {
		t.Fatalf("found = %d, in flight = %v", progress.found, progress.inFlight)
	}
	progress.finish()

	var out bytes.Buffer
	perLine := newInventoryProgressWith(&out, "wb clean", true, false, heartbeatInterval, realTicker, time.Now)
	perLine.detail(worktrees.ListProgressDetail{Found: 5})
	var nilProgress *inventoryProgress
	nilProgress.detail(worktrees.ListProgressDetail{Found: 1})
	if perLine.found != 0 || out.Len() != 0 {
		t.Fatalf("per-line renderer consumed detail: found %d, wrote %q", perLine.found, out.String())
	}
}

// Under --verbose with concurrent workers, a finish must land on a line that
// names its own candidate. Before this, A start, B start, A done appended A's
// duration to B's line.
func TestVerboseDurationIsNeverPrintedOnAnotherCandidatesLine(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newInventoryProgressWith(&out, "wb clean", true, false, heartbeatInterval, realTicker, time.Now)
	progress.report(worktrees.ListProgress{Index: 1, Task: "alpha", Path: "/p/acme/a"})
	progress.report(worktrees.ListProgress{Index: 2, Task: "beta", Path: "/p/acme/b"})
	progress.report(worktrees.ListProgress{Index: 1, Task: "alpha", Path: "/p/acme/a", Done: true, Elapsed: 2 * time.Second})
	progress.report(worktrees.ListProgress{Index: 2, Task: "beta", Path: "/p/acme/b", Done: true, Elapsed: time.Second})
	progress.finish()
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	for _, line := range lines {
		if strings.Contains(line, "2s") && !strings.Contains(line, "alpha") {
			t.Fatalf("alpha's duration on another line: %q in\n%s", line, out.String())
		}
		if strings.Contains(line, "beta") && strings.Contains(line, "2s") {
			t.Fatalf("beta's line carries alpha's duration: %q", line)
		}
	}
	if !strings.Contains(out.String(), "[   1] alpha acme/a  2s\n") {
		t.Fatalf("alpha's finish line missing:\n%s", out.String())
	}
}

// The command label follows the verb, so wb worktree retire's heartbeat does
// not claim to be wb clean.
func TestHeartbeatLabelNamesTheCommand(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newInventoryProgressFor(&invocation{nonInteractive: true}, &out, false, "wb worktree retire")
	if progress.label != "wb worktree retire" || !progress.heartbeat || progress.enabled {
		t.Fatalf("label %q heartbeat %t per-line %t", progress.label, progress.heartbeat, progress.enabled)
	}
	quiet := newInventoryProgressFor(&invocation{quiet: true, nonInteractive: true}, &out, false, "wb clean")
	if quiet.heartbeat || quiet.enabled {
		t.Fatal("--quiet must disable the heartbeat")
	}
}

// The real ticker is a time.Ticker whose stop function stops it.
func TestRealTickerTicks(t *testing.T) {
	t.Parallel()
	ticks, stop := realTicker(time.Millisecond)
	defer stop()
	select {
	case <-ticks:
	case <-time.After(5 * time.Second):
		t.Fatal("real ticker never ticked")
	}
}
