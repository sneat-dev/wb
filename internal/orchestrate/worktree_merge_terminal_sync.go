package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// recoverAlreadyTerminalizedWorktreeMergeCleanup handles the narrow crash and
// cross-session recovery case where supported cleanup has already sealed and
// removed one or more exact receipt worktrees, but did not update this merge
// receipt. It trusts neither an absent worktree nor a caller-supplied cleanup
// report: immutable claim+terminal evidence must reproduce every absent
// identity before remaining live assets may enter ordinary cleanup.
func recoverAlreadyTerminalizedWorktreeMergeCleanup(ctx context.Context, projectsRoot string, receipt *WorktreeMergeReceipt, timeout time.Duration, retry int) (bool, error) {
	if receipt == nil {
		return false, errors.New("nil merge receipt")
	}
	expectations, err := terminalWorkLogExpectations(*receipt)
	if err != nil {
		return false, err
	}
	absent := make([]worktrees.TerminalWorkLogExpectation, 0, len(expectations))
	for _, expectation := range expectations {
		if _, statErr := os.Lstat(expectation.Worktree); statErr == nil {
			continue
		} else if os.IsNotExist(statErr) {
			absent = append(absent, expectation)
		} else {
			return false, fmt.Errorf("inspect receipted cleanup worktree %s: %w", expectation.Worktree, statErr)
		}
	}
	if len(absent) == 0 {
		return false, nil
	}
	if err := worktrees.ValidateRemovedTerminalWorkLogs(projectsRoot, absent); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("exact removed Work Log evidence does not corroborate completed cleanup: %w", err)
		}
		if len(absent) != len(expectations) {
			return false, fmt.Errorf("exact removed Work Log evidence does not corroborate partially terminalized cleanup: %w", err)
		}
		ackPath := receipt.ReceiptPath + worktreeMergeMissingCleanupAcknowledgementSuffix
		if _, ackErr := validateMissingCleanupAcknowledgement(ctx, projectsRoot, *receipt, ackPath, timeout, retry); ackErr != nil {
			return false, fmt.Errorf("exact removed Work Log evidence does not corroborate completed cleanup: %w; audited missing-cleanup recovery unavailable: %v", err, ackErr)
		}
	}
	if err := requireTerminalCleanupBranchesAbsent(ctx, projectsRoot, *receipt, absent, timeout, retry); err != nil {
		return false, err
	}
	cleaned := make(map[string]bool, len(receipt.CleanedTasks)+len(absent))
	for _, task := range receipt.CleanedTasks {
		cleaned[task] = true
	}
	for _, expectation := range absent {
		if !cleaned[expectation.Task] {
			receipt.CleanedTasks = append(receipt.CleanedTasks, expectation.Task)
			cleaned[expectation.Task] = true
		}
	}
	sort.Strings(receipt.CleanedTasks)
	if len(absent) != len(expectations) {
		receipt.UpdatedAt = time.Now().UTC()
		if err := persistWorktreeMergeReceipt(*receipt); err != nil {
			return false, err
		}
		return false, nil
	}
	return true, nil
}

func syncCanonicalMergeTarget(ctx context.Context, canonical, target, landing string, timeout time.Duration, retry int, checkoutUpdated func(context.Context, CheckoutUpdate)) (string, error) {
	return syncCanonicalMergeTargetWithRunner(ctx, defaultRunner, canonical, target, landing, timeout, retry, checkoutUpdated)
}

func syncCanonicalMergeTargetWithRunner(ctx context.Context, run runner.Runner, canonical, target, landing string, timeout time.Duration, retry int, checkoutUpdated func(context.Context, CheckoutUpdate)) (string, error) {
	branch, _, err := runCommand(ctx, run, timeout, retry, canonical, "git", "branch", "--show-current")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(branch) != target {
		return "not_checked_out", nil
	}
	if err := requireCleanMergeWorktreeWithRunner(ctx, run, canonical); err != nil {
		return "blocked_dirty", fmt.Errorf("remote landed, but canonical target synchronization is blocked: %w", err)
	}
	beforeHead, err := mergeRevision(ctx, run, canonical, "HEAD")
	if err != nil {
		return "", err
	}
	if _, _, err := runCommand(ctx, run, timeout, retry, canonical, "git", "fetch", "--no-tags", "origin", "+refs/heads/"+target+":refs/remotes/origin/"+target); err != nil {
		return "blocked_fetch", err
	}
	if _, _, err := runCommand(ctx, run, timeout, retry, canonical, "git", "merge", "--ff-only", "refs/remotes/origin/"+target); err != nil {
		return "blocked_diverged", fmt.Errorf("remote landed, but canonical target cannot fast-forward: %w", err)
	}
	head, err := mergeRevision(ctx, run, canonical, "HEAD")
	containsLanding, ancestorErr := isMergeAncestorWithRunner(ctx, run, canonical, landing, head)
	if err != nil || ancestorErr != nil || !containsLanding {
		if err == nil {
			err = ancestorErr
		}
		if err == nil {
			err = fmt.Errorf("canonical target %s does not contain exact landed head %s", head, landing)
		}
		return "blocked_mismatch", err
	}
	if checkoutUpdated != nil && head != beforeHead {
		checkoutUpdated(ctx, CheckoutUpdate{Checkout: canonical, OldSHA: beforeHead, NewSHA: head, Cause: "merge-land"})
	}
	return "fast_forwarded", nil
}

