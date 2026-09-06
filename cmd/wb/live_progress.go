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
	out       io.Writer
	enabled   bool
	heartbeat time.Duration

	mu         sync.Mutex
	started    time.Time
	last       string
	lineWidth  int
	finished   bool
	done       chan struct{}
	stopped    chan struct{}
	startOnce  sync.Once
	stopOnce   sync.Once
	finishOnce sync.Once
}

const universalProgressHeartbeat = 10 * time.Second

func newLiveProgress(out io.Writer, enabled bool) *liveProgress {
	return newLiveProgressWithHeartbeat(out, enabled, universalProgressHeartbeat)
}

func newLiveProgressWithHeartbeat(out io.Writer, enabled bool, heartbeat time.Duration) *liveProgress {
	return &liveProgress{
		out: out, enabled: enabled, heartbeat: heartbeat,
		done: make(chan struct{}), stopped: make(chan struct{}),
	}
}

func (progress *liveProgress) start(message string) {
	if progress == nil || !progress.enabled {
		return
	}
	progress.startOnce.Do(func() {
		progress.mu.Lock()
		progress.started = time.Now()
		progress.last = message
		progress.renderLocked(message, false)
		progress.mu.Unlock()
		if progress.heartbeat > 0 {
			go progress.runHeartbeat()
		} else {
			close(progress.stopped)
		}
	})
}

func (progress *liveProgress) update(message string) {
	if progress == nil || !progress.enabled {
		return
	}
	progress.mu.Lock()
	defer progress.mu.Unlock()
	if progress.finished {
		return
	}
	progress.last = message
	progress.renderLocked(progress.withElapsed(message), false)
}

func (progress *liveProgress) finish(message string) {
	if progress == nil || !progress.enabled {
		return
	}
	progress.finishOnce.Do(func() {
		progress.start(message)
		progress.mu.Lock()
		progress.finished = true
		progress.mu.Unlock()
		progress.stopOnce.Do(func() { close(progress.done) })
		<-progress.stopped
		progress.mu.Lock()
		defer progress.mu.Unlock()
		progress.renderLocked(progress.withElapsed(message), true)
	})
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

func (progress *liveProgress) runHeartbeat() {
	defer close(progress.stopped)
	ticker := time.NewTicker(progress.heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			progress.mu.Lock()
			if !progress.finished && progress.last != "" {
				progress.renderLocked(progress.withElapsed(progress.last), false)
			}
			progress.mu.Unlock()
		case <-progress.done:
			return
		}
	}
}

// progressLineWriter turns replaceable carriage-return updates into
// newline-delimited stderr events for non-terminal agent tools.
type progressLineWriter struct {
	out     io.Writer
	mu      sync.Mutex
	started bool
}

func (writer *progressLineWriter) Write(payload []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	text := string(payload)
	if strings.HasPrefix(text, "\r") {
		text = strings.TrimPrefix(text, "\r")
		if writer.started {
			text = "\n" + text
		}
		writer.started = true
	}
	if text == "\n" {
		writer.started = false
	}
	if _, err := io.WriteString(writer.out, text); err != nil {
		return 0, err
	}
	return len(payload), nil
}

func progressOutput(out io.Writer, interactive bool) io.Writer {
	if interactive {
		return out
	}
	return &progressLineWriter{out: out}
}
