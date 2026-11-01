package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// WorktreeMergeLegacyLandedValidation distinguishes the historical validated
// commit from its still-owned checkout. Acknowledgement releases only the old
// receipt's lane; it never seals or retires the observed descendant.
type WorktreeMergeLegacyLandedValidation struct {
	IdentityID           string                                      `json:"identity_id"`
	ObservedCandidateSHA string                                      `json:"observed_candidate_sha"`
	RetiredSources       []WorktreeMergeRetiredDescendantSourceProof `json:"retired_sources"`
}

// WorktreeMergeRetiredDescendantSourceProof binds an original source to the
// exact final commit that WB authenticated and removed. Source.SHA remains the
// original receipt SHA; FinalSHA is never substituted into historical records.
type WorktreeMergeRetiredDescendantSourceProof struct {
	WorktreeMergeRetiredSourceProof
	FinalSHA  string `json:"final_sha"`
	ClaimBase string `json:"claim_base"`
}

type legacyLandedValidationState struct {
	identity WorktreeMergeLegacyValidationFailureIdentity
	evidence WorktreeMergeLegacyLandedValidation
}

// These are the existing external evidence readers, grouped so unavailable or
// changing IO can be tested without replacing the ancestry/identity policy.
type legacyLandedProofReaders struct {
	canonical func(string, string) (string, error)
	hash      func(string) (string, error)
	cleanup   func(string, string, string, string, string, string) (*worktrees.TerminalCleanupProof, error)
	terminal  func(string, worktrees.TerminalWorkLogExpectation) (string, error)
	landing   func(context.Context, WorktreeMergeReceipt) (string, string, string, string, string, error)
}

func nativeLegacyLandedProofReaders() legacyLandedProofReaders {
	return legacyLandedProofReaders{worktrees.CanonicalRepositoryPath, worktreeMergeReceiptSHA256, worktrees.FindTerminalCleanupProof, worktrees.ReadRemovedTerminalWorkLogClaimBase, proveStrandedPullRequestLanding}
}

func legacyLandedValidationReceipt(receipt WorktreeMergeReceipt) (WorktreeMergeReceipt, error) {
	if err := validateLegacyValidationFailedReceiptShape(receipt, receipt.ReceiptPath); err != nil {
		return WorktreeMergeReceipt{}, err
	}
	if receipt.PullRequest != "" || receipt.PublishedCandidateSHA != "" || !receipt.Validation.WorkspaceClean || receipt.Validation.Status != "failed" {
		return WorktreeMergeReceipt{}, errors.New("legacy landed validation requires an unpublished clean validation identity")
	}
	if identity := receipt.ValidationIdentity; identity != nil && (identity.CandidateSHA != receipt.Validation.Revision || identity.TargetSHA != receipt.TargetSHA || !reflect.DeepEqual(identity.SourceSHAs, sourceSHAs(receipt.Sources))) {
		return WorktreeMergeReceipt{}, errors.New("legacy validation fingerprint contradicts immutable candidate, target, or sources")
	}
	effective := receipt
	effective.Candidate.SHA = receipt.Validation.Revision
	return effective, nil
}

func acknowledgeLegacyLandedValidationFailure(ctx context.Context, options WorktreeMergeLandedFailureAcknowledgementOptions, receipt WorktreeMergeReceipt, path string, run runner.Runner, read func(string) (WorktreeMergeReceipt, error), persist func(string, WorktreeMergeLandedFailureAcknowledgement) error) (WorktreeMergeLandedFailureAcknowledgement, error) {
	return acknowledgeLegacyLandedValidationFailureWithIdentityWriter(ctx, options, receipt, path, run, read, persist, persistLegacyValidationFailureIdentity)
}

