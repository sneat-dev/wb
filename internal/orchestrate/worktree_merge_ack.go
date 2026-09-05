package orchestrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/worktrees"
)

const (
	worktreeMergeLandedFailureAcknowledgementSchemaVersion    = 1
	worktreeMergeLandedFailureAcknowledgementSuffix           = ".landed-validation-failed.ack.json"
	worktreeMergeValidationFailureSupersessionSchemaVersion   = 1
	worktreeMergeValidationFailureSupersessionSuffix          = ".validation-failed.superseded.ack.json"
	worktreeMergeLegacyValidationFailureIdentitySchemaVersion = 1
	worktreeMergeLegacyValidationFailureIdentitySuffix        = ".legacy-validation-failed.identity.ack.json"
	worktreeMergeSelfSupersessionCorrectionSchemaVersion      = 1
	worktreeMergeSelfSupersessionCorrectionSuffix             = ".validation-failed.self-supersession.corrected.ack.json"
	worktreeMergePreparedRebatchSchemaVersion                 = 1
	worktreeMergePreparedRebatchSuffix                        = ".prepared.rebatched.ack.json"
	worktreeMergeReceiptCollisionAcknowledgementSchemaVersion = 1
	worktreeMergeReceiptCollisionAcknowledgementSuffix        = ".receipt-collision.ack.json"
	worktreeMergeMissingCleanupAcknowledgementSchemaVersion   = 1
	worktreeMergeMissingCleanupAcknowledgementSuffix          = ".missing-cleanup.ack.json"
)

// linkSelfSupersessionCorrection publishes a fully synced temporary file without
// replacing an existing correction. It is replaceable only by tests that prove
// the concurrent create-if-absent path is fail-closed.
var linkSelfSupersessionCorrection = os.Link

// linkLegacyValidationFailureIdentity publishes a derived legacy identity
// without replacing the historical receipt or a prior acknowledgement.
var linkLegacyValidationFailureIdentity = os.Link

// linkMissingCleanupAcknowledgement publishes audited legacy cleanup evidence
// without replacing either the historical receipt or missing Work Logs.
var linkMissingCleanupAcknowledgement = os.Link

// WorktreeMergeMissingCleanupAcknowledgement records the narrow legacy case
// where a landed receipt's exact worktrees and branches were already removed,
// but the historical cleanup did not retain terminal Work Log evidence.
type WorktreeMergeMissingCleanupAcknowledgement struct {
	SchemaVersion       int                                    `json:"schema_version"`
	ID                  string                                 `json:"id"`
	Status              string                                 `json:"status"`
	ReceiptPath         string                                 `json:"receipt_path"`
	AcknowledgementPath string                                 `json:"acknowledgement_path"`
	ReceiptSHA256       string                                 `json:"receipt_sha256"`
	ReceiptID           string                                 `json:"receipt_id"`
	Lane                string                                 `json:"lane"`
	Repository          string                                 `json:"repository"`
	Target              string                                 `json:"target"`
	LandingSHA          string                                 `json:"landing_sha"`
	CurrentTargetSHA    string                                 `json:"current_target_sha"`
	Assets              []worktrees.TerminalWorkLogExpectation `json:"absent_assets"`
	Actor               string                                 `json:"actor"`
	Reason              string                                 `json:"reason"`
	RecordedAt          time.Time                              `json:"recorded_at"`
}

type WorktreeMergeMissingCleanupAcknowledgementOptions struct {
	ProjectsRoot string
	Receipt      string
	Apply        bool
	Actor        string
	Reason       string
}

// WorktreeMergeReceiptCollisionAcknowledgement is the narrowly scoped,
// append-only recovery record for a receipt that was historically rewritten by
// the pre-guard prepare collision. Historical validation_failed is an operator
// assertion here: no byte digest of the pre-mutation receipt exists.
type WorktreeMergeReceiptCollisionAcknowledgement struct {
	SchemaVersion                               int                    `json:"schema_version"`
	ID                                          string                 `json:"id"`
	Status                                      string                 `json:"status"`
	ReceiptPath                                 string                 `json:"receipt_path"`
	AcknowledgementPath                         string                 `json:"acknowledgement_path"`
	ReceiptSHA256                               string                 `json:"receipt_sha256"`
	ImmutableClaimSHA256                        string                 `json:"immutable_claim_sha256"`
	ReceiptID                                   string                 `json:"receipt_id"`
	Lane                                        string                 `json:"lane"`
	Repository                                  string                 `json:"repository"`
	Target                                      string                 `json:"target"`
	ExpectedTargetSHA                           string                 `json:"expected_target_sha"`
	ExpectedCandidateSHA                        string                 `json:"expected_candidate_sha"`
	ExpectedCurrentSourceSHA                    string                 `json:"expected_current_source_sha"`
	ExpectedHistoricalRefreshSourceSHA          string                 `json:"expected_historical_refresh_source_sha"`
	ClaimBaseSHA                                string                 `json:"claim_base_sha"`
	Candidate                                   WorktreeMergeCandidate `json:"candidate"`
	CurrentSources                              []WorktreeMergeSource  `json:"current_sources"`
	HistoricalRefreshSources                    []WorktreeMergeSource  `json:"historical_refresh_sources"`
	HistoricalValidationFailedOperatorAssertion bool                   `json:"historical_validation_failed_operator_assertion"`
	Actor                                       string                 `json:"actor"`
	Reason                                      string                 `json:"reason"`
	RecordedAt                                  time.Time              `json:"recorded_at"`
}

type WorktreeMergeReceiptCollisionAcknowledgementOptions struct {
	ProjectsRoot, Receipt, ExpectedReceiptSHA256, ExpectedImmutableClaimSHA256                            string
	ExpectedTargetSHA, ExpectedCandidateSHA, ExpectedCurrentSourceSHA, ExpectedHistoricalRefreshSourceSHA string
	Apply                                                                                                 bool
	Actor, Reason                                                                                         string
}

// WorktreeMergePreparedRebatch is an append-only link from one unlanded
// prepared receipt to a newly prepared candidate with an additive source set.
// It deliberately retains the original candidate and its exact receipt digest;
// neither historical record is changed to make room for a later source.
type WorktreeMergePreparedRebatch struct {
	SchemaVersion          int                    `json:"schema_version"`
	ID                     string                 `json:"id"`
	Status                 string                 `json:"status"`
	ReceiptPath            string                 `json:"receipt_path"`
	AcknowledgementPath    string                 `json:"acknowledgement_path"`
	ReceiptID              string                 `json:"receipt_id"`
	ReceiptSHA256          string                 `json:"receipt_sha256"`
	ReceiptStatus          WorktreeMergeStatus    `json:"receipt_status"`
	Lane                   string                 `json:"lane"`
	Repository             string                 `json:"repository"`
	Target                 string                 `json:"target"`
	ReceiptTargetSHA       string                 `json:"receipt_target_sha"`
	CurrentTargetSHA       string                 `json:"current_target_sha"`
	OriginalCandidate      WorktreeMergeCandidate `json:"original_candidate"`
	OriginalSources        []WorktreeMergeSource  `json:"original_sources"`
	ReplacementReceiptPath string                 `json:"replacement_receipt_path"`
	Replacement            WorktreeMergeCandidate `json:"replacement"`
	Sources                []WorktreeMergeSource  `json:"sources"`
	RecordedAt             time.Time              `json:"recorded_at"`
}

// WorktreeMergeLandedFailureAcknowledgement is a separate, append-only
// acknowledgement for a historical merge receipt whose candidate is proved to
// be present in the current target but whose validation boundary never became
// terminal. The original merge receipt and Work Log remain untouched.
type WorktreeMergeLandedFailureAcknowledgement struct {
	SchemaVersion       int                   `json:"schema_version"`
	ID                  string                `json:"id"`
	Status              string                `json:"status"`
	ReceiptPath         string                `json:"receipt_path"`
	AcknowledgementPath string                `json:"acknowledgement_path"`
	ReceiptID           string                `json:"receipt_id"`
	ReceiptStatus       WorktreeMergeStatus   `json:"receipt_status"`
	Lane                string                `json:"lane"`
	Repository          string                `json:"repository"`
	Target              string                `json:"target"`
	ReceiptTargetSHA    string                `json:"receipt_target_sha"`
	ReceiptLandingSHA   string                `json:"receipt_landing_sha,omitempty"`
	CurrentTargetSHA    string                `json:"current_target_sha"`
	CandidateSHA        string                `json:"candidate_sha"`
	ClaimBaseSHA        string                `json:"claim_base_sha"`
	CandidateWorktree   string                `json:"candidate_worktree"`
	CandidateBranch     string                `json:"candidate_branch"`
	Sources             []WorktreeMergeSource `json:"sources"`
	Actor               string                `json:"actor"`
	Reason              string                `json:"reason"`
	RecordedAt          time.Time             `json:"recorded_at"`
}

type WorktreeMergeLandedFailureAcknowledgementOptions struct {
	ProjectsRoot string
	Receipt      string
	Apply        bool
	Actor        string
	Reason       string
}

// WorktreeMergeValidationFailureSupersession is a separate, append-only
// transition for a failed prepare candidate that never landed. It binds the
// immutable failed receipt to one clean replacement candidate without changing
// either candidate's Work Log or the historical receipt.
type WorktreeMergeValidationFailureSupersession struct {
	SchemaVersion                  int                    `json:"schema_version"`
	ID                             string                 `json:"id"`
	Status                         string                 `json:"status"`
	ReceiptPath                    string                 `json:"receipt_path"`
	AcknowledgementPath            string                 `json:"acknowledgement_path"`
	ReceiptID                      string                 `json:"receipt_id"`
	ReceiptSHA256                  string                 `json:"receipt_sha256"`
	ReceiptStatus                  WorktreeMergeStatus    `json:"receipt_status"`
	Lane                           string                 `json:"lane"`
	Repository                     string                 `json:"repository"`
	Target                         string                 `json:"target"`
	ReceiptTargetSHA               string                 `json:"receipt_target_sha"`
	CurrentTargetSHA               string                 `json:"current_target_sha"`
	OriginalCandidate              WorktreeMergeCandidate `json:"original_candidate"`
	ObservedCandidateDescendantSHA string                 `json:"observed_candidate_descendant_sha,omitempty"`
	OriginalClaimBaseSHA           string                 `json:"original_claim_base_sha"`
	Replacement                    WorktreeMergeCandidate `json:"replacement"`
	ReplacementClaimBaseSHA        string                 `json:"replacement_claim_base_sha"`
	Sources                        []WorktreeMergeSource  `json:"sources"`
	Actor                          string                 `json:"actor"`
	Reason                         string                 `json:"reason"`
	RecordedAt                     time.Time              `json:"recorded_at"`
}

type WorktreeMergeValidationFailureSupersessionOptions struct {
	ProjectsRoot        string
	Receipt             string
	ReplacementWorktree string
	Apply               bool
	Actor               string
	Reason              string
}

// WorktreeMergeLegacyValidationFailureIdentity records the only identity WB
// may derive for a legacy validation_failed receipt whose writer omitted the
// candidate SHA. The historical receipt remains immutable; every field here is
// corroborated from its registered candidate worktree, active claim, exact
// sources, and current remote-target observation.
type WorktreeMergeLegacyValidationFailureIdentity struct {
	SchemaVersion       int                    `json:"schema_version"`
	ID                  string                 `json:"id"`
	Status              string                 `json:"status"`
	ReceiptPath         string                 `json:"receipt_path"`
	AcknowledgementPath string                 `json:"acknowledgement_path"`
	ReceiptSHA256       string                 `json:"receipt_sha256"`
	ReceiptID           string                 `json:"receipt_id"`
	Lane                string                 `json:"lane"`
	Repository          string                 `json:"repository"`
	Target              string                 `json:"target"`
	ReceiptTargetSHA    string                 `json:"receipt_target_sha"`
	CurrentTargetSHA    string                 `json:"current_target_sha"`
	Candidate           WorktreeMergeCandidate `json:"candidate"`
	ClaimBaseSHA        string                 `json:"claim_base_sha"`
	Sources             []WorktreeMergeSource  `json:"sources"`
	Actor               string                 `json:"actor"`
	Reason              string                 `json:"reason"`
	RecordedAt          time.Time              `json:"recorded_at"`
}

