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
	var expectedRoot string
	command := &cobra.Command{
		Use:   "rebind <current-path>",
		Short: "Repair a stale collaboration root after a worktree path was retired",
		Long: `Repair one collaboration snapshot after its prior worktree root has been
retired and Git has reused the same linked-worktree identity. This operation
requires the exact previous root, an absent old path, matching GitDir and
CommonDir, and a live admitted WB session. It changes no ownership and refuses
to infer or take over a checkout identity.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if expectedRoot == "" {
				return fmt.Errorf("--expected-root is required; inspect the stale snapshot before rebinding")
			}
			service, err := factory()
			if err != nil {
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
	return command
}
