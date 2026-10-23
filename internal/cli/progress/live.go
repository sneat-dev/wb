package progress

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Live renders one replaceable terminal line. Commands keep their
// machine-readable report on stdout and send short-lived human progress here,
// on stderr. The mutex lets parallel repository workers report safely.
type Live struct {
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

const Heartbeat = 10 * time.Second

func NewLive(out io.Writer, enabled bool) *Live {
	return NewLiveWithHeartbeat(out, enabled, Heartbeat)
}

func NewLiveWithHeartbeat(out io.Writer, enabled bool, heartbeat time.Duration) *Live {
	return &Live{
		out: out, enabled: enabled, heartbeat: heartbeat,
		done: make(chan struct{}), stopped: make(chan struct{}),
	}
}

func (progress *Live) Start(message string) {
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

func (progress *Live) Update(message string) {
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

func (progress *Live) Finish(message string) {
	if progress == nil || !progress.enabled {
		return
	}
	progress.finishOnce.Do(func() {
		progress.Start(message)
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

func (progress *Live) withElapsed(message string) string {
	if progress.started.IsZero() {
		return message
	}
	return fmt.Sprintf("%s (%s)", message, time.Since(progress.started).Round(time.Millisecond))
}

func (progress *Live) renderLocked(message string, newline bool) {
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

// PrintLine writes one immediate, already-terminated line and returns
// without touching the replaceable-line/elapsed-suffix/heartbeat machinery
// the rest of this type provides. Use it for callers whose message already
// carries its own timing — `wb run`'s queued/admitted/done receipts compute
// their own elapsed durations, so wrapping them in withElapsed would print a
// redundant second duration.
func (progress *Live) PrintLine(message string) {
	if progress == nil || !progress.enabled {
		return
	}
	progress.mu.Lock()
	defer progress.mu.Unlock()
	_, _ = fmt.Fprintln(progress.out, message)
	progress.lineWidth = 0
}

func (progress *Live) runHeartbeat() {
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

// lineWriter turns replaceable carriage-return updates into
// newline-delimited stderr events for non-terminal agent tools.
type lineWriter struct {
	out     io.Writer
	mu      sync.Mutex
	started bool
}

func (writer *lineWriter) Write(payload []byte) (int, error) {
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

func Output(out io.Writer, interactive bool) io.Writer {
	if interactive {
		return out
	}
	return &lineWriter{out: out}
}

// Enabled reports whether this renderer emits progress.
func (p *Live) Enabled() bool { return p != nil && p.enabled }
