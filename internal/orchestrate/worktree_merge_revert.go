package orchestrate

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// PrepareWorktreeMergeRevert creates a fresh forward candidate which applies
// the inverse landing tree delta onto today's remote target. It never resets or
// force-pushes shared history.
func PrepareWorktreeMergeRevert(ctx context.Context, projectsRoot, input string, timeout time.Duration, retry int) (WorktreeMergeReceipt, error) {
	return prepareWorktreeMergeRevertInjected(ctx, projectsRoot, input, timeout, retry, nil)
}

// prepareWorktreeMergeRevertInjected is PrepareWorktreeMergeRevert's test
// seam (task-9 PR-9): every production call site reaches it only through
// PrepareWorktreeMergeRevert, which always passes a nil *filewrite.Injector,
// so production behaviour is unchanged. A test passes its own Injector to
// reach the scratch git-diff patch file's create/write/close failure
// branches deterministically.
func prepareWorktreeMergeRevertInjected(ctx context.Context, projectsRoot, input string, timeout time.Duration, retry int, inj *filewrite.Injector) (WorktreeMergeReceipt, error) {
	return prepareWorktreeMergeRevertWithRunner(ctx, projectsRoot, input, timeout, retry, inj, defaultRunner)
}

// prepareWorktreeMergeRevertWithRunner scopes only existing Git observations and mutations to this invocation.
func prepareWorktreeMergeRevertWithRunner(ctx context.Context, projectsRoot, input string, timeout time.Duration, retry int, inj *filewrite.Injector, run runner.Runner) (WorktreeMergeReceipt, error) {
	path, err := resolveWorktreeMergeReceiptPath(projectsRoot, input)
	if err != nil {
		return WorktreeMergeReceipt{}, err
	}
	receipt, err := readWorktreeMergeReceipt(path)
	if err != nil {
		return receipt, err
	}
	if receipt.Phase == WorktreeMergePhaseRevert {
		if receipt.Status == WorktreeMergePrepared {
			return receipt, nil
		}
		return receipt, fmt.Errorf("receipt already represents a forward revert with status %s", receipt.Status)
	}
	if receipt.PreviousTargetSHA == "" || receipt.LandingSHA == "" {
		return receipt, fmt.Errorf("receipt has no landed before/after target identity to revert")
	}
	revertOf := &WorktreeMergeRevertReceipt{
		PreviousTargetSHA: receipt.PreviousTargetSHA,
		LandingSHA:        receipt.LandingSHA,
		CandidateSHA:      receipt.Candidate.SHA,
	}
	task := "revert-" + receipt.ID
	prompt, err := writeWorktreeMergePromptInjected(receipt.Repository, receipt.Target, receipt.Sources, inj)
	if err != nil {
		return receipt, err
	}
	defer func() { _ = os.Remove(prompt) }()
	created, err := worktrees.Create(ctx, []string{receipt.Repository}, worktrees.CreateOptions{
		ProjectsRoot: projectsRoot, Operation: task, Branch: "wb/revert/" + receipt.ID, BranchChosen: true, Base: receipt.Target,
		WorkLog: worktrees.WorkLogOptions{EffortID: task, RunID: task, Model: "unknown", AgentRuntime: "wb", OriginalPrompt: prompt, RequireOriginalPrompt: true},
	})
	if err != nil {
		return receipt, err
	}
	patchOutput, _, err := runCommand(ctx, run, timeout, retry, created[0].WorktreeDir, "git", "diff", "--binary", revertOf.PreviousTargetSHA, revertOf.LandingSHA)
	if err != nil {
		return receipt, err
	}
	patchPath, err := filewrite.CreateScratch("", "wb-worktree-revert-*.patch", 0, []byte(patchOutput), inj)
	if patchPath != "" {
		defer func() { _ = os.Remove(patchPath) }()
	}
	if err != nil {
		return receipt, err
	}
	if _, _, err := runCommand(ctx, run, timeout, retry, created[0].WorktreeDir, "git", "apply", "--check", "--3way", "--reverse", patchPath); err != nil {
		return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, fmt.Errorf("forward revert conflicts with current target: %w", err))
	}
	if _, _, err := runCommand(ctx, run, timeout, retry, created[0].WorktreeDir, "git", "apply", "--3way", "--reverse", "--index", patchPath); err != nil {
		return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, err)
	}
	if _, _, err := runCommand(ctx, run, timeout, retry, created[0].WorktreeDir, "git", "commit", "-m", "revert: reverse worktree merge "+receipt.ID); err != nil {
		return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, err)
	}
	receipt.Phase = WorktreeMergePhaseRevert
	receipt.Status = WorktreeMergePrepared
	receipt.TargetSHA = created[0].BaseSHA
	receipt.Sources = nil
	receipt.Candidate = WorktreeMergeCandidate{Task: task, Worktree: created[0].WorktreeDir, Branch: created[0].Branch}
	receipt.Candidate.SHA, err = mergeRevision(ctx, run, created[0].WorktreeDir, "HEAD")
	receipt.RevertOf = revertOf
	receipt.Rebase = nil
	receipt.Route = WorktreeMergeRouteDecision{}
	receipt.PullRequest = ""
	receipt.PreviousTargetSHA = ""
	receipt.LandingSHA = ""
	receipt.CanonicalSync = ""
	receipt.Checks = githubchecks.PullRequestWaitResult{}
	receipt.Cleanup = false
	receipt.CleanupReports = nil
	receipt.CleanedTasks = nil
	receipt.Failure = ""
	receipt.UpdatedAt = time.Now().UTC()
	if err != nil {
		return receipt, err
	}
	if validationErr := validateWorktreeMergeCandidate(ctx, &receipt, timeout, retry, 0, 0, nil); validationErr != nil {
		return failWorktreeMergeReceipt(receipt, WorktreeMergeValidationFailed, fmt.Errorf("forward revert candidate validation failed: %w", validationErr))
	}
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		return receipt, err
	}
	return receipt, nil
}
