package main

import (
	"github.com/sneat-dev/wb/internal/cli/cmdworktree"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func newCleanCmd(inv *invocation) *cobra.Command {
	command := cmdworktree.NewClean(newCLIRuntime(inv), cmdworktree.CleanDependencies{Run: worktrees.Clean, Progress: func(command *cobra.Command, verbose bool) cmdworktree.InventoryProgress {
		p := newInventoryProgress(inv, command.ErrOrStderr(), verbose)
		return cmdworktree.InventoryProgress{Report: p.report, Finish: p.finish}
	}})
	setDiscoveryTerms(command, "clean clean up cleanup tidy garbage collect gc retire remove delete finished landed merged squash worktrees branches stale abandoned orphans orphaned what is safe to delete hygiene disk reclaim sweep audit")
	return command
}
