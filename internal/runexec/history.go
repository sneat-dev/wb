package runexec

import (
	"context"
	"github.com/sneat-dev/wb/internal/runlog"
	"github.com/sneat-dev/wb/internal/runqueue"
	"os"
	"time"
)

type HistoryOperations struct {
	Getwd func() (string, error)
	Read  func(string) ([]runlog.Event, string, error)
	Now   func() time.Time
}

func History(ctx context.Context, request HistoryRequest) (HistoryResult, error) {
	return (HistoryOperations{os.Getwd, runlog.ReadCurrent, time.Now}).Run(ctx, request)
}
func (ops HistoryOperations) Run(_ context.Context, request HistoryRequest) (HistoryResult, error) {
	cwd, err := ops.Getwd()
	if err != nil {
		return HistoryResult{}, err
	}
	events, path, err := ops.Read(cwd)
	if err != nil {
		return HistoryResult{}, err
	}
	return HistoryResult{Summary: runlog.Summarize(events, ops.Now().AddDate(0, 0, -request.Days)), Path: path, Days: request.Days}, nil
}
func Queue(request QueueRequest) runqueue.QueueListing {
	return runqueue.ListQueue(request.ProjectsRoot, runqueue.Budget())
}
