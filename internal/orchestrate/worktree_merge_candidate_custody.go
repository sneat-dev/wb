package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func validateMergeAcknowledgementCandidate(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt, candidate WorktreeMergeCandidate) (*worktrees.WorkLogClaimView, error) {
	return validateMergeAcknowledgementCandidateWithRunner(ctx, defaultRunner, projectsRoot, receipt, candidate)
}

func validateMergeAcknowledgementCandidateWithRunner(ctx context.Context, run runner.Runner, projectsRoot string, receipt WorktreeMergeReceipt, candidate WorktreeMergeCandidate) (*worktrees.WorkLogClaimView, error) {
	guard, err := worktrees.Guard(ctx, candidate.Worktree, worktrees.GuardOptions{ProjectsRoot: projectsRoot, Base: receipt.Target})
	if err != nil {
		return nil, fmt.Errorf("guard candidate %s: %w", candidate.Worktree, err)
	}
	if guard.Kind != "linked" || guard.Transient || guard.Branch != candidate.Branch || filepath.Clean(guard.Path) != filepath.Clean(candidate.Worktree) {
		return nil, fmt.Errorf("candidate %s no longer has its exact linked-worktree identity", candidate.Worktree)
	}
	if err := requireCleanMergeWorktreeWithRunner(ctx, run, candidate.Worktree); err != nil {
		return nil, fmt.Errorf("candidate is not clean: %w", err)
	}
	head, err := mergeRevision(ctx, run, candidate.Worktree, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("read candidate HEAD: %w", err)
	}
	if head != candidate.SHA {
		return nil, fmt.Errorf("candidate HEAD %s does not match receipted candidate %s", head, candidate.SHA)
	}
	view, err := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: projectsRoot, Worktree: candidate.Worktree})
	if err != nil {
		return nil, fmt.Errorf("load candidate Work Log: %w", err)
	}
	if !mergeClaimMatchesIdentity(view.Claim, receipt.Repository, candidate.Task, candidate.Worktree, candidate.Branch) || view.Claim.Base != receipt.Target || view.Claim.BaseSHA == "" {
		return nil, errors.New("candidate has no active Work Log claim matching the immutable receipt target and identity")
	}
	return view.Claim, nil
}

func validatePrepareFailureSupersessionCandidate(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt) (*worktrees.WorkLogClaimView, string, error) {
	return validatePrepareFailureSupersessionCandidateWithRunner(ctx, defaultRunner, projectsRoot, receipt)
}

func validatePrepareFailureSupersessionCandidateWithRunner(ctx context.Context, run runner.Runner, projectsRoot string, receipt WorktreeMergeReceipt) (*worktrees.WorkLogClaimView, string, error) {
	if receipt.Status != WorktreeMergeConflict && receipt.Status != WorktreeMergeValidationFailed {
		claim, err := validateMergeAcknowledgementCandidateWithRunner(ctx, run, projectsRoot, receipt, receipt.Candidate)
		return claim, "", err
	}
	observedHead, err := mergeRevision(ctx, run, receipt.Candidate.Worktree, "HEAD")
	if err != nil {
		return nil, "", fmt.Errorf("read candidate HEAD: %w", err)
	}
	observedCandidate := receipt.Candidate
	observedCandidate.SHA = observedHead
	claim, err := validateMergeAcknowledgementCandidateWithRunner(ctx, run, projectsRoot, receipt, observedCandidate)
	if err != nil {
		return nil, "", err
	}
	if observedHead == receipt.Candidate.SHA {
		return claim, "", nil
	}
	contains, err := isMergeAncestorWithRunner(ctx, run, receipt.Candidate.Worktree, receipt.Candidate.SHA, observedHead)
	if err != nil {
		return nil, "", fmt.Errorf("verify candidate descendant ancestry: %w", err)
	}
	if !contains {
		return nil, "", fmt.Errorf("candidate HEAD %s is not a descendant of receipted candidate %s", observedHead, receipt.Candidate.SHA)
	}
	return claim, observedHead, nil
}

func validateValidationFailureReplacementWithRunner(ctx context.Context, run runner.Runner, projectsRoot string, receipt WorktreeMergeReceipt, replacementPath string) (WorktreeMergeCandidate, *worktrees.WorkLogClaimView, error) {
	guard, err := worktrees.Guard(ctx, replacementPath, worktrees.GuardOptions{ProjectsRoot: projectsRoot, Base: receipt.Target})
	if err != nil {
		return WorktreeMergeCandidate{}, nil, fmt.Errorf("guard replacement worktree %s: %w", replacementPath, err)
	}
	if guard.Kind != "linked" || guard.Transient || guard.Branch == receipt.Target {
		return WorktreeMergeCandidate{}, nil, fmt.Errorf("replacement worktree %s has no exact non-target linked-worktree identity", replacementPath)
	}
	if err := requireCleanMergeWorktreeWithRunner(ctx, run, guard.Path); err != nil {
		return WorktreeMergeCandidate{}, nil, fmt.Errorf("replacement is not clean: %w", err)
	}
	head, err := mergeRevision(ctx, run, guard.Path, "HEAD")
	if err != nil {
		return WorktreeMergeCandidate{}, nil, fmt.Errorf("read replacement HEAD: %w", err)
	}
	view, err := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: projectsRoot, Worktree: guard.Path})
	if err != nil {
		return WorktreeMergeCandidate{}, nil, fmt.Errorf("load replacement Work Log: %w", err)
	}
	if view.Claim == nil || view.Claim.Task == "" || !mergeClaimMatchesIdentity(view.Claim, receipt.Repository, view.Claim.Task, guard.Path, guard.Branch) || view.Claim.Base != receipt.Target || view.Claim.BaseSHA == "" {
		return WorktreeMergeCandidate{}, nil, errors.New("replacement has no authoritative active Work Log claim matching its identity")
	}
	return WorktreeMergeCandidate{Task: view.Claim.Task, Worktree: guard.Path, Branch: guard.Branch, SHA: head}, view.Claim, nil
}

// mergeClaimMatchesIdentity shares only active static identity. Base names and
// immutable base SHA requirements remain with each custody policy owner.
func mergeClaimMatchesIdentity(claim *worktrees.WorkLogClaimView, repository, task, worktree, branch string) bool {
	return claim != nil && claim.Lifecycle == "active" && claim.Repository == repository && claim.Task == task && filepath.Clean(claim.Worktree) == filepath.Clean(worktree) && claim.Branch == branch
}
