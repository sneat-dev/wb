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

	mu        sync.Mutex
	started   time.Time
	total     int
	completed int
	lineWidth int
}

func newStatusProgress(out io.Writer, enabled bool) *statusProgress {
	return &statusProgress{out: out, enabled: enabled}
}

func (progress *statusProgress) start(total int) {
	if progress == nil || !progress.enabled || total == 0 {
		return
	}
	progress.mu.Lock()
	defer progress.mu.Unlock()
	progress.started = time.Now()
	progress.total = total
	progress.renderLocked(fmt.Sprintf("status: 0/%d repositories inspected", total), false)
}

func (progress *statusProgress) complete(target qualityTarget, report repositoryStatusInfo) {
	if progress == nil || !progress.enabled || progress.total == 0 {
		return
	}
	progress.mu.Lock()
	defer progress.mu.Unlock()
	progress.completed++
	progress.renderLocked(fmt.Sprintf(
		"status: %d/%d repositories inspected; %s: %s (%s)",
		progress.completed,
		progress.total,
		target.repository,
		report.Status,
		time.Since(progress.started).Round(time.Millisecond),
	), false)
}

func (progress *statusProgress) finish() {
	if progress == nil || !progress.enabled || progress.total == 0 {
		return
	}
	progress.mu.Lock()
	defer progress.mu.Unlock()
	progress.renderLocked(fmt.Sprintf(
		"status: inspected %d repositories in %s",
		progress.completed,
		time.Since(progress.started).Round(time.Millisecond),
	), true)
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
