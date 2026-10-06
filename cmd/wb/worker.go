package main

import (
	"context"
	"io"

	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/cli/cmdworker"
	"github.com/sneat-dev/wb/internal/daemonruntime"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
	"github.com/sneat-dev/wb/internal/runqueue"
	"github.com/sneat-dev/wb/internal/workerrun"
	"github.com/spf13/cobra"
)

func newWorkerCmd(inv *invocation, deps daemonDependencies) *cobra.Command {
	return cmdworker.New(newCLIRuntime(inv), workerDependencies(deps))
}
func workerDependencies(deps daemonDependencies) cmdworker.Dependencies {
	service := workerrun.New(workerrun.Dependencies{Client: func(ctx context.Context, root string, out io.Writer) (daemonv1connect.DaemonServiceClient, error) {
		return daemonruntime.OperationClient(ctx, deps.Dependencies, root, out)
	}, Snapshot: buildinfo.Snapshot})
	return cmdworker.Dependencies{CanonicalRoots: workerrun.CanonicalRoots, Budget: runqueue.Budget, Connect: service.Connect}
}
