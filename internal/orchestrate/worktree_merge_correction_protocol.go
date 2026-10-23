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

// CorrectValidationFailedSelfSupersession repairs only a pre-guard
// self-supersession. It never replaces that acknowledgement: it records one
// separate correction whose identity pins the exact existing acknowledgement,
// receipt, immutable claim, and distinct replacement evidence.
func CorrectValidationFailedSelfSupersession(ctx context.Context, options WorktreeMergeSelfSupersessionCorrectionOptions) (WorktreeMergeSelfSupersessionCorrection, error) {
	return correctValidationFailedSelfSupersessionInjected(ctx, options, nil)
}

// correctValidationFailedSelfSupersessionInjected is
// CorrectValidationFailedSelfSupersession's test seam (task-9 PR-4, review
// findings): production always reaches it through
// CorrectValidationFailedSelfSupersession, which passes a nil
// *filewrite.Injector, so production behaviour is unchanged. A test passes
// its own Injector, including an Injector.Hook at the final persist's
// StepLink, to build a real concurrent-correction race at the caller level,
// covering both the post-link re-read/refuse branch and the converging
// existing-correction return.
func correctValidationFailedSelfSupersessionInjected(ctx context.Context, options WorktreeMergeSelfSupersessionCorrectionOptions, inj *filewrite.Injector) (WorktreeMergeSelfSupersessionCorrection, error) {
	return correctValidationFailedSelfSupersessionWithRunner(ctx, defaultRunner, readWorktreeMergeReceipt, worktreeMergeReceiptSHA256, options, inj)
}