func sortedUniqueMergeTasks(receipt WorktreeMergeReceipt) []string {
	seen := map[string]bool{}
	tasks := make([]string, 0, len(receipt.Sources)+1)
	for _, source := range receipt.Sources {
		if source.Task != "" && !seen[source.Task] {
			seen[source.Task] = true
			tasks = append(tasks, source.Task)
		}
	}
	if receipt.Candidate.Task != "" && !seen[receipt.Candidate.Task] {
		seen[receipt.Candidate.Task] = true
		tasks = append(tasks, receipt.Candidate.Task)
	}
	for _, candidate := range receipt.RebatchedCandidates {
		if candidate.Task != "" && !seen[candidate.Task] {
			seen[candidate.Task] = true
			tasks = append(tasks, candidate.Task)
		}
	}
	sort.Strings(tasks)
	return tasks
}

func terminalWorkLogExpectations(receipt WorktreeMergeReceipt) ([]worktrees.TerminalWorkLogExpectation, error) {
	if receipt.Repository == "" || receipt.Target == "" || receipt.Candidate.Task == "" || receipt.Candidate.Worktree == "" ||
		receipt.Candidate.Branch == "" || receipt.Candidate.SHA == "" {
		return nil, errors.New("receipt lacks exact candidate identity for terminal cleanup recovery")
	}
	expectations := []worktrees.TerminalWorkLogExpectation{{
		Task: receipt.Candidate.Task, Repository: receipt.Repository, Worktree: receipt.Candidate.Worktree,
		Branch: receipt.Candidate.Branch, Base: receipt.Target, FinalCommit: receipt.Candidate.SHA,
	}}
	byTask := map[string]worktrees.TerminalWorkLogExpectation{receipt.Candidate.Task: expectations[0]}
	for _, source := range receipt.Sources {
		if source.Task == "" || source.Worktree == "" || source.Branch == "" || source.SHA == "" {
			return nil, errors.New("receipt lacks exact source identity for terminal cleanup recovery")
		}
		expectation := worktrees.TerminalWorkLogExpectation{
			Task: source.Task, Repository: receipt.Repository, Worktree: source.Worktree,
			Branch: source.Branch, FinalCommit: source.SHA,
		}
		var err error
		expectations, err = appendTerminalWorkLogExpectation(expectations, byTask, expectation)
		if err != nil {
			return nil, err
		}
	}
	for _, candidate := range receipt.RebatchedCandidates {
		if candidate.Task == "" || candidate.Worktree == "" || candidate.Branch == "" || candidate.SHA == "" {
			return nil, errors.New("receipt lacks exact rebatched candidate identity for terminal cleanup recovery")
		}
		expectation := worktrees.TerminalWorkLogExpectation{
			Task: candidate.Task, Repository: receipt.Repository, Worktree: candidate.Worktree,
			Branch: candidate.Branch, Base: receipt.Target, FinalCommit: candidate.SHA,
		}
		var err error
		expectations, err = appendTerminalWorkLogExpectation(expectations, byTask, expectation)
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(expectations, func(i, j int) bool { return expectations[i].Task < expectations[j].Task })
	return expectations, nil
}

func appendTerminalWorkLogExpectation(expectations []worktrees.TerminalWorkLogExpectation, byTask map[string]worktrees.TerminalWorkLogExpectation, expectation worktrees.TerminalWorkLogExpectation) ([]worktrees.TerminalWorkLogExpectation, error) {
	if previous, exists := byTask[expectation.Task]; exists {
		if previous != expectation {
			return nil, fmt.Errorf("receipt has conflicting terminal cleanup identities for task %s", expectation.Task)
		}
		return expectations, nil
	}
	byTask[expectation.Task] = expectation
	return append(expectations, expectation), nil
}
