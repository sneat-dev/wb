package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AcknowledgeLandedMergeFailure proves that a non-terminal failed receipt is
// already represented by the current remote target, then writes a
// distinct audited acknowledgement. It never rewrites the historical receipt
// or any Work Log record. A failed proof is always a refusal.
func AcknowledgeLandedMergeFailure(ctx context.Context, options WorktreeMergeLandedFailureAcknowledgementOptions) (WorktreeMergeLandedFailureAcknowledgement, error) {
	return acknowledgeLandedMergeFailure(ctx, options, defaultRunner, readWorktreeMergeReceipt, persistLandedFailureAcknowledgement)
}

func acknowledgeLandedMergeFailure(ctx context.Context, options WorktreeMergeLandedFailureAcknowledgementOptions, run runner.Runner, readReceipt func(string) (WorktreeMergeReceipt, error), persist func(string, WorktreeMergeLandedFailureAcknowledgement) error) (WorktreeMergeLandedFailureAcknowledgement, error) {
	receiptPath, err := resolveWorktreeMergeReceiptPath(options.ProjectsRoot, options.Receipt)
	if err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	receipt, err := readReceipt(receiptPath)
	if err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	if err := validateLandedFailureAcknowledgementReceipt(receipt, receiptPath); err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	if receipt.Repository == "" || receipt.Target == "" || receipt.TargetSHA == "" || receipt.Candidate.SHA == "" || receipt.Candidate.Worktree == "" || receipt.Candidate.Branch == "" || len(receipt.Sources) == 0 {
		return WorktreeMergeLandedFailureAcknowledgement{}, fmt.Errorf("receipt %s lacks complete immutable target, candidate, or source identity", receiptPath)
	}
	if options.Apply && (strings.TrimSpace(options.Actor) == "" || strings.TrimSpace(options.Reason) == "") {
		return WorktreeMergeLandedFailureAcknowledgement{}, errors.New("--actor and --reason are required with --apply")
	}
	lockID := receipt.Lane
	lock, err := AcquireOperationLock(options.ProjectsRoot, lockID, true)
	if err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	defer func() { _ = lock.Release() }()
	if receipt.Status == WorktreeMergePostTargetCIFailed {
		if _, statErr := os.Lstat(receipt.Candidate.Worktree); os.IsNotExist(statErr) {
			return acknowledgeCleanedDirectPostTargetFailure(ctx, options, receipt, receiptPath, run, persist)
		} else if statErr != nil {
			return WorktreeMergeLandedFailureAcknowledgement{}, fmt.Errorf("inspect receipted candidate worktree: %w", statErr)
		}
	}

	view, err := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: options.ProjectsRoot, Worktree: receipt.Candidate.Worktree})
	if err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, fmt.Errorf("load candidate Work Log: %w", err)
	}
	if view.Claim == nil || view.Claim.Lifecycle != "active" || view.Claim.Repository != receipt.Repository ||
		view.Claim.Task != receipt.Candidate.Task || filepath.Clean(view.Claim.Worktree) != filepath.Clean(receipt.Candidate.Worktree) || view.Claim.Branch != receipt.Candidate.Branch ||
		view.Claim.Base != receipt.Target || view.Claim.BaseSHA == "" {
		return WorktreeMergeLandedFailureAcknowledgement{}, errors.New("candidate has no active Work Log claim matching the immutable receipt target and identity")
	}
	if err := requireCleanMergeWorktreeWithRunner(ctx, run, receipt.Candidate.Worktree); err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, fmt.Errorf("candidate is not clean: %w", err)
	}
	head, err := mergeRevision(ctx, run, receipt.Candidate.Worktree, "HEAD")
	if err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, fmt.Errorf("read candidate HEAD: %w", err)
	}
	if head != receipt.Candidate.SHA {
		return WorktreeMergeLandedFailureAcknowledgement{}, fmt.Errorf("candidate HEAD %s does not match receipted candidate %s", head, receipt.Candidate.SHA)
	}
	if err := requireCandidateContainsImmutableClaimBaseWithRunner(ctx, run, receipt.Candidate.Worktree, view.Claim.BaseSHA, head); err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	if contains, err := isMergeAncestorWithRunner(ctx, run, receipt.Candidate.Worktree, receipt.TargetSHA, head); err != nil || !contains {
		if err == nil {
			err = fmt.Errorf("candidate %s does not contain immutable receipt target %s", head, receipt.TargetSHA)
		}
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	for _, source := range receipt.Sources {
		if isLandedFailedValidationReceipt(receipt) {
			if err := validatePreservedLandedFailureAcknowledgementSource(ctx, options.ProjectsRoot, receipt, source); err != nil {
				return WorktreeMergeLandedFailureAcknowledgement{}, err
			}
		} else if err := validateLandedFailureAcknowledgementSource(ctx, options.ProjectsRoot, receipt, source, ""); err != nil {
			return WorktreeMergeLandedFailureAcknowledgement{}, err
		}
		contains, sourceErr := isMergeAncestorWithRunner(ctx, run, receipt.Candidate.Worktree, source.SHA, head)
		if sourceErr != nil || !contains {
			if sourceErr == nil {
				sourceErr = fmt.Errorf("candidate %s does not contain receipted source %s", head, source.SHA)
			}
			return WorktreeMergeLandedFailureAcknowledgement{}, sourceErr
		}
	}
	currentTarget, err := fetchExactMergeTargetWithRunner(ctx, run, receipt.Candidate.Worktree, receipt.Target)
	if err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	if contains, ancestorErr := isMergeAncestorWithRunner(ctx, run, receipt.Candidate.Worktree, head, currentTarget); ancestorErr != nil || !contains {
		if ancestorErr == nil {
			ancestorErr = fmt.Errorf("current remote target %s does not contain receipted candidate %s", currentTarget, head)
		}
		return WorktreeMergeLandedFailureAcknowledgement{}, ancestorErr
	}
	if receipt.Status == WorktreeMergePostTargetCIFailed {
		if contains, ancestorErr := isMergeAncestorWithRunner(ctx, run, receipt.Candidate.Worktree, receipt.LandingSHA, currentTarget); ancestorErr != nil || !contains {
			if ancestorErr == nil {
				ancestorErr = fmt.Errorf("current remote target %s does not contain receipted landing %s", currentTarget, receipt.LandingSHA)
			}
			return WorktreeMergeLandedFailureAcknowledgement{}, ancestorErr
		}
	}

	ack := WorktreeMergeLandedFailureAcknowledgement{
		SchemaVersion: worktreeMergeLandedFailureAcknowledgementSchemaVersion,
		Status:        "landed_failure_acknowledged",
		ReceiptPath:   receiptPath, ReceiptID: receipt.ID, ReceiptStatus: receipt.Status, Lane: receipt.Lane,
		AcknowledgementPath: landedFailureAcknowledgementPath(receiptPath),
		Repository:          receipt.Repository, Target: receipt.Target, ReceiptTargetSHA: receipt.TargetSHA, ReceiptLandingSHA: receipt.LandingSHA,
		CurrentTargetSHA: currentTarget, CandidateSHA: head, ClaimBaseSHA: view.Claim.BaseSHA,
		CandidateWorktree: receipt.Candidate.Worktree, CandidateBranch: receipt.Candidate.Branch,
		Sources: append([]WorktreeMergeSource(nil), receipt.Sources...), Actor: strings.TrimSpace(options.Actor),
		Reason: strings.TrimSpace(options.Reason), RecordedAt: time.Now().UTC(),
	}
	ack.ID = landedFailureAcknowledgementID(ack)
	return finishLandedFailureAcknowledgement(receipt, ack, options.Apply, persist)
}