func correctValidationFailedSelfSupersessionWithRunner(ctx context.Context, run runner.Runner, read func(string) (WorktreeMergeReceipt, error), hash func(string) (string, error), options WorktreeMergeSelfSupersessionCorrectionOptions, inj *filewrite.Injector) (WorktreeMergeSelfSupersessionCorrection, error) {
	if strings.TrimSpace(options.ExpectedSupersessionSHA256) == "" || strings.TrimSpace(options.ExpectedImmutableClaimSHA256) == "" {
		return WorktreeMergeSelfSupersessionCorrection{}, errors.New("--expected-supersession-sha256 and --expected-immutable-claim-sha256 are required")
	}
	if strings.TrimSpace(options.ReplacementWorktree) == "" {
		return WorktreeMergeSelfSupersessionCorrection{}, errors.New("replacement worktree is required")
	}
	if options.Apply && (strings.TrimSpace(options.Actor) == "" || strings.TrimSpace(options.Reason) == "") {
		return WorktreeMergeSelfSupersessionCorrection{}, errors.New("--actor and --reason are required with --apply")
	}
	receiptPath, err := resolveWorktreeMergeReceiptPath(options.ProjectsRoot, options.Receipt)
	if err != nil {
		return WorktreeMergeSelfSupersessionCorrection{}, err
	}
	receipt, err := read(receiptPath)
	if err != nil {
		return WorktreeMergeSelfSupersessionCorrection{}, err
	}
	lock, err := AcquireOperationLock(options.ProjectsRoot, receipt.Lane, true)
	if err != nil {
		return WorktreeMergeSelfSupersessionCorrection{}, err
	}
	defer func() { _ = lock.Release() }()
	// All evidence is re-read after the lane lock, including the immutable
	// receipt that the corrupt acknowledgement already hashes.
	receipt, err = read(receiptPath)
	if err != nil {
		return WorktreeMergeSelfSupersessionCorrection{}, err
	}
	if err := validateValidationFailedSupersessionReceipt(receipt, receiptPath); err != nil {
		return WorktreeMergeSelfSupersessionCorrection{}, err
	}
	ackPath := validationFailureSupersessionPath(receiptPath)
	supersession, err := readValidationFailureSupersession(ackPath, receipt)
	if err != nil {
		return WorktreeMergeSelfSupersessionCorrection{}, fmt.Errorf("read existing supersession: %w", err)
	}
	if supersession.Replacement != supersession.OriginalCandidate || supersession.OriginalCandidate != receipt.Candidate || supersession.ReplacementClaimBaseSHA != supersession.OriginalClaimBaseSHA {
		return WorktreeMergeSelfSupersessionCorrection{}, errors.New("existing supersession is not the exact self-supersession correction shape")
	}
	supersessionHash, err := hash(ackPath)
	if err != nil || supersessionHash != options.ExpectedSupersessionSHA256 {
		if err == nil {
			err = fmt.Errorf("supersession SHA256 %s does not match expected %s", supersessionHash, options.ExpectedSupersessionSHA256)
		}
		return WorktreeMergeSelfSupersessionCorrection{}, err
	}
	originalClaim, err := validateMergeAcknowledgementCandidateWithRunner(ctx, run, options.ProjectsRoot, receipt, receipt.Candidate)
	if err != nil {
		return WorktreeMergeSelfSupersessionCorrection{}, fmt.Errorf("validate failed candidate: %w", err)
	}
	claimHash, err := hash(originalClaim.ClaimPath)
	if err != nil {
		return WorktreeMergeSelfSupersessionCorrection{}, fmt.Errorf("read immutable failed candidate claim: %w", err)
	}
	if claimHash != options.ExpectedImmutableClaimSHA256 || originalClaim.BaseSHA != supersession.OriginalClaimBaseSHA {
		return WorktreeMergeSelfSupersessionCorrection{}, errors.New("immutable failed candidate claim bytes or base no longer match expected supersession evidence")
	}
	replacement, replacementClaim, err := validateValidationFailureReplacementWithRunner(ctx, run, options.ProjectsRoot, receipt, options.ReplacementWorktree)
	if err != nil {
		return WorktreeMergeSelfSupersessionCorrection{}, err
	}
	if replacement == receipt.Candidate || replacement.SHA == receipt.Candidate.SHA {
		return WorktreeMergeSelfSupersessionCorrection{}, errors.New("corrected replacement candidate must be distinct from the failed receipt candidate")
	}
	if err := requireImmutableHistoricalWorktreeMergeSources(ctx, replacement.Worktree, receipt); err != nil {
		return WorktreeMergeSelfSupersessionCorrection{}, fmt.Errorf("validate immutable historical source evidence: %w", err)
	}
	currentTarget, err := fetchExactMergeTargetWithRunner(ctx, run, replacement.Worktree, receipt.Target)
	if err != nil {
		return WorktreeMergeSelfSupersessionCorrection{}, err
	}
	for _, root := range append([]string{originalClaim.BaseSHA, receipt.TargetSHA, supersession.CurrentTargetSHA, currentTarget, replacementClaim.BaseSHA}, sourceSHAs(immutableHistoricalWorktreeMergeSources(receipt))...) {
		contains, ancestorErr := isMergeAncestorWithRunner(ctx, run, replacement.Worktree, root, replacement.SHA)
		if ancestorErr != nil || !contains {
			if ancestorErr == nil {
				ancestorErr = fmt.Errorf("corrected replacement %s does not contain required immutable root %s", replacement.SHA, root)
			}
			return WorktreeMergeSelfSupersessionCorrection{}, ancestorErr
		}
	}
	receiptHash, err := hash(receiptPath)
	if err != nil || receiptHash != supersession.ReceiptSHA256 {
		if err == nil {
			err = errors.New("receipt bytes no longer match the existing supersession acknowledgement")
		}
		return WorktreeMergeSelfSupersessionCorrection{}, err
	}
	correctionPath := selfSupersessionCorrectionPath(receiptPath)
	correction := WorktreeMergeSelfSupersessionCorrection{
		SchemaVersion: worktreeMergeSelfSupersessionCorrectionSchemaVersion, Status: "validation_failure_self_supersession_corrected",
		CorrectionPath: correctionPath, ReceiptPath: receiptPath, ReceiptSHA256: receiptHash, ImmutableClaimSHA256: claimHash,
		SupersessionPath: ackPath, SupersessionSHA256: supersessionHash, SupersessionID: supersession.ID,
		OriginalCandidate: receipt.Candidate, OriginalClaimBaseSHA: originalClaim.BaseSHA,
		CorrectedReplacement: replacement, ReplacementClaimBaseSHA: replacementClaim.BaseSHA, CurrentTargetSHA: currentTarget,
		Sources: append([]WorktreeMergeSource(nil), receipt.Sources...), Actor: strings.TrimSpace(options.Actor), Reason: strings.TrimSpace(options.Reason), RecordedAt: time.Now().UTC(),
	}
	correction.ID = selfSupersessionCorrectionID(correction)
	if existing, readErr := readSelfSupersessionCorrection(correctionPath, receipt, supersession); readErr == nil {
		if !sameSelfSupersessionCorrection(existing, correction) {
			return WorktreeMergeSelfSupersessionCorrection{}, fmt.Errorf("self-supersession correction %s binds different immutable evidence", correctionPath)
		}
		return existing, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return WorktreeMergeSelfSupersessionCorrection{}, readErr
	}
	if !options.Apply {
		return correction, nil
	}
	if err := persistSelfSupersessionCorrectionInjected(correctionPath, correction, inj); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return WorktreeMergeSelfSupersessionCorrection{}, err
		}
		existing, readErr := readSelfSupersessionCorrection(correctionPath, receipt, supersession)
		if readErr != nil || !sameSelfSupersessionCorrection(existing, correction) {
			if readErr != nil {
				return WorktreeMergeSelfSupersessionCorrection{}, readErr
			}
			return WorktreeMergeSelfSupersessionCorrection{}, fmt.Errorf("concurrent self-supersession correction %s binds different immutable evidence", correctionPath)
		}
		return existing, nil
	}
	return correction, nil
}

