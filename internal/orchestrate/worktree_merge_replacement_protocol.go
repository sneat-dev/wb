package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AcknowledgeWorktreeMergeReceiptCollision records the one audited recovery
// path for a known historical receipt collision. It never infers the incident:
// the caller must pin every observed digest and revision before --apply can
// write the separate acknowledgement.
func AcknowledgeWorktreeMergeReceiptCollision(ctx context.Context, options WorktreeMergeReceiptCollisionAcknowledgementOptions) (WorktreeMergeReceiptCollisionAcknowledgement, error) {
	return acknowledgeWorktreeMergeReceiptCollisionInjected(ctx, options, nil)
}

// acknowledgeWorktreeMergeReceiptCollisionInjected is
// AcknowledgeWorktreeMergeReceiptCollision's test seam (task-9 PR-4, review
// findings): production always reaches it through
// AcknowledgeWorktreeMergeReceiptCollision, which passes a nil
// *filewrite.Injector, so production behaviour is unchanged. A test passes
// its own Injector, including an Injector.Hook at the final persist's
// StepLink, to build a real concurrent-collision race at the caller level,
// covering both the "after atomic create collision" re-read and the
// converging existing-acknowledgement return.
func acknowledgeWorktreeMergeReceiptCollisionInjected(ctx context.Context, options WorktreeMergeReceiptCollisionAcknowledgementOptions, inj *filewrite.Injector) (WorktreeMergeReceiptCollisionAcknowledgement, error) {
	return acknowledgeWorktreeMergeReceiptCollisionWithRunner(ctx, defaultRunner, readWorktreeMergeReceipt, worktreeMergeReceiptSHA256, options, inj)
}

func acknowledgeWorktreeMergeReceiptCollisionWithRunner(ctx context.Context, run runner.Runner, read func(string) (WorktreeMergeReceipt, error), hash func(string) (string, error), options WorktreeMergeReceiptCollisionAcknowledgementOptions, inj *filewrite.Injector) (WorktreeMergeReceiptCollisionAcknowledgement, error) {
	if err := requireReceiptCollisionExpectations(options); err != nil {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, err
	}
	if options.Apply && (strings.TrimSpace(options.Actor) == "" || strings.TrimSpace(options.Reason) == "") {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, errors.New("--actor and --reason are required with --apply")
	}
	receiptPath, err := resolveWorktreeMergeReceiptPath(options.ProjectsRoot, options.Receipt)
	if err != nil {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, err
	}
	receipt, err := read(receiptPath)
	if err != nil {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, err
	}
	lock, err := AcquireOperationLock(options.ProjectsRoot, receipt.Lane, true)
	if err != nil {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, err
	}
	defer func() { _ = lock.Release() }()

	// Re-read beneath the lane lock before every proof and before the only write.
	receipt, err = read(receiptPath)
	if err != nil {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, err
	}
	if err := validateReceiptCollisionShape(receipt, receiptPath, options); err != nil {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, err
	}
	receiptHash, err := hash(receiptPath)
	if err != nil || receiptHash != options.ExpectedReceiptSHA256 {
		if err == nil {
			err = fmt.Errorf("receipt SHA256 %s does not match expected %s", receiptHash, options.ExpectedReceiptSHA256)
		}
		return WorktreeMergeReceiptCollisionAcknowledgement{}, err
	}
	claim, err := validateMergeAcknowledgementCandidateWithRunner(ctx, run, options.ProjectsRoot, receipt, receipt.Candidate)
	if err != nil {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, fmt.Errorf("validate collision candidate: %w", err)
	}
	claimHash, err := hash(claim.ClaimPath)
	if err != nil {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, fmt.Errorf("read immutable candidate claim: %w", err)
	}
	if claimHash != options.ExpectedImmutableClaimSHA256 {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, fmt.Errorf("immutable claim SHA256 %s does not match expected %s", claimHash, options.ExpectedImmutableClaimSHA256)
	}
	remote, _, err := runCommand(ctx, run, 0, 0, receipt.Candidate.Worktree, "git", "ls-remote", "--heads", "origin", "refs/heads/"+receipt.Candidate.Branch)
	if err != nil {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, err
	}
	if strings.TrimSpace(remote) != "" {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, errors.New("collision candidate is published")
	}
	currentTarget, err := fetchExactMergeTargetWithRunner(ctx, run, receipt.Candidate.Worktree, receipt.Target)
	if err != nil {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, err
	}
	if currentTarget != options.ExpectedTargetSHA || receipt.TargetSHA != currentTarget {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, fmt.Errorf("exact current target %s or receipt target %s does not match expected %s", currentTarget, receipt.TargetSHA, options.ExpectedTargetSHA)
	}
	for _, root := range []string{claim.BaseSHA, receipt.TargetSHA, receipt.Sources[0].SHA, receipt.SourceRefreshes[0].Sources[0].SHA} {
		contains, ancestorErr := isMergeAncestorWithRunner(ctx, run, receipt.Candidate.Worktree, root, receipt.Candidate.SHA)
		if ancestorErr != nil || !contains {
			if ancestorErr == nil {
				ancestorErr = fmt.Errorf("collision candidate %s does not contain required root %s", receipt.Candidate.SHA, root)
			}
			return WorktreeMergeReceiptCollisionAcknowledgement{}, ancestorErr
		}
	}
	ackPath := receiptCollisionAcknowledgementPath(receiptPath)
	ack := WorktreeMergeReceiptCollisionAcknowledgement{
		SchemaVersion: worktreeMergeReceiptCollisionAcknowledgementSchemaVersion, Status: "receipt_collision_acknowledged",
		ReceiptPath: receiptPath, AcknowledgementPath: ackPath, ReceiptSHA256: receiptHash, ImmutableClaimSHA256: claimHash,
		ReceiptID: receipt.ID, Lane: receipt.Lane, Repository: receipt.Repository, Target: receipt.Target,
		ExpectedTargetSHA: options.ExpectedTargetSHA, ExpectedCandidateSHA: options.ExpectedCandidateSHA,
		ExpectedCurrentSourceSHA: options.ExpectedCurrentSourceSHA, ExpectedHistoricalRefreshSourceSHA: options.ExpectedHistoricalRefreshSourceSHA,
		ClaimBaseSHA: claim.BaseSHA, Candidate: receipt.Candidate, CurrentSources: append([]WorktreeMergeSource(nil), receipt.Sources...),
		HistoricalRefreshSources:                    append([]WorktreeMergeSource(nil), receipt.SourceRefreshes[0].Sources...),
		HistoricalValidationFailedOperatorAssertion: true, Actor: strings.TrimSpace(options.Actor), Reason: strings.TrimSpace(options.Reason), RecordedAt: time.Now().UTC(),
	}
	ack.ID = receiptCollisionAcknowledgementID(ack)
	if existing, readErr := readReceiptCollisionAcknowledgement(ackPath, receipt); readErr == nil {
		if !sameReceiptCollisionAcknowledgement(existing, ack) {
			return WorktreeMergeReceiptCollisionAcknowledgement{}, fmt.Errorf("receipt-collision acknowledgement %s binds different immutable evidence", ackPath)
		}
		return existing, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, readErr
	}
	if !options.Apply {
		return ack, nil
	}
	if err := persistReceiptCollisionAcknowledgementInjected(ackPath, ack, inj); err != nil {
		if errors.Is(err, os.ErrExist) {
			existing, readErr := readReceiptCollisionAcknowledgement(ackPath, receipt)
			if readErr != nil {
				return WorktreeMergeReceiptCollisionAcknowledgement{}, fmt.Errorf("re-read receipt-collision acknowledgement after atomic create collision: %w", readErr)
			}
			if !sameReceiptCollisionAcknowledgement(existing, ack) {
				return WorktreeMergeReceiptCollisionAcknowledgement{}, fmt.Errorf("receipt-collision acknowledgement %s binds different immutable evidence", ackPath)
			}
			return existing, nil
		}
		return WorktreeMergeReceiptCollisionAcknowledgement{}, err
	}
	return ack, nil
}