func acknowledgeLegacyLandedValidationFailureWithIdentityWriter(ctx context.Context, options WorktreeMergeLandedFailureAcknowledgementOptions, receipt WorktreeMergeReceipt, path string, run runner.Runner, read func(string) (WorktreeMergeReceipt, error), persist func(string, WorktreeMergeLandedFailureAcknowledgement) error, persistIdentity func(string, WorktreeMergeLegacyValidationFailureIdentity) error) (WorktreeMergeLandedFailureAcknowledgement, error) {
	if _, err := legacyLandedValidationReceipt(receipt); err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	if options.Apply && (strings.TrimSpace(options.Actor) == "" || strings.TrimSpace(options.Reason) == "") {
		return WorktreeMergeLandedFailureAcknowledgement{}, errors.New("--actor and --reason are required with --apply")
	}
	lock, err := AcquireOperationLock(options.ProjectsRoot, receipt.Lane, true)
	if err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	defer func() { _ = lock.Release() }()
	locked, err := read(path)
	if err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	if !reflect.DeepEqual(locked, receipt) {
		return WorktreeMergeLandedFailureAcknowledgement{}, errors.New("legacy validation receipt changed before proof")
	}
	state, err := inspectLegacyLandedValidation(ctx, options, receipt, run)
	if err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	if options.Apply {
		// Reobserve custody, terminal/cleanup/PR evidence and exact remote target
		// immediately before publishing either append-only acknowledgement.
		fresh, err := inspectLegacyLandedValidation(ctx, options, receipt, run)
		if err != nil {
			return WorktreeMergeLandedFailureAcknowledgement{}, err
		}
		if !reflect.DeepEqual(state, fresh) {
			return WorktreeMergeLandedFailureAcknowledgement{}, errors.New("legacy landed validation proof changed before acknowledgement")
		}
		locked, err := read(path)
		if err != nil {
			return WorktreeMergeLandedFailureAcknowledgement{}, err
		}
		if !reflect.DeepEqual(locked, receipt) {
			return WorktreeMergeLandedFailureAcknowledgement{}, errors.New("legacy validation receipt changed before acknowledgement")
		}
		state.identity.RecordedAt = time.Now().UTC()
		if existing, err := readLegacyValidationFailureIdentity(state.identity.AcknowledgementPath, receipt, state.identity.Candidate); err == nil {
			if !sameLegacyValidationFailureIdentity(existing, state.identity) {
				return WorktreeMergeLandedFailureAcknowledgement{}, errors.New("legacy landed identity binds different proof")
			}
			state.identity = existing
		} else if !errors.Is(err, os.ErrNotExist) {
			return WorktreeMergeLandedFailureAcknowledgement{}, err
		} else if err := persistIdentity(state.identity.AcknowledgementPath, state.identity); err != nil {
			return WorktreeMergeLandedFailureAcknowledgement{}, err
		}
	}
	if state.identity.RecordedAt.IsZero() {
		state.identity.RecordedAt = time.Now().UTC()
	}
	i := state.identity
	ack := WorktreeMergeLandedFailureAcknowledgement{
		SchemaVersion: worktreeMergeLandedFailureAcknowledgementSchemaVersion, Status: "landed_failure_acknowledged",
		ReceiptPath: path, ReceiptID: receipt.ID, ReceiptStatus: receipt.Status, Lane: receipt.Lane,
		AcknowledgementPath: landedFailureAcknowledgementPath(path), Repository: receipt.Repository, Target: receipt.Target,
		ReceiptSHA256: i.ReceiptSHA256, ReceiptTargetSHA: receipt.TargetSHA, CurrentTargetSHA: i.CurrentTargetSHA,
		CandidateSHA: i.Candidate.SHA, ClaimBaseSHA: i.ClaimBaseSHA, CandidateWorktree: i.Candidate.Worktree,
		CandidateBranch: i.Candidate.Branch, Sources: append([]WorktreeMergeSource(nil), receipt.Sources...),
		Actor: i.Actor, Reason: i.Reason, RecordedAt: i.RecordedAt, LegacyValidation: &state.evidence,
	}
	ack.ID = landedFailureAcknowledgementID(ack)
	return finishLandedFailureAcknowledgement(receipt, ack, options.Apply, persist)
}

func inspectLegacyLandedValidation(ctx context.Context, options WorktreeMergeLandedFailureAcknowledgementOptions, receipt WorktreeMergeReceipt, run runner.Runner) (legacyLandedValidationState, error) {
	return inspectLegacyLandedValidationWithReaders(ctx, options, receipt, run, nativeLegacyLandedProofReaders())
}

