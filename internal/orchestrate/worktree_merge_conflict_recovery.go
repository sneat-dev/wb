package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// recoverResolvedWorktreeMergeCandidate repairs the one durable prepare gap
// where a conflict receipt necessarily predates the human resolution commit.
// It accepts only the exact receipted WB candidate and proves its Work Log,
// branch, target, sources, unpublished state, cleanliness, and validation
// before recording the resolved commit. Every other empty-SHA receipt remains
// a hard refusal.
func recoverResolvedWorktreeMergeCandidate(ctx context.Context, projectsRoot string, receipt *WorktreeMergeReceipt, timeout time.Duration, retry int) (bool, error) {
	return recoverResolvedCandidateWithRunner(ctx, defaultRunner, projectsRoot, receipt, timeout, retry)
}

func recoverResolvedCandidateWithRunner(ctx context.Context, run runner.Runner, projectsRoot string, receipt *WorktreeMergeReceipt, timeout time.Duration, retry int) (bool, error) {
	if receipt == nil || receipt.Candidate.SHA != "" {
		return false, nil
	}
	if receipt.Phase != WorktreeMergePhasePrepare ||
		(receipt.Status != WorktreeMergeConflict && receipt.Status != WorktreeMergeValidationFailed) ||
		receipt.LandingSHA != "" || receipt.PullRequest != "" || receipt.PublishedCandidateSHA != "" ||
		receipt.Candidate.Task == "" || receipt.Candidate.Worktree == "" || receipt.Candidate.Branch == "" ||
		receipt.Repository == "" || receipt.Target == "" || receipt.TargetSHA == "" || len(receipt.Sources) == 0 {
		return false, fmt.Errorf("receipt %s has no recoverable prepared candidate", receipt.ReceiptPath)
	}

	guard, err := worktrees.Guard(ctx, receipt.Candidate.Worktree, worktrees.GuardOptions{ProjectsRoot: projectsRoot, Base: receipt.Target})
	if err != nil {
		return false, fmt.Errorf("guard receipted candidate: %w", err)
	}
	if guard.Kind != "linked" || guard.Transient || filepath.Clean(guard.Path) != filepath.Clean(receipt.Candidate.Worktree) || guard.Branch != receipt.Candidate.Branch {
		return false, fmt.Errorf("receipted candidate worktree or branch does not match WB Guard")
	}
	expectedCanonical, canonicalMatches, canonicalErr := recoveryCanonicalMatches(projectsRoot, receipt.Repository, guard.CanonicalDir)
	if canonicalErr != nil {
		return false, canonicalErr
	}
	if !canonicalMatches {
		return false, fmt.Errorf("receipted candidate canonical repository does not match %s", expectedCanonical)
	}
	view, err := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: projectsRoot, Worktree: guard.Path})
	if err != nil {
		return false, fmt.Errorf("load Work Log for receipted candidate: %w", err)
	}
	if !recoveryClaimMatches(view.Claim, *receipt, guard.Path) {
		return false, fmt.Errorf("receipted candidate Work Log claim does not match task, repository, worktree, branch, or base")
	}
	if err := requireCleanMergeWorktreeWithRunner(ctx, run, guard.Path); err != nil {
		return false, fmt.Errorf("receipted candidate: %w", err)
	}
	remote, _, err := runCommand(ctx, run, timeout, retry, guard.Path, "git", "ls-remote", "--heads", "origin", "refs/heads/"+receipt.Candidate.Branch)
	if err != nil {
		return false, fmt.Errorf("inspect receipted candidate publication state: %w", err)
	}
	if strings.TrimSpace(remote) != "" {
		return false, fmt.Errorf("receipted candidate branch %s is already published without a recorded candidate SHA", receipt.Candidate.Branch)
	}
	if err := recheckWorktreeMergeSourcesWithRunner(ctx, run, receipt.Sources); err != nil {
		return false, err
	}

	head, err := mergeRevision(ctx, run, guard.Path, "HEAD")
	if err != nil {
		return false, err
	}
	if view.Claim.BaseSHA != receipt.TargetSHA {
		if err := proveConflictTargetNormalizationWithRunner(ctx, run, guard.Path, receipt.Target, receipt.TargetSHA, view.Claim.BaseSHA, head, timeout, retry); err != nil {
			return false, fmt.Errorf("receipted candidate Work Log claim base differs from receipt target: %w", err)
		}
	}
	containsTarget, err := isMergeAncestorWithRunner(ctx, run, guard.Path, receipt.TargetSHA, head)
	if err != nil || !containsTarget {
		if err == nil {
			err = fmt.Errorf("resolved candidate %s does not contain recorded target %s", head, receipt.TargetSHA)
		}
		return false, err
	}
	for _, source := range receipt.Sources {
		containsSource, ancestorErr := isMergeAncestorWithRunner(ctx, run, guard.Path, source.SHA, head)
		if ancestorErr != nil || !containsSource {
			if ancestorErr == nil {
				ancestorErr = fmt.Errorf("resolved candidate %s does not contain receipted source %s", head, source.SHA)
			}
			return false, ancestorErr
		}
	}

	recordRecoveredMergeCandidate(receipt, head)
	return true, nil
}