func requireReceiptCollisionExpectations(options WorktreeMergeReceiptCollisionAcknowledgementOptions) error {
	for _, expected := range []string{options.ExpectedReceiptSHA256, options.ExpectedImmutableClaimSHA256, options.ExpectedTargetSHA, options.ExpectedCandidateSHA, options.ExpectedCurrentSourceSHA, options.ExpectedHistoricalRefreshSourceSHA} {
		if strings.TrimSpace(expected) == "" {
			return errors.New("all expected receipt, claim, target, candidate, current-source, and historical-source identities are required")
		}
	}
	return nil
}

func validateReceiptCollisionShape(receipt WorktreeMergeReceipt, receiptPath string, options WorktreeMergeReceiptCollisionAcknowledgementOptions) error {
	if receipt.ReceiptPath != receiptPath || receipt.Phase != WorktreeMergePhasePrepare || receipt.Status != WorktreeMergePreparing || receipt.LandingSHA != "" || receipt.PullRequest != "" || receipt.PublishedCandidateSHA != "" || receipt.ID == "" || receipt.Lane != worktreeMergeLaneID(receipt.Repository, receipt.Target) {
		return errors.New("receipt is not an exact unlanded preparing collision shape")
	}
	if receipt.Candidate.SHA != options.ExpectedCandidateSHA || len(receipt.Sources) != 1 || receipt.Sources[0].SHA != options.ExpectedCurrentSourceSHA || len(receipt.SourceRefreshes) != 1 || len(receipt.SourceRefreshes[0].Sources) != 1 || receipt.SourceRefreshes[0].Sources[0].SHA != options.ExpectedHistoricalRefreshSourceSHA {
		return errors.New("receipt collision sources or candidate do not match explicit expected identity")
	}
	return nil
}

// SupersedeValidationFailedWorktreeMerge proves that a clean replacement
// candidate contains every immutable root of an unlanded prepare failure. The
// failed candidate itself need not be an ancestor: it may have diverged after
// validation failed. This transition is deliberately narrower than the landed
// acknowledgement because it never asserts that the failed candidate landed.
func SupersedeValidationFailedWorktreeMerge(ctx context.Context, options WorktreeMergeValidationFailureSupersessionOptions) (WorktreeMergeValidationFailureSupersession, error) {
	return supersedeValidationFailedWorktreeMergeWithRunner(ctx, defaultRunner, readWorktreeMergeReceipt, worktreeMergeReceiptSHA256, options)
}