func inspectLegacyLandedValidationWithReaders(ctx context.Context, options WorktreeMergeLandedFailureAcknowledgementOptions, receipt WorktreeMergeReceipt, run runner.Runner, readers legacyLandedProofReaders) (legacyLandedValidationState, error) {
	var empty legacyLandedValidationState
	effective, err := legacyLandedValidationReceipt(receipt)
	if err != nil {
		return empty, err
	}
	claim, descendant, err := validatePrepareFailureSupersessionCandidateWithRunner(ctx, run, options.ProjectsRoot, effective)
	if err != nil {
		return empty, err
	}
	if descendant == "" {
		descendant = effective.Candidate.SHA
	}
	if err := requireCandidateContainsImmutableClaimBaseWithRunner(ctx, run, effective.Candidate.Worktree, claim.BaseSHA, effective.Candidate.SHA); err != nil {
		return empty, err
	}
	canonical, err := readers.canonical(options.ProjectsRoot, receipt.Repository)
	if err != nil {
		return empty, err
	}
	target, err := fetchExactMergeTargetWithRunner(ctx, run, canonical, receipt.Target)
	if err != nil {
		return empty, err
	}
	roots := append([]string{claim.BaseSHA, receipt.TargetSHA, effective.Candidate.SHA}, sourceSHAs(receipt.Sources)...)
	for _, root := range roots {
		if err := requireLegacyLandedAncestor(ctx, run, canonical, root, target); err != nil {
			return empty, err
		}
		if err := requireLegacyLandedAncestor(ctx, run, canonical, root, effective.Candidate.SHA); err != nil {
			return empty, err
		}
	}
	proofs := make([]WorktreeMergeRetiredDescendantSourceProof, 0, len(receipt.Sources))
	for _, source := range receipt.Sources {
		proof, err := proveLegacyLandedRetiredSource(ctx, options.ProjectsRoot, effective, source, canonical, target, run, readers)
		if err != nil {
			return empty, err
		}
		proofs = append(proofs, proof)
	}
	digest, err := readers.hash(receipt.ReceiptPath)
	if err != nil {
		return empty, err
	}
	// The checkout and earlier source identities must remain unchanged while
	// later source/PR reads run. An acknowledged descendant is not retired.
	finalClaim, finalDescendant, err := validatePrepareFailureSupersessionCandidateWithRunner(ctx, run, options.ProjectsRoot, effective)
	if err != nil {
		return empty, err
	}
	if finalDescendant == "" {
		finalDescendant = effective.Candidate.SHA
	}
	if finalDescendant != descendant || !reflect.DeepEqual(finalClaim, claim) {
		return empty, errors.New("legacy candidate custody changed during proof")
	}
	for _, source := range receipt.Sources {
		if err := requireAbsentRetiredSource(ctx, canonical, source, run); err != nil {
			return empty, err
		}
	}
	finalTarget, err := fetchExactMergeTargetWithRunner(ctx, run, canonical, receipt.Target)
	if err != nil {
		return empty, err
	}
	if finalTarget != target {
		return empty, errors.New("legacy landed target changed during proof")
	}
	identity := WorktreeMergeLegacyValidationFailureIdentity{
		SchemaVersion: worktreeMergeLegacyValidationFailureIdentitySchemaVersion, Status: "legacy_validation_failed_identity_correlated",
		ReceiptPath: receipt.ReceiptPath, AcknowledgementPath: legacyValidationFailureIdentityPath(receipt.ReceiptPath),
		ReceiptSHA256: digest, ReceiptID: receipt.ID, Lane: receipt.Lane, Repository: receipt.Repository, Target: receipt.Target,
		ReceiptTargetSHA: receipt.TargetSHA, CurrentTargetSHA: target, Candidate: effective.Candidate, ClaimBaseSHA: claim.BaseSHA,
		Sources: append([]WorktreeMergeSource(nil), receipt.Sources...), Actor: strings.TrimSpace(options.Actor), Reason: strings.TrimSpace(options.Reason),
	}
	// A dry run has no operator assertion and never persists it. Stable timestamps
	// are selected only after comparing the independently observed proof states.
	identity.ID = legacyValidationFailureIdentityID(identity)
	return legacyLandedValidationState{identity: identity, evidence: WorktreeMergeLegacyLandedValidation{IdentityID: identity.ID, ObservedCandidateSHA: descendant, RetiredSources: proofs}}, nil
}

func requireLegacyLandedAncestor(ctx context.Context, run runner.Runner, path, ancestor, descendant string) error {
	contains, err := isMergeAncestorWithRunner(ctx, run, path, ancestor, descendant)
	if err != nil {
		return err
	}
	if !contains {
		return fmt.Errorf("legacy landed proof %s does not contain required root %s", descendant, ancestor)
	}
	return nil
}