func validateSelfSupersessionCorrection(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt, supersession WorktreeMergeValidationFailureSupersession) error {
	return validateSelfSupersessionCorrectionWithRunner(ctx, defaultRunner, worktreeMergeReceiptSHA256, projectsRoot, receipt, supersession)
}

func validateSelfSupersessionCorrectionWithRunner(ctx context.Context, run runner.Runner, hash func(string) (string, error), projectsRoot string, receipt WorktreeMergeReceipt, supersession WorktreeMergeValidationFailureSupersession) error {
	if err := validateValidationFailedSupersessionReceipt(receipt, receipt.ReceiptPath); err != nil {
		return err
	}
	correction, err := readSelfSupersessionCorrection(selfSupersessionCorrectionPath(receipt.ReceiptPath), receipt, supersession)
	if err != nil {
		return err
	}
	originalClaim, err := validateMergeAcknowledgementCandidateWithRunner(ctx, run, projectsRoot, receipt, receipt.Candidate)
	if err != nil {
		return fmt.Errorf("validate corrected self-supersession original candidate: %w", err)
	}
	originalClaimHash, err := hash(originalClaim.ClaimPath)
	if err != nil {
		return fmt.Errorf("read corrected self-supersession immutable claim: %w", err)
	}
	if originalClaimHash != correction.ImmutableClaimSHA256 ||
		originalClaim.BaseSHA != supersession.OriginalClaimBaseSHA || originalClaim.BaseSHA != correction.OriginalClaimBaseSHA {
		return errors.New("corrected self-supersession immutable claim SHA256 or base no longer matches recorded evidence")
	}
	if _, statErr := os.Lstat(correction.CorrectedReplacement.Worktree); errors.Is(statErr, os.ErrNotExist) {
		return validateCleanedSelfSupersessionReplacementWithRunner(ctx, run, worktrees.CanonicalRepositoryPath, projectsRoot, receipt, supersession, correction)
	} else if statErr != nil {
		return fmt.Errorf("inspect corrected self-supersession replacement: %w", statErr)
	}
	replacement, replacementClaim, err := validateValidationFailureReplacementWithRunner(ctx, run, projectsRoot, receipt, correction.CorrectedReplacement.Worktree)
	if err != nil {
		return fmt.Errorf("validate corrected self-supersession replacement: %w", err)
	}
	if replacement.Task != correction.CorrectedReplacement.Task || filepath.Clean(replacement.Worktree) != filepath.Clean(correction.CorrectedReplacement.Worktree) ||
		replacement.Branch != correction.CorrectedReplacement.Branch || replacementClaim.BaseSHA != correction.ReplacementClaimBaseSHA {
		return errors.New("corrected self-supersession replacement identity or claim base no longer matches recorded evidence")
	}
	containsRecordedReplacement, ancestorErr := isMergeAncestorWithRunner(ctx, run, replacement.Worktree, correction.CorrectedReplacement.SHA, replacement.SHA)
	if ancestorErr != nil {
		return fmt.Errorf("verify corrected self-supersession replacement ancestry: %w", ancestorErr)
	}
	if !containsRecordedReplacement {
		return errors.New("corrected self-supersession replacement does not retain its recorded replacement commit")
	}
	if err := requireImmutableHistoricalWorktreeMergeSources(ctx, replacement.Worktree, receipt); err != nil {
		return fmt.Errorf("validate corrected self-supersession historical source: %w", err)
	}
	currentTarget, err := fetchExactMergeTargetWithRunner(ctx, run, replacement.Worktree, receipt.Target)
	if err != nil {
		return err
	}
	if currentTarget != supersession.CurrentTargetSHA || currentTarget != correction.CurrentTargetSHA {
		containsRecordedTarget, targetAncestorErr := isMergeAncestorWithRunner(ctx, run, replacement.Worktree, correction.CurrentTargetSHA, currentTarget)
		if targetAncestorErr != nil {
			return fmt.Errorf("verify corrected self-supersession target ancestry: %w", targetAncestorErr)
		}
		if !containsRecordedTarget {
			return fmt.Errorf("corrected self-supersession target %s is not a descendant of recorded target %s", currentTarget, correction.CurrentTargetSHA)
		}
	}
	for _, root := range append([]string{correction.CorrectedReplacement.SHA, originalClaim.BaseSHA, receipt.TargetSHA, supersession.CurrentTargetSHA, correction.CurrentTargetSHA, replacementClaim.BaseSHA}, sourceSHAs(immutableHistoricalWorktreeMergeSources(receipt))...) {
		contains, ancestorErr := isMergeAncestorWithRunner(ctx, run, replacement.Worktree, root, replacement.SHA)
		if ancestorErr != nil || !contains {
			if ancestorErr == nil {
				ancestorErr = fmt.Errorf("corrected self-supersession replacement %s does not contain recorded immutable root %s", replacement.SHA, root)
			}
			return ancestorErr
		}
	}
	return nil
}

