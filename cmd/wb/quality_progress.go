package main

import (
	"fmt"
	"io"
	"sync"

	"github.com/sneat-dev/wb/internal/quality"
)

type qualityProgress struct {
	live      *liveProgress
	operation string
	total     int

	mu        sync.Mutex
	completed int
}

func newQualityProgress(out io.Writer, enabled bool, operation string, total int) *qualityProgress {
	return &qualityProgress{live: newLiveProgress(out, enabled), operation: operation, total: total}
}

func (progress *qualityProgress) start() {
	if progress == nil || progress.total == 0 {
		return
	}
	progress.live.start(fmt.Sprintf("%s: 0/%d repositories completed", progress.operation, progress.total))
}

func (progress *qualityProgress) report(event quality.Progress) {
	if progress == nil || progress.total == 0 {
		return
	}
	progress.mu.Lock()
	defer progress.mu.Unlock()
	if event.State == quality.ProgressRepositoryCompleted {
		progress.completed++
		progress.live.update(fmt.Sprintf(
			"%s: %d/%d repositories completed; %s: %s",
			progress.operation, progress.completed, progress.total, event.Repository, event.Status,
		))
		return
	}
	module := event.Module
	if module == "" {
		module = "."
	}
	state := string(event.State)
	if event.State == quality.ProgressCompleted {
		state = string(event.Status)
	}
	progress.live.update(fmt.Sprintf(
		"%s: %d/%d completed; %s %s — %s: %s",
		progress.operation, progress.completed, progress.total, event.Repository, module, event.Command, state,
	))
}

func (progress *qualityProgress) finish() {
	if progress == nil || progress.total == 0 {
		return
	}
	progress.mu.Lock()
	defer progress.mu.Unlock()
	progress.live.finish(fmt.Sprintf("%s: completed %d repositories", progress.operation, progress.completed))
}