func proveLegacyLandedRetiredSource(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt, source WorktreeMergeSource, canonical, target string, run runner.Runner, readers legacyLandedProofReaders) (WorktreeMergeRetiredDescendantSourceProof, error) {
	var empty WorktreeMergeRetiredDescendantSourceProof
	if err := requireAbsentRetiredSource(ctx, canonical, source, run); err != nil {
		return empty, err
	}
	cleanup, err := readers.cleanup(projectsRoot, receipt.Repository, receipt.Target, source.Task, source.Worktree, source.Branch)
	if err != nil {
		return empty, err
	}
	r := cleanup.Result
	digest, err := readers.hash(cleanup.ReportPath)
	if err != nil {
		return empty, err
	}
	pr := r.MergedPullRequest
	if pr == nil || pr.Repository != receipt.Repository || pr.Base != receipt.Target || pr.HeadSHA != r.HeadSHA || pr.State != "MERGED" || pr.URL == "" || pr.Number <= 0 || pr.MergeSHA == "" || pr.Merged == nil {
		return empty, errors.New("legacy retired source lacks exact successful cleanup and merged pull-request identity")
	}
	base := r.RecordedBase
	if base == "" {
		base = r.Base
	}
	expectation := worktrees.TerminalWorkLogExpectation{Task: source.Task, Repository: receipt.Repository, Worktree: source.Worktree, Branch: source.Branch, Base: base, FinalCommit: r.HeadSHA}
	claimBase, err := readers.terminal(projectsRoot, expectation)
	if err != nil {
		return empty, err
	}
	for _, pair := range [][2]string{{claimBase, source.SHA}, {source.SHA, r.HeadSHA}, {r.HeadSHA, target}, {r.RemoteTargetSHA, target}, {pr.MergeSHA, target}} {
		if err := requireLegacyLandedAncestor(ctx, run, canonical, pair[0], pair[1]); err != nil {
			return empty, err
		}
	}
	synthetic := receipt
	synthetic.Candidate = WorktreeMergeCandidate{Task: source.Task, Worktree: source.Worktree, Branch: source.Branch, SHA: r.HeadSHA}
	synthetic.PullRequest, synthetic.TargetSHA = fmt.Sprint(pr.Number), claimBase
	landing, current, changedHead, kind, tree, err := readers.landing(ctx, synthetic)
	if err != nil {
		return empty, err
	}
	if current != target || landing != pr.MergeSHA || changedHead != "" || kind != "ancestor" || tree != "" {
		return empty, errors.New("legacy retired source pull request or exact target changed")
	}
	finalDigest, err := readers.hash(cleanup.ReportPath)
	if err != nil {
		return empty, err
	}
	if finalDigest != digest {
		return empty, errors.New("legacy source cleanup changed during proof")
	}
	if err := requireAbsentRetiredSource(ctx, canonical, source, run); err != nil {
		return empty, err
	}
	return WorktreeMergeRetiredDescendantSourceProof{WorktreeMergeRetiredSourceProof: WorktreeMergeRetiredSourceProof{
		Source: source, ClaimBaseSHA: claimBase, CleanupReport: cleanup.ReportPath, CleanupSHA256: digest,
		PullRequest: pr.URL, LandingSHA: landing, CurrentTargetSHA: target, Kind: kind,
	}, FinalSHA: r.HeadSHA, ClaimBase: base}, nil
}

func resolveLegacyLandedAcknowledgement(ack WorktreeMergeLandedFailureAcknowledgement, receipt WorktreeMergeReceipt) (WorktreeMergeReceipt, error) {
	if ack.LegacyValidation == nil {
		return receipt, nil
	}
	effective, err := legacyLandedValidationReceipt(receipt)
	if err != nil {
		return WorktreeMergeReceipt{}, err
	}
	identity, err := readLegacyValidationFailureIdentity(legacyValidationFailureIdentityPath(receipt.ReceiptPath), receipt, effective.Candidate)
	if err != nil {
		// A present landed acknowledgement requires its identity sidecar. An absent
		// identity must not masquerade as an absent acknowledgement to scanners.
		return WorktreeMergeReceipt{}, fmt.Errorf("authenticate legacy landed identity: %v", err)
	}
	e := ack.LegacyValidation
	if e.IdentityID != identity.ID || e.ObservedCandidateSHA == "" || ack.CandidateWorktree != identity.Candidate.Worktree || ack.CandidateBranch != identity.Candidate.Branch || ack.ReceiptSHA256 != identity.ReceiptSHA256 || ack.CurrentTargetSHA != identity.CurrentTargetSHA || ack.ClaimBaseSHA != identity.ClaimBaseSHA || ack.Actor != identity.Actor || ack.Reason != identity.Reason || ack.RecordedAt.IsZero() || !ack.RecordedAt.Equal(identity.RecordedAt) || len(e.RetiredSources) != len(receipt.Sources) {
		return WorktreeMergeReceipt{}, errors.New("legacy landed acknowledgement has mismatched correlated evidence")
	}
	for i, source := range receipt.Sources {
		proof := e.RetiredSources[i]
		if proof.Source != source || proof.FinalSHA == "" || proof.ClaimBase == "" || proof.ClaimBaseSHA == "" || proof.CleanupReport == "" || proof.CleanupSHA256 == "" || proof.PullRequest == "" || proof.LandingSHA == "" || proof.CurrentTargetSHA != ack.CurrentTargetSHA || proof.Kind != "ancestor" || proof.TreeSHA != "" {
			return WorktreeMergeReceipt{}, errors.New("legacy landed acknowledgement has incomplete retired source evidence")
		}
	}
	return effective, nil
}
