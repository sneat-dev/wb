package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
	out     io.Writer
	enabled bool

	mu      sync.Mutex
	started time.Time
	open    bool // a start line is written and awaiting its duration
	slowest time.Duration
	slowAt  string
	count   int
}

// newInventoryProgress renders when there is a human at the terminal, or when
// verbose forces it. Forcing matters most for the unattended case: a scripted
// or backgrounded run is exactly where a silent forty-minute stall is invisible.
func newInventoryProgress(out io.Writer, verbose bool) *inventoryProgress {
	return &inventoryProgress{
		out:     out,
		enabled: verbose || console.Interactive(os.Stderr, nonInteractive),
		started: time.Now(),
	}
}

// report is the worktrees.ListProgress sink. It returns nil when disabled so
// the walk skips the bookkeeping entirely.
func (p *inventoryProgress) report(event worktrees.ListProgress) {
	if p == nil || !p.enabled {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if !event.Done {
		// Concurrent workers interleave, so a start line can only be left open
		// when nothing else has written since. Otherwise announce the candidate
		// on its own line and let its Done event report the duration.
		if p.open {
			_, _ = fmt.Fprintln(p.out)
		}
		_, _ = fmt.Fprintf(p.out, "[%4d] %s %s", event.Index, event.Task, shortPath(event.Path))
		p.open = true
		return
	}

	p.count++
	if p.open {
		_, _ = fmt.Fprintf(p.out, "  %s\n", event.Elapsed.Round(time.Millisecond))
		p.open = false
	} else {
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
	if p == nil || !p.enabled {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
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
func shortPath(path string) string {
	trimmed := strings.TrimRight(filepath.Clean(path), string(filepath.Separator))
	owner, repository := filepath.Split(trimmed)
	owner = strings.TrimRight(owner, string(filepath.Separator))
	if base := filepath.Base(owner); base != "" && base != "." && base != string(filepath.Separator) {
		return base + "/" + repository
	}
	return repository
}
