package main

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// statusProgress keeps a fleet status scan visibly alive without contaminating
// its report. It renders one replaceable line on stderr; stdout therefore
// remains a complete Markdown, YAML, or JSON document.
type statusProgress struct {
	out     io.Writer
	enabled bool

	mu         sync.Mutex
	started    time.Time
	total      int
	completed  int
	last       string
	lineWidth  int
	heartbeat  time.Duration
	done       chan struct{}
	stopped    chan struct{}
	stopOnce   sync.Once
	finishOnce sync.Once
}

func newStatusProgress(out io.Writer, enabled bool) *statusProgress {
	return newStatusProgressWithHeartbeat(out, enabled, time.Second)
}

func newStatusProgressWithHeartbeat(out io.Writer, enabled bool, heartbeat time.Duration) *statusProgress {
	return &statusProgress{
		out: out, enabled: enabled, heartbeat: heartbeat,
		done: make(chan struct{}), stopped: make(chan struct{}),
	}
}

func (progress *statusProgress) start(total int) {
	if progress == nil || !progress.enabled || total == 0 {
		return
	}
	progress.mu.Lock()
	defer progress.mu.Unlock()
	progress.started = time.Now()
	progress.total = total
	progress.renderCurrentLocked()
	if progress.heartbeat > 0 {
		go progress.runHeartbeat()
	} else {
		close(progress.stopped)
	}
}

func (progress *statusProgress) complete(target qualityTarget, report repositoryStatusInfo) {
	if progress == nil || !progress.enabled || progress.total == 0 {
		return
	}
	progress.mu.Lock()
	defer progress.mu.Unlock()
	progress.completed++
	progress.last = fmt.Sprintf("; %s: %s", target.repository, report.Status)
	progress.renderCurrentLocked()
}

func (progress *statusProgress) finish() {
	if progress == nil || !progress.enabled || progress.total == 0 {
		return
	}
	progress.finishOnce.Do(func() {
		progress.stopOnce.Do(func() { close(progress.done) })
		<-progress.stopped
		progress.mu.Lock()
		defer progress.mu.Unlock()
		progress.renderLocked(fmt.Sprintf(
			"status: inspected %d repositories in %s",
			progress.completed,
			time.Since(progress.started).Round(time.Millisecond),
		), true)
	})
}

func (progress *statusProgress) renderCurrentLocked() {
	progress.renderLocked(fmt.Sprintf(
		"status: %d/%d repositories inspected%s (%s)",
		progress.completed,
		progress.total,
		progress.last,
		time.Since(progress.started).Round(time.Millisecond),
	), false)
}

func (progress *statusProgress) runHeartbeat() {
	defer close(progress.stopped)
	ticker := time.NewTicker(progress.heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			progress.mu.Lock()
			progress.renderCurrentLocked()
			progress.mu.Unlock()
		case <-progress.done:
			return
		}
	}
}

func (progress *statusProgress) renderLocked(message string, newline bool) {
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