// advanceResolvedConflictWorktreeMergeCandidate records the only permitted
// non-empty candidate movement after prepare has stopped at a conflict. A
// human may commit a clean resolution into WB's preserved candidate worktree;
// this proves that exact descendant before the mutable receipt can name it.
func advanceResolvedConflictWorktreeMergeCandidate(ctx context.Context, projectsRoot string, receipt *WorktreeMergeReceipt, timeout time.Duration, retry int) (bool, error) {
	return advanceResolvedConflictCandidateWithRunner(ctx, defaultRunner, projectsRoot, receipt, timeout, retry, nativeConflictAdvanceStore())
}

func advanceResolvedConflictCandidateWithRunner(ctx context.Context, run runner.Runner, projectsRoot string, receipt *WorktreeMergeReceipt, timeout time.Duration, retry int, store conflictAdvanceStore) (bool, error) {
	if receipt == nil || receipt.Candidate.SHA == "" || receipt.Status != WorktreeMergeConflict {
		return false, nil
	}
	// Older WB versions converted an otherwise recoverable published-candidate
	// drift into conflict before the recorded-predecessor path could inspect it.
	// Leave that state for advancePublishedWorktreeMergeCandidate below; this
	// helper owns only unpublished prepare conflicts.
	if receipt.PullRequest != "" && receipt.PublishedCandidateSHA != "" && receipt.LandingSHA == "" {
		return false, nil
	}
	if receipt.Phase != WorktreeMergePhasePrepare || receipt.LandingSHA != "" || receipt.PullRequest != "" || receipt.PublishedCandidateSHA != "" ||
		receipt.Candidate.Task == "" || receipt.Candidate.Worktree == "" || receipt.Candidate.Branch == "" ||
		receipt.Repository == "" || receipt.Target == "" || receipt.TargetSHA == "" || len(receipt.Sources) == 0 {
		return false, fmt.Errorf("receipt %s has no recoverable conflict candidate", receipt.ReceiptPath)
	}
	guard, err := worktrees.Guard(ctx, receipt.Candidate.Worktree, worktrees.GuardOptions{ProjectsRoot: projectsRoot, Base: receipt.Target})
	if err != nil {
		return false, fmt.Errorf("guard receipted conflict candidate: %w", err)
	}
	if guard.Kind != "linked" || guard.Transient || filepath.Clean(guard.Path) != filepath.Clean(receipt.Candidate.Worktree) || guard.Branch != receipt.Candidate.Branch {
		return false, errors.New("receipted conflict candidate worktree or branch does not match WB Guard")
	}
	expectedCanonical, canonicalMatches, canonicalErr := recoveryCanonicalMatches(projectsRoot, receipt.Repository, guard.CanonicalDir)
	if canonicalErr != nil {
		return false, canonicalErr
	}
	if !canonicalMatches {
		return false, fmt.Errorf("receipted conflict candidate canonical repository does not match %s", expectedCanonical)
	}
	view, err := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: projectsRoot, Worktree: guard.Path})
	if err != nil {
		return false, fmt.Errorf("load Work Log for receipted conflict candidate: %w", err)
	}
	if !recoveryClaimMatches(view.Claim, *receipt, guard.Path) || view.Claim.BaseSHA != receipt.TargetSHA {
		return false, errors.New("receipted conflict candidate Work Log claim does not match its exact receipt identity and target")
	}
	if err := requireCleanMergeWorktreeWithRunner(ctx, run, guard.Path); err != nil {
		return false, fmt.Errorf("receipted conflict candidate: %w", err)
	}
	head, err := mergeRevision(ctx, run, guard.Path, "HEAD")
	if err != nil {
		return false, err
	}
	if head == receipt.Candidate.SHA {
		return false, nil
	}
	containsOriginal, err := isMergeAncestorWithRunner(ctx, run, guard.Path, receipt.Candidate.SHA, head)
	if err != nil {
		return false, fmt.Errorf("verify receipted candidate ancestry: %w", err)
	}
	if !containsOriginal {
		return false, fmt.Errorf("candidate HEAD %s is not a descendant of receipted candidate %s", head, receipt.Candidate.SHA)
	}
	remoteCandidate, _, err := runCommand(ctx, run, timeout, retry, guard.Path, "git", "ls-remote", "--heads", "origin", "refs/heads/"+receipt.Candidate.Branch)
	if err != nil {
		return false, fmt.Errorf("inspect receipted conflict candidate publication state: %w", err)
	}
	if strings.TrimSpace(remoteCandidate) != "" {
		return false, fmt.Errorf("receipted conflict candidate branch %s is already published without a consistent published predecessor", receipt.Candidate.Branch)
	}
	if err := recheckWorktreeMergeSourcesWithRunner(ctx, run, receipt.Sources); err != nil {
		return false, err
	}
	currentTarget, err := fetchExactMergeTargetWithRunner(ctx, run, guard.Path, receipt.Target)
	if err != nil {
		return false, err
	}
	if currentTarget != receipt.TargetSHA {
		return false, fmt.Errorf("target drifted from recorded %s to %s while conflict candidate was resolved", receipt.TargetSHA, currentTarget)
	}
	for _, root := range append([]string{receipt.TargetSHA}, sourceSHAs(receipt.Sources)...) {
		contains, ancestorErr := isMergeAncestorWithRunner(ctx, run, guard.Path, root, head)
		if ancestorErr != nil || !contains {
			if ancestorErr == nil {
				ancestorErr = fmt.Errorf("resolved candidate %s does not contain required immutable root %s", head, root)
			}
			return false, ancestorErr
		}
	}
	receiptHash, err := store.receiptSHA256(receipt.ReceiptPath)
	if err != nil {
		return false, err
	}
	ackPath := conflictCandidateAdvancePath(receipt.ReceiptPath)
	ack := WorktreeMergeConflictCandidateAdvance{
		SchemaVersion: worktreeMergeConflictCandidateAdvanceSchemaVersion, Status: "conflict_candidate_advanced",
		ReceiptPath: receipt.ReceiptPath, AcknowledgementPath: ackPath, ReceiptSHA256: receiptHash,
		ReceiptID: receipt.ID, Lane: receipt.Lane, Repository: receipt.Repository, Target: receipt.Target,
		ReceiptTargetSHA: receipt.TargetSHA, CurrentTargetSHA: currentTarget, OriginalCandidate: receipt.Candidate,
		AdvancedCandidateSHA: head, ClaimBaseSHA: view.Claim.BaseSHA, Sources: append([]WorktreeMergeSource(nil), receipt.Sources...), RecordedAt: time.Now().UTC(),
	}
	ack.ID = conflictCandidateAdvanceID(ack)
	if existing, readErr := store.read(ackPath); readErr == nil {
		if existing.ID != ack.ID {
			return false, fmt.Errorf("conflict-candidate advance %s binds different immutable evidence", ackPath)
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return false, readErr
	} else if err := store.persist(ackPath, ack); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return false, err
		}
		existing, readErr := store.read(ackPath)
		if readErr != nil || existing.ID != ack.ID {
			if readErr != nil {
				return false, readErr
			}
			return false, fmt.Errorf("concurrent conflict-candidate advance %s binds different immutable evidence", ackPath)
		}
	}
	recordRecoveredMergeCandidate(receipt, head)
	return true, nil
}

