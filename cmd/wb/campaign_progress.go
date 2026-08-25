package main

import (
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/sneat-dev/wb/internal/progress"
)

// campaignProgress adapts typed engine events to the shared one-line terminal
// renderer. Machine-readable command output remains exclusively on stdout.
type campaignProgress struct {
	live      *liveProgress
	operation string
	once      sync.Once
}

func newCampaignProgress(out io.Writer, enabled bool, operation string) *campaignProgress {
	return &campaignProgress{live: newLiveProgress(out, enabled), operation: operation}
}

func (p *campaignProgress) reporter() progress.Reporter {
	if p == nil || p.live == nil || !p.live.enabled {
		return nil
	}
	return p.report
}

func (p *campaignProgress) report(event progress.Event) {
	p.once.Do(func() { p.live.start(p.operation + ": starting") })
	parts := []string{p.operation}
	if event.Wave > 0 {
		parts = append(parts, fmt.Sprintf("wave %d", event.Wave))
	}
	if event.Layer > 0 {
		parts = append(parts, fmt.Sprintf("layer %d", event.Layer))
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
	} else if event.State != "" && event.State != progress.Running {
		parts = append(parts, string(event.State))
	}
	p.live.update(strings.Join(parts, ": "))
}

func (p *campaignProgress) finish(message string) {
	if p == nil || p.live == nil || !p.live.enabled {
		return
	}
	p.once.Do(func() { p.live.start(p.operation + ": starting") })
	p.live.finish(p.operation + ": " + message)
}