func supersedeValidationFailedWorktreeMergeWithRunner(ctx context.Context, run runner.Runner, read func(string) (WorktreeMergeReceipt, error), hash func(string) (string, error), options WorktreeMergeValidationFailureSupersessionOptions) (WorktreeMergeValidationFailureSupersession, error) {
	receiptPath, err := resolveWorktreeMergeReceiptPath(options.ProjectsRoot, options.Receipt)
	if err != nil {
		return WorktreeMergeValidationFailureSupersession{}, err
	}
	receipt, err := read(receiptPath)
	if err != nil {
		return WorktreeMergeValidationFailureSupersession{}, err
	}
	if strings.TrimSpace(options.ReplacementWorktree) == "" {
		return WorktreeMergeValidationFailureSupersession{}, errors.New("replacement worktree is required")
	}
	if options.Apply && (strings.TrimSpace(options.Actor) == "" || strings.TrimSpace(options.Reason) == "") {
		return WorktreeMergeValidationFailureSupersession{}, errors.New("--actor and --reason are required with --apply")
	}
	if receipt.Lane == "" {
		return WorktreeMergeValidationFailureSupersession{}, fmt.Errorf("receipt %s has no lane identity", receiptPath)
	}
	lock, err := AcquireOperationLock(options.ProjectsRoot, receipt.Lane, true)
	if err != nil {
		return WorktreeMergeValidationFailureSupersession{}, err
	}
	defer func() { _ = lock.Release() }()
	receipt, err = read(receiptPath)
	if err != nil {
		return WorktreeMergeValidationFailureSupersession{}, err
	}
	var conflictIdentity *WorktreeMergeLegacyConflictIdentity
	var conflictIdentityNeedsPersist bool
	originalReceipt := receipt
	receipt, legacyIdentity, legacyIdentityNeedsPersist, err := resolveValidationFailedSupersessionReceiptWithRunner(ctx, run, hash, options.ProjectsRoot, receipt, receiptPath, options.Actor, options.Reason)
	if err != nil {
		prepareFailureErr := err
		receipt = originalReceipt
		if receipt.Status != WorktreeMergeConflict || receipt.Candidate.SHA != "" {
			return WorktreeMergeValidationFailureSupersession{}, prepareFailureErr
		}
		receipt, conflictIdentity, conflictIdentityNeedsPersist, err = resolveLegacyConflictSupersessionReceiptWithRunner(ctx, run, hash, options.ProjectsRoot, receipt, receiptPath, options.Actor, options.Reason)
		if err != nil {
			return WorktreeMergeValidationFailureSupersession{}, err
		}
	}

	originalClaim, observedCandidateDescendantSHA, err := validatePrepareFailureSupersessionCandidateWithRunner(ctx, run, options.ProjectsRoot, receipt)
	if err != nil {
		return WorktreeMergeValidationFailureSupersession{}, fmt.Errorf("validate failed candidate: %w", err)
	}
	replacement, replacementClaim, err := validateValidationFailureReplacementWithRunner(ctx, run, options.ProjectsRoot, receipt, options.ReplacementWorktree)
	if err != nil {
		return WorktreeMergeValidationFailureSupersession{}, err
	}
	if replacement == receipt.Candidate || replacement.SHA == receipt.Candidate.SHA {
		return WorktreeMergeValidationFailureSupersession{}, errors.New("replacement candidate must be distinct from the failed receipt candidate")
	}
	currentSourceHeads := make([]string, 0, len(receipt.Sources))
	currentSourceClaimBases := make([]string, 0, len(receipt.Sources))
	for _, source := range receipt.Sources {
		currentSourceHead, sourceClaimBase, sourceErr := validateValidationFailedSupersessionSourceWithRunner(ctx, run, options.ProjectsRoot, receipt, source)
		if sourceErr != nil {
			return WorktreeMergeValidationFailureSupersession{}, sourceErr
		}
		currentSourceHeads = append(currentSourceHeads, currentSourceHead)
		currentSourceClaimBases = append(currentSourceClaimBases, sourceClaimBase)
	}
	currentTarget, err := fetchExactMergeTargetWithRunner(ctx, run, replacement.Worktree, receipt.Target)
	if err != nil {
		return WorktreeMergeValidationFailureSupersession{}, err
	}
	targetTree, err := mergeTreeRevision(ctx, run, replacement.Worktree, currentTarget)
	if err != nil {
		return WorktreeMergeValidationFailureSupersession{}, fmt.Errorf("read current target tree: %w", err)
	}
	observedSourceDescendants := make([]string, 0, len(receipt.Sources))
	for _, source := range receipt.Sources {
		allowedDescendantSHA := ""
		if filepath.Clean(source.Worktree) == filepath.Clean(replacement.Worktree) {
			allowedDescendantSHA = replacement.SHA
		} else if sourceHead, headErr := mergeRevision(ctx, run, source.Worktree, "HEAD"); headErr != nil {
			return WorktreeMergeValidationFailureSupersession{}, fmt.Errorf("read receipted source %s HEAD: %w", source.Worktree, headErr)
		} else if sourceHead != source.SHA {
			allowedDescendantSHA = sourceHead
			sourceTree, treeErr := mergeTreeRevision(ctx, run, source.Worktree, sourceHead)
			if treeErr != nil {
				return WorktreeMergeValidationFailureSupersession{}, fmt.Errorf("read advanced receipted source tree: %w", treeErr)
			}
			if sourceTree != targetTree {
				return WorktreeMergeValidationFailureSupersession{}, fmt.Errorf("advanced receipted source %s tree %s differs from landed target tree %s", source.Worktree, sourceTree, targetTree)
			}
			observedSourceDescendants = append(observedSourceDescendants, sourceHead)
		}
		if err := validateLandedFailureAcknowledgementSource(ctx, options.ProjectsRoot, receipt, source, allowedDescendantSHA); err != nil {
			return WorktreeMergeValidationFailureSupersession{}, err
		}
	}
	requiredRoots := []string{originalClaim.BaseSHA, receipt.TargetSHA, currentTarget, replacementClaim.BaseSHA}
	if observedCandidateDescendantSHA != "" {
		requiredRoots = append(requiredRoots, receipt.Candidate.SHA, observedCandidateDescendantSHA)
	} else if conflictIdentity != nil {
		requiredRoots = append(requiredRoots, receipt.Candidate.SHA)
	}
	requiredRoots = append(requiredRoots, observedSourceDescendants...)
	requiredRoots = append(requiredRoots, currentSourceHeads...)
	requiredRoots = append(requiredRoots, currentSourceClaimBases...)
	for _, root := range append(requiredRoots, sourceSHAs(receipt.Sources)...) {
		contains, ancestorErr := isMergeAncestorWithRunner(ctx, run, replacement.Worktree, root, replacement.SHA)
		if ancestorErr != nil || !contains {
			if ancestorErr == nil {
				ancestorErr = fmt.Errorf("replacement %s does not contain required immutable root %s", replacement.SHA, root)
			}
			return WorktreeMergeValidationFailureSupersession{}, ancestorErr
		}
	}
	receiptHash, err := hash(receiptPath)
	if err != nil {
		return WorktreeMergeValidationFailureSupersession{}, err
	}
	ackPath := validationFailureSupersessionPath(receiptPath)
	ack := WorktreeMergeValidationFailureSupersession{
		SchemaVersion: worktreeMergeValidationFailureSupersessionSchemaVersion,
		Status:        "validation_failure_superseded", ReceiptPath: receiptPath, AcknowledgementPath: ackPath,
		ReceiptID: receipt.ID, ReceiptSHA256: receiptHash, ReceiptStatus: receipt.Status, Lane: receipt.Lane,
		Repository: receipt.Repository, Target: receipt.Target, ReceiptTargetSHA: receipt.TargetSHA, CurrentTargetSHA: currentTarget,
		OriginalCandidate: receipt.Candidate, ObservedCandidateDescendantSHA: observedCandidateDescendantSHA, OriginalClaimBaseSHA: originalClaim.BaseSHA,
		Replacement: replacement, ReplacementClaimBaseSHA: replacementClaim.BaseSHA,
		Sources: append([]WorktreeMergeSource(nil), receipt.Sources...), Actor: strings.TrimSpace(options.Actor), Reason: strings.TrimSpace(options.Reason), RecordedAt: time.Now().UTC(),
	}
	ack.ID = validationFailureSupersessionID(ack)
	if existing, readErr := readValidationFailureSupersession(ackPath, receipt); readErr == nil {
		if existing.CurrentTargetSHA != currentTarget || existing.Replacement != replacement || existing.ObservedCandidateDescendantSHA != observedCandidateDescendantSHA {
			return WorktreeMergeValidationFailureSupersession{}, fmt.Errorf("supersession %s binds different target or replacement evidence", ackPath)
		}
		return existing, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return WorktreeMergeValidationFailureSupersession{}, readErr
	}
	if !options.Apply {
		return ack, nil
	}
	if legacyIdentityNeedsPersist {
		if err := persistLegacyValidationFailureIdentity(legacyIdentity.AcknowledgementPath, *legacyIdentity); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return WorktreeMergeValidationFailureSupersession{}, err
			}
			existing, readErr := readLegacyValidationFailureIdentity(legacyIdentity.AcknowledgementPath, receipt, receipt.Candidate)
			if readErr != nil || !sameLegacyValidationFailureIdentity(existing, *legacyIdentity) {
				if readErr != nil {
					return WorktreeMergeValidationFailureSupersession{}, readErr
				}
				return WorktreeMergeValidationFailureSupersession{}, fmt.Errorf("legacy validation-failed identity %s binds different immutable evidence", legacyIdentity.AcknowledgementPath)
			}
		}
	}
	if conflictIdentityNeedsPersist {
		if err := persistLegacyConflictIdentity(conflictIdentity.AcknowledgementPath, *conflictIdentity); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return WorktreeMergeValidationFailureSupersession{}, err
			}
			existing, readErr := readLegacyConflictIdentity(conflictIdentity.AcknowledgementPath, receipt, receipt.Candidate)
			if readErr != nil || !sameLegacyConflictIdentity(existing, *conflictIdentity) {
				if readErr != nil {
					return WorktreeMergeValidationFailureSupersession{}, readErr
				}
				return WorktreeMergeValidationFailureSupersession{}, fmt.Errorf("legacy conflict identity %s binds different immutable evidence", conflictIdentity.AcknowledgementPath)
			}
		}
	}
	if err := persistValidationFailureSupersession(ackPath, ack); err != nil {
		return WorktreeMergeValidationFailureSupersession{}, err
	}
	return ack, nil
}

