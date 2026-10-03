package main

import (
	"context"
	"github.com/sneat-dev/wb/internal/cli/cmdbranch"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func newBranchCmd(inv *invocation) *cobra.Command {
	return cmdbranch.New(newCLIRuntime(inv), cmdbranch.Dependencies{List: worktrees.BranchList, Cleanup: worktrees.BranchCleanup, Quarantine: worktrees.BranchQuarantine, ArchiveTarget: func(ctx context.Context, repository string) (worktrees.RetiredArchivePlan, error) {
		return worktrees.PlanRetiredArchivePreflight(ctx, repository, nil)
	}})
}
