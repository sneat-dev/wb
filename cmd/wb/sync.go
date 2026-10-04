package main

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/sneat-dev/wb/internal/cli/cmdsync"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetdiscovery"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/lifecyclehooks"
	"github.com/sneat-dev/wb/internal/syncrun"
	"github.com/spf13/cobra"
)

func newSyncCmd(inv *invocation) *cobra.Command {
	command := cmdsync.New(newCLIRuntime(inv), func(ctx context.Context, options syncrun.Options, out, errOut io.Writer) int {
		return runSync(ctx, inv, options.ProjectsRoot, options.Filter, options.Owners, options.Workers, options.DryRun, options.Publish, options.PruneArchived, defaultRemoteDeps(), out, errOut)
	})
	setDiscoveryTerms(command, "sync update pull clone fleet repositories reconcile refresh prune archived")
	return command
}
func runSync(ctx context.Context, inv *invocation, projectsRoot, filter string, only []string, workers int, dryRun, publish, pruneArchived bool, deps remoteDeps, out, errOut io.Writer) int {
	native := syncrun.Dependencies{Discovery: fleetdiscovery.New(os.Stderr), Reconcile: func(ctx context.Context, repos []discover.Repo) []discover.Repo {
		return discover.ReconcileTransfers(ctx, repos, discover.ResolveCanonicalRepository)
	}, Dispatch: lifecyclehooks.Dispatch, Now: time.Now}
	service := syncrun.New(native, func(meta fleetsync.RunMeta, results []fleetsync.Result, options syncrun.Options, reportOut, errOut io.Writer) int {
		return finishSync(inv, meta, results, options.Publish, options.DryRun, deps, options.ProjectsRoot, options.Filter, options.Workers, reportOut, errOut)
	}, cmdsync.Presentation())
	return service.Run(ctx, syncrun.Options{ProjectsRoot: projectsRoot, Filter: filter, Owners: only, Workers: workers, DryRun: dryRun, Publish: publish, PruneArchived: pruneArchived, Interactive: console.Interactive(out, inv.nonInteractive)}, out, errOut)
}
func finishSync(inv *invocation, meta fleetsync.RunMeta, results []fleetsync.Result, publish, dryRun bool, deps remoteDeps, projectsRoot, filter string, workers int, out, errOut io.Writer) int {
	return syncrun.Finalize(meta, results, syncrun.Options{ProjectsRoot: projectsRoot, Filter: filter, Workers: workers, Publish: publish, DryRun: dryRun}, syncrun.Effects{Markers: refreshSyncedCheckoutMarkers, Publish: func(root, filter string, workers int, out, errOut io.Writer) error {
		return runRemotePublishWithProgress(deps, root, filter, workers, false, false, out, errOut, inv)
	}}, out, errOut)
}