// validateValidationFailedSupersessionReceipt defines the immutable boundary
// shared by ordinary supersession and the exceptional self-supersession
// correction. A correction may only describe an exact, unlanded prepare
// failure; it must never bless a malformed or later-landed receipt.
func validateValidationFailedSupersessionReceipt(receipt WorktreeMergeReceipt, receiptPath string) error {
	if err := validateLandedFailureAcknowledgementReceipt(receipt, receiptPath); err != nil {
		return err
	}
	if receipt.SchemaVersion != WorktreeMergeSchemaVersion || receipt.Phase != WorktreeMergePhasePrepare || receipt.Status != WorktreeMergeValidationFailed || receipt.LandingSHA != "" ||
		receipt.Repository == "" || receipt.Target == "" || receipt.TargetSHA == "" || len(receipt.Sources) == 0 ||
		receipt.Candidate.Task == "" || receipt.Candidate.Worktree == "" || receipt.Candidate.Branch == "" || receipt.Candidate.SHA == "" ||
		!worktreeMergeOperationIDMatchesRecordedSourceSet(receipt) || receipt.Candidate.Task != receipt.ID || receipt.CreatedAt.IsZero() || receipt.UpdatedAt.IsZero() {
		return fmt.Errorf("receipt %s lacks a complete exact validation_failed immutable identity", receiptPath)
	}
	for _, source := range receipt.Sources {
		if source.Task == "" || source.Worktree == "" || source.Branch == "" || source.SHA == "" {
			return fmt.Errorf("receipt %s has an incomplete immutable source identity", receiptPath)
		}
	}
	return nil
}

