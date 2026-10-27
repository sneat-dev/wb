package main

import (
	"fmt"

	"github.com/sneat-dev/wb/internal/cli/cmdworktree"
	"github.com/sneat-dev/wb/internal/worktreecollab"
	"github.com/sneat-dev/wb/internal/worktreerun"
	"github.com/spf13/cobra"
)

type collaborationServiceFactory func() (worktreecollab.Service, error)

func newCollaborationService(inv *invocation) (worktreecollab.Service, error) {
	return worktreerun.NewCollaborationService(inv.projectsRoot)
}

func collaborationFactory(inv *invocation) collaborationServiceFactory {
	return func() (worktreecollab.Service, error) { return newCollaborationService(inv) }
}

func addCollaborationCommands(command *cobra.Command, inv *invocation) {
	rebind := newWorktreeRebindCmd(collaborationFactory(inv))
	rebind.GroupID = "recover"
	command.AddCommand(rebind)
	for _, child := range cmdworktree.CollaborationCommands(newCLIRuntime(inv), func(projectsRoot string) (cmdworktree.CollaborationOperations, error) {
		return worktreerun.NewCollaborationService(projectsRoot)
	}) {
		command.AddCommand(child)
	}
}

func newWorktreeRebindCmd(factory collaborationServiceFactory) *cobra.Command {
	var expectedRoot, expectedOwner, reason string
	var expectedOwnerEpoch, expectedRevision uint64
	var takeInactiveOwnership bool
	command := &cobra.Command{
		Use:   "rebind <current-path>",
		Short: "Repair a stale collaboration root after a worktree path was retired",
		Long: `Repair one collaboration snapshot after its prior worktree root has been
retired and Git has reused the same linked-worktree identity. This operation
requires the exact previous root, an absent old path, matching GitDir and
CommonDir, and a live admitted WB session. By default it changes no ownership
and refuses to infer or take over a checkout identity.

With --take-inactive-ownership it also transfers ownership away from the owner
recorded in the stale snapshot, but only when every observed field is supplied
exactly, the caller is a separately corroborated live session, and the recorded
owner is proven inactive. Live, unknown, ambiguous, or unreadable owners refuse,
and the recovery then publishes one ownership change plus one root rebind.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if takeInactiveOwnership {
				for _, required := range []string{"expected-root", "expected-owner", "expected-owner-epoch", "expected-revision", "reason"} {
					if !cmd.Flags().Changed(required) {
						return fmt.Errorf("--take-inactive-ownership requires --%s with the exact value observed in the stale snapshot", required)
					}
				}
				if expectedOwnerEpoch == 0 || expectedRevision == 0 {
					return fmt.Errorf("--expected-owner-epoch and --expected-revision must be the observed counters, which are never zero")
				}
			} else {
				for _, field := range []string{"expected-owner", "expected-owner-epoch", "expected-revision", "reason"} {
					if cmd.Flags().Changed(field) {
						return fmt.Errorf("--%s is only supported with --take-inactive-ownership", field)
					}
				}
				if expectedRoot == "" {
					return fmt.Errorf("--expected-root is required; inspect the stale snapshot before rebinding")
				}
			}
			service, err := factory()
			if err != nil {
				return err
			}
			if takeInactiveOwnership {
				state, err := service.RecoverInactiveOwner(cmd.Context(), args[0], worktreecollab.InactiveOwnerRecovery{
					ExpectedRoot: expectedRoot, ExpectedOwner: expectedOwner,
					ExpectedEpoch: expectedOwnerEpoch, ExpectedRevision: expectedRevision, Reason: reason,
				})
				if err != nil {
					return err
				}
				change := state.CheckoutRebinds[len(state.CheckoutRebinds)-1]
				owner := state.OwnerChanges[len(state.OwnerChanges)-1]
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "recovered %s root %s -> %s owner %s -> %s (epoch %d, revision %d)\n",
					state.Checkout.ID, change.PreviousRoot, change.CurrentRoot, owner.Previous, state.Owner, state.OwnerEpoch, state.Revision)
				return err
			}
			state, err := service.Rebind(cmd.Context(), args[0], expectedRoot)
			if err != nil {
				return err
			}
			change := state.CheckoutRebinds[len(state.CheckoutRebinds)-1]
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "rebound %s root %s -> %s (revision %d)\n", state.Checkout.ID, change.PreviousRoot, change.CurrentRoot, state.Revision)
			return err
		},
	}
	command.Flags().StringVar(&expectedRoot, "expected-root", "", "exact prior checkout root recorded in the stale snapshot")
	command.Flags().BoolVar(&takeInactiveOwnership, "take-inactive-ownership", false, "also replace a recorded owner that is proven inactive")
	command.Flags().StringVar(&expectedOwner, "expected-owner", "", "exact owner recorded in the stale snapshot")
	command.Flags().Uint64Var(&expectedOwnerEpoch, "expected-owner-epoch", 0, "exact owner epoch recorded in the stale snapshot")
	command.Flags().Uint64Var(&expectedRevision, "expected-revision", 0, "exact coordination revision recorded in the stale snapshot")
	command.Flags().StringVar(&reason, "reason", "", "audited reason required with --take-inactive-ownership")
	return command
}
