package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// inventoryProgress renders the worktree inventory walk as it happens.
//
// Every write here discards its error deliberately. This is advisory output on
// stderr: a broken pipe or a full disk must never abort a fleet sweep, alter
// which worktrees it judges eligible, or replace the real outcome with a
// reporting failure. The sweep's result is what matters; the commentary is not.
//
// The walk is one long blocking call that fetches from origin once per
// candidate. Before this existed a fleet-wide cleanup ran for forty minutes
// with no output at all, and when it died it left no report — indistinguishable
// from a hang, and afterwards indistinguishable from never having run. Progress
// goes to stderr so --format json on stdout stays machine-readable.
type inventoryProgress struct {
	out io.Writer
	// enabled is per-candidate line mode: a terminal, or --verbose.
	enabled bool
	// heartbeat is the coalesced mode for a caller that is not a terminal:
	// one line per tick instead of two per candidate, so an agent's piped
	// stderr shows the sweep is alive without being flooded by it.
	heartbeat bool
	label     string
	interval  time.Duration
	ticker    func(time.Duration) (<-chan time.Time, func())
	now       func() time.Time

	mu        sync.Mutex
	started   time.Time
	open      bool // a start line is written and awaiting its duration
	openIndex int  // the candidate that open line names
	slowest   time.Duration
	slowAt    string
	count     int
	found     int
	inFlight  map[int]*inFlightCandidate
	phase     string
	beats     int
	stop      func()
	done      chan struct{}
}

type inFlightCandidate struct {
	task, path, step string
}

// heartbeatInterval is how often a non-terminal sweep proves it is alive.
const heartbeatInterval = 10 * time.Second

// newInventoryProgress renders per candidate when there is a human at the
// terminal, or when verbose forces it. Otherwise, unless --quiet, it writes a
// heartbeat every heartbeatInterval: on 2026-11-27 an agent's piped dry run
// printed nothing at all, and its tool's timeout killed a sweep that was
// working. --quiet wins over both: a caller who asked for the outcome alone
// gets it.
func newInventoryProgress(inv *invocation, out io.Writer, verbose bool) *inventoryProgress {
	return newInventoryProgressFor(inv, out, verbose, "wb clean")
}

// newInventoryProgressFor names the command in heartbeat lines.
func newInventoryProgressFor(inv *invocation, out io.Writer, verbose bool, label string) *inventoryProgress {
	perLine := !inv.quiet && (verbose || console.Interactive(os.Stderr, inv.nonInteractive))
	return newInventoryProgressWith(out, label, perLine, !inv.quiet && !perLine, heartbeatInterval, realTicker, time.Now)
}

func realTicker(interval time.Duration) (<-chan time.Time, func()) {
	ticker := time.NewTicker(interval)
	return ticker.C, ticker.Stop
}

// newInventoryProgressWith takes the clock and ticker explicitly, so the
// heartbeat's timing is a test input rather than a wait.
func newInventoryProgressWith(out io.Writer, label string, perLine, heartbeat bool, interval time.Duration,
	ticker func(time.Duration) (<-chan time.Time, func()), now func() time.Time) *inventoryProgress {
	return &inventoryProgress{
		out: out, enabled: perLine, heartbeat: heartbeat, label: label,
		interval: interval, ticker: ticker, now: now, started: now(),
		inFlight: map[int]*inFlightCandidate{},
	}
}

// startHeartbeat begins ticking on the first event, so a command that never
// inspects anything never starts a goroutine.
func (p *inventoryProgress) startHeartbeatLocked() {
	if p.stop != nil {
		return
	}
	ticks, stop := p.ticker(p.interval)
	p.done = make(chan struct{})
	p.stop = stop
	done := p.done
	go func() {
		for {
			select {
			case <-done:
				return
			case <-ticks:
				p.beat()
			}
		}
	}()
}

// beat writes one heartbeat line.
func (p *inventoryProgress) beat() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done == nil {
		return
	}
	p.beats++
	_, _ = fmt.Fprintln(p.out, p.statusLocked())
}

// statusLocked is the heartbeat text. The running count is called "found so
// far" and never a total: enumeration and inspection interleave, so the
// number grows while the walk runs.
func (p *inventoryProgress) statusLocked() string {
	var line strings.Builder
	fmt.Fprintf(&line, "%s: inspected %d of %d found so far, %s", p.label, p.count, p.found,
		p.now().Sub(p.started).Round(time.Second))
	if p.phase != "" {
		fmt.Fprintf(&line, "; %s", p.phase)
	}
	if len(p.inFlight) > 0 {
		indexes := make([]int, 0, len(p.inFlight))
		for index := range p.inFlight {
			indexes = append(indexes, index)
		}
		sort.Ints(indexes)
		line.WriteString("; in flight:")
		for i, index := range indexes {
			candidate := p.inFlight[index]
			if i > 0 {
				line.WriteString(",")
			}
			fmt.Fprintf(&line, " %s %s (%s)", candidate.task, shortPath(candidate.path), candidate.step)
		}
	}
	return line.String()
}

