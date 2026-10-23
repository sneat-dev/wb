package progress

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	engineprogress "github.com/sneat-dev/wb/internal/progress"
)

// Campaign adapts typed engine events to the shared one-line terminal
// renderer. Machine-readable command output remains exclusively on stdout.
type Campaign struct {
	live      *Live
	operation string
	heartbeat time.Duration
	done      chan struct{}
	stopped   chan struct{}

	startOnce  sync.Once
	stopOnce   sync.Once
	finishOnce sync.Once
	mu         sync.RWMutex
	last       string
	finished   bool
}

func NewCampaign(out io.Writer, enabled bool, operation string) *Campaign {
	return NewCampaignWithHeartbeat(out, enabled, operation, time.Second)
}

func NewCampaignWithHeartbeat(out io.Writer, enabled bool, operation string, heartbeat time.Duration) *Campaign {
	return &Campaign{
		live: NewLiveWithHeartbeat(out, enabled, 0), operation: operation,
		heartbeat: heartbeat, done: make(chan struct{}), stopped: make(chan struct{}),
	}
}

func (p *Campaign) Reporter() engineprogress.Reporter {
	if p == nil || p.live == nil || !p.live.Enabled() {
		return nil
	}
	return p.Report
}

func (p *Campaign) Report(event engineprogress.Event) {
	p.ensureStarted()
	parts := []string{p.operation}
	if event.Wave > 0 {
		parts = append(parts, fmt.Sprintf("wave %d", event.Wave))
	}
	if event.Layer != nil {
		parts = append(parts, fmt.Sprintf("layer %d", *event.Layer))
	}
	if event.Phase != "" {
		parts = append(parts, strings.ReplaceAll(event.Phase, "_", " "))
	}
	if event.Repository != "" {
		parts = append(parts, event.Repository)
	}
	if event.Completed > 0 || event.Total > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d", event.Completed, event.Total))
	}
	if event.Detail != "" {
		parts = append(parts, event.Detail)
	}
	if event.State != "" && event.State != engineprogress.Running {
		parts = append(parts, string(event.State))
	}
	message := strings.Join(parts, ": ")
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finished {
		return
	}
	p.last = message
	if event.State == engineprogress.Completed {
		phase := strings.ReplaceAll(event.Phase, "_", " ")
		p.last = p.operation + ": alive; last completed " + phase
	}
	p.live.Update(message)
}

func (p *Campaign) Finish(message string) {
	if p == nil || p.live == nil || !p.live.Enabled() {
		return
	}
	p.finishOnce.Do(func() {
		p.ensureStarted()
		p.mu.Lock()
		p.finished = true
		p.mu.Unlock()
		p.stopOnce.Do(func() { close(p.done) })
		<-p.stopped
		p.mu.Lock()
		defer p.mu.Unlock()
		p.live.Finish(p.operation + ": " + message)
	})
}

func (p *Campaign) ensureStarted() {
	p.startOnce.Do(func() {
		message := p.operation + ": starting"
		p.mu.Lock()
		p.last = message
		p.mu.Unlock()
		p.live.Start(message)
		if p.heartbeat > 0 {
			go p.runHeartbeat()
		} else {
			close(p.stopped)
		}
	})
}

func (p *Campaign) runHeartbeat() {
	defer close(p.stopped)
	ticker := time.NewTicker(p.heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.refresh()
		case <-p.done:
			return
		}
	}
}

// refresh keeps a queued tick from rendering after Finish has begun joining.
func (p *Campaign) refresh() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finished {
		return
	}
	// ensureStarted assigns last before the heartbeat goroutine starts.
	p.live.Update(p.last)
}
