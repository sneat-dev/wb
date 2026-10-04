package main

import (
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
	for _, child := range cmdworktree.CollaborationCommands(newCLIRuntime(inv), func(projectsRoot string) (cmdworktree.CollaborationOperations, error) {
		return worktreerun.NewCollaborationService(projectsRoot)
	}) {
		command.AddCommand(child)
	}
}