// conflictCandidateAdvanceNeedsValidation closes the interruption window after
// the acknowledgement is durable but before validation has become terminal.
func conflictCandidateAdvanceNeedsValidation(receipt WorktreeMergeReceipt) (bool, error) {
	return conflictCandidateNeedsValidationWithStore(receipt, nativeConflictAdvanceStore())
}

func conflictCandidateNeedsValidationWithStore(receipt WorktreeMergeReceipt, store conflictAdvanceStore) (bool, error) {
	ack, err := store.read(conflictCandidateAdvancePath(receipt.ReceiptPath))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// A recorded server update (the PR-land engine's update-branch advance;
	// see TargetRefreshes and red-team finding M2) can legitimately move
	// receipt.TargetSHA past the snapshot this acknowledgement took. That is
	// not a mismatch to hard-fail on: the receipt's own append-only history
	// proves why it changed.
	targetMatches := ack.ReceiptTargetSHA == receipt.TargetSHA && ack.CurrentTargetSHA == receipt.TargetSHA ||
		worktreeMergeTargetAdvanceRecorded(receipt, ack.ReceiptTargetSHA, receipt.TargetSHA) &&
			worktreeMergeTargetAdvanceRecorded(receipt, ack.CurrentTargetSHA, receipt.TargetSHA)
	// The same tolerance applies to the candidate side (red-team finding
	// M2): one or more recorded server-side update-branch advances since
	// this acknowledgement was taken must not be mistaken for a mismatch
	// either. worktreeMergeCandidateAdvanceRecorded walks every hop
	// TargetRefreshes records, not just a single one.
	candidateMatches := ack.AdvancedCandidateSHA == receipt.Candidate.SHA ||
		worktreeMergeCandidateAdvanceRecorded(receipt, ack.AdvancedCandidateSHA, receipt.Candidate.SHA)
	if ack.ReceiptPath != receipt.ReceiptPath || ack.ReceiptID != receipt.ID || ack.Lane != receipt.Lane ||
		ack.Repository != receipt.Repository || ack.Target != receipt.Target || !targetMatches ||
		!sameWorktreeMergeSources(ack.Sources, receipt.Sources) ||
		ack.OriginalCandidate.Task != receipt.Candidate.Task || ack.OriginalCandidate.Worktree != receipt.Candidate.Worktree ||
		ack.OriginalCandidate.Branch != receipt.Candidate.Branch || !candidateMatches {
		return false, fmt.Errorf("conflict-candidate advance %s does not match the current receipt", ack.AcknowledgementPath)
	}
	if receipt.Status == WorktreeMergePrepared {
		if receipt.Validation.Revision != receipt.Candidate.SHA || receipt.Validation.Status != quality.StatusPassed {
			return false, fmt.Errorf("advanced conflict candidate %s has no matching successful validation", receipt.Candidate.SHA)
		}
		return false, nil
	}
	return receipt.Status == WorktreeMergePreparing, nil
}

