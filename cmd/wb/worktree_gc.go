package main

import (
	"github.com/sneat-dev/wb/internal/cli/cmdworktree"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func newWorktreeGCCmd(inv *invocation) *cobra.Command {
	command := cmdworktree.NewGC(newCLIRuntime(inv), cmdworktree.GCDependencies{Run: worktrees.GC, Progress: func(command *cobra.Command, verbose bool) cmdworktree.InventoryProgress {
		p := newInventoryProgress(inv, command.ErrOrStderr(), verbose)
		return cmdworktree.InventoryProgress{Report: p.report, Finish: p.finish}
	}})
	setDiscoveryTerms(command, "garbage collect gc retire abandoned stale worktree hygiene detached review checkout squash merged residue disk reclaim cleanup sweep")
	return command
}
