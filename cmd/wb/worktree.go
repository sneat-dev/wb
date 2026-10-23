package main

import (
	"context"
	"github.com/sneat-dev/wb/internal/checkoutsetup"
	"io"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/cli/cmdworktree"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/worktreerun"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// autoReleaseWriter selects the output stream for auto-release lines based on
// format. For json output, the release line is informational and belongs on
// stderr to keep stdout as pure JSON (matching worktree create's pattern of
// routing auto-claim output to io.Discard in json mode). For text output,
// stdout is correct.
// remoteClaimWriter returns the stream remote-claim notes belong on.
//
// They are diagnostics about a side channel, not the result of the command the
// user asked for, and wb's contract puts diagnostics on stderr. Keeping them
// off stdout means a json document is never corrupted, a text result is never
// padded with unrelated lines, and a command that fails writes nothing to
// stdout at all.
func remoteClaimWriter(cmd *cobra.Command) io.Writer {
	return cmd.ErrOrStderr()
}

var recoverRetiredStages = worktrees.RecoverRetiredStages

// cleanupWorktreeTasks is the cleanup engine the command drives. It is a
// variable so the flag-to-option mapping can be asserted without Git.
var cleanupWorktreeTasks = worktrees.Cleanup

func newWorktreeCmd(inv *invocation) *cobra.Command {
	command := &cobra.Command{
		Use:     "worktree",
		Aliases: []string{"worktrees", "wt"},
		Short:   "Create, inspect, merge, and safely retire isolated agent work",
	}
	command.AddGroup(
		&cobra.Group{ID: "start", Title: "Start work"},
		&cobra.Group{ID: "finish", Title: "Finish work"},
		&cobra.Group{ID: "inspect", Title: "Inspect progress"},
		&cobra.Group{ID: "recover", Title: "Recover and coordinate"},
		&cobra.Group{ID: "admin", Title: "Administration"},
	)
	children := []struct {
		command *cobra.Command
		group   string
	}{
		{newWorktreeCreateCmd(inv), "start"},
		{newWorktreeAdoptCmd(inv), "start"},
		{newWorktreeMergeCmd(inv), "finish"},
		{newWorktreeLandCmd(inv), "finish"},
		{newWorktreeEndCmd(inv), "finish"},
		{newWorktreeCleanupCmd(inv), "finish"},
		{newWorktreeRetireCmd(inv), "finish"},
		{newWorktreeGCCmd(inv), "finish"},
		{newWorktreeAbortCmd(inv), "finish"},
		{newWorktreeSummaryCmd(inv), "inspect"},
		{newWorktreeActiveCmd(inv), "inspect"},
		{newWorktreeInfoCmd(inv), "inspect"},
		{newWorktreeListCmd(inv), "inspect"},
		{newWorktreeGuardCmd(inv), "recover"},
		{newWorktreeRescueCmd(inv), "recover"},
		{newWorktreeWorkLogCmd(inv), "recover"},
		{newWorktreeCheckpointFetchCmd(), "recover"},
		{newWorktreeOwnCmd(), "recover"},
		{newWorktreeMarkerCmd(inv), "admin"},
		{newWorktreeRelocateCmd(inv), "admin"},
		{newWorktreeRenameCmd(inv), "admin"},
		{newWorktreeCorrectIdentityCmd(inv), "admin"},
		{newWorktreeSetCmd(inv), "admin"},
		{newWorktreeOrphansCmd(inv), "admin"},
		{newWorktreeBackfillCmd(inv), "admin"},
	}
	for _, child := range children {
		child.command.GroupID = child.group
		command.AddCommand(child.command)
	}
	addCollaborationCommands(command, inv)
	return command
}

func newWorktreeRelocateCmd(inv *invocation) *cobra.Command {
	command := cmdworktree.NewRelocate(newCLIRuntime(inv), cmdworktree.RelocateDependencies{Run: worktrees.Relocate, Admit: requireMutationAdmission, AfterApply: func(command *cobra.Command, results []worktrees.RelocateResult) {
		markRelocatedCheckouts(inv, command, results)
	}, StartProgress: worktreerun.StartRelocationProgress})
	addMutationAdmissionFlags(command)
	return command
}

func newWorktreeCheckpointFetchCmd() *cobra.Command {
	return cmdworktree.NewCheckpointFetch(worktrees.FetchRemoteCheckpoint)
}

func newWorktreeInfoCmd(inv *invocation) *cobra.Command {
	service := worktreerun.DefaultInfoService()
	return cmdworktree.NewInfo(newCLIRuntime(inv), service.Inspect)
}

func newWorktreeWorkLogCmd(inv *invocation) *cobra.Command {
	return cmdworktree.NewWorkLog(newCLIRuntime(inv), worktreeJournalOperations(), requireMutationAdmission)
}

func newWorktreeCorrectIdentityCmd(inv *invocation) *cobra.Command {
	command := cmdworktree.NewCorrectIdentity(newCLIRuntime(inv), worktrees.CorrectExecutionIdentity, requireMutationAdmission, mutationInitiator)
	addMutationAdmissionFlags(command)
	return command
}

func newWorktreeAbortCmd(inv *invocation) *cobra.Command {
	return cmdworktree.NewAbort(newCLIRuntime(inv), cmdworktree.AbortDependencies{Run: worktrees.Abort, Release: func(command *cobra.Command, root, task string) bool {
		result := releaseRemoteClaim(root, task, remoteClaimWriter(command))
		return exitNonZeroOnReleaseLeak && result.Leaked()
	}, SkipRelease: func(command *cobra.Command, reason string) { skippedAutoRelease(remoteClaimWriter(command), reason) }})
}

func newWorktreeCreateCmd(inv *invocation) *cobra.Command {
	command := cmdworktree.NewCreate(newCLIRuntime(inv), worktreeCreateDependencies(inv))
	setDiscoveryTerms(command, "start begin create new work task agent isolated worktree branch implement edit code save tokens multi repo multiple repositories cross-repository repo")
	markQuietVerb(command)
	return command
}

func worktreeCreateDependencies(inv *invocation) cmdworktree.CreateDependencies {
	return cmdworktree.CreateDependencies{
		Run:                worktrees.Create,
		OriginSlug:         worktrees.OriginSlug,
		PrepareLog:         worktrees.PrepareWorkLogOptions,
		RegisteredIdentity: worktrees.RegisteredIdentity,
		BeforeCreate: func(root string, repositories []string) error {
			return checkoutsetup.BeforeCreate(root, repositories, checkoutsetup.DefaultHookDependencies(hookExecutable))
		},
		Claim: func(command *cobra.Command, noClaim bool, root, task string) worktreerun.RemoteClaimOutcome {
			return claimRemoteTask(noClaim, root, task, outcomeClaimWriter(command, inv.quiet))
		},
		AfterCreate: func(command *cobra.Command, base string, results []worktrees.CreateResult) {
			markCreatedCheckouts(inv, command, base, results)
		},
	}
}

// newCreateCmd is the root-level alias for `wb worktree create`: `wb create`.
// Starting isolated work is the other half of the agent workflow `wb land`
// (see newLandCmd in worktree_merge.go) closes out, and it had the identical
// discoverability problem: nothing at the top level named it, so it sat one
// noun below where an agent skimming `wb --help` would look first.
//
// It is built from the exact same constructor as `wb worktree create`, so the
// two commands share identical flags, help text, and exit codes by
// construction rather than by two copies staying in sync; only the command
// path they resolve under differs ("wb create" vs "wb worktree create").
func newCreateCmd(inv *invocation) *cobra.Command {
	return newWorktreeCreateCmd(inv)
}

func newWorktreeGuardCmd(inv *invocation) *cobra.Command {
	return cmdworktree.NewGuard(newCLIRuntime(inv), worktrees.Guard, console.IsTerminal)
}

// newWorktreeSetCmd is the human-facing remedy the admission gate names. It
// deliberately records a prompt rather than setting a bypass flag: the act of
// unblocking a commit is itself the record of who directed it.
func newWorktreeSetCmd(inv *invocation) *cobra.Command {
	return cmdworktree.NewSet(newCLIRuntime(inv), worktrees.LogSteer)
}

func newWorktreeBackfillCmd(inv *invocation) *cobra.Command {
	return cmdworktree.NewBackfill(newCLIRuntime(inv), worktrees.Backfill)
}

func newWorktreeAdoptCmd(inv *invocation) *cobra.Command {
	command := cmdworktree.NewAdopt(newCLIRuntime(inv), worktrees.Adopt, func(command *cobra.Command, apply bool) (func(), error) {
		_, release, err := requireMutationAdmission(command, apply)
		return release, err
	})
	addMutationAdmissionFlags(command)
	return command
}

func newWorktreeOrphansCmd(inv *invocation) *cobra.Command {
	return cmdworktree.NewOrphans(newCLIRuntime(inv), worktrees.Orphans)
}

func newWorktreeListCmd(inv *invocation) *cobra.Command {
	return cmdworktree.NewList(newCLIRuntime(inv), worktrees.ListWithDiagnostics)
}

func newWorktreeSummaryCmd(inv *invocation) *cobra.Command {
	return cmdworktree.NewSummary(newCLIRuntime(inv), worktrees.ListWithDiagnostics)
}

func newWorktreeCleanupCmd(inv *invocation) *cobra.Command {
	command := cmdworktree.NewCleanup(newCLIRuntime(inv), cmdworktree.CleanupDependencies{Run: func(ctx context.Context, options worktrees.CleanupOptions) (worktrees.CleanupOutcome, error) {
		return cleanupWorktreeTasks(ctx, options)
	}, Shells: worktrees.RetireTaskShells, Recover: func(ctx context.Context, options worktrees.RetiredStageRecoveryOptions) (worktrees.RetiredStageRecoveryOutcome, error) {
		return recoverRetiredStages(ctx, options)
	}, Progress: func(command *cobra.Command, verbose bool) cmdworktree.InventoryProgress {
		p := newInventoryProgress(inv, command.ErrOrStderr(), verbose)
		return cmdworktree.InventoryProgress{Report: p.report, Finish: p.finish}
	}, Release: func(command *cobra.Command, root, task string) bool {
		result := releaseRemoteClaim(root, task, outcomeClaimWriter(command, inv.quiet))
		return exitNonZeroOnReleaseLeak && result.Leaked()
	}})
	setDiscoveryTerms(command, "cleanup clean up retire remove landed merged worktrees branches tasks")
	markQuietVerb(command)
	return command
}

func newWorktreeRenameCmd(inv *invocation) *cobra.Command {
	command := cmdworktree.NewRename(newCLIRuntime(inv), cmdworktree.RenameDependencies{Run: worktrees.Rename, Admit: requireMutationAdmission, AfterApply: func(command *cobra.Command, base string, results []worktrees.RenameResult) {
		markRenamedCheckouts(inv, command, base, results)
	}})
	addMutationAdmissionFlags(command)
	return command
}

func requireOutputFormat(value string, allowed ...string) error {
	return shared.RequireOutputFormat(value, allowed...)
}

func worktreeJournalOperations() cmdworktree.JournalOperations {
	return cmdworktree.JournalOperations{Load: worktrees.LoadWorkLogView, Show: worktrees.LogShow, Init: worktrees.LogInit, Steer: worktrees.LogSteer, Checkpoint: worktrees.LogCheckpoint, Refresh: worktrees.LogRefresh, Integrate: worktrees.LogIntegrate, Handoff: worktrees.LogHandoff, Recover: worktrees.LogRecover, Finalize: worktrees.LogFinalize, Sync: worktrees.LogSync, Archive: worktrees.LogArchive}
}
