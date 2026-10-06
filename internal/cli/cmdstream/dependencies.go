package cmdstream

import (
	"context"

	"github.com/sneat-dev/wb/internal/streamrun"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/streamsync"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// Dependencies are the concrete operations consumed by the six stream verbs.
type Dependencies struct {
	Start             func(context.Context, streamrun.Creation, streams.StartOptions) (streams.StartResult, error)
	Join              func(context.Context, streamrun.Creation, streams.JoinOptions) (streams.StartResult, error)
	List              func(context.Context, string) ([]streams.Stream, []streams.Unreadable, error)
	Status            func(context.Context, string, string) (streams.Status, error)
	End               func(context.Context, string, streams.EndOptions) (streams.EndResult, error)
	Delete            func(string, string) error
	Sync              func(context.Context, streamrun.SyncRequest) ([]streamsync.Result, error)
	RegisteredSession func() bool
	PrepareWorkLog    func(string, string, worktrees.WorkLogOptions) (worktrees.WorkLogOptions, error)
}
