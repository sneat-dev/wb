package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// publishedPrepareConflictShaped is the exact durable state a prepare refresh
// of an already published candidate leaves behind when it stops before it can
// record the refreshed head: still in prepare, stopped at conflict, with the
// open pull request and the last published predecessor retained. Land-phase,
// landed, and unpublished conflicts keep their existing owners.
func publishedPrepareConflictShaped(r WorktreeMergeReceipt) bool {
	return r.Phase == WorktreeMergePhasePrepare && r.Status == WorktreeMergeConflict &&
		r.PullRequest != "" && r.PublishedCandidateSHA != "" && r.LandingSHA == ""
}

// validatePublishedPrepareConflictReceipt keeps the legacy proof boundary
// explicit: an empty candidate SHA is admissible only together with the exact
// repository, target, candidate worktree, branch, task, and source snapshots
// the proof below re-reads.
func validatePublishedPrepareConflictReceipt(r WorktreeMergeReceipt) error {
	if r.Phase != WorktreeMergePhasePrepare || r.Status != WorktreeMergeConflict ||
		r.ID == "" || r.Repository == "" || r.Target == "" || r.TargetSHA == "" ||
		r.PullRequest == "" || r.PublishedCandidateSHA == "" || r.LandingSHA != "" ||
		r.Candidate.Task == "" || r.Candidate.Worktree == "" || r.Candidate.Branch == "" ||
		len(r.Sources) == 0 {
		return fmt.Errorf("receipt %s is not an exact unlanded published prepare conflict", r.ReceiptPath)
	}
	for _, source := range r.Sources {
		if source.Worktree == "" || source.Branch == "" || source.SHA == "" {
			return fmt.Errorf("receipt %s has an incomplete receipted source", r.ReceiptPath)
		}
	}
	return nil
}

// publishedPrepareConflictMissingSource reports whether the recorded candidate
// cannot account for the receipt's own current source snapshots. That is the
// failed-refresh shape this recovery owns; an ordinary published conflict whose
// candidate already contains every source keeps its existing advance, target
// refresh, and validation owners.
func publishedPrepareConflictMissingSource(ctx context.Context, run runner.Runner, r WorktreeMergeReceipt) (bool, error) {
	for _, source := range r.Sources {
		contains, err := isMergeAncestorWithRunner(ctx, run, r.Candidate.Worktree, source.SHA, r.Candidate.SHA)
		if err != nil {
			return false, fmt.Errorf("inspect published prepare candidate source %s: %w", source.Branch, err)
		}
		if !contains {
			return true, nil
		}
	}
	return false, nil
}

// recoverPublishedPrepareConflictCandidate proves, before recording anything,
// that a clean managed candidate descends from the exact published predecessor,
// the recorded target, the recorded candidate (when one is still named), and
// every exact source snapshot, while the open pull request and its remote
// branch still stand at that predecessor. The predecessor supplies the
// identity to PROVE, never permission: every fact is re-read from private Git
// and the hosted pull request under the caller's existing operation and lane
// locks, and a refusal leaves the receipt untouched so the next ordinary
// resume re-proves the same evidence.
func recoverPublishedPrepareConflictCandidate(ctx context.Context, projectsRoot string, receipt *WorktreeMergeReceipt, timeout time.Duration, retry int) (bool, error) {
	return recoverPublishedPrepareConflictCandidateWithRunner(ctx, defaultRunner, projectsRoot, receipt, timeout, retry)
}

func recoverPublishedPrepareConflictCandidateWithRunner(ctx context.Context, run runner.Runner, projectsRoot string, receipt *WorktreeMergeReceipt, timeout time.Duration, retry int) (bool, error) {
	if receipt == nil || !publishedPrepareConflictShaped(*receipt) {
		return false, nil
	}
	if err := validatePublishedPrepareConflictReceipt(*receipt); err != nil {
		return false, err
	}
	if receipt.Candidate.SHA != "" {
		missing, err := publishedPrepareConflictMissingSource(ctx, run, *receipt)
		if err != nil {
			return false, err
		}
		if !missing {
			return false, nil
		}
	}

	guard, err := worktrees.Guard(ctx, receipt.Candidate.Worktree, worktrees.GuardOptions{ProjectsRoot: projectsRoot, Base: receipt.Target})
	if err != nil {
		return false, fmt.Errorf("guard published prepare candidate: %w", err)
	}
	if guard.Kind != "linked" || guard.Transient || filepath.Clean(guard.Path) != filepath.Clean(receipt.Candidate.Worktree) || guard.Branch != receipt.Candidate.Branch {
		return false, errors.New("published prepare candidate worktree or branch does not match WB Guard")
	}
	expectedCanonical, canonicalMatches, canonicalErr := recoveryCanonicalMatches(projectsRoot, receipt.Repository, guard.CanonicalDir)
	if canonicalErr != nil {
		return false, canonicalErr
	}
	if !canonicalMatches {
		return false, fmt.Errorf("published prepare candidate canonical repository does not match %s", expectedCanonical)
	}
	view, err := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: projectsRoot, Worktree: guard.Path})
	if err != nil {
		return false, fmt.Errorf("load Work Log for published prepare candidate: %w", err)
	}
	if !recoveryClaimMatches(view.Claim, *receipt, guard.Path) {
		return false, errors.New("published prepare candidate has no active Work Log claim matching its exact task, repository, worktree, branch, and target")
	}
	if err := requireCleanMergeWorktreeWithRunner(ctx, run, guard.Path); err != nil {
		return false, fmt.Errorf("published prepare candidate: %w", err)
	}
	if err := recheckWorktreeMergeSourcesWithRunner(ctx, run, receipt.Sources); err != nil {
		return false, err
	}
	head, err := mergeRevision(ctx, run, guard.Path, "HEAD")
	if err != nil {
		return false, err
	}
	currentTarget, err := fetchExactMergeTargetWithRunner(ctx, run, guard.Path, receipt.Target)
	if err != nil {
		return false, err
	}
	if currentTarget != receipt.TargetSHA {
		return false, fmt.Errorf("target drifted from recorded %s to %s while the published prepare candidate was unresolved", receipt.TargetSHA, currentTarget)
	}
	if view.Claim.BaseSHA != receipt.TargetSHA {
		if err := proveConflictTargetNormalizationWithRunner(ctx, run, guard.Path, receipt.Target, receipt.TargetSHA, view.Claim.BaseSHA, head, timeout, retry); err != nil {
			return false, fmt.Errorf("published prepare candidate Work Log claim base differs from receipt target: %w", err)
		}
	}
	roots := []string{receipt.TargetSHA, receipt.PublishedCandidateSHA}
	if receipt.Candidate.SHA != "" {
		roots = append(roots, receipt.Candidate.SHA)
	}
	roots = append(roots, sourceSHAs(receipt.Sources)...)
	for _, root := range roots {
		contains, ancestorErr := isMergeAncestorWithRunner(ctx, run, guard.Path, root, head)
		if ancestorErr != nil || !contains {
			if ancestorErr == nil {
				ancestorErr = fmt.Errorf("published prepare candidate %s does not contain required immutable root %s", head, root)
			}
			return false, ancestorErr
		}
	}

	publication := *receipt
	publication.Candidate.SHA = receipt.PublishedCandidateSHA
	if err := provePublishedCandidatePullRequest(ctx, run, publication, receipt.PullRequest); err != nil {
		return false, fmt.Errorf("prove published predecessor pull request: %w", err)
	}

	recordRecoveredMergeCandidate(receipt, head)
	return true, nil
}