// validatePrepareFailureSupersessionReceipt extends the existing supersession
// boundary only to an exact unlanded prepare conflict. All immutable identity
// requirements remain identical; the caller separately proves the original
// candidate and replacement are clean, claimed, and fully contained.
func validatePrepareFailureSupersessionReceipt(receipt WorktreeMergeReceipt, receiptPath string) error {
	if receipt.Status == WorktreeMergeValidationFailed {
		return validateValidationFailedSupersessionReceipt(receipt, receiptPath)
	}
	unpublished, unpublishedErr := effectiveUnpublishedConflict(receipt)
	if unpublishedErr != nil {
		return unpublishedErr
	}
	if receipt.ReceiptPath != receiptPath || receipt.Lane == "" || receipt.Lane != worktreeMergeLaneID(receipt.Repository, receipt.Target) ||
		receipt.SchemaVersion != WorktreeMergeSchemaVersion || receipt.Phase != WorktreeMergePhasePrepare || receipt.Status != WorktreeMergeConflict ||
		receipt.LandingSHA != "" || !unpublished || receipt.Repository == "" || receipt.Target == "" ||
		receipt.TargetSHA == "" || len(receipt.Sources) == 0 || receipt.Candidate.Task == "" || receipt.Candidate.Worktree == "" ||
		receipt.Candidate.Branch == "" || receipt.Candidate.SHA == "" ||
		receipt.Candidate.Task != receipt.ID || receipt.CreatedAt.IsZero() || receipt.UpdatedAt.IsZero() {
		return fmt.Errorf("receipt %s lacks a complete exact identity; want prepare validation_failed or unpublished conflict", receiptPath)
	}
	for _, source := range receipt.Sources {
		if source.Task == "" || source.Worktree == "" || source.Branch == "" || source.SHA == "" {
			return fmt.Errorf("receipt %s has an incomplete immutable source identity", receiptPath)
		}
	}
	if err := validateWorktreeMergeSupersededOperationIDMatchesRecordedSourceSet(receipt, receiptPath); err != nil {
		return fmt.Errorf("receipt %s has an invalid superseded conflict identity: %w", receiptPath, err)
	}
	return nil
}

// resolveValidationFailedSupersessionReceiptWithRunner converts one narrowly defined
// legacy receipt into an in-memory effective receipt. The source JSON is never
// changed: an apply records the derived identity in its own sidecar only after
// all replacement proof has passed.

