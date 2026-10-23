package main

import (
	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/checkoutmarker"
	"github.com/sneat-dev/wb/internal/checkoutsetup"
	"github.com/sneat-dev/wb/internal/cli/cmdworktree"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
	"io"
)

func newWorktreeMarkerCmd(inv *invocation) *cobra.Command {
	return cmdworktree.NewMarker(newCLIRuntime(inv), cmdworktree.MarkerOperations{Run: checkoutsetup.NewMarkers(checkoutsetup.DefaultMarkerRunDependencies()).Run, Version: buildinfo.Version})
}

// markCreatedCheckouts refreshes the marker for every worktree a create call
// produced, and for the canonical clone each was cut from.
//
// It is deliberately best-effort. `wb worktree create` succeeding is the
// result the caller depends on; a marker WB could not write is a diagnostic,
// not a reason to fail a checkout that already exists on disk and already
// carries a claim.
func markCreatedCheckouts(inv *invocation, command *cobra.Command, base string, results []worktrees.CreateResult) {
	checkoutsetup.AfterCreate(checkoutmarker.DescribeOptions{ProjectsRoot: inv.projectsRoot, BaseBranch: base, Version: "wb " + buildinfo.Version()}, command.ErrOrStderr(), results, checkoutsetup.DefaultMarkerDependencies())
}

// refreshSyncedCheckoutMarkers marks every canonical clone a sync touched.
//
// Only the clones, not their worktrees: sync operates on canonical clones, and
// enumerating every worktree of every repository would turn a fleet sync into
// a second fleet walk for a file that `wb worktree marker --fleet` refreshes
// on demand anyway.
func refreshSyncedCheckoutMarkers(results []fleetsync.Result, projectsRoot string, errOut io.Writer) {
	checkoutsetup.NewMarkers(checkoutsetup.DefaultMarkerRunDependencies()).RefreshSynced(results, checkoutmarker.DescribeOptions{ProjectsRoot: projectsRoot, BaseBranch: "main", Version: "wb " + buildinfo.Version()}, errOut)
}

// markRenamedCheckouts refreshes the marker of every worktree a recycle moved.
//
// Best-effort for the same reason as create: the move already happened and is
// already recorded, so a marker WB could not rewrite is a diagnostic rather
// than a reason to report a completed rename as a failure.
func markRenamedCheckouts(inv *invocation, command *cobra.Command, base string, results []worktrees.RenameResult) {
	checkoutsetup.NewMarkers(checkoutsetup.DefaultMarkerRunDependencies()).AfterRename(results, checkoutmarker.DescribeOptions{ProjectsRoot: inv.projectsRoot, BaseBranch: base, Version: "wb " + buildinfo.Version()}, command.ErrOrStderr())
}

// markRelocatedCheckouts refreshes the generated location projection after a
// successful physical move. Failure is a warning: the durable relocation
// receipt and Git registration already establish the completed operation, and
// a later `wb worktree marker --fleet` can safely repair this convenience file.
func markRelocatedCheckouts(inv *invocation, command *cobra.Command, results []worktrees.RelocateResult) {
	checkoutsetup.NewMarkers(checkoutsetup.DefaultMarkerRunDependencies()).AfterRelocate(results, checkoutmarker.DescribeOptions{ProjectsRoot: inv.projectsRoot, BaseBranch: "main", Version: "wb " + buildinfo.Version()}, command.ErrOrStderr())
}