// proveConflictResolvedCandidateTargetNormalization permits the one explicit
// recovery exception for a conflict-resolved candidate whose immutable Work
// Log base predates the receipt target snapshot. It never rewrites either
// historical record. The candidate must already contain the immutable claim
// base, the receipt target, and the freshly fetched current remote target; the
// caller separately proves every receipted source is also contained.
func proveConflictResolvedCandidateTargetNormalization(ctx context.Context, worktree, target, receiptTarget, claimBase, head string, timeout time.Duration, retry int) error {
	return proveConflictTargetNormalizationWithRunner(ctx, defaultRunner, worktree, target, receiptTarget, claimBase, head, timeout, retry)
}

func proveConflictTargetNormalizationWithRunner(ctx context.Context, run runner.Runner, worktree, target, receiptTarget, claimBase, head string, timeout time.Duration, retry int) error {
	for _, evidence := range []struct {
		label    string
		revision string
	}{
		{label: "immutable Work Log base", revision: claimBase},
		{label: "receipt target", revision: receiptTarget},
	} {
		contains, err := isMergeAncestorWithRunner(ctx, run, worktree, evidence.revision, head)
		if err != nil {
			return err
		}
		if !contains {
			return fmt.Errorf("resolved candidate %s does not contain %s %s", head, evidence.label, evidence.revision)
		}
	}
	currentTarget, err := fetchExactMergeTargetWithRunner(ctx, run, worktree, target)
	if err != nil {
		return err
	}
	containsCurrentTarget, err := isMergeAncestorWithRunner(ctx, run, worktree, currentTarget, head)
	if err != nil {
		return err
	}
	if !containsCurrentTarget {
		return fmt.Errorf("resolved candidate %s does not contain current remote target %s", head, currentTarget)
	}
	return nil
}