// acknowledgeCleanedDirectPostTargetFailure recovers only the narrow direct
// landing case whose candidate and sources were already removed by exact WB
// cleanup. Immutable terminal Work Logs provide the claim bases after checkout
// removal; fresh canonical ancestry proves every recorded root remains remote.
func acknowledgeCleanedDirectPostTargetFailure(ctx context.Context, options WorktreeMergeLandedFailureAcknowledgementOptions, receipt WorktreeMergeReceipt, receiptPath string, run runner.Runner, persist func(string, WorktreeMergeLandedFailureAcknowledgement) error) (WorktreeMergeLandedFailureAcknowledgement, error) {
	if receipt.Route.Route != WorktreeMergeRouteDirect || receipt.LandingSHA == "" || receipt.LandingSHA != receipt.Candidate.SHA {
		return WorktreeMergeLandedFailureAcknowledgement{}, errors.New("cleaned post-target-CI recovery requires a direct route with landing equal to candidate SHA")
	}
	expectations, err := terminalWorkLogExpectations(receipt)
	if err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	canonical, err := worktrees.CanonicalRepositoryPath(options.ProjectsRoot, receipt.Repository)
	if err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, fmt.Errorf("resolve canonical repository for cleaned landing: %w", err)
	}
	claimBases := make(map[string]string, len(expectations))
	cleanupRemoteRoots := make([]string, 0, len(expectations))
	for _, expectation := range expectations {
		if _, statErr := os.Lstat(expectation.Worktree); statErr == nil {
			return WorktreeMergeLandedFailureAcknowledgement{}, fmt.Errorf("receipted checkout %s still exists; cleaned recovery requires every checkout removed", expectation.Worktree)
		} else if !os.IsNotExist(statErr) {
			return WorktreeMergeLandedFailureAcknowledgement{}, fmt.Errorf("inspect receipted checkout %s: %w", expectation.Worktree, statErr)
		}
		proof, proofErr := worktrees.FindTerminalCleanupProof(options.ProjectsRoot, expectation.Repository, receipt.Target, expectation.Task, expectation.Worktree, expectation.Branch)
		if proofErr != nil {
			return WorktreeMergeLandedFailureAcknowledgement{}, fmt.Errorf("validate exact terminal cleanup for %s: %w", expectation.Task, proofErr)
		}
		if proof.Result.HeadSHA != expectation.FinalCommit || proof.Result.Repository != expectation.Repository ||
			filepath.Clean(proof.Result.WorktreeDir) != filepath.Clean(expectation.Worktree) || proof.Result.Branch != expectation.Branch ||
			proof.Result.Base != receipt.Target || filepath.Clean(proof.Result.CanonicalDir) != filepath.Clean(canonical) {
			return WorktreeMergeLandedFailureAcknowledgement{}, fmt.Errorf("terminal cleanup receipt does not exactly match receipted identity and head for %s", expectation.Task)
		}
		cleanupRemoteRoots = append(cleanupRemoteRoots, proof.Result.RemoteTargetSHA)
		baseSHA, baseErr := worktrees.ReadRemovedTerminalWorkLogClaimBase(options.ProjectsRoot, expectation)
		if baseErr != nil {
			return WorktreeMergeLandedFailureAcknowledgement{}, fmt.Errorf("validate removed terminal Work Log for %s: %w", expectation.Task, baseErr)
		}
		claimBases[expectation.Task] = baseSHA
	}
	currentTarget, err := fetchExactMergeTargetWithRunner(ctx, run, canonical, receipt.Target)
	if err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	roots := []string{receipt.TargetSHA, receipt.LandingSHA, receipt.Candidate.SHA}
	roots = append(roots, sourceSHAs(receipt.Sources)...)
	for _, expectation := range expectations {
		roots = append(roots, claimBases[expectation.Task])
	}
	roots = append(roots, cleanupRemoteRoots...)
	seenRoots := make(map[string]bool, len(roots))
	for _, root := range roots {
		if root == "" || seenRoots[root] {
			continue
		}
		seenRoots[root] = true
		contains, ancestorErr := isMergeAncestorWithRunner(ctx, run, canonical, root, currentTarget)
		if ancestorErr != nil || !contains {
			if ancestorErr == nil {
				ancestorErr = fmt.Errorf("current remote target %s does not contain recorded root %s", currentTarget, root)
			}
			return WorktreeMergeLandedFailureAcknowledgement{}, ancestorErr
		}
	}
	if err := validateCleanedLandedFailureAncestry(ctx, run, canonical, receipt, claimBases); err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	ack := WorktreeMergeLandedFailureAcknowledgement{
		SchemaVersion: worktreeMergeLandedFailureAcknowledgementSchemaVersion, Status: "landed_failure_acknowledged",
		ReceiptPath: receiptPath, ReceiptID: receipt.ID, ReceiptStatus: receipt.Status, Lane: receipt.Lane,
		AcknowledgementPath: landedFailureAcknowledgementPath(receiptPath), Repository: receipt.Repository, Target: receipt.Target,
		ReceiptTargetSHA: receipt.TargetSHA, ReceiptLandingSHA: receipt.LandingSHA, CurrentTargetSHA: currentTarget,
		CandidateSHA: receipt.Candidate.SHA, ClaimBaseSHA: claimBases[receipt.Candidate.Task], CandidateWorktree: receipt.Candidate.Worktree,
		CandidateBranch: receipt.Candidate.Branch, Sources: append([]WorktreeMergeSource(nil), receipt.Sources...),
		Actor: strings.TrimSpace(options.Actor), Reason: strings.TrimSpace(options.Reason), RecordedAt: time.Now().UTC(),
	}
	ack.ID = landedFailureAcknowledgementID(ack)
	return finishLandedFailureAcknowledgement(receipt, ack, options.Apply, persist)
}