// WorktreeMergeSelfSupersessionCorrection is the only repair for a historical
// supersession acknowledgement that incorrectly named the failed candidate as
// its own replacement. It is append-only and binds both the exact corrupt ack
// bytes and one distinct, fully revalidated replacement candidate.
type WorktreeMergeSelfSupersessionCorrection struct {
	SchemaVersion           int                    `json:"schema_version"`
	ID                      string                 `json:"id"`
	Status                  string                 `json:"status"`
	CorrectionPath          string                 `json:"correction_path"`
	ReceiptPath             string                 `json:"receipt_path"`
	ReceiptSHA256           string                 `json:"receipt_sha256"`
	ImmutableClaimSHA256    string                 `json:"immutable_claim_sha256"`
	SupersessionPath        string                 `json:"supersession_path"`
	SupersessionSHA256      string                 `json:"supersession_sha256"`
	SupersessionID          string                 `json:"supersession_id"`
	OriginalCandidate       WorktreeMergeCandidate `json:"original_candidate"`
	OriginalClaimBaseSHA    string                 `json:"original_claim_base_sha"`
	CorrectedReplacement    WorktreeMergeCandidate `json:"corrected_replacement"`
	ReplacementClaimBaseSHA string                 `json:"replacement_claim_base_sha"`
	CurrentTargetSHA        string                 `json:"current_target_sha"`
	Sources                 []WorktreeMergeSource  `json:"sources"`
	Actor                   string                 `json:"actor"`
	Reason                  string                 `json:"reason"`
	RecordedAt              time.Time              `json:"recorded_at"`
}

type WorktreeMergeSelfSupersessionCorrectionOptions struct {
	ProjectsRoot, Receipt, ReplacementWorktree, ExpectedSupersessionSHA256, ExpectedImmutableClaimSHA256 string
	Apply                                                                                                bool
	Actor, Reason                                                                                        string
}