func resolveValidationFailedSupersessionReceiptWithRunner(ctx context.Context, run runner.Runner, hash func(string) (string, error), projectsRoot string, receipt WorktreeMergeReceipt, receiptPath, actor, reason string) (WorktreeMergeReceipt, *WorktreeMergeLegacyValidationFailureIdentity, bool, error) {
	prepareFailureErr := validatePrepareFailureSupersessionReceipt(receipt, receiptPath)
	if prepareFailureErr == nil {
		return receipt, nil, false, nil
	}
	if receipt.Status != WorktreeMergeValidationFailed {
		return WorktreeMergeReceipt{}, nil, false, prepareFailureErr
	}
	if err := validateLegacyValidationFailedReceiptShape(receipt, receiptPath); err != nil {
		return WorktreeMergeReceipt{}, nil, false, err
	}

	candidate := receipt.Candidate
	head, err := mergeRevision(ctx, run, candidate.Worktree, "HEAD")
	if err != nil {
		return WorktreeMergeReceipt{}, nil, false, fmt.Errorf("read legacy candidate HEAD: %w", err)
	}
	if head != receipt.Validation.Revision {
		return WorktreeMergeReceipt{}, nil, false, fmt.Errorf("legacy candidate HEAD %s does not match immutable validation revision %s", head, receipt.Validation.Revision)
	}
	candidate.SHA = head
	effective := receipt
	effective.Candidate = candidate
	claim, err := validateMergeAcknowledgementCandidateWithRunner(ctx, run, projectsRoot, effective, candidate)
	if err != nil {
		return WorktreeMergeReceipt{}, nil, false, fmt.Errorf("corroborate legacy candidate identity: %w", err)
	}
	if err := requireCandidateContainsImmutableClaimBaseWithRunner(ctx, run, candidate.Worktree, claim.BaseSHA, candidate.SHA); err != nil {
		return WorktreeMergeReceipt{}, nil, false, err
	}
	if contains, ancestorErr := isMergeAncestorWithRunner(ctx, run, candidate.Worktree, receipt.TargetSHA, candidate.SHA); ancestorErr != nil || !contains {
		if ancestorErr == nil {
			ancestorErr = fmt.Errorf("legacy candidate %s does not contain receipt target %s", candidate.SHA, receipt.TargetSHA)
		}
		return WorktreeMergeReceipt{}, nil, false, ancestorErr
	}
	for _, source := range receipt.Sources {
		if err := validateLandedFailureAcknowledgementSource(ctx, projectsRoot, effective, source, ""); err != nil {
			return WorktreeMergeReceipt{}, nil, false, err
		}
		if contains, sourceErr := isMergeAncestorWithRunner(ctx, run, candidate.Worktree, source.SHA, candidate.SHA); sourceErr != nil || !contains {
			if sourceErr == nil {
				sourceErr = fmt.Errorf("legacy candidate %s does not contain receipted source %s", candidate.SHA, source.SHA)
			}
			return WorktreeMergeReceipt{}, nil, false, sourceErr
		}
	}
	currentTarget, err := fetchExactMergeTargetWithRunner(ctx, run, candidate.Worktree, receipt.Target)
	if err != nil {
		return WorktreeMergeReceipt{}, nil, false, err
	}
	if landed, landingErr := isMergeAncestorWithRunner(ctx, run, candidate.Worktree, candidate.SHA, currentTarget); landingErr != nil || landed {
		if landingErr == nil {
			landingErr = fmt.Errorf("legacy candidate %s is already contained in current remote target %s", candidate.SHA, currentTarget)
		}
		return WorktreeMergeReceipt{}, nil, false, landingErr
	}
	receiptHash, err := hash(receiptPath)
	if err != nil {
		return WorktreeMergeReceipt{}, nil, false, err
	}
	identity := WorktreeMergeLegacyValidationFailureIdentity{
		SchemaVersion:       worktreeMergeLegacyValidationFailureIdentitySchemaVersion,
		Status:              "legacy_validation_failed_identity_correlated",
		ReceiptPath:         receiptPath,
		AcknowledgementPath: legacyValidationFailureIdentityPath(receiptPath),
		ReceiptSHA256:       receiptHash,
		ReceiptID:           receipt.ID,
		Lane:                receipt.Lane,
		Repository:          receipt.Repository,
		Target:              receipt.Target,
		ReceiptTargetSHA:    receipt.TargetSHA,
		CurrentTargetSHA:    currentTarget,
		Candidate:           candidate,
		ClaimBaseSHA:        claim.BaseSHA,
		Sources:             append([]WorktreeMergeSource(nil), receipt.Sources...),
		Actor:               strings.TrimSpace(actor),
		Reason:              strings.TrimSpace(reason),
		RecordedAt:          time.Now().UTC(),
	}
	identity.ID = legacyValidationFailureIdentityID(identity)
	if existing, readErr := readLegacyValidationFailureIdentity(identity.AcknowledgementPath, receipt, candidate); readErr == nil {
		if existing.CurrentTargetSHA != currentTarget || existing.ClaimBaseSHA != claim.BaseSHA {
			return WorktreeMergeReceipt{}, nil, false, fmt.Errorf("legacy validation-failed identity %s no longer matches current target or candidate claim", identity.AcknowledgementPath)
		}
		return effective, nil, false, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return WorktreeMergeReceipt{}, nil, false, readErr
	}
	return effective, &identity, true, nil
}

// resolveLegacyConflictSupersessionReceiptWithRunner correlates the candidate SHA omitted
// by one legacy unpublished-conflict writer. It accepts no other incomplete
// receipt shape and derives identity only from the still-clean claimed
// candidate after proving the receipt target and all receipted sources are
// ancestors and the candidate is neither published nor landed.

