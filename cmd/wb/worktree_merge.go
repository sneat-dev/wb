package main

import (
	"os"

	"github.com/sneat-dev/wb/internal/cli/cmdworktree"
	"github.com/sneat-dev/wb/internal/landingcontext"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/spf13/cobra"
)

func newWorktreeMergeCmd(inv *invocation) *cobra.Command {
	return cmdworktree.NewMerge(newCLIRuntime(inv), cmdworktree.DefaultMergeOperations(), mergeBindings())
}
func newWorktreeLandCmd(inv *invocation) *cobra.Command {
	return cmdworktree.NewLand(newCLIRuntime(inv), cmdworktree.DefaultMergeOperations(), mergeBindings())
}
func newLandCmd(inv *invocation) *cobra.Command { return newWorktreeLandCmd(inv) }
func mergeBindings() cmdworktree.MergeBindings {
	return cmdworktree.MergeBindings{
		Admission: requireMutationAdmission, Initiator: mutationInitiator, Discovery: setDiscoveryTerms, Quiet: markQuietVerb, Landing: markLandingGuard,
		RefusePaths: func(root string, paths []string) error {
			return landingGuardError(landingcontext.CheckWorktrees(root, paths))
		},
		RefuseReceipt: func(root string, path string) error {
			return landingGuardError(landingcontext.CheckReceipt(root, path))
		},
		LaneRequest: func(root, command, reason string, takeOver bool) orchestrate.LaneGuardRequest {
			return landingcontext.LaneRequest(root, command, reason, takeOver, os.Getpid())
		},
		ReleaseLane: func(root string, receipt orchestrate.WorktreeMergeReceipt) {
			landingcontext.ReleaseWorktreeLane(root, receipt, os.Getpid())
		},
		CheckoutUpdated: lifecycleCheckoutUpdated,
	}
}