func validateCleanedSelfSupersessionReplacementWithRunner(ctx context.Context, run runner.Runner, resolveCanonical func(string, string) (string, error), projectsRoot string, receipt WorktreeMergeReceipt, supersession WorktreeMergeValidationFailureSupersession, correction WorktreeMergeSelfSupersessionCorrection) error {
	proof, err := worktrees.FindTerminalCleanupProof(
		projectsRoot,
		receipt.Repository,
		receipt.Target,
		correction.CorrectedReplacement.Task,
		correction.CorrectedReplacement.Worktree,
		correction.CorrectedReplacement.Branch,
	)
	if err != nil {
		return fmt.Errorf("validate cleaned corrected self-supersession replacement: %w", err)
	}
	canonical, err := resolveCanonical(projectsRoot, receipt.Repository)
	if err != nil {
		return fmt.Errorf("resolve cleaned corrected self-supersession canonical repository: %w", err)
	}
	if err := requireImmutableHistoricalWorktreeMergeSources(ctx, canonical, receipt); err != nil {
		return fmt.Errorf("validate cleaned corrected self-supersession historical source: %w", err)
	}
	currentTarget, err := fetchExactMergeTargetWithRunner(ctx, run, canonical, receipt.Target)
	if err != nil {
		return err
	}
	if currentTarget != supersession.CurrentTargetSHA || currentTarget != correction.CurrentTargetSHA {
		containsRecordedTarget, targetAncestorErr := isMergeAncestorWithRunner(ctx, run, canonical, correction.CurrentTargetSHA, currentTarget)
		if targetAncestorErr != nil {
			return fmt.Errorf("verify cleaned corrected self-supersession target ancestry: %w", targetAncestorErr)
		}
		if !containsRecordedTarget {
			return fmt.Errorf("cleaned corrected self-supersession target %s is not a descendant of recorded target %s", currentTarget, correction.CurrentTargetSHA)
		}
	}
	containsCleanupHead, err := isMergeAncestorWithRunner(ctx, run, canonical, proof.Result.HeadSHA, currentTarget)
	if err != nil || !containsCleanupHead {
		if err == nil {
			err = fmt.Errorf("current target %s does not contain cleaned replacement head %s", currentTarget, proof.Result.HeadSHA)
		}
		return fmt.Errorf("verify cleaned corrected self-supersession landing: %w", err)
	}
	for _, root := range append([]string{correction.CorrectedReplacement.SHA, correction.OriginalClaimBaseSHA, correction.ReplacementClaimBaseSHA, receipt.TargetSHA, supersession.CurrentTargetSHA, correction.CurrentTargetSHA}, sourceSHAs(immutableHistoricalWorktreeMergeSources(receipt))...) {
		contains, ancestorErr := isMergeAncestorWithRunner(ctx, run, canonical, root, proof.Result.HeadSHA)
		if ancestorErr != nil || !contains {
			if ancestorErr == nil {
				ancestorErr = fmt.Errorf("cleaned corrected self-supersession replacement %s does not contain recorded immutable root %s", proof.Result.HeadSHA, root)
			}
			return ancestorErr
		}
	}
	return nil
}
