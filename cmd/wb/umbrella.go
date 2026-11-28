package main

import (
	"github.com/sneat-dev/wb/internal/cli/cmdumbrella"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func newUmbrellaCmd(inv *invocation) *cobra.Command {
	return cmdumbrella.New(newCLIRuntime(inv), cmdumbrella.Dependencies{
		DefaultRegistry: func() (string, error) {
			return worktrees.ResolveUmbrellaDefaultRegistry()
		},
		Members: worktrees.UmbrellaMembers,
	})
}
