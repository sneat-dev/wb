package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sneat-dev/wb/internal/runner"
)

func canRefreshWorktreeMergeReceipt(ctx context.Context, prior WorktreeMergeReceipt, sources []WorktreeMergeSource) (bool, error) {
	return canRefreshWorktreeMergeReceiptWithRunner(ctx, defaultRunner, prior, sources)
}

func canRefreshWorktreeMergeReceiptWithRunner(ctx context.Context, run runner.Runner, prior WorktreeMergeReceipt, sources []WorktreeMergeSource) (bool, error) {
	switch prior.Status {
	case WorktreeMergePreparing, WorktreeMergePrepared, WorktreeMergeConflict, WorktreeMergeChecksFailed, WorktreeMergeChecksPending, WorktreeMergePublished:
	default:
		return false, nil
	}
	if prior.LandingSHA != "" || prior.Candidate.Worktree == "" || prior.Candidate.Branch == "" {
		return false, nil
	}
	advanced, err := worktreeMergeSourcesAdvanced(ctx, run, prior.Sources, sources)
	if err != nil {
		return false, err
	}
	if !advanced || requireCleanMergeWorktreeWithRunner(ctx, run, prior.Candidate.Worktree) != nil {
		return false, nil
	}
	remote, _, err := runCommand(ctx, run, 0, 0, prior.Candidate.Worktree, "git", "ls-remote", "--heads", "origin", "refs/heads/"+prior.Candidate.Branch)
	if err != nil {
		return false, err
	}
	remote = strings.TrimSpace(remote)
	if prior.PullRequest == "" {
		return remote == "", nil
	}
	localHead, headErr := mergeRevision(ctx, run, prior.Candidate.Worktree, "HEAD")
	if headErr != nil || localHead != prior.Candidate.SHA {
		return false, headErr
	}
	published := prior.PublishedCandidateSHA
	if published == "" {
		published = prior.Candidate.SHA
	}
	return strings.HasPrefix(remote, published+"\t"), nil
}

// canPreparePostTargetRepair recognizes the one safe continuation after a
// remote landing: exact target CI failed, the same source worktrees advanced
// additively, and the retained candidate has not moved. Prepare then advances
// that candidate to the fetched landed target before integrating the repair.
// Other landed states still own the lane and fail closed.
func canPreparePostTargetRepair(ctx context.Context, prior WorktreeMergeReceipt, sources []WorktreeMergeSource) (bool, error) {
	return canPreparePostTargetRepairWithRunner(ctx, defaultRunner, prior, sources)
}

func canPreparePostTargetRepairWithRunner(ctx context.Context, run runner.Runner, prior WorktreeMergeReceipt, sources []WorktreeMergeSource) (bool, error) {
	if prior.Status != WorktreeMergePostTargetCIFailed || prior.LandingSHA == "" ||
		prior.Candidate.Worktree == "" || prior.Candidate.Branch == "" || prior.Candidate.SHA == "" {
		return false, nil
	}
	advanced, err := worktreeMergeSourcesAdvanced(ctx, run, prior.Sources, sources)
	if err != nil {
		return false, err
	}
	if !advanced || requireCleanMergeWorktreeWithRunner(ctx, run, prior.Candidate.Worktree) != nil {
		return false, nil
	}
	localHead, err := mergeRevision(ctx, run, prior.Candidate.Worktree, "HEAD")
	if err != nil || localHead != prior.Candidate.SHA {
		return false, err
	}
	remote, _, err := runCommand(ctx, run, 0, 0, prior.Candidate.Worktree, "git", "ls-remote", "--heads", "origin", "refs/heads/"+prior.Candidate.Branch)
	if err != nil {
		return false, err
	}
	remote = strings.TrimSpace(remote)
	published := prior.PublishedCandidateSHA
	if published == "" {
		published = prior.Candidate.SHA
	}
	return remote == "" || strings.HasPrefix(remote, published+"\t"), nil
}

// validateExactPreparingWorktreeMergeReceipt defines the normal recovery
// boundary for an existing deterministic receipt path. A receipt that has left
// preparing must not be reset in place; validation_failed has explicit audited
// recovery paths instead.
func validateExactPreparingWorktreeMergeReceipt(ctx context.Context, receipt WorktreeMergeReceipt, lane, operation string, sources []WorktreeMergeSource) error {
	return validateExactPreparingWorktreeMergeReceiptWithRunner(ctx, defaultRunner, receipt, lane, operation, sources)
}

