package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
	"github.com/spf13/cobra"
)

// Helpers kept for tests only: no production caller remains.

func (controller daemonController) Restart(ctx context.Context, ifRunning bool) (daemonResult, error) {
	return controller.RestartWithProgress(ctx, ifRunning, nil, false)
}

func daemonFileRequestTarget(procedure string, body []byte) (string, error) {
	target, _, err := daemonFilePrepareRequest(procedure, body, "")
	return target, err
}

func newDaemonOperationClient(root, token string) (daemonv1connect.DaemonServiceClient, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("daemon lifecycle state has no authentication token")
	}
	client, err := daemonLocalHTTPClient(root, token)
	if err != nil {
		return nil, err
	}
	return daemonv1connect.NewDaemonServiceClient(client, daemonRPCBaseURL), nil
}

// newRootCmd builds the command tree for callers that only inspect it (help
// text, subcommand paths, flag matrices) rather than execute it through
// runWithStdin. It is the ~50 existing test call sites' entry point, and
// stays a zero-argument constructor: it hands newRootCmdFor a throwaway
// invocation that is never read back.
func newRootCmd() *cobra.Command {
	return newRootCmdFor(&invocation{})
}

func newRunQueueProgress(out io.Writer, enabled bool, configPath string) *runQueueProgress {
	return newRunQueueProgressWithHeartbeat(out, enabled, configPath, universalProgressHeartbeat)
}

// runPreparedNpmPublish owns the irreversible boundary after the complete
// preflight has selected a stable fleet. Keeping it separate makes the
// campaign-lock contract directly testable without a live GitHub or npm call.
func runPreparedNpmPublish(command *cobra.Command, options npmPublishOptions, prepared npmPublishPrepared, inv *invocation) error {
	// Every path below writes a durable report. Take both campaign and
	// package-version locks before either a dry-run plan or --apply can touch
	// it, so a plan cannot overwrite an in-progress apply/resume handoff and an
	// overlapping campaign cannot publish the same npm version concurrently.
	locks, err := acquireNpmPublicationLocks(inv, prepared.operation, prepared.releases, options.resume)
	if err != nil {
		return err
	}
	defer locks.Release()
	return runPreparedNpmPublishLocked(command, options, prepared, inv)
}
