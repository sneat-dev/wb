package main

import (
	"fmt"
	"io"
	"sync"

	"github.com/sneat-dev/wb/internal/worktrees"
)

type remotePublishProgress struct {
	live *liveProgress

	mu                   sync.Mutex
	repositoriesTotal    int
	repositoriesComplete int
	worktreesComplete    int
}

func newRemotePublishProgress(out io.Writer, enabled bool) *remotePublishProgress {
	return &remotePublishProgress{live: newLiveProgress(out, enabled)}
}

func (progress *remotePublishProgress) start(total int) {
	if progress == nil {
		return
	}
	progress.mu.Lock()
	defer progress.mu.Unlock()
	progress.repositoriesTotal = total
	progress.live.start(fmt.Sprintf("remote publish: scanning 0/%d repositories", total))
}

func (progress *remotePublishProgress) repositoryComplete(repository string, err error) {
	if progress == nil {
		return
	}
	progress.mu.Lock()
	defer progress.mu.Unlock()
	progress.repositoriesComplete++
	status := "ok"
	if err != nil {
		status = "error"
	}
	progress.live.update(fmt.Sprintf(
		"remote publish: scanned %d/%d repositories; %s: %s",
		progress.repositoriesComplete, progress.repositoriesTotal, repository, status,
	))
}

func (progress *remotePublishProgress) phase(phase string) {
	if progress == nil {
		return
	}
	progress.live.update("remote publish: " + phase)
}

func (progress *remotePublishProgress) worktree(event worktrees.ListProgress) {
	if progress == nil {
		return
	}
	progress.mu.Lock()
	defer progress.mu.Unlock()
	if event.Done {
		progress.worktreesComplete++
	}
	state := "inspecting"
	if event.Done {
		state = "inspected"
	}
	target := event.Repository
	if target == "" {
		target = shortPath(event.Path)
	}
	progress.live.update(fmt.Sprintf(
		"remote publish: worktrees %s %s; %d completed",
		state, target, progress.worktreesComplete,
	))
}

func (progress *remotePublishProgress) finish(message string) {
	if progress == nil {
		return
	}
	progress.live.finish("remote publish: " + message)
}

func (progress *remotePublishProgress) fail(err error) {
	if progress == nil {
		return
	}
	message := "remote publish: failed"
	if err != nil {
		message += ": " + err.Error()
	}
	progress.live.finish(message)
}
