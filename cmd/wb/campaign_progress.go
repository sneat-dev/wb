package main

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/progress"
)

// campaignProgress adapts typed engine events to the shared one-line terminal
// renderer. Machine-readable command output remains exclusively on stdout.
type campaignProgress struct {
	live      *liveProgress
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

func newCampaignProgress(out io.Writer, enabled bool, operation string) *campaignProgress {
	return newCampaignProgressWithHeartbeat(out, enabled, operation, time.Second)
}

func newCampaignProgressWithHeartbeat(out io.Writer, enabled bool, operation string, heartbeat time.Duration) *campaignProgress {
	return &campaignProgress{
		live: newLiveProgressWithHeartbeat(out, enabled, 0), operation: operation,
		heartbeat: heartbeat, done: make(chan struct{}), stopped: make(chan struct{}),
	}
}

func (p *campaignProgress) reporter() progress.Reporter {
	if p == nil || p.live == nil || !p.live.enabled {
		return nil
	}
	return p.report
}

func (p *campaignProgress) report(event progress.Event) {
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
	if event.State != "" && event.State != progress.Running {
		parts = append(parts, string(event.State))
	}
	message := strings.Join(parts, ": ")
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finished {
		return
	}
	p.last = message
	if event.State == progress.Completed {
		phase := strings.ReplaceAll(event.Phase, "_", " ")
		p.last = p.operation + ": alive; last completed " + phase
	}
	p.live.update(message)
}

func (p *campaignProgress) finish(message string) {
	if p == nil || p.live == nil || !p.live.enabled {
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
		p.live.finish(p.operation + ": " + message)
	})
}

func (p *campaignProgress) ensureStarted() {
	p.startOnce.Do(func() {
		message := p.operation + ": starting"
		p.mu.Lock()
		p.last = message
		p.mu.Unlock()
		p.live.start(message)
		if p.heartbeat > 0 {
			go p.runHeartbeat()
		} else {
			close(p.stopped)
		}
	})
}

func (p *campaignProgress) runHeartbeat() {
	defer close(p.stopped)
	ticker := time.NewTicker(p.heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.mu.Lock()
			if p.finished {
				p.mu.Unlock()
				continue
			}
			message := p.last
			if message != "" {
				p.live.update(message)
			}
			p.mu.Unlock()
		case <-p.done:
			return
		}
	}
}