func validateCleanedLandedFailureAncestry(ctx context.Context, run runner.Runner, canonical string, receipt WorktreeMergeReceipt, claimBases map[string]string) error {
	candidateRoots := []struct {
		sha, description string
	}{
		{receipt.TargetSHA, "immutable receipt target"},
		{claimBases[receipt.Candidate.Task], "immutable candidate claim base"},
	}
	for _, root := range candidateRoots {
		contains, err := isMergeAncestorWithRunner(ctx, run, canonical, root.sha, receipt.Candidate.SHA)
		if err != nil {
			return fmt.Errorf("verify candidate ancestry for %s: %w", root.description, err)
		}
		if !contains {
			return fmt.Errorf("candidate %s does not contain %s %s", receipt.Candidate.SHA, root.description, root.sha)
		}
	}
	for _, source := range receipt.Sources {
		baseSHA := claimBases[source.Task]
		containsBase, err := isMergeAncestorWithRunner(ctx, run, canonical, baseSHA, source.SHA)
		if err != nil {
			return fmt.Errorf("verify receipted source %s claim-base ancestry: %w", source.Task, err)
		}
		if !containsBase {
			return fmt.Errorf("receipted source %s at %s does not contain immutable claim base %s", source.Task, source.SHA, baseSHA)
		}
		containsSource, err := isMergeAncestorWithRunner(ctx, run, canonical, source.SHA, receipt.Candidate.SHA)
		if err != nil {
			return fmt.Errorf("verify candidate ancestry for receipted source %s: %w", source.Task, err)
		}
		if !containsSource {
			return fmt.Errorf("candidate %s does not contain receipted source %s at %s", receipt.Candidate.SHA, source.Task, source.SHA)
		}
	}
	return nil
}

func finishLandedFailureAcknowledgement(receipt WorktreeMergeReceipt, ack WorktreeMergeLandedFailureAcknowledgement, apply bool, persist func(string, WorktreeMergeLandedFailureAcknowledgement) error) (WorktreeMergeLandedFailureAcknowledgement, error) {
	ackPath := landedFailureAcknowledgementPath(ack.ReceiptPath)
	if existing, readErr := readLandedFailureAcknowledgement(ackPath, receipt); readErr == nil {
		if existing.CurrentTargetSHA != ack.CurrentTargetSHA || existing.CandidateSHA != ack.CandidateSHA {
			return WorktreeMergeLandedFailureAcknowledgement{}, fmt.Errorf("acknowledgement %s binds different target or candidate evidence", ackPath)
		}
		return existing, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return WorktreeMergeLandedFailureAcknowledgement{}, readErr
	}
	if !apply {
		return ack, nil
	}
	if err := persist(ackPath, ack); err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	return ack, nil
}
