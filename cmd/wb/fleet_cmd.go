package main

import (
	"os"

	"github.com/sneat-dev/wb/internal/cli/cmdfleet"
	"github.com/sneat-dev/wb/internal/defaultbranch"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetdiscovery"
	"github.com/sneat-dev/wb/internal/fleetinspect"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/layout"
	"github.com/sneat-dev/wb/internal/mergepolicy"
	"github.com/sneat-dev/wb/internal/prinventory"
	"github.com/sneat-dev/wb/internal/reposelection"
	"github.com/sneat-dev/wb/internal/repostatus"
	"github.com/sneat-dev/wb/internal/runqueue"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func fleetCommandDependencies() cmdfleet.Dependencies {
	resolver := fleetdiscovery.New(os.Stderr)
	inspect := fleetinspect.New(fleetinspect.Dependencies{Select: reposelection.Select, Inspect: repostatus.InspectTargets, Scan: discover.ScanLocal, Layout: layout.Counts, Worktrees: worktrees.List, Remote: resolver.Discover, Owners: resolver.Owners, Sync: fleetsync.Sync, Hooks: hooks.Check, Executable: hookExecutable})
	return cmdfleet.Dependencies{Overview: inspect.Overview, Stats: inspect.Stats, Inventory: prinventory.Inventory, InventoryOwners: func(extra []string) ([]prinventory.Owner, []prinventory.Diagnostic) {
		return prinventory.ResolveOwners(extra, discover.AuthUser, discover.MemberOrgs)
	}, MergePolicy: mergepolicy.New().Run, DefaultBranch: defaultbranch.New().Run, Status: statusCommandDependencies(), Budget: runqueue.Budget, MkdirAll: os.MkdirAll, WriteFile: os.WriteFile}
}
func newFleetCmd(inv *invocation) *cobra.Command {
	command := cmdfleet.New(newCLIRuntime(inv), fleetCommandDependencies())
	command.AddCommand(newFleetCoverageCmd(inv))
	return command
}
