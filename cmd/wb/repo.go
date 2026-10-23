package main

import (
	"io"

	"github.com/sneat-dev/wb/internal/cli/cmdrepo"
	"github.com/sneat-dev/wb/internal/cli/statusview"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/repostatus"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func statusCommandDependencies() statusview.Dependencies {
	return statusview.Dependencies{Collect: repostatus.Collect, WriteReports: statusview.WriteReports, Interactive: func(out io.Writer, forced bool) bool { return console.Interactive(out, forced) }}
}
func newRepoCmd(inv *invocation) *cobra.Command {
	return cmdrepo.New(newCLIRuntime(inv), cmdrepo.Dependencies{
		Status: statusCommandDependencies(), SetSkipSync: gitops.SetSkipSync,
		UnsetSkipSync: gitops.UnsetSkipSync, InitRemote: gitops.InitRemote,
		RecoverTransfer: worktrees.RecoverRepositoryTransferCleanup,
	})
}
