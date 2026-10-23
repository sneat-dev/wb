package cmdsync

import (
	"context"
	"fmt"
	"io"

	tea "charm.land/bubbletea/v2"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/tui"
)

func runSyncTUI(ctx context.Context, repos []discover.Repo, orgTotal map[string]int, projectsRoot string, workers int, dryRun, pruneArchived bool, errOut io.Writer, programOptions ...tea.ProgramOption) []fleetsync.Result {
	workerContext, cancel := context.WithCancel(ctx)
	p := tea.NewProgram(tui.NewProgressModel(orgTotal, workers), programOptions...)
	workersDone := make(chan struct{})
	go func() {
		defer close(workersDone)
		fleetsync.Batch(workerContext, repos, fleetsync.BatchOptions{ProjectsRoot: projectsRoot, Workers: workers, DryRun: dryRun, PruneArchived: pruneArchived, StopPendingOnCancel: true}, fleetsync.BatchObserver{Started: func(repo discover.Repo) { p.Send(tui.RepoStarted{Org: repo.Org, Name: repo.Name}) }, Done: func(result fleetsync.Result) { p.Send(tui.RepoDone{Result: result}) }})
		p.Send(tui.SyncDone{})
	}()
	final, err := p.Run()
	cancel()
	<-workersDone
	if err != nil {
		_, _ = fmt.Fprintln(errOut, "tui error:", err)
	}
	pm, _ := final.(tui.ProgressModel)
	return pm.Results
}