func validateExactPreparingWorktreeMergeReceiptWithRunner(ctx context.Context, run runner.Runner, receipt WorktreeMergeReceipt, lane, operation string, sources []WorktreeMergeSource) error {
	if receipt.Phase != WorktreeMergePhasePrepare || receipt.Status != WorktreeMergePreparing {
		return fmt.Errorf("receipt is %s/%s; only an exact preparing receipt may resume", receipt.Phase, receipt.Status)
	}
	if receipt.Lane != lane || receipt.ID != operation || !sameWorktreeMergeSources(receipt.Sources, sources) {
		return errors.New("receipt immutable operation identity differs")
	}
	if receipt.Candidate.Task != operation || receipt.Candidate.Worktree == "" || receipt.Candidate.Branch == "" {
		return errors.New("receipt candidate identity is incomplete or differs")
	}
	if err := requireCleanMergeWorktreeWithRunner(ctx, run, receipt.Candidate.Worktree); err != nil {
		return fmt.Errorf("receipt candidate is not safely resumable: %w", err)
	}
	if receipt.Candidate.SHA != "" {
		head, err := mergeRevision(ctx, run, receipt.Candidate.Worktree, "HEAD")
		if err != nil {
			return fmt.Errorf("read receipt candidate head: %w", err)
		}
		if head != receipt.Candidate.SHA {
			return fmt.Errorf("receipt candidate head drifted from %s to %s", receipt.Candidate.SHA, head)
		}
	}
	remote, _, err := runCommand(ctx, run, 0, 0, receipt.Candidate.Worktree, "git", "ls-remote", "--heads", "origin", "refs/heads/"+receipt.Candidate.Branch)
	if err != nil {
		return fmt.Errorf("read receipt candidate remote: %w", err)
	}
	if strings.TrimSpace(remote) != "" {
		return errors.New("receipt candidate was published")
	}
	return nil
}

// validatePreparingWorktreeMergeCandidate closes the interruption window
// between persisting an integrated candidate SHA and persisting its completed
// validation receipt. Landing may resume that exact candidate, but it must
// prove the complete source/target graph before rerunning validation.
func validatePreparingWorktreeMergeCandidate(ctx context.Context, receipt WorktreeMergeReceipt) error {
	return validatePreparingWorktreeMergeCandidateWithRunner(ctx, defaultRunner, receipt)
}

func validatePreparingWorktreeMergeCandidateWithRunner(ctx context.Context, run runner.Runner, receipt WorktreeMergeReceipt) error {
	if receipt.Phase != WorktreeMergePhasePrepare || receipt.Status != WorktreeMergePreparing || receipt.Candidate.SHA == "" {
		return errors.New("receipt has no exact interrupted preparing candidate")
	}
	if err := requireCleanMergeWorktreeWithRunner(ctx, run, receipt.Candidate.Worktree); err != nil {
		return fmt.Errorf("interrupted candidate is not clean: %w", err)
	}
	head, err := mergeRevision(ctx, run, receipt.Candidate.Worktree, "HEAD")
	if err != nil {
		return fmt.Errorf("read interrupted candidate head: %w", err)
	}
	if head != receipt.Candidate.SHA {
		return fmt.Errorf("interrupted candidate head drifted from %s to %s", receipt.Candidate.SHA, head)
	}
	containsTarget, err := isMergeAncestorWithRunner(ctx, run, receipt.Candidate.Worktree, receipt.TargetSHA, head)
	if err != nil || !containsTarget {
		if err == nil {
			err = fmt.Errorf("candidate %s does not contain target %s", head, receipt.TargetSHA)
		}
		return fmt.Errorf("verify interrupted candidate target: %w", err)
	}
	for _, source := range receipt.Sources {
		containsSource, ancestorErr := isMergeAncestorWithRunner(ctx, run, receipt.Candidate.Worktree, source.SHA, head)
		if ancestorErr != nil || !containsSource {
			if ancestorErr == nil {
				ancestorErr = fmt.Errorf("candidate %s does not contain source %s", head, source.SHA)
			}
			return fmt.Errorf("verify interrupted candidate source %s: %w", source.Branch, ancestorErr)
		}
	}
	return nil
}

// worktreeMergeSourcesAdvanced admits only the same ordered source identity
// with at least one actual descendant commit. Receipt status, candidate
// cleanliness and publication policy remain with each continuation owner.
func worktreeMergeSourcesAdvanced(ctx context.Context, run runner.Runner, previous, current []WorktreeMergeSource) (bool, error) {
	if len(previous) != len(current) {
		return false, nil
	}
	advanced := false
	for index := range current {
		oldSource, newSource := previous[index], current[index]
		if oldSource.Worktree != newSource.Worktree || oldSource.Branch != newSource.Branch || oldSource.Task != newSource.Task {
			return false, nil
		}
		if oldSource.SHA == newSource.SHA {
			continue
		}
		containsOld, err := isMergeAncestorWithRunner(ctx, run, newSource.Worktree, oldSource.SHA, newSource.SHA)
		if err != nil {
			return false, err
		}
		if !containsOld {
			return false, nil
		}
		advanced = true
	}
	return advanced, nil
}
