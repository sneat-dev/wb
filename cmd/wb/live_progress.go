package main

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// liveProgress renders one replaceable terminal line. Commands keep their
// machine-readable report on stdout and send short-lived human progress here,
// on stderr. The mutex lets parallel repository workers report safely.
type liveProgress struct {
	out     io.Writer
	enabled bool

	mu        sync.Mutex
	started   time.Time
	lineWidth int
}

func newLiveProgress(out io.Writer, enabled bool) *liveProgress {
	return &liveProgress{out: out, enabled: enabled}
}

func (progress *liveProgress) start(message string) {
	if progress == nil || !progress.enabled {
		return
	}
	progress.mu.Lock()
	defer progress.mu.Unlock()
	progress.started = time.Now()
	progress.renderLocked(message, false)
}

func (progress *liveProgress) update(message string) {
	if progress == nil || !progress.enabled {
		return
	}
	progress.mu.Lock()
	defer progress.mu.Unlock()
	progress.renderLocked(progress.withElapsed(message), false)
}

func (progress *liveProgress) finish(message string) {
	if progress == nil || !progress.enabled {
		return
	}
	progress.mu.Lock()
	defer progress.mu.Unlock()
	progress.renderLocked(progress.withElapsed(message), true)
}

func (progress *liveProgress) withElapsed(message string) string {
	if progress.started.IsZero() {
		return message
	}
	return fmt.Sprintf("%s (%s)", message, time.Since(progress.started).Round(time.Millisecond))
}

func (progress *liveProgress) renderLocked(message string, newline bool) {
	padding := progress.lineWidth - len(message)
	if padding < 0 {
		padding = 0
	}
	_, _ = fmt.Fprintf(progress.out, "\r%s%s", message, strings.Repeat(" ", padding))
	if newline {
		_, _ = fmt.Fprintln(progress.out)
	}
	progress.lineWidth = len(message)
}