func resolveLegacyConflictSupersessionReceiptWithRunner(ctx context.Context, run runner.Runner, hash func(string) (string, error), projectsRoot string, receipt WorktreeMergeReceipt, receiptPath, actor, reason string) (WorktreeMergeReceipt, *WorktreeMergeLegacyConflictIdentity, bool, error) {
	if err := validateLegacyConflictReceiptShape(receipt, receiptPath); err != nil {
		return WorktreeMergeReceipt{}, nil, false, err
	}
	candidate := receipt.Candidate
	head, err := mergeRevision(ctx, run, candidate.Worktree, "HEAD")
	if err != nil {
		return WorktreeMergeReceipt{}, nil, false, fmt.Errorf("read legacy conflict candidate HEAD: %w", err)
	}
	candidate.SHA = head
	effective := receipt
	effective.Candidate = candidate
	if err := validatePrepareFailureSupersessionReceipt(effective, receiptPath); err != nil {
		return WorktreeMergeReceipt{}, nil, false, fmt.Errorf("corroborate legacy conflict receipt: %w", err)
	}
	claim, err := validateMergeAcknowledgementCandidateWithRunner(ctx, run, projectsRoot, effective, candidate)
	if err != nil {
		return WorktreeMergeReceipt{}, nil, false, fmt.Errorf("corroborate legacy conflict candidate identity: %w", err)
	}
	if err := requireCandidateContainsImmutableClaimBaseWithRunner(ctx, run, candidate.Worktree, claim.BaseSHA, candidate.SHA); err != nil {
		return WorktreeMergeReceipt{}, nil, false, err
	}
	containsTarget, ancestorErr := isMergeAncestorWithRunner(ctx, run, candidate.Worktree, receipt.TargetSHA, candidate.SHA)
	if ancestorErr != nil || !containsTarget {
		if ancestorErr == nil {
			ancestorErr = fmt.Errorf("legacy conflict candidate %s does not contain receipt target %s", candidate.SHA, receipt.TargetSHA)
		}
		return WorktreeMergeReceipt{}, nil, false, ancestorErr
	}
	for _, source := range receipt.Sources {
		contains, sourceErr := isMergeAncestorWithRunner(ctx, run, candidate.Worktree, source.SHA, candidate.SHA)
		if sourceErr != nil || !contains {
			if sourceErr == nil {
				sourceErr = fmt.Errorf("legacy conflict candidate %s does not contain receipted source %s", candidate.SHA, source.SHA)
			}
			return WorktreeMergeReceipt{}, nil, false, sourceErr
		}
	}
	currentTarget, err := fetchExactMergeTargetWithRunner(ctx, run, candidate.Worktree, receipt.Target)
	if err != nil {
		return WorktreeMergeReceipt{}, nil, false, err
	}
	if landed, landingErr := isMergeAncestorWithRunner(ctx, run, candidate.Worktree, candidate.SHA, currentTarget); landingErr != nil || landed {
		if landingErr == nil {
			landingErr = fmt.Errorf("legacy conflict candidate %s is already contained in current remote target %s", candidate.SHA, currentTarget)
		}
		return WorktreeMergeReceipt{}, nil, false, landingErr
	}
	receiptHash, err := hash(receiptPath)
	if err != nil {
		return WorktreeMergeReceipt{}, nil, false, err
	}
	identity := WorktreeMergeLegacyConflictIdentity{
		SchemaVersion:       worktreeMergeLegacyConflictIdentitySchemaVersion,
		Status:              "legacy_conflict_identity_correlated",
		ReceiptPath:         receiptPath,
		AcknowledgementPath: legacyConflictIdentityPath(receiptPath),
		ReceiptSHA256:       receiptHash,
		ReceiptID:           receipt.ID,
		Lane:                receipt.Lane,
		Repository:          receipt.Repository,
		Target:              receipt.Target,
		ReceiptTargetSHA:    receipt.TargetSHA,
		CurrentTargetSHA:    currentTarget,
		Candidate:           candidate,
		ClaimBaseSHA:        claim.BaseSHA,
		Sources:             append([]WorktreeMergeSource(nil), receipt.Sources...),
		Actor:               strings.TrimSpace(actor),
		Reason:              strings.TrimSpace(reason),
		RecordedAt:          time.Now().UTC(),
	}
	identity.ID = legacyConflictIdentityID(identity)
	if existing, readErr := readLegacyConflictIdentity(identity.AcknowledgementPath, effective, candidate); readErr == nil {
		if existing.CurrentTargetSHA != currentTarget || existing.ClaimBaseSHA != claim.BaseSHA {
			return WorktreeMergeReceipt{}, nil, false, fmt.Errorf("legacy conflict identity %s no longer matches current target or candidate claim", identity.AcknowledgementPath)
		}
		return effective, nil, false, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return WorktreeMergeReceipt{}, nil, false, readErr
	}
	return effective, &identity, true, nil
}

func validateLegacyValidationFailedReceiptShape(receipt WorktreeMergeReceipt, receiptPath string) error {
	if err := validateLandedFailureAcknowledgementReceipt(receipt, receiptPath); err != nil {
		return err
	}
	if receipt.SchemaVersion != WorktreeMergeSchemaVersion || receipt.Phase != WorktreeMergePhasePrepare || receipt.Status != WorktreeMergeValidationFailed || receipt.LandingSHA != "" ||
		receipt.Repository == "" || receipt.Target == "" || receipt.TargetSHA == "" || len(receipt.Sources) == 0 ||
		receipt.Candidate.Task == "" || receipt.Candidate.Worktree == "" || receipt.Candidate.Branch == "" || receipt.Candidate.SHA != "" ||
		receipt.Validation.Repository != receipt.Repository || receipt.Validation.Path != receipt.Candidate.Worktree || receipt.Validation.Revision == "" ||
		!worktreeMergeOperationIDMatchesRecordedSourceSet(receipt) || receipt.Candidate.Task != receipt.ID || receipt.CreatedAt.IsZero() || receipt.UpdatedAt.IsZero() {
		return fmt.Errorf("receipt %s is not the exact legacy validation_failed missing-candidate-SHA shape", receiptPath)
	}
	for _, source := range receipt.Sources {
		if source.Task == "" || source.Worktree == "" || source.Branch == "" || source.SHA == "" {
			return fmt.Errorf("receipt %s has an incomplete immutable source identity", receiptPath)
		}
	}
	return nil
}