// AcknowledgeWorktreeMergeReceiptCollision records the one audited recovery
// path for a known historical receipt collision. It never infers the incident:
// the caller must pin every observed digest and revision before --apply can
// write the separate acknowledgement.
func AcknowledgeWorktreeMergeReceiptCollision(ctx context.Context, options WorktreeMergeReceiptCollisionAcknowledgementOptions) (WorktreeMergeReceiptCollisionAcknowledgement, error) {
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
	receipt, err := readWorktreeMergeReceipt(receiptPath)
	if err != nil {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, err
	}
	lock, err := AcquireOperationLock(options.ProjectsRoot, receipt.Lane, true)
	if err != nil {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, err
	}
	defer func() { _ = lock.Release() }()

	// Re-read beneath the lane lock before every proof and before the only write.
	receipt, err = readWorktreeMergeReceipt(receiptPath)
	if err != nil {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, err
	}
	if err := validateReceiptCollisionShape(receipt, receiptPath, options); err != nil {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, err
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receiptPath)
	if err != nil || receiptHash != options.ExpectedReceiptSHA256 {
		if err == nil {
			err = fmt.Errorf("receipt SHA256 %s does not match expected %s", receiptHash, options.ExpectedReceiptSHA256)
		}
		return WorktreeMergeReceiptCollisionAcknowledgement{}, err
	}
	claim, err := validateMergeAcknowledgementCandidate(ctx, options.ProjectsRoot, receipt, receipt.Candidate)
	if err != nil {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, fmt.Errorf("validate collision candidate: %w", err)
	}
	claimBytes, err := os.ReadFile(claim.ClaimPath)
	if err != nil {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, fmt.Errorf("read immutable candidate claim: %w", err)
	}
	claimDigest := sha256.Sum256(claimBytes)
	claimHash := hex.EncodeToString(claimDigest[:])
	if claimHash != options.ExpectedImmutableClaimSHA256 {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, fmt.Errorf("immutable claim SHA256 %s does not match expected %s", claimHash, options.ExpectedImmutableClaimSHA256)
	}
	remote, _, err := runCommand(ctx, 0, 0, receipt.Candidate.Worktree, "git", "ls-remote", "--heads", "origin", "refs/heads/"+receipt.Candidate.Branch)
	if err != nil {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, err
	}
	if strings.TrimSpace(remote) != "" {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, errors.New("collision candidate is published")
	}
	currentTarget, err := fetchExactMergeTarget(ctx, receipt.Candidate.Worktree, receipt.Target)
	if err != nil {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, err
	}
	if currentTarget != options.ExpectedTargetSHA || receipt.TargetSHA != currentTarget {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, fmt.Errorf("exact current target %s or receipt target %s does not match expected %s", currentTarget, receipt.TargetSHA, options.ExpectedTargetSHA)
	}
	for _, root := range []string{claim.BaseSHA, receipt.TargetSHA, receipt.Sources[0].SHA, receipt.SourceRefreshes[0].Sources[0].SHA} {
		contains, ancestorErr := isMergeAncestor(ctx, receipt.Candidate.Worktree, root, receipt.Candidate.SHA)
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
	if err := persistReceiptCollisionAcknowledgement(ackPath, ack); err != nil {
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

func receiptCollisionAcknowledgementPath(receiptPath string) string {
	return receiptPath + worktreeMergeReceiptCollisionAcknowledgementSuffix
}

func receiptCollisionAcknowledgementID(ack WorktreeMergeReceiptCollisionAcknowledgement) string {
	hash := sha256.New()
	for _, value := range []string{ack.ReceiptPath, ack.ReceiptSHA256, ack.ImmutableClaimSHA256, ack.ReceiptID, ack.Lane, ack.ExpectedTargetSHA, ack.ExpectedCandidateSHA, ack.ExpectedCurrentSourceSHA, ack.ExpectedHistoricalRefreshSourceSHA, ack.ClaimBaseSHA, ack.Actor, ack.Reason} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func sameReceiptCollisionAcknowledgement(left, right WorktreeMergeReceiptCollisionAcknowledgement) bool {
	return left.ID == right.ID && left.Status == right.Status && left.ReceiptPath == right.ReceiptPath &&
		left.AcknowledgementPath == right.AcknowledgementPath && left.ReceiptSHA256 == right.ReceiptSHA256 &&
		left.ImmutableClaimSHA256 == right.ImmutableClaimSHA256 && left.ReceiptID == right.ReceiptID &&
		left.Lane == right.Lane && left.Repository == right.Repository && left.Target == right.Target &&
		left.ExpectedTargetSHA == right.ExpectedTargetSHA && left.ExpectedCandidateSHA == right.ExpectedCandidateSHA &&
		left.ExpectedCurrentSourceSHA == right.ExpectedCurrentSourceSHA && left.ExpectedHistoricalRefreshSourceSHA == right.ExpectedHistoricalRefreshSourceSHA &&
		left.ClaimBaseSHA == right.ClaimBaseSHA && left.Candidate == right.Candidate &&
		sameWorktreeMergeSources(left.CurrentSources, right.CurrentSources) &&
		sameWorktreeMergeSources(left.HistoricalRefreshSources, right.HistoricalRefreshSources) &&
		left.HistoricalValidationFailedOperatorAssertion == right.HistoricalValidationFailedOperatorAssertion &&
		left.Actor == right.Actor && left.Reason == right.Reason
}

var linkReceiptCollisionAcknowledgement = os.Link

func persistReceiptCollisionAcknowledgement(path string, ack WorktreeMergeReceiptCollisionAcknowledgement) error {
	contents, err := json.MarshalIndent(ack, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".receipt-collision-ack-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return linkReceiptCollisionAcknowledgement(temporaryPath, path)
}

func readReceiptCollisionAcknowledgement(path string, receipt WorktreeMergeReceipt) (WorktreeMergeReceiptCollisionAcknowledgement, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, err
	}
	var ack WorktreeMergeReceiptCollisionAcknowledgement
	if err := json.Unmarshal(contents, &ack); err != nil {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, fmt.Errorf("decode receipt-collision acknowledgement %s: %w", path, err)
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, err
	}
	if ack.SchemaVersion != worktreeMergeReceiptCollisionAcknowledgementSchemaVersion || ack.Status != "receipt_collision_acknowledged" ||
		ack.AcknowledgementPath != path || ack.ReceiptPath != receipt.ReceiptPath || ack.ReceiptSHA256 != receiptHash || ack.ReceiptID != receipt.ID || ack.Lane != receipt.Lane ||
		ack.Repository != receipt.Repository || ack.Target != receipt.Target || ack.Candidate != receipt.Candidate || !sameWorktreeMergeSources(ack.CurrentSources, receipt.Sources) ||
		receipt.Phase != WorktreeMergePhasePrepare || receipt.Status != WorktreeMergePreparing || receipt.LandingSHA != "" || receipt.PullRequest != "" || receipt.PublishedCandidateSHA != "" ||
		len(receipt.Sources) != 1 || len(receipt.SourceRefreshes) != 1 || len(receipt.SourceRefreshes[0].Sources) != 1 || !sameWorktreeMergeSources(ack.HistoricalRefreshSources, receipt.SourceRefreshes[0].Sources) ||
		ack.ExpectedTargetSHA != receipt.TargetSHA || ack.ExpectedCandidateSHA != receipt.Candidate.SHA || ack.ExpectedCurrentSourceSHA != receipt.Sources[0].SHA || ack.ExpectedHistoricalRefreshSourceSHA != receipt.SourceRefreshes[0].Sources[0].SHA ||
		!ack.HistoricalValidationFailedOperatorAssertion || ack.ImmutableClaimSHA256 == "" || ack.ClaimBaseSHA == "" || ack.Actor == "" || ack.Reason == "" || ack.RecordedAt.IsZero() || ack.ID != receiptCollisionAcknowledgementID(ack) {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, fmt.Errorf("receipt-collision acknowledgement %s has invalid immutable identity", path)
	}
	return ack, nil
}

func hasReceiptCollisionAcknowledgement(receipt WorktreeMergeReceipt) (bool, error) {
	_, err := readReceiptCollisionAcknowledgement(receiptCollisionAcknowledgementPath(receipt.ReceiptPath), receipt)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// validateReceiptCollisionAcknowledgement replays the static acknowledgement
// identity and its live immutable-claim digest before rebatch or cleanup can
// rely on the historically corrupted preparing receipt.
func validateReceiptCollisionAcknowledgement(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt) (WorktreeMergeReceiptCollisionAcknowledgement, error) {
	ack, err := readReceiptCollisionAcknowledgement(receiptCollisionAcknowledgementPath(receipt.ReceiptPath), receipt)
	if err != nil {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, err
	}
	claim, err := validateMergeAcknowledgementCandidate(ctx, projectsRoot, receipt, receipt.Candidate)
	if err != nil {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, fmt.Errorf("validate collision acknowledgement candidate: %w", err)
	}
	claimBytes, err := os.ReadFile(claim.ClaimPath)
	if err != nil {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, fmt.Errorf("read collision acknowledgement immutable claim: %w", err)
	}
	digest := sha256.Sum256(claimBytes)
	claimHash := hex.EncodeToString(digest[:])
	if claimHash != ack.ImmutableClaimSHA256 || claim.BaseSHA != ack.ClaimBaseSHA {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, errors.New("collision acknowledgement immutable claim SHA256 or base no longer matches recorded evidence")
	}
	return ack, nil
}

// validatePreparedWorktreeMergeRebatch proves that the old prepared lane is
// untouched and that the requested source list is a strict additive rebatch:
// every old branch remains and may only advance by ancestry; new branches are
// distinct. The remote target must not have moved because a rebatch is a
// source-set transition, not an implicit target rebase.
func validatePreparedWorktreeMergeRebatch(ctx context.Context, projectsRoot, receiptInput, repository, target string, sources []WorktreeMergeSource) (*WorktreeMergePreparedRebatch, error) {
	receiptPath, err := resolveWorktreeMergeReceiptPath(projectsRoot, receiptInput)
	if err != nil {
		return nil, err
	}
	receipt, err := readWorktreeMergeReceipt(receiptPath)
	if err != nil {
		return nil, err
	}
	collisionAcknowledged := false
	if receipt.Status == WorktreeMergePreparing {
		if _, err := validateReceiptCollisionAcknowledgement(ctx, projectsRoot, receipt); err != nil {
			return nil, err
		}
		collisionAcknowledged = true
	}
	preparedOrAcknowledgedCollision := receipt.Status == WorktreeMergePrepared ||
		(collisionAcknowledged && receipt.Phase == WorktreeMergePhasePrepare && receipt.Status == WorktreeMergePreparing)
	if receipt.Phase != WorktreeMergePhasePrepare || !preparedOrAcknowledgedCollision || receipt.LandingSHA != "" ||
		receipt.PullRequest != "" || receipt.PublishedCandidateSHA != "" || receipt.Repository != repository || receipt.Target != target ||
		receipt.TargetSHA == "" || receipt.Candidate.Task == "" || receipt.Candidate.Worktree == "" || receipt.Candidate.Branch == "" || receipt.Candidate.SHA == "" || len(receipt.Sources) == 0 {
		return nil, fmt.Errorf("rebatch receipt %s is not an unlanded prepared candidate with complete immutable identity", receiptPath)
	}
	if receipt.Lane != worktreeMergeLaneID(repository, target) {
		return nil, fmt.Errorf("rebatch receipt %s has inconsistent lane identity", receiptPath)
	}
	if _, err := validateMergeAcknowledgementCandidate(ctx, projectsRoot, receipt, receipt.Candidate); err != nil {
		return nil, fmt.Errorf("validate prepared rebatch candidate: %w", err)
	}
	currentTarget, err := fetchExactMergeTarget(ctx, receipt.Candidate.Worktree, target)
	if err != nil {
		return nil, err
	}
	if currentTarget != receipt.TargetSHA {
		return nil, fmt.Errorf("rebatch refuses target drift from %s to %s", receipt.TargetSHA, currentTarget)
	}
	byBranch := make(map[string]WorktreeMergeSource, len(sources))
	for _, source := range sources {
		if source.Branch == "" || source.SHA == "" {
			return nil, errors.New("rebatch source has incomplete ref identity")
		}
		if _, exists := byBranch[source.Branch]; exists {
			return nil, fmt.Errorf("rebatch source ref %s was supplied more than once", source.Branch)
		}
		byBranch[source.Branch] = source
	}
	if len(sources) <= len(receipt.Sources) {
		return nil, fmt.Errorf("rebatch source set must add at least one distinct source ref")
	}
	for _, oldSource := range receipt.Sources {
		newSource, ok := byBranch[oldSource.Branch]
		if !ok {
			return nil, fmt.Errorf("rebatch removes immutable source ref %s", oldSource.Branch)
		}
		containsOld, err := isMergeAncestor(ctx, newSource.Worktree, oldSource.SHA, newSource.SHA)
		if err != nil {
			return nil, fmt.Errorf("verify rebatch source %s ancestry: %w", oldSource.Branch, err)
		}
		if !containsOld {
			return nil, fmt.Errorf("rebatch source ref %s is not a descendant of receipted head %s", oldSource.Branch, oldSource.SHA)
		}
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receiptPath)
	if err != nil {
		return nil, err
	}
	return &WorktreeMergePreparedRebatch{
		SchemaVersion: worktreeMergePreparedRebatchSchemaVersion, Status: "prepared_rebatched",
		ReceiptPath: receiptPath, AcknowledgementPath: rebatchPath(receiptPath), ReceiptID: receipt.ID, ReceiptSHA256: receiptHash,
		ReceiptStatus: receipt.Status, Lane: receipt.Lane, Repository: repository, Target: target, ReceiptTargetSHA: receipt.TargetSHA,
		CurrentTargetSHA: currentTarget, OriginalCandidate: receipt.Candidate,
		OriginalSources: append([]WorktreeMergeSource(nil), receipt.Sources...),
	}, nil
}

func completePreparedWorktreeMergeRebatch(rebatch WorktreeMergePreparedRebatch, replacement WorktreeMergeReceipt) WorktreeMergePreparedRebatch {
	rebatch.ReplacementReceiptPath = replacement.ReceiptPath
	rebatch.Replacement = replacement.Candidate
	rebatch.Sources = append([]WorktreeMergeSource(nil), replacement.Sources...)
	rebatch.RecordedAt = time.Now().UTC()
	rebatch.ID = preparedRebatchID(rebatch)
	return rebatch
}

func preparedRebatchID(rebatch WorktreeMergePreparedRebatch) string {
	hash := sha256.New()
	for _, value := range []string{rebatch.ReceiptID, rebatch.ReceiptPath, rebatch.ReceiptSHA256, string(rebatch.ReceiptStatus), rebatch.ReceiptTargetSHA, rebatch.CurrentTargetSHA, rebatch.OriginalCandidate.Task, rebatch.OriginalCandidate.Worktree, rebatch.OriginalCandidate.Branch, rebatch.OriginalCandidate.SHA, rebatch.ReplacementReceiptPath, rebatch.Replacement.Task, rebatch.Replacement.Worktree, rebatch.Replacement.Branch, rebatch.Replacement.SHA} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	for _, group := range [][]WorktreeMergeSource{rebatch.OriginalSources, rebatch.Sources} {
		for _, source := range group {
			for _, value := range []string{source.Task, source.Worktree, source.Branch, source.SHA} {
				_, _ = hash.Write([]byte(value))
				_, _ = hash.Write([]byte{0})
			}
		}
		_, _ = hash.Write([]byte{0xff})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func rebatchPath(receiptPath string) string { return receiptPath + worktreeMergePreparedRebatchSuffix }

func persistPreparedWorktreeMergeRebatch(path string, rebatch WorktreeMergePreparedRebatch) error {
	contents, err := json.MarshalIndent(rebatch, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".prepared-rebatch-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

// persistPreparedWorktreeMergeRebatchForPrepare is a narrow test seam for the
// durable receipt -> acknowledgement boundary. Production always persists the
// append-only acknowledgement with the atomic writer above.
var persistPreparedWorktreeMergeRebatchForPrepare = persistPreparedWorktreeMergeRebatch

func ensurePreparedWorktreeMergeRebatch(rebatch *WorktreeMergePreparedRebatch, replacement WorktreeMergeReceipt) error {
	if rebatch == nil {
		return errors.New("prepared rebatch evidence is required")
	}
	path := rebatchPath(rebatch.ReceiptPath)
	complete := completePreparedWorktreeMergeRebatch(*rebatch, replacement)
	original, err := readWorktreeMergeReceipt(rebatch.ReceiptPath)
	if err != nil {
		return err
	}
	if existing, err := readPreparedWorktreeMergeRebatch(path, original); err == nil {
		if existing.ReplacementReceiptPath != replacement.ReceiptPath || existing.Replacement != replacement.Candidate || !sameWorktreeMergeSources(existing.Sources, replacement.Sources) {
			return fmt.Errorf("prepared rebatch acknowledgement %s binds different replacement evidence", path)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return persistPreparedWorktreeMergeRebatchForPrepare(path, complete)
}

func readPreparedWorktreeMergeRebatch(path string, receipt WorktreeMergeReceipt) (WorktreeMergePreparedRebatch, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return WorktreeMergePreparedRebatch{}, err
	}
	var rebatch WorktreeMergePreparedRebatch
	if err := json.Unmarshal(contents, &rebatch); err != nil {
		return WorktreeMergePreparedRebatch{}, fmt.Errorf("decode prepared rebatch %s: %w", path, err)
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		return WorktreeMergePreparedRebatch{}, err
	}
	replacement, err := readWorktreeMergeReceipt(rebatch.ReplacementReceiptPath)
	if err != nil {
		return WorktreeMergePreparedRebatch{}, err
	}
	collisionAcknowledged, collisionErr := hasReceiptCollisionAcknowledgement(receipt)
	if collisionErr != nil {
		return WorktreeMergePreparedRebatch{}, collisionErr
	}
	preparedOrAcknowledgedCollision := receipt.Status == WorktreeMergePrepared ||
		(collisionAcknowledged && receipt.Phase == WorktreeMergePhasePrepare && receipt.Status == WorktreeMergePreparing)
	if rebatch.SchemaVersion != worktreeMergePreparedRebatchSchemaVersion || rebatch.Status != "prepared_rebatched" ||
		rebatch.AcknowledgementPath != path || rebatch.ReceiptPath != receipt.ReceiptPath || rebatch.ReceiptID != receipt.ID ||
		rebatch.ReceiptSHA256 != receiptHash || !preparedOrAcknowledgedCollision || rebatch.ReceiptStatus != receipt.Status || rebatch.Lane != receipt.Lane ||
		rebatch.Repository != receipt.Repository || rebatch.Target != receipt.Target || rebatch.ReceiptTargetSHA != receipt.TargetSHA ||
		rebatch.CurrentTargetSHA != receipt.TargetSHA || rebatch.OriginalCandidate != receipt.Candidate ||
		!sameWorktreeMergeSources(rebatch.OriginalSources, receipt.Sources) || rebatch.ReplacementReceiptPath == receipt.ReceiptPath ||
		replacement.RebatchOf != receipt.ReceiptPath || replacement.Repository != receipt.Repository || replacement.Target != receipt.Target ||
		replacement.TargetSHA != receipt.TargetSHA || replacement.Candidate != rebatch.Replacement || len(replacement.RebatchedCandidates) != 1 || replacement.RebatchedCandidates[0] != receipt.Candidate || !sameWorktreeMergeSources(replacement.Sources, rebatch.Sources) ||
		len(rebatch.Sources) <= len(rebatch.OriginalSources) || rebatch.RecordedAt.IsZero() || rebatch.ID != preparedRebatchID(rebatch) {
		return WorktreeMergePreparedRebatch{}, fmt.Errorf("prepared rebatch %s has invalid immutable identity", path)
	}
	return rebatch, nil
}

func hasPreparedWorktreeMergeRebatch(receipt WorktreeMergeReceipt) (bool, error) {
	_, err := readPreparedWorktreeMergeRebatch(rebatchPath(receipt.ReceiptPath), receipt)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// AcknowledgeLandedMergeFailure proves that a non-terminal failed receipt is
// already represented by the current remote target, then writes a
// distinct audited acknowledgement. It never rewrites the historical receipt
// or any Work Log record. A failed proof is always a refusal.
func AcknowledgeLandedMergeFailure(ctx context.Context, options WorktreeMergeLandedFailureAcknowledgementOptions) (WorktreeMergeLandedFailureAcknowledgement, error) {
	receiptPath, err := resolveWorktreeMergeReceiptPath(options.ProjectsRoot, options.Receipt)
	if err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	receipt, err := readWorktreeMergeReceipt(receiptPath)
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
	if lockID == "" {
		lockID = worktreeMergeLaneID(receipt.Repository, receipt.Target)
	}
	lock, err := AcquireOperationLock(options.ProjectsRoot, lockID, true)
	if err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	defer func() { _ = lock.Release() }()

	view, err := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: options.ProjectsRoot, Worktree: receipt.Candidate.Worktree})
	if err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, fmt.Errorf("load candidate Work Log: %w", err)
	}
	if view.Claim == nil || view.Claim.Lifecycle != "active" || view.Claim.Repository != receipt.Repository ||
		view.Claim.Task != receipt.Candidate.Task || filepath.Clean(view.Claim.Worktree) != filepath.Clean(receipt.Candidate.Worktree) || view.Claim.Branch != receipt.Candidate.Branch ||
		view.Claim.Base != receipt.Target || view.Claim.BaseSHA == "" {
		return WorktreeMergeLandedFailureAcknowledgement{}, errors.New("candidate has no active Work Log claim matching the immutable receipt target and identity")
	}
	if err := requireCleanMergeWorktree(ctx, receipt.Candidate.Worktree); err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, fmt.Errorf("candidate is not clean: %w", err)
	}
	head, err := mergeRevision(ctx, receipt.Candidate.Worktree, "HEAD")
	if err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, fmt.Errorf("read candidate HEAD: %w", err)
	}
	if head != receipt.Candidate.SHA {
		return WorktreeMergeLandedFailureAcknowledgement{}, fmt.Errorf("candidate HEAD %s does not match receipted candidate %s", head, receipt.Candidate.SHA)
	}
	if err := requireCandidateContainsImmutableClaimBase(ctx, receipt.Candidate.Worktree, view.Claim.BaseSHA, head); err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	if contains, err := isMergeAncestor(ctx, receipt.Candidate.Worktree, receipt.TargetSHA, head); err != nil || !contains {
		if err == nil {
			err = fmt.Errorf("candidate %s does not contain immutable receipt target %s", head, receipt.TargetSHA)
		}
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	for _, source := range receipt.Sources {
		if err := validateLandedFailureAcknowledgementSource(ctx, options.ProjectsRoot, receipt, source); err != nil {
			return WorktreeMergeLandedFailureAcknowledgement{}, err
		}
		contains, sourceErr := isMergeAncestor(ctx, receipt.Candidate.Worktree, source.SHA, head)
		if sourceErr != nil || !contains {
			if sourceErr == nil {
				sourceErr = fmt.Errorf("candidate %s does not contain receipted source %s", head, source.SHA)
			}
			return WorktreeMergeLandedFailureAcknowledgement{}, sourceErr
		}
	}
	currentTarget, err := fetchExactMergeTarget(ctx, receipt.Candidate.Worktree, receipt.Target)
	if err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	if contains, ancestorErr := isMergeAncestor(ctx, receipt.Candidate.Worktree, head, currentTarget); ancestorErr != nil || !contains {
		if ancestorErr == nil {
			ancestorErr = fmt.Errorf("current remote target %s does not contain receipted candidate %s", currentTarget, head)
		}
		return WorktreeMergeLandedFailureAcknowledgement{}, ancestorErr
	}
	if receipt.Status == WorktreeMergePostTargetCIFailed {
		if contains, ancestorErr := isMergeAncestor(ctx, receipt.Candidate.Worktree, receipt.LandingSHA, currentTarget); ancestorErr != nil || !contains {
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
	ackPath := landedFailureAcknowledgementPath(receiptPath)
	if existing, readErr := readLandedFailureAcknowledgement(ackPath, receipt); readErr == nil {
		if existing.CurrentTargetSHA != currentTarget || existing.CandidateSHA != head {
			return WorktreeMergeLandedFailureAcknowledgement{}, fmt.Errorf("acknowledgement %s binds different target or candidate evidence", ackPath)
		}
		return existing, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return WorktreeMergeLandedFailureAcknowledgement{}, readErr
	}
	ackPath = landedFailureAcknowledgementPath(receiptPath)
	ack.ReceiptPath = receiptPath
	if !options.Apply {
		return ack, nil
	}
	if err := persistLandedFailureAcknowledgement(ackPath, ack); err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	return ack, nil
}

// SupersedeValidationFailedWorktreeMerge proves that a clean replacement
// candidate contains every immutable root of an unlanded prepare failure. The
// failed candidate itself need not be an ancestor: it may have diverged after
// validation failed. This transition is deliberately narrower than the landed
// acknowledgement because it never asserts that the failed candidate landed.
func SupersedeValidationFailedWorktreeMerge(ctx context.Context, options WorktreeMergeValidationFailureSupersessionOptions) (WorktreeMergeValidationFailureSupersession, error) {
	receiptPath, err := resolveWorktreeMergeReceiptPath(options.ProjectsRoot, options.Receipt)
	if err != nil {
		return WorktreeMergeValidationFailureSupersession{}, err
	}
	receipt, err := readWorktreeMergeReceipt(receiptPath)
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
	receipt, err = readWorktreeMergeReceipt(receiptPath)
	if err != nil {
		return WorktreeMergeValidationFailureSupersession{}, err
	}
	receipt, legacyIdentity, legacyIdentityNeedsPersist, err := resolveValidationFailedSupersessionReceipt(ctx, options.ProjectsRoot, receipt, receiptPath, options.Actor, options.Reason)
	if err != nil {
		return WorktreeMergeValidationFailureSupersession{}, err
	}

	originalClaim, observedCandidateDescendantSHA, err := validatePrepareFailureSupersessionCandidate(ctx, options.ProjectsRoot, receipt)
	if err != nil {
		return WorktreeMergeValidationFailureSupersession{}, fmt.Errorf("validate failed candidate: %w", err)
	}
	replacement, replacementClaim, err := validateValidationFailureReplacement(ctx, options.ProjectsRoot, receipt, options.ReplacementWorktree)
	if err != nil {
		return WorktreeMergeValidationFailureSupersession{}, err
	}
	if replacement == receipt.Candidate || replacement.SHA == receipt.Candidate.SHA {
		return WorktreeMergeValidationFailureSupersession{}, errors.New("replacement candidate must be distinct from the failed receipt candidate")
	}
	for _, source := range receipt.Sources {
		if err := validateLandedFailureAcknowledgementSource(ctx, options.ProjectsRoot, receipt, source); err != nil {
			return WorktreeMergeValidationFailureSupersession{}, err
		}
	}
	currentTarget, err := fetchExactMergeTarget(ctx, replacement.Worktree, receipt.Target)
	if err != nil {
		return WorktreeMergeValidationFailureSupersession{}, err
	}
	requiredRoots := []string{originalClaim.BaseSHA, receipt.TargetSHA, currentTarget, replacementClaim.BaseSHA}
	if observedCandidateDescendantSHA != "" {
		requiredRoots = append(requiredRoots, receipt.Candidate.SHA, observedCandidateDescendantSHA)
	}
	for _, root := range append(requiredRoots, sourceSHAs(receipt.Sources)...) {
		contains, ancestorErr := isMergeAncestor(ctx, replacement.Worktree, root, replacement.SHA)
		if ancestorErr != nil || !contains {
			if ancestorErr == nil {
				ancestorErr = fmt.Errorf("replacement %s does not contain required immutable root %s", replacement.SHA, root)
			}
			return WorktreeMergeValidationFailureSupersession{}, ancestorErr
		}
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receiptPath)
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
	if err := persistValidationFailureSupersession(ackPath, ack); err != nil {
		return WorktreeMergeValidationFailureSupersession{}, err
	}
	return ack, nil
}

// CorrectValidationFailedSelfSupersession repairs only a pre-guard
// self-supersession. It never replaces that acknowledgement: it records one
// separate correction whose identity pins the exact existing acknowledgement,
// receipt, immutable claim, and distinct replacement evidence.
func CorrectValidationFailedSelfSupersession(ctx context.Context, options WorktreeMergeSelfSupersessionCorrectionOptions) (WorktreeMergeSelfSupersessionCorrection, error) {
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
	receipt, err := readWorktreeMergeReceipt(receiptPath)
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
	receipt, err = readWorktreeMergeReceipt(receiptPath)
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
	supersessionHash, err := worktreeMergeReceiptSHA256(ackPath)
	if err != nil || supersessionHash != options.ExpectedSupersessionSHA256 {
		if err == nil {
			err = fmt.Errorf("supersession SHA256 %s does not match expected %s", supersessionHash, options.ExpectedSupersessionSHA256)
		}
		return WorktreeMergeSelfSupersessionCorrection{}, err
	}
	originalClaim, err := validateMergeAcknowledgementCandidate(ctx, options.ProjectsRoot, receipt, receipt.Candidate)
	if err != nil {
		return WorktreeMergeSelfSupersessionCorrection{}, fmt.Errorf("validate failed candidate: %w", err)
	}
	claimBytes, err := os.ReadFile(originalClaim.ClaimPath)
	if err != nil {
		return WorktreeMergeSelfSupersessionCorrection{}, fmt.Errorf("read immutable failed candidate claim: %w", err)
	}
	claimDigest := sha256.Sum256(claimBytes)
	claimHash := hex.EncodeToString(claimDigest[:])
	if claimHash != options.ExpectedImmutableClaimSHA256 || originalClaim.BaseSHA != supersession.OriginalClaimBaseSHA {
		return WorktreeMergeSelfSupersessionCorrection{}, errors.New("immutable failed candidate claim bytes or base no longer match expected supersession evidence")
	}
	replacement, replacementClaim, err := validateValidationFailureReplacement(ctx, options.ProjectsRoot, receipt, options.ReplacementWorktree)
	if err != nil {
		return WorktreeMergeSelfSupersessionCorrection{}, err
	}
	if replacement == receipt.Candidate || replacement.SHA == receipt.Candidate.SHA {
		return WorktreeMergeSelfSupersessionCorrection{}, errors.New("corrected replacement candidate must be distinct from the failed receipt candidate")
	}
	if err := requireImmutableHistoricalWorktreeMergeSources(ctx, replacement.Worktree, receipt); err != nil {
		return WorktreeMergeSelfSupersessionCorrection{}, fmt.Errorf("validate immutable historical source evidence: %w", err)
	}
	currentTarget, err := fetchExactMergeTarget(ctx, replacement.Worktree, receipt.Target)
	if err != nil {
		return WorktreeMergeSelfSupersessionCorrection{}, err
	}
	for _, root := range append([]string{originalClaim.BaseSHA, receipt.TargetSHA, supersession.CurrentTargetSHA, currentTarget, replacementClaim.BaseSHA}, sourceSHAs(immutableHistoricalWorktreeMergeSources(receipt))...) {
		contains, ancestorErr := isMergeAncestor(ctx, replacement.Worktree, root, replacement.SHA)
		if ancestorErr != nil || !contains {
			if ancestorErr == nil {
				ancestorErr = fmt.Errorf("corrected replacement %s does not contain required immutable root %s", replacement.SHA, root)
			}
			return WorktreeMergeSelfSupersessionCorrection{}, ancestorErr
		}
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receiptPath)
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
	if err := persistSelfSupersessionCorrection(correctionPath, correction); err != nil {
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

func requireCandidateContainsImmutableClaimBase(ctx context.Context, candidateWorktree, claimBaseSHA, candidateSHA string) error {
	contains, err := isMergeAncestor(ctx, candidateWorktree, claimBaseSHA, candidateSHA)
	if err != nil {
		return err
	}
	if !contains {
		return fmt.Errorf("candidate %s does not contain immutable claim base %s", candidateSHA, claimBaseSHA)
	}
	return nil
}

func validateMergeAcknowledgementCandidate(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt, candidate WorktreeMergeCandidate) (*worktrees.WorkLogClaimView, error) {
	guard, err := worktrees.Guard(ctx, candidate.Worktree, worktrees.GuardOptions{ProjectsRoot: projectsRoot, Base: receipt.Target})
	if err != nil {
		return nil, fmt.Errorf("guard candidate %s: %w", candidate.Worktree, err)
	}
	if guard.Kind != "linked" || guard.Transient || guard.Branch != candidate.Branch || filepath.Clean(guard.Path) != filepath.Clean(candidate.Worktree) {
		return nil, fmt.Errorf("candidate %s no longer has its exact linked-worktree identity", candidate.Worktree)
	}
	if err := requireCleanMergeWorktree(ctx, candidate.Worktree); err != nil {
		return nil, fmt.Errorf("candidate is not clean: %w", err)
	}
	head, err := mergeRevision(ctx, candidate.Worktree, "HEAD")
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
	if view.Claim == nil || view.Claim.Lifecycle != "active" || view.Claim.Repository != receipt.Repository || view.Claim.Task != candidate.Task ||
		filepath.Clean(view.Claim.Worktree) != filepath.Clean(candidate.Worktree) || view.Claim.Branch != candidate.Branch || view.Claim.Base != receipt.Target || view.Claim.BaseSHA == "" {
		return nil, errors.New("candidate has no active Work Log claim matching the immutable receipt target and identity")
	}
	return view.Claim, nil
}

func validatePrepareFailureSupersessionCandidate(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt) (*worktrees.WorkLogClaimView, string, error) {
	if receipt.Status != WorktreeMergeConflict {
		claim, err := validateMergeAcknowledgementCandidate(ctx, projectsRoot, receipt, receipt.Candidate)
		return claim, "", err
	}
	observedHead, err := mergeRevision(ctx, receipt.Candidate.Worktree, "HEAD")
	if err != nil {
		return nil, "", fmt.Errorf("read candidate HEAD: %w", err)
	}
	observedCandidate := receipt.Candidate
	observedCandidate.SHA = observedHead
	claim, err := validateMergeAcknowledgementCandidate(ctx, projectsRoot, receipt, observedCandidate)
	if err != nil {
		return nil, "", err
	}
	if observedHead == receipt.Candidate.SHA {
		return claim, "", nil
	}
	contains, err := isMergeAncestor(ctx, receipt.Candidate.Worktree, receipt.Candidate.SHA, observedHead)
	if err != nil {
		return nil, "", fmt.Errorf("verify candidate descendant ancestry: %w", err)
	}
	if !contains {
		return nil, "", fmt.Errorf("candidate HEAD %s is not a descendant of receipted candidate %s", observedHead, receipt.Candidate.SHA)
	}
	return claim, observedHead, nil
}

func validateValidationFailureReplacement(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt, replacementPath string) (WorktreeMergeCandidate, *worktrees.WorkLogClaimView, error) {
	guard, err := worktrees.Guard(ctx, replacementPath, worktrees.GuardOptions{ProjectsRoot: projectsRoot, Base: receipt.Target})
	if err != nil {
		return WorktreeMergeCandidate{}, nil, fmt.Errorf("guard replacement worktree %s: %w", replacementPath, err)
	}
	if guard.Kind != "linked" || guard.Transient || guard.Branch == receipt.Target {
		return WorktreeMergeCandidate{}, nil, fmt.Errorf("replacement worktree %s has no exact non-target linked-worktree identity", replacementPath)
	}
	if err := requireCleanMergeWorktree(ctx, guard.Path); err != nil {
		return WorktreeMergeCandidate{}, nil, fmt.Errorf("replacement is not clean: %w", err)
	}
	head, err := mergeRevision(ctx, guard.Path, "HEAD")
	if err != nil {
		return WorktreeMergeCandidate{}, nil, fmt.Errorf("read replacement HEAD: %w", err)
	}
	view, err := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: projectsRoot, Worktree: guard.Path})
	if err != nil {
		return WorktreeMergeCandidate{}, nil, fmt.Errorf("load replacement Work Log: %w", err)
	}
	if view.Claim == nil || view.Claim.Lifecycle != "active" || view.Claim.Repository != receipt.Repository || view.Claim.Task == "" ||
		filepath.Clean(view.Claim.Worktree) != filepath.Clean(guard.Path) || view.Claim.Branch != guard.Branch || view.Claim.Base != receipt.Target || view.Claim.BaseSHA == "" {
		return WorktreeMergeCandidate{}, nil, errors.New("replacement has no authoritative active Work Log claim matching its identity")
	}
	return WorktreeMergeCandidate{Task: view.Claim.Task, Worktree: guard.Path, Branch: guard.Branch, SHA: head}, view.Claim, nil
}

func sourceSHAs(sources []WorktreeMergeSource) []string {
	values := make([]string, 0, len(sources))
	for _, source := range sources {
		values = append(values, source.SHA)
	}
	return values
}

func validateLandedFailureAcknowledgementReceipt(receipt WorktreeMergeReceipt, receiptPath string) error {
	if receipt.ReceiptPath != receiptPath || receipt.ID == "" || receipt.Lane == "" || receipt.Lane != worktreeMergeLaneID(receipt.Repository, receipt.Target) {
		return fmt.Errorf("receipt %s has inconsistent immutable receipt identity", receiptPath)
	}
	switch receipt.Status {
	case WorktreeMergeValidationFailed:
		if receipt.Phase != WorktreeMergePhasePrepare || receipt.LandingSHA != "" {
			return fmt.Errorf("receipt %s is %s with invalid prepare failure state", receiptPath, receipt.Status)
		}
	case WorktreeMergePostTargetCIFailed:
		if receipt.Phase != WorktreeMergePhaseLand || receipt.LandingSHA == "" || receipt.Checks.Status != PullRequestWaitFailed || receipt.Checks.Head != receipt.LandingSHA {
			return fmt.Errorf("receipt %s is %s without an exact failed post-target CI receipt", receiptPath, receipt.Status)
		}
	default:
		return fmt.Errorf("receipt %s is %s, want prepare validation_failed or landed_post_target_ci_failed", receiptPath, receipt.Status)
	}
	return nil
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
		receipt.ID != worktreeMergeOperationID(receipt.Lane, receipt.Sources) || receipt.Candidate.Task != receipt.ID || receipt.CreatedAt.IsZero() || receipt.UpdatedAt.IsZero() {
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
	if receipt.ReceiptPath != receiptPath || receipt.Lane == "" || receipt.Lane != worktreeMergeLaneID(receipt.Repository, receipt.Target) ||
		receipt.SchemaVersion != WorktreeMergeSchemaVersion || receipt.Phase != WorktreeMergePhasePrepare || receipt.Status != WorktreeMergeConflict ||
		receipt.LandingSHA != "" || receipt.PullRequest != "" || receipt.PublishedCandidateSHA != "" || receipt.Repository == "" || receipt.Target == "" ||
		receipt.TargetSHA == "" || len(receipt.Sources) == 0 || receipt.Candidate.Task == "" || receipt.Candidate.Worktree == "" ||
		receipt.Candidate.Branch == "" || receipt.Candidate.SHA == "" || receipt.ID != worktreeMergeOperationID(receipt.Lane, receipt.Sources) ||
		receipt.Candidate.Task != receipt.ID || receipt.CreatedAt.IsZero() || receipt.UpdatedAt.IsZero() {
		return fmt.Errorf("receipt %s lacks a complete exact identity; want prepare validation_failed or unpublished conflict", receiptPath)
	}
	for _, source := range receipt.Sources {
		if source.Task == "" || source.Worktree == "" || source.Branch == "" || source.SHA == "" {
			return fmt.Errorf("receipt %s has an incomplete immutable source identity", receiptPath)
		}
	}
	return nil
}

// resolveValidationFailedSupersessionReceipt converts one narrowly defined
// legacy receipt into an in-memory effective receipt. The source JSON is never
// changed: an apply records the derived identity in its own sidecar only after
// all replacement proof has passed.
func resolveValidationFailedSupersessionReceipt(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt, receiptPath, actor, reason string) (WorktreeMergeReceipt, *WorktreeMergeLegacyValidationFailureIdentity, bool, error) {
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
	head, err := mergeRevision(ctx, candidate.Worktree, "HEAD")
	if err != nil {
		return WorktreeMergeReceipt{}, nil, false, fmt.Errorf("read legacy candidate HEAD: %w", err)
	}
	if head != receipt.Validation.Revision {
		return WorktreeMergeReceipt{}, nil, false, fmt.Errorf("legacy candidate HEAD %s does not match immutable validation revision %s", head, receipt.Validation.Revision)
	}
	candidate.SHA = head
	effective := receipt
	effective.Candidate = candidate
	claim, err := validateMergeAcknowledgementCandidate(ctx, projectsRoot, effective, candidate)
	if err != nil {
		return WorktreeMergeReceipt{}, nil, false, fmt.Errorf("corroborate legacy candidate identity: %w", err)
	}
	if err := requireCandidateContainsImmutableClaimBase(ctx, candidate.Worktree, claim.BaseSHA, candidate.SHA); err != nil {
		return WorktreeMergeReceipt{}, nil, false, err
	}
	if contains, ancestorErr := isMergeAncestor(ctx, candidate.Worktree, receipt.TargetSHA, candidate.SHA); ancestorErr != nil || !contains {
		if ancestorErr == nil {
			ancestorErr = fmt.Errorf("legacy candidate %s does not contain receipt target %s", candidate.SHA, receipt.TargetSHA)
		}
		return WorktreeMergeReceipt{}, nil, false, ancestorErr
	}
	for _, source := range receipt.Sources {
		if err := validateLandedFailureAcknowledgementSource(ctx, projectsRoot, effective, source); err != nil {
			return WorktreeMergeReceipt{}, nil, false, err
		}
		if contains, sourceErr := isMergeAncestor(ctx, candidate.Worktree, source.SHA, candidate.SHA); sourceErr != nil || !contains {
			if sourceErr == nil {
				sourceErr = fmt.Errorf("legacy candidate %s does not contain receipted source %s", candidate.SHA, source.SHA)
			}
			return WorktreeMergeReceipt{}, nil, false, sourceErr
		}
	}
	currentTarget, err := fetchExactMergeTarget(ctx, candidate.Worktree, receipt.Target)
	if err != nil {
		return WorktreeMergeReceipt{}, nil, false, err
	}
	if landed, landingErr := isMergeAncestor(ctx, candidate.Worktree, candidate.SHA, currentTarget); landingErr != nil || landed {
		if landingErr == nil {
			landingErr = fmt.Errorf("legacy candidate %s is already contained in current remote target %s", candidate.SHA, currentTarget)
		}
		return WorktreeMergeReceipt{}, nil, false, landingErr
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receiptPath)
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

func validateLegacyValidationFailedReceiptShape(receipt WorktreeMergeReceipt, receiptPath string) error {
	if err := validateLandedFailureAcknowledgementReceipt(receipt, receiptPath); err != nil {
		return err
	}
	if receipt.SchemaVersion != WorktreeMergeSchemaVersion || receipt.Phase != WorktreeMergePhasePrepare || receipt.Status != WorktreeMergeValidationFailed || receipt.LandingSHA != "" ||
		receipt.Repository == "" || receipt.Target == "" || receipt.TargetSHA == "" || len(receipt.Sources) == 0 ||
		receipt.Candidate.Task == "" || receipt.Candidate.Worktree == "" || receipt.Candidate.Branch == "" || receipt.Candidate.SHA != "" ||
		receipt.Validation.Repository != receipt.Repository || receipt.Validation.Path != receipt.Candidate.Worktree || receipt.Validation.Revision == "" ||
		receipt.ID != worktreeMergeOperationID(receipt.Lane, receipt.Sources) || receipt.Candidate.Task != receipt.ID || receipt.CreatedAt.IsZero() || receipt.UpdatedAt.IsZero() {
		return fmt.Errorf("receipt %s is not the exact legacy validation_failed missing-candidate-SHA shape", receiptPath)
	}
	for _, source := range receipt.Sources {
		if source.Task == "" || source.Worktree == "" || source.Branch == "" || source.SHA == "" {
			return fmt.Errorf("receipt %s has an incomplete immutable source identity", receiptPath)
		}
	}
	return nil
}

func validateLandedFailureAcknowledgementSource(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt, source WorktreeMergeSource) error {
	if source.Task == "" || source.Worktree == "" || source.Branch == "" || source.SHA == "" {
		return errors.New("receipt contains an incomplete source identity")
	}
	guard, err := worktrees.Guard(ctx, source.Worktree, worktrees.GuardOptions{ProjectsRoot: projectsRoot, Base: receipt.Target})
	if err != nil {
		return fmt.Errorf("guard receipted source %s: %w", source.Worktree, err)
	}
	if guard.Kind != "linked" || guard.Transient || guard.Branch != source.Branch || filepath.Clean(guard.Path) != filepath.Clean(source.Worktree) {
		return fmt.Errorf("receipted source %s no longer has its exact linked-worktree identity", source.Worktree)
	}
	if err := requireCleanMergeWorktree(ctx, source.Worktree); err != nil {
		return fmt.Errorf("receipted source %s is not clean: %w", source.Worktree, err)
	}
	head, err := mergeRevision(ctx, source.Worktree, "HEAD")
	if err != nil {
		return fmt.Errorf("read receipted source %s HEAD: %w", source.Worktree, err)
	}
	if head != source.SHA {
		return fmt.Errorf("receipted source %s HEAD %s does not match %s", source.Worktree, head, source.SHA)
	}
	view, err := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: projectsRoot, Worktree: source.Worktree})
	if err != nil {
		return fmt.Errorf("load receipted source Work Log %s: %w", source.Worktree, err)
	}
	if view.Claim == nil || view.Claim.Lifecycle != "active" || view.Claim.Repository != receipt.Repository || view.Claim.Task != source.Task ||
		filepath.Clean(view.Claim.Worktree) != filepath.Clean(source.Worktree) || view.Claim.Branch != source.Branch {
		return fmt.Errorf("receipted source %s has no matching active Work Log claim", source.Worktree)
	}
	return nil
}

func landedFailureAcknowledgementID(ack WorktreeMergeLandedFailureAcknowledgement) string {
	hash := sha256.New()
	for _, value := range []string{ack.ReceiptID, ack.ReceiptPath, string(ack.ReceiptStatus), ack.ReceiptTargetSHA, ack.ReceiptLandingSHA, ack.CurrentTargetSHA, ack.CandidateSHA} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	for _, source := range ack.Sources {
		for _, value := range []string{source.Task, source.Worktree, source.Branch, source.SHA} {
			_, _ = hash.Write([]byte(value))
			_, _ = hash.Write([]byte{0})
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func landedFailureAcknowledgementPath(receiptPath string) string {
	return receiptPath + worktreeMergeLandedFailureAcknowledgementSuffix
}

func persistLandedFailureAcknowledgement(path string, ack WorktreeMergeLandedFailureAcknowledgement) error {
	contents, err := json.MarshalIndent(ack, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".landed-validation-failed-ack-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func readLandedFailureAcknowledgement(path string, receipt WorktreeMergeReceipt) (WorktreeMergeLandedFailureAcknowledgement, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
	}
	var ack WorktreeMergeLandedFailureAcknowledgement
	if err := json.Unmarshal(contents, &ack); err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, fmt.Errorf("decode landed-failure acknowledgement %s: %w", path, err)
	}
	if ack.SchemaVersion != worktreeMergeLandedFailureAcknowledgementSchemaVersion || ack.Status != "landed_failure_acknowledged" ||
		ack.AcknowledgementPath != path || ack.CurrentTargetSHA == "" || ack.CandidateSHA == "" || ack.ClaimBaseSHA == "" ||
		ack.ReceiptPath != receipt.ReceiptPath || ack.ReceiptID != receipt.ID || ack.Lane != receipt.Lane || ack.Repository != receipt.Repository ||
		ack.Target != receipt.Target || ack.ReceiptStatus != receipt.Status || ack.ReceiptTargetSHA != receipt.TargetSHA || ack.ReceiptLandingSHA != receipt.LandingSHA || ack.CandidateSHA != receipt.Candidate.SHA ||
		!sameWorktreeMergeSources(ack.Sources, receipt.Sources) || ack.ID != landedFailureAcknowledgementID(ack) {
		return WorktreeMergeLandedFailureAcknowledgement{}, fmt.Errorf("landed-failure acknowledgement %s has invalid receipt identity", path)
	}
	return ack, nil
}

func hasLandedFailureAcknowledgement(receipt WorktreeMergeReceipt) (bool, error) {
	_, err := readLandedFailureAcknowledgement(landedFailureAcknowledgementPath(receipt.ReceiptPath), receipt)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func validationFailureSupersessionID(ack WorktreeMergeValidationFailureSupersession) string {
	hash := sha256.New()
	for _, value := range []string{ack.ReceiptID, ack.ReceiptPath, ack.ReceiptSHA256, string(ack.ReceiptStatus), ack.ReceiptTargetSHA, ack.CurrentTargetSHA, ack.OriginalClaimBaseSHA, ack.ReplacementClaimBaseSHA, ack.Replacement.Task, ack.Replacement.Worktree, ack.Replacement.Branch, ack.Replacement.SHA} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	if ack.ObservedCandidateDescendantSHA != "" {
		_, _ = hash.Write([]byte("observed_candidate_descendant_sha"))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(ack.ObservedCandidateDescendantSHA))
		_, _ = hash.Write([]byte{0})
	}
	for _, source := range ack.Sources {
		for _, value := range []string{source.Task, source.Worktree, source.Branch, source.SHA} {
			_, _ = hash.Write([]byte(value))
			_, _ = hash.Write([]byte{0})
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func validationFailureSupersessionPath(receiptPath string) string {
	return receiptPath + worktreeMergeValidationFailureSupersessionSuffix
}

func legacyValidationFailureIdentityPath(receiptPath string) string {
	return receiptPath + worktreeMergeLegacyValidationFailureIdentitySuffix
}

func legacyValidationFailureIdentityID(ack WorktreeMergeLegacyValidationFailureIdentity) string {
	hash := sha256.New()
	for _, value := range []string{ack.ReceiptPath, ack.ReceiptSHA256, ack.ReceiptID, ack.Lane, ack.Repository, ack.Target, ack.ReceiptTargetSHA, ack.CurrentTargetSHA, ack.Candidate.Task, ack.Candidate.Worktree, ack.Candidate.Branch, ack.Candidate.SHA, ack.ClaimBaseSHA, ack.Actor, ack.Reason} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	for _, source := range ack.Sources {
		for _, value := range []string{source.Task, source.Worktree, source.Branch, source.SHA} {
			_, _ = hash.Write([]byte(value))
			_, _ = hash.Write([]byte{0})
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func sameLegacyValidationFailureIdentity(left, right WorktreeMergeLegacyValidationFailureIdentity) bool {
	return left.ID == right.ID && left.Status == right.Status && left.ReceiptPath == right.ReceiptPath &&
		left.AcknowledgementPath == right.AcknowledgementPath && left.ReceiptSHA256 == right.ReceiptSHA256 &&
		left.ReceiptID == right.ReceiptID && left.Lane == right.Lane && left.Repository == right.Repository &&
		left.Target == right.Target && left.ReceiptTargetSHA == right.ReceiptTargetSHA && left.CurrentTargetSHA == right.CurrentTargetSHA &&
		left.Candidate == right.Candidate && left.ClaimBaseSHA == right.ClaimBaseSHA && sameWorktreeMergeSources(left.Sources, right.Sources) &&
		left.Actor == right.Actor && left.Reason == right.Reason
}

func persistLegacyValidationFailureIdentity(path string, ack WorktreeMergeLegacyValidationFailureIdentity) error {
	contents, err := json.MarshalIndent(ack, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".legacy-validation-failed-identity-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return linkLegacyValidationFailureIdentity(temporaryPath, path)
}

func readLegacyValidationFailureIdentity(path string, receipt WorktreeMergeReceipt, candidate WorktreeMergeCandidate) (WorktreeMergeLegacyValidationFailureIdentity, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return WorktreeMergeLegacyValidationFailureIdentity{}, err
	}
	var ack WorktreeMergeLegacyValidationFailureIdentity
	if err := json.Unmarshal(contents, &ack); err != nil {
		return WorktreeMergeLegacyValidationFailureIdentity{}, fmt.Errorf("decode legacy validation-failed identity %s: %w", path, err)
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		return WorktreeMergeLegacyValidationFailureIdentity{}, err
	}
	if ack.SchemaVersion != worktreeMergeLegacyValidationFailureIdentitySchemaVersion || ack.Status != "legacy_validation_failed_identity_correlated" ||
		ack.AcknowledgementPath != path || ack.ReceiptPath != receipt.ReceiptPath || ack.ReceiptSHA256 != receiptHash || ack.ReceiptID != receipt.ID ||
		ack.Lane != receipt.Lane || ack.Repository != receipt.Repository || ack.Target != receipt.Target || ack.ReceiptTargetSHA != receipt.TargetSHA ||
		ack.Candidate != candidate || ack.ClaimBaseSHA == "" || ack.CurrentTargetSHA == "" || !sameWorktreeMergeSources(ack.Sources, receipt.Sources) ||
		ack.Actor == "" || ack.Reason == "" || ack.RecordedAt.IsZero() || ack.ID != legacyValidationFailureIdentityID(ack) {
		return WorktreeMergeLegacyValidationFailureIdentity{}, fmt.Errorf("legacy validation-failed identity %s has invalid immutable evidence", path)
	}
	return ack, nil
}

func worktreeMergeReceiptSHA256(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:]), nil
}

// AcknowledgeMissingWorktreeMergeCleanup records independently reproducible
// evidence for legacy cleanup which removed every exact asset but lost one or
// more terminal Work Logs. It never creates replacement Work Log evidence.
func AcknowledgeMissingWorktreeMergeCleanup(ctx context.Context, options WorktreeMergeMissingCleanupAcknowledgementOptions) (WorktreeMergeMissingCleanupAcknowledgement, error) {
	receiptPath, err := resolveWorktreeMergeReceiptPath(options.ProjectsRoot, options.Receipt)
	if err != nil {
		return WorktreeMergeMissingCleanupAcknowledgement{}, err
	}
	receipt, err := readWorktreeMergeReceipt(receiptPath)
	if err != nil {
		return WorktreeMergeMissingCleanupAcknowledgement{}, err
	}
	if receipt.Lane == "" {
		return WorktreeMergeMissingCleanupAcknowledgement{}, fmt.Errorf("receipt %s has no lane identity", receiptPath)
	}
	if options.Apply && (strings.TrimSpace(options.Actor) == "" || strings.TrimSpace(options.Reason) == "") {
		return WorktreeMergeMissingCleanupAcknowledgement{}, errors.New("--actor and --reason are required with --apply")
	}
	lock, err := AcquireOperationLock(options.ProjectsRoot, receipt.Lane, true)
	if err != nil {
		return WorktreeMergeMissingCleanupAcknowledgement{}, err
	}
	defer func() { _ = lock.Release() }()
	receipt, err = readWorktreeMergeReceipt(receiptPath)
	if err != nil {
		return WorktreeMergeMissingCleanupAcknowledgement{}, err
	}
	ack, err := inspectMissingWorktreeMergeCleanup(ctx, options.ProjectsRoot, receipt, strings.TrimSpace(options.Actor), strings.TrimSpace(options.Reason), 0, 0)
	if err != nil {
		return WorktreeMergeMissingCleanupAcknowledgement{}, err
	}
	if !options.Apply {
		return ack, nil
	}
	if err := persistMissingCleanupAcknowledgement(ack.AcknowledgementPath, ack); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return WorktreeMergeMissingCleanupAcknowledgement{}, err
		}
		existing, readErr := validateMissingCleanupAcknowledgement(ctx, options.ProjectsRoot, receipt, ack.AcknowledgementPath, 0, 0)
		if readErr != nil {
			return WorktreeMergeMissingCleanupAcknowledgement{}, readErr
		}
		return existing, nil
	}
	return ack, nil
}

func inspectMissingWorktreeMergeCleanup(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt, actor, reason string, timeout time.Duration, retry int) (WorktreeMergeMissingCleanupAcknowledgement, error) {
	if receipt.SchemaVersion != WorktreeMergeSchemaVersion || receipt.Phase != WorktreeMergePhaseLand || receipt.Status != WorktreeMergeLanded ||
		!receipt.Cleanup || receipt.ID == "" || receipt.Lane == "" || receipt.Repository == "" || receipt.Target == "" || receipt.LandingSHA == "" ||
		receipt.ID != worktreeMergeOperationID(receipt.Lane, receipt.Sources) || receipt.Candidate.Task != receipt.ID {
		return WorktreeMergeMissingCleanupAcknowledgement{}, fmt.Errorf("receipt %s is not an exact landed cleanup-pending receipt", receipt.ReceiptPath)
	}
	if receipt.Checks.Status != PullRequestWaitPassed || (receipt.CanonicalSync != "fast_forwarded" && receipt.CanonicalSync != "not_checked_out") {
		return WorktreeMergeMissingCleanupAcknowledgement{}, fmt.Errorf("receipt %s lacks completed exact checks or canonical synchronization", receipt.ReceiptPath)
	}
	assets, err := terminalWorkLogExpectations(receipt)
	if err != nil {
		return WorktreeMergeMissingCleanupAcknowledgement{}, err
	}
	for _, asset := range assets {
		if _, statErr := os.Lstat(asset.Worktree); statErr == nil {
			return WorktreeMergeMissingCleanupAcknowledgement{}, fmt.Errorf("missing-cleanup acknowledgement refuses task %s because worktree %s remains", asset.Task, asset.Worktree)
		} else if !os.IsNotExist(statErr) {
			return WorktreeMergeMissingCleanupAcknowledgement{}, fmt.Errorf("inspect receipted cleanup worktree %s: %w", asset.Worktree, statErr)
		}
	}
	canonical := filepath.Join(projectsRoot, filepath.FromSlash(receipt.Repository))
	currentTarget, err := fetchExactMergeTarget(ctx, canonical, receipt.Target)
	if err != nil {
		return WorktreeMergeMissingCleanupAcknowledgement{}, err
	}
	contains, err := isMergeAncestor(ctx, canonical, receipt.LandingSHA, currentTarget)
	if err != nil || !contains {
		if err == nil {
			err = fmt.Errorf("exact current remote target %s does not contain receipted landing %s", currentTarget, receipt.LandingSHA)
		}
		return WorktreeMergeMissingCleanupAcknowledgement{}, err
	}
	if err := requireTerminalCleanupBranchesAbsent(ctx, projectsRoot, receipt, assets, timeout, retry); err != nil {
		return WorktreeMergeMissingCleanupAcknowledgement{}, err
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		return WorktreeMergeMissingCleanupAcknowledgement{}, err
	}
	ack := WorktreeMergeMissingCleanupAcknowledgement{
		SchemaVersion: worktreeMergeMissingCleanupAcknowledgementSchemaVersion,
		Status:        "missing_cleanup_acknowledged", ReceiptPath: receipt.ReceiptPath,
		AcknowledgementPath: receipt.ReceiptPath + worktreeMergeMissingCleanupAcknowledgementSuffix,
		ReceiptSHA256:       receiptHash, ReceiptID: receipt.ID, Lane: receipt.Lane,
		Repository: receipt.Repository, Target: receipt.Target, LandingSHA: receipt.LandingSHA,
		CurrentTargetSHA: currentTarget, Assets: append([]worktrees.TerminalWorkLogExpectation(nil), assets...),
		Actor: actor, Reason: reason, RecordedAt: time.Now().UTC(),
	}
	ack.ID = missingCleanupAcknowledgementID(ack)
	return ack, nil
}

func missingCleanupAcknowledgementID(ack WorktreeMergeMissingCleanupAcknowledgement) string {
	hash := sha256.New()
	for _, value := range []string{ack.ReceiptPath, ack.ReceiptSHA256, ack.ReceiptID, ack.Lane, ack.Repository, ack.Target, ack.LandingSHA, ack.CurrentTargetSHA, ack.Actor, ack.Reason} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	for _, asset := range ack.Assets {
		for _, value := range []string{asset.Task, asset.Repository, asset.Worktree, asset.Branch, asset.Base, asset.FinalCommit} {
			_, _ = hash.Write([]byte(value))
			_, _ = hash.Write([]byte{0})
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func sameMissingCleanupAcknowledgement(left, right WorktreeMergeMissingCleanupAcknowledgement) bool {
	return left.ID == right.ID && left.Status == right.Status && left.ReceiptPath == right.ReceiptPath &&
		left.AcknowledgementPath == right.AcknowledgementPath && left.ReceiptSHA256 == right.ReceiptSHA256 &&
		left.ReceiptID == right.ReceiptID && left.Lane == right.Lane && left.Repository == right.Repository && left.Target == right.Target &&
		left.LandingSHA == right.LandingSHA && left.CurrentTargetSHA == right.CurrentTargetSHA &&
		sameTerminalCleanupAssets(left.Assets, right.Assets) && left.Actor == right.Actor && left.Reason == right.Reason
}

func sameTerminalCleanupAssets(left, right []worktrees.TerminalWorkLogExpectation) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func persistMissingCleanupAcknowledgement(path string, ack WorktreeMergeMissingCleanupAcknowledgement) error {
	contents, err := json.MarshalIndent(ack, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".missing-cleanup-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return linkMissingCleanupAcknowledgement(temporaryPath, path)
}

func readMissingCleanupAcknowledgement(path string, receipt WorktreeMergeReceipt) (WorktreeMergeMissingCleanupAcknowledgement, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return WorktreeMergeMissingCleanupAcknowledgement{}, err
	}
	var ack WorktreeMergeMissingCleanupAcknowledgement
	if err := json.Unmarshal(contents, &ack); err != nil {
		return ack, fmt.Errorf("decode missing-cleanup acknowledgement %s: %w", path, err)
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		return ack, err
	}
	expectedAssets, err := terminalWorkLogExpectations(receipt)
	if err != nil {
		return ack, err
	}
	if ack.SchemaVersion != worktreeMergeMissingCleanupAcknowledgementSchemaVersion || ack.Status != "missing_cleanup_acknowledged" ||
		ack.AcknowledgementPath != path || ack.ReceiptPath != receipt.ReceiptPath || ack.ReceiptSHA256 != receiptHash || ack.ReceiptID != receipt.ID ||
		ack.Lane != receipt.Lane || ack.Repository != receipt.Repository || ack.Target != receipt.Target || ack.LandingSHA != receipt.LandingSHA ||
		ack.CurrentTargetSHA == "" || !sameTerminalCleanupAssets(ack.Assets, expectedAssets) || ack.Actor == "" || ack.Reason == "" || ack.RecordedAt.IsZero() || ack.ID != missingCleanupAcknowledgementID(ack) {
		return ack, fmt.Errorf("missing-cleanup acknowledgement %s has invalid immutable evidence", path)
	}
	return ack, nil
}

func validateMissingCleanupAcknowledgement(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt, path string, timeout time.Duration, retry int) (WorktreeMergeMissingCleanupAcknowledgement, error) {
	ack, err := readMissingCleanupAcknowledgement(path, receipt)
	if err != nil {
		return ack, err
	}
	observed, err := inspectMissingWorktreeMergeCleanup(ctx, projectsRoot, receipt, ack.Actor, ack.Reason, timeout, retry)
	if err != nil {
		return ack, err
	}
	canonical := filepath.Join(projectsRoot, filepath.FromSlash(receipt.Repository))
	containsAcknowledgedTarget, ancestorErr := isMergeAncestor(ctx, canonical, ack.CurrentTargetSHA, observed.CurrentTargetSHA)
	if ancestorErr != nil || !containsAcknowledgedTarget {
		if ancestorErr == nil {
			ancestorErr = fmt.Errorf("current remote target %s no longer contains acknowledged target %s", observed.CurrentTargetSHA, ack.CurrentTargetSHA)
		}
		return ack, ancestorErr
	}
	// A forward target advance is benign. Preserve and compare the exact target
	// which the immutable acknowledgement originally observed.
	observed.CurrentTargetSHA = ack.CurrentTargetSHA
	observed.ID = missingCleanupAcknowledgementID(observed)
	if !sameMissingCleanupAcknowledgement(ack, observed) {
		return ack, fmt.Errorf("missing-cleanup acknowledgement %s no longer matches its receipt or absent assets", path)
	}
	return ack, nil
}

func persistValidationFailureSupersession(path string, ack WorktreeMergeValidationFailureSupersession) error {
	contents, err := json.MarshalIndent(ack, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".validation-failed-supersession-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func readValidationFailureSupersession(path string, receipt WorktreeMergeReceipt) (WorktreeMergeValidationFailureSupersession, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return WorktreeMergeValidationFailureSupersession{}, err
	}
	if err := validatePrepareFailureSupersessionReceipt(receipt, receipt.ReceiptPath); err != nil {
		return WorktreeMergeValidationFailureSupersession{}, err
	}
	var ack WorktreeMergeValidationFailureSupersession
	if err := json.Unmarshal(contents, &ack); err != nil {
		return WorktreeMergeValidationFailureSupersession{}, fmt.Errorf("decode validation-failed supersession %s: %w", path, err)
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		return WorktreeMergeValidationFailureSupersession{}, err
	}
	if ack.SchemaVersion != worktreeMergeValidationFailureSupersessionSchemaVersion || ack.Status != "validation_failure_superseded" || ack.AcknowledgementPath != path ||
		ack.ReceiptPath != receipt.ReceiptPath || ack.ReceiptID != receipt.ID || ack.ReceiptSHA256 != receiptHash || ack.ReceiptStatus != receipt.Status ||
		ack.Lane != receipt.Lane || ack.Repository != receipt.Repository || ack.Target != receipt.Target || ack.ReceiptTargetSHA != receipt.TargetSHA ||
		ack.OriginalCandidate != receipt.Candidate || (ack.ObservedCandidateDescendantSHA != "" && (receipt.Status != WorktreeMergeConflict || ack.ObservedCandidateDescendantSHA == receipt.Candidate.SHA)) || ack.OriginalClaimBaseSHA == "" || ack.CurrentTargetSHA == "" || ack.Replacement.Task == "" || ack.Replacement.Worktree == "" || ack.Replacement.Branch == "" || ack.Replacement.SHA == "" || ack.ReplacementClaimBaseSHA == "" ||
		ack.Actor == "" || ack.Reason == "" || ack.RecordedAt.IsZero() || !sameWorktreeMergeSources(ack.Sources, receipt.Sources) || ack.ID != validationFailureSupersessionID(ack) {
		return WorktreeMergeValidationFailureSupersession{}, fmt.Errorf("validation-failed supersession %s has invalid immutable identity", path)
	}
	return ack, nil
}

// readValidationFailureSupersessionWithLegacyIdentity authenticates a
// supersession acknowledgement against the effective candidate identity. A
// legacy receipt omitted candidate.SHA, so the immutable identity sidecar is
// the only permitted source for that field when the supersession is read by a
// global lane scanner. The historical receipt is never rewritten.
func readValidationFailureSupersessionWithLegacyIdentity(path string, receipt WorktreeMergeReceipt) (WorktreeMergeValidationFailureSupersession, WorktreeMergeReceipt, error) {
	ack, err := readValidationFailureSupersession(path, receipt)
	if err == nil {
		return ack, receipt, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return WorktreeMergeValidationFailureSupersession{}, WorktreeMergeReceipt{}, err
	}
	if legacyErr := validateLegacyValidationFailedReceiptShape(receipt, receipt.ReceiptPath); legacyErr != nil {
		return WorktreeMergeValidationFailureSupersession{}, WorktreeMergeReceipt{}, err
	}

	identityPath := legacyValidationFailureIdentityPath(receipt.ReceiptPath)
	contents, readErr := os.ReadFile(identityPath)
	if readErr != nil {
		// A legacy receipt with a supersession acknowledgement requires its
		// identity sidecar. Do not retain os.ErrNotExist here: the caller uses
		// that sentinel only for an absent supersession acknowledgement.
		return WorktreeMergeValidationFailureSupersession{}, WorktreeMergeReceipt{}, fmt.Errorf("read legacy validation-failed identity %s: %v", identityPath, readErr)
	}
	var identity WorktreeMergeLegacyValidationFailureIdentity
	if readErr := json.Unmarshal(contents, &identity); readErr != nil {
		return WorktreeMergeValidationFailureSupersession{}, WorktreeMergeReceipt{}, fmt.Errorf("decode legacy validation-failed identity %s: %w", identityPath, readErr)
	}
	candidate := identity.Candidate
	if candidate.Task != receipt.Candidate.Task || filepath.Clean(candidate.Worktree) != filepath.Clean(receipt.Candidate.Worktree) ||
		candidate.Branch != receipt.Candidate.Branch || candidate.SHA == "" || candidate.SHA != receipt.Validation.Revision {
		return WorktreeMergeValidationFailureSupersession{}, WorktreeMergeReceipt{}, fmt.Errorf("legacy validation-failed identity %s has mismatched candidate identity", identityPath)
	}
	if _, readErr := readLegacyValidationFailureIdentity(identityPath, receipt, candidate); readErr != nil {
		return WorktreeMergeValidationFailureSupersession{}, WorktreeMergeReceipt{}, readErr
	}
	effective := receipt
	effective.Candidate = candidate
	ack, err = readValidationFailureSupersession(path, effective)
	if err != nil {
		return WorktreeMergeValidationFailureSupersession{}, WorktreeMergeReceipt{}, err
	}
	return ack, effective, nil
}

// hasValidationFailureSupersession refuses to treat a historical self-
// supersession as effective until its separate correction still matches all
// live receipt, claim, source, target, and candidate evidence.
func hasValidationFailureSupersession(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt) (bool, error) {
	ackPath := validationFailureSupersessionPath(receipt.ReceiptPath)
	ack, effectiveReceipt, err := readValidationFailureSupersessionWithLegacyIdentity(ackPath, receipt)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if ack.Replacement == ack.OriginalCandidate {
		if correctionErr := validateSelfSupersessionCorrection(ctx, projectsRoot, effectiveReceipt, ack); correctionErr != nil {
			if errors.Is(correctionErr, os.ErrNotExist) {
				return false, fmt.Errorf("validation-failed supersession %s is a self-supersession and requires an append-only correction", ackPath)
			}
			return false, correctionErr
		}
	}
	return true, nil
}

func validateSelfSupersessionCorrection(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt, supersession WorktreeMergeValidationFailureSupersession) error {
	if err := validateValidationFailedSupersessionReceipt(receipt, receipt.ReceiptPath); err != nil {
		return err
	}
	correction, err := readSelfSupersessionCorrection(selfSupersessionCorrectionPath(receipt.ReceiptPath), receipt, supersession)
	if err != nil {
		return err
	}
	originalClaim, err := validateMergeAcknowledgementCandidate(ctx, projectsRoot, receipt, receipt.Candidate)
	if err != nil {
		return fmt.Errorf("validate corrected self-supersession original candidate: %w", err)
	}
	originalClaimBytes, err := os.ReadFile(originalClaim.ClaimPath)
	if err != nil {
		return fmt.Errorf("read corrected self-supersession immutable claim: %w", err)
	}
	originalClaimDigest := sha256.Sum256(originalClaimBytes)
	if originalClaimHash := hex.EncodeToString(originalClaimDigest[:]); originalClaimHash != correction.ImmutableClaimSHA256 ||
		originalClaim.BaseSHA != supersession.OriginalClaimBaseSHA || originalClaim.BaseSHA != correction.OriginalClaimBaseSHA {
		return errors.New("corrected self-supersession immutable claim SHA256 or base no longer matches recorded evidence")
	}
	replacement, replacementClaim, err := validateValidationFailureReplacement(ctx, projectsRoot, receipt, correction.CorrectedReplacement.Worktree)
	if err != nil {
		return fmt.Errorf("validate corrected self-supersession replacement: %w", err)
	}
	if replacement.Task != correction.CorrectedReplacement.Task || filepath.Clean(replacement.Worktree) != filepath.Clean(correction.CorrectedReplacement.Worktree) ||
		replacement.Branch != correction.CorrectedReplacement.Branch || replacementClaim.BaseSHA != correction.ReplacementClaimBaseSHA {
		return errors.New("corrected self-supersession replacement identity or claim base no longer matches recorded evidence")
	}
	containsRecordedReplacement, ancestorErr := isMergeAncestor(ctx, replacement.Worktree, correction.CorrectedReplacement.SHA, replacement.SHA)
	if ancestorErr != nil {
		return fmt.Errorf("verify corrected self-supersession replacement ancestry: %w", ancestorErr)
	}
	if !containsRecordedReplacement {
		return errors.New("corrected self-supersession replacement does not retain its recorded replacement commit")
	}
	if err := requireImmutableHistoricalWorktreeMergeSources(ctx, replacement.Worktree, receipt); err != nil {
		return fmt.Errorf("validate corrected self-supersession historical source: %w", err)
	}
	currentTarget, err := fetchExactMergeTarget(ctx, replacement.Worktree, receipt.Target)
	if err != nil {
		return err
	}
	if currentTarget != supersession.CurrentTargetSHA || currentTarget != correction.CurrentTargetSHA {
		containsRecordedTarget, targetAncestorErr := isMergeAncestor(ctx, replacement.Worktree, correction.CurrentTargetSHA, currentTarget)
		if targetAncestorErr != nil {
			return fmt.Errorf("verify corrected self-supersession target ancestry: %w", targetAncestorErr)
		}
		if !containsRecordedTarget {
			return fmt.Errorf("corrected self-supersession target %s is not a descendant of recorded target %s", currentTarget, correction.CurrentTargetSHA)
		}
	}
	for _, root := range append([]string{correction.CorrectedReplacement.SHA, originalClaim.BaseSHA, receipt.TargetSHA, supersession.CurrentTargetSHA, correction.CurrentTargetSHA, replacementClaim.BaseSHA}, sourceSHAs(immutableHistoricalWorktreeMergeSources(receipt))...) {
		contains, ancestorErr := isMergeAncestor(ctx, replacement.Worktree, root, replacement.SHA)
		if ancestorErr != nil || !contains {
			if ancestorErr == nil {
				ancestorErr = fmt.Errorf("corrected self-supersession replacement %s does not contain recorded immutable root %s", replacement.SHA, root)
			}
			return ancestorErr
		}
	}
	return nil
}

func selfSupersessionCorrectionPath(receiptPath string) string {
	return receiptPath + worktreeMergeSelfSupersessionCorrectionSuffix
}

func selfSupersessionCorrectionID(correction WorktreeMergeSelfSupersessionCorrection) string {
	hash := sha256.New()
	for _, value := range []string{correction.ReceiptPath, correction.ReceiptSHA256, correction.ImmutableClaimSHA256, correction.SupersessionPath, correction.SupersessionSHA256, correction.SupersessionID, correction.OriginalClaimBaseSHA, correction.ReplacementClaimBaseSHA, correction.CurrentTargetSHA, correction.CorrectedReplacement.Task, correction.CorrectedReplacement.Worktree, correction.CorrectedReplacement.Branch, correction.CorrectedReplacement.SHA, correction.Actor, correction.Reason} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	for _, source := range correction.Sources {
		for _, value := range []string{source.Task, source.Worktree, source.Branch, source.SHA} {
			_, _ = hash.Write([]byte(value))
			_, _ = hash.Write([]byte{0})
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func sameSelfSupersessionCorrection(left, right WorktreeMergeSelfSupersessionCorrection) bool {
	return left.ID == right.ID && left.Status == right.Status && left.CorrectionPath == right.CorrectionPath &&
		left.ReceiptPath == right.ReceiptPath && left.ReceiptSHA256 == right.ReceiptSHA256 && left.ImmutableClaimSHA256 == right.ImmutableClaimSHA256 &&
		left.SupersessionPath == right.SupersessionPath && left.SupersessionSHA256 == right.SupersessionSHA256 && left.SupersessionID == right.SupersessionID &&
		left.OriginalCandidate == right.OriginalCandidate && left.OriginalClaimBaseSHA == right.OriginalClaimBaseSHA &&
		left.CorrectedReplacement == right.CorrectedReplacement && left.ReplacementClaimBaseSHA == right.ReplacementClaimBaseSHA &&
		left.CurrentTargetSHA == right.CurrentTargetSHA && sameWorktreeMergeSources(left.Sources, right.Sources) &&
		left.Actor == right.Actor && left.Reason == right.Reason
}

func readSelfSupersessionCorrection(path string, receipt WorktreeMergeReceipt, supersession WorktreeMergeValidationFailureSupersession) (WorktreeMergeSelfSupersessionCorrection, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return WorktreeMergeSelfSupersessionCorrection{}, err
	}
	var correction WorktreeMergeSelfSupersessionCorrection
	if err := json.Unmarshal(contents, &correction); err != nil {
		return WorktreeMergeSelfSupersessionCorrection{}, fmt.Errorf("decode self-supersession correction %s: %w", path, err)
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		return WorktreeMergeSelfSupersessionCorrection{}, err
	}
	supersessionHash, err := worktreeMergeReceiptSHA256(supersession.AcknowledgementPath)
	if err != nil {
		return WorktreeMergeSelfSupersessionCorrection{}, err
	}
	if correction.SchemaVersion != worktreeMergeSelfSupersessionCorrectionSchemaVersion || correction.Status != "validation_failure_self_supersession_corrected" ||
		correction.CorrectionPath != path || correction.ReceiptPath != receipt.ReceiptPath || correction.ReceiptSHA256 != receiptHash ||
		correction.SupersessionPath != supersession.AcknowledgementPath || correction.SupersessionSHA256 != supersessionHash || correction.SupersessionID != supersession.ID ||
		correction.OriginalCandidate != receipt.Candidate || correction.OriginalCandidate != supersession.OriginalCandidate || supersession.Replacement != supersession.OriginalCandidate || supersession.ReplacementClaimBaseSHA != supersession.OriginalClaimBaseSHA ||
		correction.OriginalClaimBaseSHA != supersession.OriginalClaimBaseSHA || correction.ImmutableClaimSHA256 == "" || correction.ReplacementClaimBaseSHA == "" || correction.CurrentTargetSHA == "" ||
		correction.CurrentTargetSHA != supersession.CurrentTargetSHA ||
		correction.CorrectedReplacement.Task == "" || correction.CorrectedReplacement.Worktree == "" || correction.CorrectedReplacement.Branch == "" || correction.CorrectedReplacement.SHA == "" ||
		correction.CorrectedReplacement == receipt.Candidate || correction.CorrectedReplacement.SHA == receipt.Candidate.SHA || !sameWorktreeMergeSources(correction.Sources, receipt.Sources) ||
		correction.Actor == "" || correction.Reason == "" || correction.RecordedAt.IsZero() || correction.ID != selfSupersessionCorrectionID(correction) {
		return WorktreeMergeSelfSupersessionCorrection{}, fmt.Errorf("self-supersession correction %s has invalid immutable identity", path)
	}
	return correction, nil
}

func persistSelfSupersessionCorrection(path string, correction WorktreeMergeSelfSupersessionCorrection) error {
	contents, err := json.MarshalIndent(correction, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".validation-failed-self-supersession-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return linkSelfSupersessionCorrection(temporaryPath, path)
}