// recoveryCanonicalMatches keeps the native canonical/symlink proof shared by
// both unpublished conflict recovery paths. Failed symlink resolution retains
// the original lexical path, matching the existing guard policy.
func recoveryCanonicalMatches(projectsRoot, repository, actualCanonical string) (string, bool, error) {
	expected, err := worktrees.CanonicalRepositoryPath(projectsRoot, repository)
	if err != nil {
		return "", false, err
	}
	if resolved, err := filepath.EvalSymlinks(expected); err == nil {
		expected = resolved
	}
	if resolved, err := filepath.EvalSymlinks(actualCanonical); err == nil {
		actualCanonical = resolved
	}
	return expected, filepath.Clean(actualCanonical) == filepath.Clean(expected), nil
}

// BaseSHA is deliberately excluded: empty-SHA recovery proves historical-base
// normalization separately, while recorded conflict advancement requires an
// exact base before it may inspect or adopt a descendant.
func recoveryClaimMatches(claim *worktrees.WorkLogClaimView, receipt WorktreeMergeReceipt, path string) bool {
	return claim != nil && claim.Task == receipt.Candidate.Task && claim.Repository == receipt.Repository &&
		filepath.Clean(claim.Worktree) == filepath.Clean(path) && claim.Branch == receipt.Candidate.Branch &&
		claim.Base == receipt.Target && claim.Lifecycle == "active"
}

func recordRecoveredMergeCandidate(receipt *WorktreeMergeReceipt, head string) {
	receipt.Candidate.SHA = head
	receipt.Status = WorktreeMergePreparing
	receipt.Failure = ""
	receipt.UpdatedAt = time.Now().UTC()
}

// Only acknowledgement effects vary here. Native Git, Guard and Work Log
// custody still run through their actual owners in every recovery operation.
type conflictAdvanceStore struct {
	read          func(string) (WorktreeMergeConflictCandidateAdvance, error)
	persist       func(string, WorktreeMergeConflictCandidateAdvance) error
	receiptSHA256 func(string) (string, error)
}

func nativeConflictAdvanceStore() conflictAdvanceStore {
	return conflictAdvanceStore{read: readConflictCandidateAdvance, persist: persistConflictCandidateAdvance, receiptSHA256: worktreeMergeReceiptSHA256}
}

func requireCleanMergeWorktreeWithRunner(ctx context.Context, run runner.Runner, path string) error {
	status, _, err := runCommand(ctx, run, 0, 0, path, "git", "status", "--porcelain=v1")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("worktree is dirty: %s", strings.TrimSpace(status))
	}
	return nil
}

func recheckWorktreeMergeSourcesWithRunner(ctx context.Context, run runner.Runner, sources []WorktreeMergeSource) error {
	for _, source := range sources {
		if err := requireCleanMergeWorktreeWithRunner(ctx, run, source.Worktree); err != nil {
			return fmt.Errorf("source %s changed during prepare: %w", source.Worktree, err)
		}
		head, err := mergeRevision(ctx, run, source.Worktree, "HEAD")
		if err != nil {
			return err
		}
		if head != source.SHA {
			return fmt.Errorf("source %s advanced from %s to %s during prepare", source.Worktree, source.SHA, head)
		}
	}
	return nil
}

func isMergeAncestorWithRunner(ctx context.Context, run runner.Runner, path, ancestor, descendant string) (bool, error) {
	output, _, err := runCommand(ctx, run, 0, 0, path, "git", "merge-base", ancestor, descendant)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(output) == ancestor, nil
}

func fetchExactMergeTargetWithRunner(ctx context.Context, run runner.Runner, worktree, target string) (string, error) {
	refspec := "+refs/heads/" + target + ":refs/remotes/origin/" + target
	if _, _, err := runCommand(ctx, run, 0, 0, worktree, "git", "fetch", "--no-tags", "origin", refspec); err != nil {
		return "", fmt.Errorf("fetch exact remote target %s: %w", target, err)
	}
	return mergeRevision(ctx, run, worktree, "refs/remotes/origin/"+target)
}
