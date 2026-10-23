package fleetsync

import (
	"context"
	"sync"

	"github.com/sneat-dev/wb/internal/discover"
)

type BatchOptions struct {
	ProjectsRoot          string
	Workers               int
	DryRun, PruneArchived bool
	StopPendingOnCancel   bool
}
type BatchObserver struct {
	Started func(discover.Repo)
	Done    func(Result)
}

func Batch(ctx context.Context, repos []discover.Repo, options BatchOptions, observer BatchObserver) []Result {
	if options.Workers <= 0 {
		return nil
	}
	var stopPending <-chan struct{}
	if options.StopPendingOnCancel {
		stopPending = ctx.Done()
	}
	jobs := make(chan discover.Repo)
	feederDone := make(chan struct{})
	go func() {
		defer close(feederDone)
		defer close(jobs)
		for _, repo := range repos {
			select {
			case jobs <- repo:
			case <-stopPending:
				return
			}
		}
	}()
	resultsCh := make(chan Result)
	var wg sync.WaitGroup
	for i := 0; i < options.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for repo := range jobs {
				if observer.Started != nil {
					observer.Started(repo)
				}
				result := Sync(ctx, repo, options.ProjectsRoot, options.DryRun, options.PruneArchived)
				if observer.Done != nil {
					observer.Done(result)
				}
				resultsCh <- result
			}
		}()
	}
	go func() { wg.Wait(); close(resultsCh) }()
	var results []Result
	for result := range resultsCh {
		results = append(results, result)
	}
	<-feederDone
	return results
}