func validateLegacyConflictReceiptShape(receipt WorktreeMergeReceipt, receiptPath string) error {
	unpublished, unpublishedErr := effectiveUnpublishedConflict(receipt)
	if unpublishedErr != nil {
		return unpublishedErr
	}
	if receipt.ReceiptPath != receiptPath || receipt.SchemaVersion != WorktreeMergeSchemaVersion || receipt.Phase != WorktreeMergePhasePrepare ||
		receipt.Status != WorktreeMergeConflict || receipt.LandingSHA != "" || !unpublished || receipt.Repository == "" || receipt.Target == "" ||
		receipt.TargetSHA == "" || len(receipt.Sources) == 0 || receipt.Candidate.Task == "" || receipt.Candidate.Worktree == "" ||
		receipt.Candidate.Branch == "" || receipt.Candidate.SHA != "" || receipt.Candidate.Task != receipt.ID || receipt.Lane == "" ||
		receipt.Lane != worktreeMergeLaneID(receipt.Repository, receipt.Target) || receipt.CreatedAt.IsZero() || receipt.UpdatedAt.IsZero() {
		return fmt.Errorf("receipt %s is not the exact legacy unpublished conflict missing-candidate-SHA shape", receiptPath)
	}
	if !worktreeMergeOperationIDMatchesRecordedSourceSet(receipt) {
		return fmt.Errorf("receipt %s has an invalid legacy conflict operation identity", receiptPath)
	}
	for _, source := range receipt.Sources {
		if source.Task == "" || source.Worktree == "" || source.Branch == "" || source.SHA == "" {
			return fmt.Errorf("receipt %s has an incomplete immutable source identity", receiptPath)
		}
	}
	return nil
}

// validateValidationFailedSupersessionSourceWithRunner preserves the exact historical
// source identity for an unlanded validation failure. The sole forward-repair
// exception permits that same managed source to advance, but only when its
// immutable commit and claim base remain ancestry roots of the replacement.
// A sibling worktree, branch, task, changed base, or rewritten source remains
// an exact-identity refusal.

func validateValidationFailedSupersessionSourceWithRunner(ctx context.Context, run runner.Runner, projectsRoot string, receipt WorktreeMergeReceipt, source WorktreeMergeSource) (head, claimBase string, err error) {
	guard, err := worktrees.Guard(ctx, source.Worktree, worktrees.GuardOptions{ProjectsRoot: projectsRoot, Base: receipt.Target})
	if err != nil {
		return "", "", fmt.Errorf("guard receipted source %s: %w", source.Worktree, err)
	}
	if guard.Kind != "linked" || guard.Transient || guard.Branch != source.Branch || filepath.Clean(guard.Path) != filepath.Clean(source.Worktree) {
		return "", "", fmt.Errorf("receipted source %s no longer has its exact linked-worktree identity", source.Worktree)
	}
	if err := requireCleanMergeWorktreeWithRunner(ctx, run, source.Worktree); err != nil {
		return "", "", fmt.Errorf("receipted source %s is not clean: %w", source.Worktree, err)
	}
	head, err = mergeRevision(ctx, run, source.Worktree, "HEAD")
	if err != nil {
		return "", "", fmt.Errorf("read receipted source %s HEAD: %w", source.Worktree, err)
	}
	view, err := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: projectsRoot, Worktree: source.Worktree})
	if err != nil {
		return "", "", fmt.Errorf("load receipted source Work Log %s: %w", source.Worktree, err)
	}
	if view.Claim == nil || view.Claim.Lifecycle != "active" || view.Claim.Repository != receipt.Repository || view.Claim.Task != source.Task ||
		filepath.Clean(view.Claim.Worktree) != filepath.Clean(source.Worktree) || view.Claim.Branch != source.Branch {
		return "", "", fmt.Errorf("receipted source %s has no matching active Work Log claim", source.Worktree)
	}
	if strings.TrimSpace(view.Claim.Base) == "" || strings.TrimSpace(view.Claim.BaseSHA) == "" {
		return "", "", fmt.Errorf("receipted source %s has no immutable claim base", source.Worktree)
	}
	containsClaimBase, ancestorErr := isMergeAncestorWithRunner(ctx, run, source.Worktree, view.Claim.BaseSHA, source.SHA)
	if ancestorErr != nil {
		return "", "", fmt.Errorf("receipted source %s does not descend from immutable claim base %s: %w", source.Worktree, view.Claim.BaseSHA, ancestorErr)
	}
	if !containsClaimBase {
		return "", "", fmt.Errorf("receipted source %s does not descend from immutable claim base %s", source.Worktree, view.Claim.BaseSHA)
	}
	containsRecordedSource, ancestorErr := isMergeAncestorWithRunner(ctx, run, source.Worktree, source.SHA, head)
	if ancestorErr != nil {
		return "", "", fmt.Errorf("verify receipted source %s descendant ancestry: %w", source.Worktree, ancestorErr)
	}
	if !containsRecordedSource {
		return "", "", fmt.Errorf("receipted source %s does not retain recorded source %s", source.Worktree, source.SHA)
	}
	return head, view.Claim.BaseSHA, nil
}