// detail is the worktrees.ListProgressDetail sink: found counts, steps and
// post-walk phases, which only the heartbeat shows.
func (p *inventoryProgress) detail(event worktrees.ListProgressDetail) {
	if p == nil || !p.heartbeat {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.startHeartbeatLocked()
	if event.Found > p.found {
		p.found = event.Found
	}
	if event.Step != "" {
		if candidate, ok := p.inFlight[event.Index]; ok {
			candidate.step = event.Step
		}
	}
	if event.Phase != "" {
		p.phase = fmt.Sprintf("%s %d/%d", event.Phase, event.PhaseDone, event.PhaseTotal)
	}
}

// report is the worktrees.ListProgress sink. It returns nil when disabled so
// the walk skips the bookkeeping entirely.
func (p *inventoryProgress) report(event worktrees.ListProgress) {
	if p == nil || (!p.enabled && !p.heartbeat) {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.heartbeat {
		p.startHeartbeatLocked()
		if !event.Done {
			p.inFlight[event.Index] = &inFlightCandidate{task: event.Task, path: event.Path, step: "starting"}
			return
		}
		delete(p.inFlight, event.Index)
		p.count++
		return
	}

	if !event.Done {
		// Concurrent workers interleave, so a start line can only be left open
		// when nothing else has written since. Otherwise announce the candidate
		// on its own line and let its Done event report the duration.
		if p.open {
			_, _ = fmt.Fprintln(p.out)
		}
		_, _ = fmt.Fprintf(p.out, "[%4d] %s %s", event.Index, event.Task, shortPath(event.Path))
		p.open = true
		p.openIndex = event.Index
		return
	}

	p.count++
	if p.open && p.openIndex == event.Index {
		_, _ = fmt.Fprintf(p.out, "  %s\n", event.Elapsed.Round(time.Millisecond))
		p.open = false
	} else {
		// The open line, if any, names another candidate: close it rather
		// than append this candidate's duration to a stranger's line.
		if p.open {
			_, _ = fmt.Fprintln(p.out)
			p.open = false
		}
		_, _ = fmt.Fprintf(p.out, "[%4d] %s %s  %s\n", event.Index, event.Task,
			shortPath(event.Path), event.Elapsed.Round(time.Millisecond))
	}
	if event.Elapsed > p.slowest {
		p.slowest = event.Elapsed
		p.slowAt = event.Task + " " + shortPath(event.Path)
	}
}

// finish closes the stream with the totals that answer "why did that take so
// long" — how many candidates, over what wall clock, and which single one cost
// the most.
func (p *inventoryProgress) finish() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.heartbeat {
		p.finishHeartbeatLocked()
		return
	}
	if !p.enabled {
		return
	}
	if p.open {
		_, _ = fmt.Fprintln(p.out)
		p.open = false
	}
	if p.count == 0 {
		return
	}
	_, _ = fmt.Fprintf(p.out, "inspected %d candidates in %s", p.count, time.Since(p.started).Round(time.Second))
	if p.slowest > 0 {
		_, _ = fmt.Fprintf(p.out, "; slowest %s (%s)", p.slowest.Round(time.Millisecond), p.slowAt)
	}
	_, _ = fmt.Fprintln(p.out)
}

// shortPath keeps the owner/repository tail of a worktree path rather than the
// long shared prefix every candidate has.
func shortPath(path string) string { return shared.ShortPath(path) }

// finishHeartbeatLocked stops the ticker and, when any heartbeat was written,
// closes the counts. A sweep that ended before its first tick stays silent.
// finish may be called twice (deferred and explicit); the second is a no-op.
func (p *inventoryProgress) finishHeartbeatLocked() {
	if p.done == nil {
		return
	}
	p.stop()
	close(p.done)
	p.done = nil
	if p.beats > 0 {
		p.inFlight = map[int]*inFlightCandidate{}
		p.phase = ""
		_, _ = fmt.Fprintf(p.out, "%s: done; inspected %d candidates in %s\n", p.label, p.count,
			p.now().Sub(p.started).Round(time.Second))
	}
}
