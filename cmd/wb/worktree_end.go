package main

import (
	"github.com/sneat-dev/wb/internal/cli/cmdworktree"
	"github.com/sneat-dev/wb/internal/worktreeend"
	"github.com/sneat-dev/wb/internal/worktreerun"
	"github.com/spf13/cobra"
	"io"
)

func newWorktreeEndCmd(inv *invocation) *cobra.Command {
	command := cmdworktree.NewEnd(newCLIRuntime(inv), worktreeEndEngine)
	setDiscoveryTerms(command, "worktree end finish close task done retire cleanup claim release stash capture lane contract")
	return command
}
func worktreeEndEngine(root string, out io.Writer) (*worktreeend.Engine, error) {
	return worktreerun.NewEndEngine(root, out, func(root, task string, out io.Writer) { releaseRemoteClaim(root, task, out) })
}
