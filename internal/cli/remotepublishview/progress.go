package remotepublishview

import (
	"fmt"
	"io"
	"sync"

	cliprogress "github.com/sneat-dev/wb/internal/cli/progress"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/remotepublish"
	"github.com/sneat-dev/wb/internal/worktrees"
)

type Progress struct {
	live *cliprogress.Live

	mu                   sync.Mutex
	repositoriesTotal    int
	repositoriesComplete int
	worktreesComplete    int
}

func NewProgress(out io.Writer, enabled bool) *Progress {
	return &Progress{live: cliprogress.NewLive(out, enabled)}
}

func (progress *Progress) Start(total int) {
	if progress == nil {
		return
	}
	progress.mu.Lock()
	defer progress.mu.Unlock()
	progress.repositoriesTotal = total
	progress.live.Start(fmt.Sprintf("remote publish: scanning 0/%d repositories", total))
}

func (progress *Progress) RepositoryComplete(repository string, err error) {
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
	progress.live.Update(fmt.Sprintf(
		"remote publish: scanned %d/%d repositories; %s: %s",
		progress.repositoriesComplete, progress.repositoriesTotal, repository, status,
	))
}

func (progress *Progress) Phase(phase string) {
	if progress == nil {
		return
	}
	progress.live.Update("remote publish: " + phase)
}

func (progress *Progress) Worktree(event worktrees.ListProgress) {
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
		target = shared.ShortPath(event.Path)
	}
	progress.live.Update(fmt.Sprintf(
		"remote publish: worktrees %s %s; %d completed",
		state, target, progress.worktreesComplete,
	))
}

func (progress *Progress) Finish(message string) {
	if progress == nil {
		return
	}
	progress.live.Finish("remote publish: " + message)
}

func (progress *Progress) Fail(err error) {
	if progress == nil {
		return
	}
	message := "remote publish: failed"
	if err != nil {
		message += ": " + err.Error()
	}
	progress.live.Finish(message)
}

func (p *Progress) Callbacks() remotepublish.Progress {
	return remotepublish.Progress{Start: p.Start, RepositoryComplete: p.RepositoryComplete, Phase: p.Phase, Worktree: p.Worktree, Finish: p.Finish, Fail: p.Fail}
}
