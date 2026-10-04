package main

import (
	"context"

	"connectrpc.com/connect"
	"github.com/sneat-dev/wb/internal/cli/cmdrun"
	"github.com/sneat-dev/wb/internal/daemonruntime"
	"github.com/sneat-dev/wb/internal/discover"
	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/operationreceipt"
	"github.com/sneat-dev/wb/internal/runexec"
	"github.com/spf13/cobra"
)

func newRunCmd(inv *invocation) *cobra.Command {
	return newRunCmdWithDaemonDependencies(inv, defaultDaemonDependencies())
}
func newRunCmdWithDaemonDependencies(inv *invocation, deps daemonDependencies) *cobra.Command {
	recipes := runexec.NewRecipes(func(root, filter string, extraOrgs []string) ([]discover.Repo, error) {
		return fleet(root, filter, func() []string { return fleetOwners(extraOrgs) })
	})
	submit := runexec.NewSubmission(func(ctx context.Context, request runexec.SubmitRequest, submission runexec.Submission) (operationreceipt.Receipt, error) {
		client, err := daemonruntime.OperationClient(ctx, deps.Dependencies, request.ProjectsRoot, request.Stderr)
		if err != nil {
			return operationreceipt.Receipt{}, err
		}
		response, err := client.SubmitOperation(ctx, connect.NewRequest(&daemonv1.SubmitOperationRequest{WorkingDirectory: submission.WorkingDirectory, Argv: submission.Argv, TargetWorkerId: submission.WorkerID, IdempotencyKey: submission.IdempotencyKey}))
		if err != nil {
			return operationreceipt.Receipt{}, err
		}
		return operationreceipt.FromOperation(response.Msg), nil
	})
	return cmdrun.New(newCLIRuntime(inv), cmdrun.Dependencies{Execute: runexec.Execute, Changed: runexec.Changed, History: runexec.History, Queue: runexec.Queue, Recipes: recipes.Run, Submit: submit.Run, LoadHint: runexec.LoadHint})
}
