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
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/githubobserver"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

const (
	worktreeMergeLandedFailureAcknowledgementSchemaVersion    = 1
	worktreeMergeLandedFailureAcknowledgementSuffix           = ".landed-validation-failed.ack.json"
	worktreeMergeValidationFailureSupersessionSchemaVersion   = 1
	worktreeMergeValidationFailureSupersessionSuffix          = ".validation-failed.superseded.ack.json"
	worktreeMergeLegacyValidationFailureIdentitySchemaVersion = 1
	worktreeMergeLegacyValidationFailureIdentitySuffix        = ".legacy-validation-failed.identity.ack.json"
	worktreeMergeLegacyConflictIdentitySchemaVersion          = 1
	worktreeMergeLegacyConflictIdentitySuffix                 = ".legacy-conflict.identity.ack.json"
	worktreeMergeSelfSupersessionCorrectionSchemaVersion      = 1
	worktreeMergeSelfSupersessionCorrectionSuffix             = ".validation-failed.self-supersession.corrected.ack.json"
	worktreeMergePreparedRebatchSchemaVersion                 = 1
	worktreeMergePreparedRebatchSuffix                        = ".prepared.rebatched.ack.json"
	worktreeMergeReceiptCollisionAcknowledgementSchemaVersion = 1
	worktreeMergeReceiptCollisionAcknowledgementSuffix        = ".receipt-collision.ack.json"
	worktreeMergeMissingCleanupAcknowledgementSchemaVersion   = 1
	worktreeMergeMissingCleanupAcknowledgementSuffix          = ".missing-cleanup.ack.json"
	worktreeMergeConflictCandidateAdvanceSchemaVersion        = 1
	worktreeMergeConflictCandidateAdvanceSuffix               = ".conflict-candidate-advanced.ack.json"
)

// WorktreeMergeConflictCandidateAdvance is the append-only bridge between a
// conflict receipt's original candidate and the clean, manually resolved
// descendant. It is written before the mutable receipt is advanced to that
// descendant, so an interrupted resume cannot publish an unaudited head.
type WorktreeMergeConflictCandidateAdvance struct {
	SchemaVersion        int                    `json:"schema_version"`
	ID                   string                 `json:"id"`
	Status               string                 `json:"status"`
	ReceiptPath          string                 `json:"receipt_path"`
	AcknowledgementPath  string                 `json:"acknowledgement_path"`
	ReceiptSHA256        string                 `json:"receipt_sha256"`
	ReceiptID            string                 `json:"receipt_id"`
	Lane                 string                 `json:"lane"`
	Repository           string                 `json:"repository"`
	Target               string                 `json:"target"`
	ReceiptTargetSHA     string                 `json:"receipt_target_sha"`
	CurrentTargetSHA     string                 `json:"current_target_sha"`
	OriginalCandidate    WorktreeMergeCandidate `json:"original_candidate"`
	AdvancedCandidateSHA string                 `json:"advanced_candidate_sha"`
	ClaimBaseSHA         string                 `json:"claim_base_sha"`
	Sources              []WorktreeMergeSource  `json:"sources"`
	RecordedAt           time.Time              `json:"recorded_at"`
}

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
	// ClosedPullRequest names the superseded original candidate's own pull
	// request when this rebatch closed it (red-team finding M6): a
	// published-unlanded original has very likely already armed GitHub
	// auto-merge (the PR-land engine arms it before any check wait), and
	// leaving it open would let GitHub land the superseded content
	// alongside this replacement the moment its checks — or a later
	// update-branch — go green. Empty when the original never published a
	// pull request, so there was nothing to close.
	ClosedPullRequest string `json:"closed_pull_request,omitempty"`
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

// WorktreeMergeLegacyConflictIdentity records the only candidate SHA WB may
// derive for an unpublished prepare conflict whose writer omitted it. The
// candidate must still be clean, locally claimed, unpublished, unlanded, and
// contain the receipt target plus every exact receipted source.
type WorktreeMergeLegacyConflictIdentity struct {
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

// persistReceiptCollisionAcknowledgementInjected persists the acknowledgement
// with a test seam (task-9 PR-4): production call sites pass a nil
// *filewrite.Injector, so production behaviour is unchanged; a test passes
// its own Injector directly to reach a create/chmod/write/sync/close/link
// failure branch deterministically, and can use Injector.Hook to build a
// real concurrent-collision race at the final LinkPath call the way the
// package-level linkReceiptCollisionAcknowledgement var this replaces used
// to let a test do by reassignment.
func persistReceiptCollisionAcknowledgementInjected(path string, ack WorktreeMergeReceiptCollisionAcknowledgement, inj *filewrite.Injector) error {
	return persistMergeAcknowledgement(path, ".receipt-collision-ack-*.tmp", ack, filewrite.LinkPath, inj)
}

func readReceiptCollisionAcknowledgement(path string, receipt WorktreeMergeReceipt) (WorktreeMergeReceiptCollisionAcknowledgement, error) {
	var ack WorktreeMergeReceiptCollisionAcknowledgement
	if err := readMergeAcknowledgement(path, "receipt-collision acknowledgement", &ack); err != nil {
		return WorktreeMergeReceiptCollisionAcknowledgement{}, err
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

// validatePreparedWorktreeMergeRebatch proves that the old unlanded lane is
// untouched and that the requested source list is a strict additive rebatch:
// every old branch remains and may only advance by ancestry; new branches are
// distinct. An exact published-unlanded candidate may be replaced on a proven
// fast-forward target; unpublished evidence still requires target equality.
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
	publishedUnlanded := worktreeMergeReceiptPublishedUnlanded(receipt)
	if !preparedRebatchOriginalEligible(receipt, collisionAcknowledged) || receipt.LandingSHA != "" ||
		receipt.Repository != repository || receipt.Target != target ||
		receipt.TargetSHA == "" || receipt.Candidate.Task == "" || receipt.Candidate.Worktree == "" || receipt.Candidate.Branch == "" || receipt.Candidate.SHA == "" || len(receipt.Sources) == 0 {
		return nil, fmt.Errorf("rebatch receipt %s is not an unlanded prepared candidate with complete immutable identity", receiptPath)
	}
	if receipt.Lane != worktreeMergeLaneID(repository, target) {
		return nil, fmt.Errorf("rebatch receipt %s has inconsistent lane identity", receiptPath)
	}
	if _, err := validateMergeAcknowledgementCandidate(ctx, projectsRoot, receipt, receipt.Candidate); err != nil {
		return nil, fmt.Errorf("validate prepared rebatch candidate: %w", err)
	}
	if publishedUnlanded {
		if err := validatePublishedUnlandedRebatch(ctx, projectsRoot, receipt, repository, target, sources); err != nil {
			return nil, err
		}
	}
	currentTarget, err := fetchExactMergeTarget(ctx, receipt.Candidate.Worktree, target)
	if err != nil {
		return nil, err
	}
	if currentTarget != receipt.TargetSHA {
		if !publishedUnlanded {
			return nil, fmt.Errorf("rebatch refuses target drift from %s to %s", receipt.TargetSHA, currentTarget)
		}
		advanced, ancestryErr := isMergeAncestor(ctx, receipt.Candidate.Worktree, receipt.TargetSHA, currentTarget)
		if ancestryErr != nil || !advanced {
			return nil, fmt.Errorf("published rebatch target is not a proven fast-forward from %s to %s: ancestor=%t err=%v", receipt.TargetSHA, currentTarget, advanced, ancestryErr)
		}
	}
	if landed, ancestorErr := isMergeAncestor(ctx, receipt.Candidate.Worktree, receipt.Candidate.SHA, currentTarget); ancestorErr != nil || landed {
		return nil, fmt.Errorf("rebatch candidate must remain unlanded: ancestor=%t err=%v", landed, ancestorErr)
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
		return nil, fmt.Errorf("rebatch source set must add at least one distinct source ref; to retry this exact candidate and preserve its receipt and pull-request lineage, run: wb worktree merge resume %s", receiptPath)
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

func validatePublishedUnlandedRebatch(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt, repository, target string, sources []WorktreeMergeSource) error {
	remote, _, err := runCommand(ctx, defaultRunner, 30*time.Second, 0, receipt.Candidate.Worktree,
		"git", "ls-remote", "--heads", "origin", "refs/heads/"+receipt.Candidate.Branch)
	if err != nil {
		return fmt.Errorf("read checks-failed candidate ref: %w", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(remote), receipt.Candidate.SHA+"\t") {
		return fmt.Errorf("checks-failed candidate ref drifted from %s", receipt.Candidate.SHA)
	}
	view, err := githubchecks.ReadPullRequest(ctx, receipt.Repository, receipt.PullRequest)
	if err != nil {
		return fmt.Errorf("read checks-failed pull request: %w", err)
	}
	if view.Base.Ref != receipt.Target || view.Head.SHA != receipt.Candidate.SHA {
		return fmt.Errorf("checks-failed pull request is not the exact open unmerged candidate")
	}
	if strings.EqualFold(view.State, "open") && !view.Merged {
		return nil
	}
	// Red-team finding M5 (second half): a crash between a previous
	// rebatch's close of this exact pull request and its own acknowledgement
	// leaves the pull request reading closed-and-not-merged on resume, not
	// open. That is never grounds to proceed on its own - an externally
	// closed pull request must still refuse - but WB's own prior attempt at
	// this exact rebatch (same lane, same requested sources) persists its
	// close intent on the replacement receipt BEFORE the PATCH that closes
	// it, at the same deterministic path a resume recomputes here. Only when
	// that replacement already names this pull request as superseded does a
	// resume accept "closed, not merged" as the retired state its own prior
	// attempt already produced.
	if !view.Merged && strings.EqualFold(view.State, "closed") {
		if recorded, recordedErr := worktreeMergeReplacementRecordsSupersession(projectsRoot, repository, target, sources, receipt.ReceiptPath, receipt.PullRequest); recordedErr == nil && recorded {
			return nil
		}
	}
	return fmt.Errorf("checks-failed pull request is not the exact open unmerged candidate")
}

// worktreeMergeReplacementRecordsSupersession reports whether the
// replacement receipt a rebatch of (repository, target, sources) would
// resolve to already exists on disk, is itself bound back to
// originalReceiptPath via its own RebatchOf field, and already names
// pullRequest as its SupersededPullRequest - i.e. a previous attempt at this
// exact rebatch already persisted the close intent this resume is
// recovering, per the persist-before-PATCH ordering in
// ensurePreparedWorktreeMergeRebatch. It never itself closes or verifies
// anything; it only reads a receipt WB itself would have written.
//
// Red-team finding M6 (minor): SupersededPullRequest alone is just a string
// field - nothing stops a corrupted or hand-edited receipt from naming an
// arbitrary pull request there. Requiring replacement.RebatchOf to name
// EXACTLY originalReceiptPath - the receipt validatePublishedUnlandedRebatch
// is already validating - before trusting SupersededPullRequest binds the
// close intent to the one rebatch chain it was durably recorded for; a
// replacement rebatching some other receipt can never vouch for this one's
// pull request.
func worktreeMergeReplacementRecordsSupersession(projectsRoot, repository, target string, sources []WorktreeMergeSource, originalReceiptPath, pullRequest string) (bool, error) {
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return false, err
	}
	lane := worktreeMergeLaneID(repository, target)
	operation := worktreeMergeOperationID(lane, sources)
	receiptPath := filepath.Join(home, "reports", "worktree-merge", operation+".json")
	replacement, err := readWorktreeMergeReceipt(receiptPath)
	if err != nil {
		return false, err
	}
	if replacement.RebatchOf != originalReceiptPath {
		return false, nil
	}
	return replacement.SupersededPullRequest != "" && replacement.SupersededPullRequest == pullRequest, nil
}

// preparedRebatchOriginalEligible compares the original receipt shape only.
// Collision authentication remains the responsibility of each physical reader.
func preparedRebatchOriginalEligible(receipt WorktreeMergeReceipt, collisionAcknowledged bool) bool {
	prepared := receipt.Phase == WorktreeMergePhasePrepare && receipt.PullRequest == "" &&
		receipt.PublishedCandidateSHA == "" && receipt.LandingSHA == "" &&
		(receipt.Status == WorktreeMergePrepared || (collisionAcknowledged && receipt.Status == WorktreeMergePreparing))
	return prepared || worktreeMergeReceiptPublishedUnlanded(receipt)
}

func preparedRebatchReplacementMatches(rebatch WorktreeMergePreparedRebatch, replacement WorktreeMergeReceipt) bool {
	return rebatch.ReplacementReceiptPath == replacement.ReceiptPath && rebatch.Replacement == replacement.Candidate &&
		sameWorktreeMergeSources(rebatch.Sources, replacement.Sources)
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
	return persistPreparedWorktreeMergeRebatchInjected(path, rebatch, nil)
}

// persistPreparedWorktreeMergeRebatchInjected is
// persistPreparedWorktreeMergeRebatch's test seam (task-9 PR-4): every
// production call site reaches it only through
// persistPreparedWorktreeMergeRebatch, which always passes a nil
// *filewrite.Injector, so production behaviour is unchanged; a test passes
// its own Injector directly to reach a create/chmod/write/sync/close/rename
// failure branch deterministically.
func persistPreparedWorktreeMergeRebatchInjected(path string, rebatch WorktreeMergePreparedRebatch, inj *filewrite.Injector) error {
	return persistMergeAcknowledgement(path, ".prepared-rebatch-*.tmp", rebatch, filewrite.Rename, inj)
}

// persistPreparedWorktreeMergeRebatchForPrepare is a narrow test seam for the
// durable receipt -> acknowledgement boundary. Production always persists the
// append-only acknowledgement with the atomic writer above.
var persistPreparedWorktreeMergeRebatchForPrepare = persistPreparedWorktreeMergeRebatch

// worktreeMergeReceiptPublishedUnlanded reports whether receipt is an
// unlanded candidate that already published a pull request for its exact
// current head: the land phase, a checks-failed or published-handoff status,
// an open pull request naming this exact candidate, and no landing yet.
func worktreeMergeReceiptPublishedUnlanded(receipt WorktreeMergeReceipt) bool {
	return receipt.Phase == WorktreeMergePhaseLand &&
		(receipt.Status == WorktreeMergeChecksFailed || receipt.Status == WorktreeMergePublished) &&
		receipt.PullRequest != "" && receipt.PublishedCandidateSHA == receipt.Candidate.SHA && receipt.LandingSHA == ""
}

// closeSupersededWorktreeMergePullRequest retires a superseded original
// candidate's own pull request (red-team finding M6). It never disarms
// auto-merge on a red result — that stays forbidden — this is retirement of
// a candidate a rebatch has already replaced, so its armed auto-merge cannot
// land it alongside the replacement.
//
// sleep is the verify-retry backoff seam: every production caller passes
// time.Sleep (through ensurePreparedWorktreeMergeRebatch); a test passes a
// recorder. It is a function parameter, not a package-level mutable var, so
// a test cannot leave shared package state mutated for another test running
// in parallel.
func closeSupersededWorktreeMergePullRequest(ctx context.Context, repository, pullRequest string, sleep func(time.Duration)) error {
	numberText, err := githubchecks.PullRequestNumber(pullRequest)
	if err != nil {
		return fmt.Errorf("resolve superseded pull request number: %w", err)
	}
	number, err := strconv.Atoi(numberText)
	if err != nil {
		return fmt.Errorf("parse superseded pull request number %q: %w", numberText, err)
	}
	remote := githubSourcePullRequestRemote{}
	if err := remote.close(ctx, repository, number); err != nil {
		return err
	}
	// GitHub's close call on a pull request that was merged before this
	// call reached it succeeds without error - a merged PR reports "closed"
	// too, and PATCH state=closed on it is a harmless no-op rather than a
	// refusal. That would let a rebatch believe an armed, already-landed
	// candidate had been retired when it was actually merged. Re-read and
	// require both closed and not merged before trusting the close.
	//
	// Red-team finding M5: the close PATCH above already succeeded by this
	// point. A transient GitHub read failure on the verification below is
	// not a verdict on the close - it is exactly the kind of blip every
	// other read in this area (ciwait.go, pr_land_engine.go) retries rather
	// than surfaces as a failure that leaves an already-closed pull request
	// looking stuck and prompts a manual reopen. Retry it a few times before
	// giving up.
	const verifyAttempts = 3
	const verifyDelay = 500 * time.Millisecond
	var view githubchecks.PullRequestView
	var readErr error
	for attempt := 1; attempt <= verifyAttempts; attempt++ {
		view, readErr = githubchecks.ReadPullRequest(ctx, repository, numberText)
		if readErr == nil || !githubobserver.IsTransientReadFailure(readErr) || attempt == verifyAttempts {
			break
		}
		sleep(verifyDelay)
	}
	if readErr != nil {
		return fmt.Errorf("verify superseded pull request %s was closed, not merged: %w", pullRequest, readErr)
	}
	if view.Merged || strings.EqualFold(view.State, "merged") {
		return fmt.Errorf("superseded pull request %s was merged before it could be closed as superseded; a rebatch must not proceed past an already-landed candidate", pullRequest)
	}
	if !strings.EqualFold(view.State, "closed") {
		return fmt.Errorf("superseded pull request %s did not close (state is %s)", pullRequest, view.State)
	}
	return nil
}

// sleep is threaded through to closeSupersededWorktreeMergePullRequest's
// verify-retry seam; see that function's doc comment.
func ensurePreparedWorktreeMergeRebatch(ctx context.Context, rebatch *WorktreeMergePreparedRebatch, replacement *WorktreeMergeReceipt, sleep func(time.Duration)) error {
	if rebatch == nil {
		return errors.New("prepared rebatch evidence is required")
	}
	if replacement == nil {
		return errors.New("prepared rebatch replacement receipt is required")
	}
	path := rebatchPath(rebatch.ReceiptPath)
	complete := completePreparedWorktreeMergeRebatch(*rebatch, *replacement)
	original, err := readWorktreeMergeReceipt(rebatch.ReceiptPath)
	if err != nil {
		return err
	}
	if existing, err := readPreparedWorktreeMergeRebatch(path, original); err == nil {
		if !preparedRebatchReplacementMatches(existing, *replacement) {
			return fmt.Errorf("prepared rebatch acknowledgement %s binds different replacement evidence", path)
		}
		if existing.ClosedPullRequest != "" && replacement.SupersededPullRequest != existing.ClosedPullRequest {
			replacement.SupersededPullRequest = existing.ClosedPullRequest
			if persistErr := persistWorktreeMergeReceipt(*replacement); persistErr != nil {
				return persistErr
			}
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// M6: close the superseded original's pull request BEFORE writing the
	// append-only acknowledgement. A closure failure refuses the rebatch
	// rather than proceeding with a live stale candidate whose auto-merge
	// may already be armed — this is retirement of a replaced candidate,
	// never disarm-on-red, which stays forbidden.
	if worktreeMergeReceiptPublishedUnlanded(original) {
		// Red-team finding M5: persist the close intent on the replacement
		// receipt BEFORE the PATCH that closes the superseded original, not
		// after. A crash, kill, or lost connection between that PATCH and
		// this function's own bookkeeping must not leave a resume unable to
		// tell "WB already closed this as superseded" from an unrelated,
		// unexplained closed pull request - the durable receipt says so
		// either way, and a resume that re-reads a closed-not-merged PR here
		// can trust it as the retired state rather than needing a manual
		// reopen to make sense of it.
		replacement.SupersededPullRequest = original.PullRequest
		if persistErr := persistWorktreeMergeReceipt(*replacement); persistErr != nil {
			return persistErr
		}
		if closeErr := closeSupersededWorktreeMergePullRequest(ctx, original.Repository, original.PullRequest, sleep); closeErr != nil {
			return fmt.Errorf("close superseded pull request %s before rebatch: %w", original.PullRequest, closeErr)
		}
		complete.ClosedPullRequest = original.PullRequest
	}
	return persistPreparedWorktreeMergeRebatchForPrepare(path, complete)
}

func readPreparedWorktreeMergeRebatch(path string, receipt WorktreeMergeReceipt) (WorktreeMergePreparedRebatch, error) {
	var rebatch WorktreeMergePreparedRebatch
	if err := readMergeAcknowledgement(path, "prepared rebatch", &rebatch); err != nil {
		return WorktreeMergePreparedRebatch{}, err
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
	publishedUnlanded := worktreeMergeReceiptPublishedUnlanded(receipt)
	if rebatch.SchemaVersion != worktreeMergePreparedRebatchSchemaVersion || rebatch.Status != "prepared_rebatched" ||
		rebatch.AcknowledgementPath != path || rebatch.ReceiptPath != receipt.ReceiptPath || rebatch.ReceiptID != receipt.ID ||
		rebatch.ReceiptSHA256 != receiptHash || !preparedRebatchOriginalEligible(receipt, collisionAcknowledged) || rebatch.ReceiptStatus != receipt.Status || rebatch.Lane != receipt.Lane ||
		rebatch.Repository != receipt.Repository || rebatch.Target != receipt.Target || rebatch.ReceiptTargetSHA != receipt.TargetSHA ||
		rebatch.CurrentTargetSHA == "" || (!publishedUnlanded && rebatch.CurrentTargetSHA != receipt.TargetSHA) || rebatch.OriginalCandidate != receipt.Candidate ||
		!sameWorktreeMergeSources(rebatch.OriginalSources, receipt.Sources) || rebatch.ReplacementReceiptPath == receipt.ReceiptPath ||
		replacement.RebatchOf != receipt.ReceiptPath || replacement.Repository != receipt.Repository || replacement.Target != receipt.Target ||
		replacement.TargetSHA != rebatch.CurrentTargetSHA || !preparedRebatchReplacementMatches(rebatch, replacement) || len(replacement.RebatchedCandidates) != 1 || replacement.RebatchedCandidates[0] != receipt.Candidate ||
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

func requireCandidateContainsImmutableClaimBase(ctx context.Context, candidateWorktree, claimBaseSHA, candidateSHA string) error {
	return requireCandidateContainsImmutableClaimBaseWithRunner(ctx, defaultRunner, candidateWorktree, claimBaseSHA, candidateSHA)
}

func requireCandidateContainsImmutableClaimBaseWithRunner(ctx context.Context, run runner.Runner, candidateWorktree, claimBaseSHA, candidateSHA string) error {
	contains, err := isMergeAncestorWithRunner(ctx, run, candidateWorktree, claimBaseSHA, candidateSHA)
	if err != nil {
		return err
	}
	if !contains {
		return fmt.Errorf("candidate %s does not contain immutable claim base %s", candidateSHA, claimBaseSHA)
	}
	return nil
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
		if receipt.Phase != WorktreeMergePhaseLand || receipt.LandingSHA == "" || receipt.Checks.Status != githubchecks.PullRequestWaitFailed || receipt.Checks.Head != receipt.LandingSHA {
			return fmt.Errorf("receipt %s is %s without an exact failed post-target CI receipt", receiptPath, receipt.Status)
		}
	case WorktreeMergeLanded:
		if !isLandedFailedValidationReceipt(receipt) {
			return fmt.Errorf("receipt %s is %s without an exact landed failed-validation receipt", receiptPath, receipt.Status)
		}
	default:
		return fmt.Errorf("receipt %s is %s, want prepare validation_failed, landed failed-validation, or landed_post_target_ci_failed", receiptPath, receipt.Status)
	}
	return nil
}

func isLandedFailedValidationReceipt(receipt WorktreeMergeReceipt) bool {
	return receipt.Status == WorktreeMergeLanded && receipt.Phase == WorktreeMergePhaseLand &&
		receipt.LandingSHA != "" && receipt.LandingSHA == receipt.Candidate.SHA && receipt.Validation.Status == quality.StatusFailed
}

func validateLandedFailureAcknowledgementSource(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt, source WorktreeMergeSource, allowedDescendantSHA string) error {
	return validateLandedFailureAcknowledgementSourceHead(ctx, projectsRoot, receipt, source, allowedDescendantSHA, false)
}

func validatePreservedLandedFailureAcknowledgementSource(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt, source WorktreeMergeSource) error {
	return validateLandedFailureAcknowledgementSourceHead(ctx, projectsRoot, receipt, source, "", true)
}

func validatePreservedLandedFailureAcknowledgementSourceWithHead(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt, source WorktreeMergeSource) (string, error) {
	var head string
	err := validateLandedFailureAcknowledgementSourceHead(ctx, projectsRoot, receipt, source, "", true, &head)
	return head, err
}

func validateLandedFailureAcknowledgementSourceHead(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt, source WorktreeMergeSource, allowedDescendantSHA string, allowAnyDescendant bool, validatedHead ...*string) error {
	return validateLandedFailureAcknowledgementSourceHeadWithRunner(ctx, defaultRunner, projectsRoot, receipt, source, allowedDescendantSHA, allowAnyDescendant, validatedHead...)
}

func validateLandedFailureAcknowledgementSourceHeadWithRunner(ctx context.Context, run runner.Runner, projectsRoot string, receipt WorktreeMergeReceipt, source WorktreeMergeSource, allowedDescendantSHA string, allowAnyDescendant bool, validatedHead ...*string) error {
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
	if err := requireCleanMergeWorktreeWithRunner(ctx, run, source.Worktree); err != nil {
		return fmt.Errorf("receipted source %s is not clean: %w", source.Worktree, err)
	}
	head, err := mergeRevision(ctx, run, source.Worktree, "HEAD")
	if err != nil {
		return fmt.Errorf("read receipted source %s HEAD: %w", source.Worktree, err)
	}
	if allowAnyDescendant {
		contains, ancestorErr := isMergeAncestorWithRunner(ctx, run, source.Worktree, source.SHA, head)
		if ancestorErr != nil || !contains {
			if ancestorErr == nil {
				ancestorErr = fmt.Errorf("current HEAD %s does not descend from receipted SHA %s", head, source.SHA)
			}
			return fmt.Errorf("receipted source %s was rewritten after landing: %w", source.Worktree, ancestorErr)
		}
	} else if head != source.SHA {
		if allowedDescendantSHA == "" || head != allowedDescendantSHA {
			return fmt.Errorf("receipted source %s HEAD %s does not match %s", source.Worktree, head, source.SHA)
		}
		contains, ancestorErr := isMergeAncestorWithRunner(ctx, run, source.Worktree, source.SHA, head)
		if ancestorErr != nil {
			return fmt.Errorf("verify receipted source descendant ancestry: %w", ancestorErr)
		}
		if !contains {
			return fmt.Errorf("receipted source %s HEAD %s is not a descendant of %s", source.Worktree, head, source.SHA)
		}
	}
	view, err := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: projectsRoot, Worktree: source.Worktree})
	if err != nil {
		return fmt.Errorf("load receipted source Work Log %s: %w", source.Worktree, err)
	}
	if !mergeClaimMatchesIdentity(view.Claim, receipt.Repository, source.Task, source.Worktree, source.Branch) {
		return fmt.Errorf("receipted source %s has no matching active Work Log claim", source.Worktree)
	}
	if len(validatedHead) > 0 && validatedHead[0] != nil {
		*validatedHead[0] = head
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
	return persistLandedFailureAcknowledgementInjected(path, ack, nil)
}

// persistLandedFailureAcknowledgementInjected is
// persistLandedFailureAcknowledgement's test seam (task-9 PR-4): every
// production call site reaches it only through
// persistLandedFailureAcknowledgement, which always passes a nil
// *filewrite.Injector, so production behaviour is unchanged; a test passes
// its own Injector directly to reach a create/chmod/write/sync/close/rename
// failure branch deterministically.
func persistLandedFailureAcknowledgementInjected(path string, ack WorktreeMergeLandedFailureAcknowledgement, inj *filewrite.Injector) error {
	return persistMergeAcknowledgement(path, ".landed-validation-failed-ack-*.tmp", ack, filewrite.Rename, inj)
}

func readLandedFailureAcknowledgement(path string, receipt WorktreeMergeReceipt) (WorktreeMergeLandedFailureAcknowledgement, error) {
	var ack WorktreeMergeLandedFailureAcknowledgement
	if err := readMergeAcknowledgement(path, "landed-failure acknowledgement", &ack); err != nil {
		return WorktreeMergeLandedFailureAcknowledgement{}, err
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

func conflictCandidateAdvancePath(receiptPath string) string {
	return receiptPath + worktreeMergeConflictCandidateAdvanceSuffix
}

func conflictCandidateAdvanceID(ack WorktreeMergeConflictCandidateAdvance) string {
	hash := sha256.New()
	for _, value := range []string{
		ack.ReceiptPath, ack.ReceiptSHA256, ack.ReceiptID, ack.Lane, ack.Repository,
		ack.Target, ack.ReceiptTargetSHA, ack.CurrentTargetSHA, ack.OriginalCandidate.Task,
		ack.OriginalCandidate.Worktree, ack.OriginalCandidate.Branch, ack.OriginalCandidate.SHA,
		ack.AdvancedCandidateSHA, ack.ClaimBaseSHA,
	} {
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

func persistConflictCandidateAdvance(path string, ack WorktreeMergeConflictCandidateAdvance) error {
	return persistConflictCandidateAdvanceInjected(path, ack, nil)
}

// persistConflictCandidateAdvanceInjected is
// persistConflictCandidateAdvance's test seam (task-9 PR-4): every
// production call site reaches it only through
// persistConflictCandidateAdvance, which always passes a nil
// *filewrite.Injector, so production behaviour is unchanged; a test passes
// its own Injector directly to reach a create/chmod/write/sync/close/link/
// dir-sync failure branch deterministically.
func persistConflictCandidateAdvanceInjected(path string, ack WorktreeMergeConflictCandidateAdvance, inj *filewrite.Injector) error {
	return persistMergeAcknowledgement(path, ".conflict-candidate-advance-*.tmp", ack, func(temporaryPath, path string, inj *filewrite.Injector) error {
		return publishMergeAcknowledgementAndSyncDirectory(temporaryPath, path, inj, os.Open)
	}, inj)
}

func readConflictCandidateAdvance(path string) (WorktreeMergeConflictCandidateAdvance, error) {
	var ack WorktreeMergeConflictCandidateAdvance
	if err := readMergeAcknowledgement(path, "conflict-candidate advance", &ack); err != nil {
		return ack, err
	}
	if ack.SchemaVersion != worktreeMergeConflictCandidateAdvanceSchemaVersion ||
		ack.Status != "conflict_candidate_advanced" || ack.AcknowledgementPath != path ||
		ack.ReceiptPath == "" || ack.ReceiptSHA256 == "" || ack.ReceiptID == "" || ack.Lane == "" ||
		ack.Repository == "" || ack.Target == "" || ack.ReceiptTargetSHA == "" || ack.CurrentTargetSHA == "" ||
		ack.OriginalCandidate.Task == "" || ack.OriginalCandidate.Worktree == "" || ack.OriginalCandidate.Branch == "" || ack.OriginalCandidate.SHA == "" ||
		ack.AdvancedCandidateSHA == "" || ack.AdvancedCandidateSHA == ack.OriginalCandidate.SHA || ack.ClaimBaseSHA == "" ||
		len(ack.Sources) == 0 || ack.RecordedAt.IsZero() || ack.ID != conflictCandidateAdvanceID(ack) {
		return ack, fmt.Errorf("conflict-candidate advance %s has invalid immutable identity", path)
	}
	return ack, nil
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
	return persistLegacyValidationFailureIdentityInjected(path, ack, nil)
}

// persistLegacyValidationFailureIdentityInjected is
// persistLegacyValidationFailureIdentity's test seam (task-9 PR-4): every
// production call site reaches it only through
// persistLegacyValidationFailureIdentity, which always passes a nil
// *filewrite.Injector, so production behaviour is unchanged; a test passes
// its own Injector directly to reach a create/chmod/write/sync/close/link
// failure branch deterministically.
func persistLegacyValidationFailureIdentityInjected(path string, ack WorktreeMergeLegacyValidationFailureIdentity, inj *filewrite.Injector) error {
	return persistMergeAcknowledgement(path, ".legacy-validation-failed-identity-*.tmp", ack, filewrite.LinkPath, inj)
}

func readLegacyValidationFailureIdentity(path string, receipt WorktreeMergeReceipt, candidate WorktreeMergeCandidate) (WorktreeMergeLegacyValidationFailureIdentity, error) {
	var ack WorktreeMergeLegacyValidationFailureIdentity
	if err := readMergeAcknowledgement(path, "legacy validation-failed identity", &ack); err != nil {
		return WorktreeMergeLegacyValidationFailureIdentity{}, err
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

func legacyConflictIdentityPath(receiptPath string) string {
	return receiptPath + worktreeMergeLegacyConflictIdentitySuffix
}

func legacyConflictIdentityID(ack WorktreeMergeLegacyConflictIdentity) string {
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

func sameLegacyConflictIdentity(left, right WorktreeMergeLegacyConflictIdentity) bool {
	return left.ID == right.ID && left.Status == right.Status && left.ReceiptPath == right.ReceiptPath &&
		left.AcknowledgementPath == right.AcknowledgementPath && left.ReceiptSHA256 == right.ReceiptSHA256 &&
		left.ReceiptID == right.ReceiptID && left.Lane == right.Lane && left.Repository == right.Repository &&
		left.Target == right.Target && left.ReceiptTargetSHA == right.ReceiptTargetSHA && left.CurrentTargetSHA == right.CurrentTargetSHA &&
		left.Candidate == right.Candidate && left.ClaimBaseSHA == right.ClaimBaseSHA && sameWorktreeMergeSources(left.Sources, right.Sources) &&
		left.Actor == right.Actor && left.Reason == right.Reason
}

func persistLegacyConflictIdentity(path string, ack WorktreeMergeLegacyConflictIdentity) error {
	return persistLegacyConflictIdentityInjected(path, ack, nil)
}

// persistLegacyConflictIdentityInjected is persistLegacyConflictIdentity's
// test seam (task-9 PR-4): every production call site reaches it only
// through persistLegacyConflictIdentity, which always passes a nil
// *filewrite.Injector, so production behaviour is unchanged; a test passes
// its own Injector directly to reach a create/chmod/write/sync/close/link
// failure branch deterministically.
func persistLegacyConflictIdentityInjected(path string, ack WorktreeMergeLegacyConflictIdentity, inj *filewrite.Injector) error {
	return persistMergeAcknowledgement(path, ".legacy-conflict-identity-*.tmp", ack, filewrite.LinkPath, inj)
}

func readLegacyConflictIdentity(path string, receipt WorktreeMergeReceipt, candidate WorktreeMergeCandidate) (WorktreeMergeLegacyConflictIdentity, error) {
	var ack WorktreeMergeLegacyConflictIdentity
	if err := readMergeAcknowledgement(path, "legacy conflict identity", &ack); err != nil {
		return WorktreeMergeLegacyConflictIdentity{}, err
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		return WorktreeMergeLegacyConflictIdentity{}, err
	}
	if ack.SchemaVersion != worktreeMergeLegacyConflictIdentitySchemaVersion || ack.Status != "legacy_conflict_identity_correlated" ||
		ack.AcknowledgementPath != path || ack.ReceiptPath != receipt.ReceiptPath || ack.ReceiptSHA256 != receiptHash || ack.ReceiptID != receipt.ID ||
		ack.Lane != receipt.Lane || ack.Repository != receipt.Repository || ack.Target != receipt.Target || ack.ReceiptTargetSHA != receipt.TargetSHA ||
		ack.Candidate != candidate || ack.ClaimBaseSHA == "" || ack.CurrentTargetSHA == "" || !sameWorktreeMergeSources(ack.Sources, receipt.Sources) ||
		ack.Actor == "" || ack.Reason == "" || ack.RecordedAt.IsZero() || ack.ID != legacyConflictIdentityID(ack) {
		return WorktreeMergeLegacyConflictIdentity{}, fmt.Errorf("legacy conflict identity %s has invalid immutable evidence", path)
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
	return persistMissingCleanupAcknowledgementInjected(path, ack, nil)
}

// persistMissingCleanupAcknowledgementInjected is
// persistMissingCleanupAcknowledgement's test seam (task-9 PR-4): every
// production call site reaches it only through
// persistMissingCleanupAcknowledgement, which always passes a nil
// *filewrite.Injector, so production behaviour is unchanged; a test passes
// its own Injector directly to reach a create/chmod/write/sync/close/link
// failure branch deterministically.
func persistMissingCleanupAcknowledgementInjected(path string, ack WorktreeMergeMissingCleanupAcknowledgement, inj *filewrite.Injector) error {
	return persistMergeAcknowledgement(path, ".missing-cleanup-*.tmp", ack, filewrite.LinkPath, inj)
}

func readMissingCleanupAcknowledgement(path string, receipt WorktreeMergeReceipt) (WorktreeMergeMissingCleanupAcknowledgement, error) {
	var ack WorktreeMergeMissingCleanupAcknowledgement
	if err := readMergeAcknowledgement(path, "missing-cleanup acknowledgement", &ack); err != nil {
		return ack, err
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

func persistValidationFailureSupersession(path string, ack WorktreeMergeValidationFailureSupersession) error {
	return persistValidationFailureSupersessionInjected(path, ack, nil)
}

// persistValidationFailureSupersessionInjected is
// persistValidationFailureSupersession's test seam (task-9 PR-4): every
// production call site reaches it only through
// persistValidationFailureSupersession, which always passes a nil
// *filewrite.Injector, so production behaviour is unchanged; a test passes
// its own Injector directly to reach a create/chmod/write/sync/close/rename
// failure branch deterministically.
func persistValidationFailureSupersessionInjected(path string, ack WorktreeMergeValidationFailureSupersession, inj *filewrite.Injector) error {
	return persistMergeAcknowledgement(path, ".validation-failed-supersession-*.tmp", ack, filewrite.Rename, inj)
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
	if err := decodeMergeAcknowledgement(contents, path, "validation-failed supersession", &ack); err != nil {
		return WorktreeMergeValidationFailureSupersession{}, err
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		return WorktreeMergeValidationFailureSupersession{}, err
	}
	if ack.SchemaVersion != worktreeMergeValidationFailureSupersessionSchemaVersion || ack.Status != "validation_failure_superseded" || ack.AcknowledgementPath != path ||
		ack.ReceiptPath != receipt.ReceiptPath || ack.ReceiptID != receipt.ID || ack.ReceiptSHA256 != receiptHash || ack.ReceiptStatus != receipt.Status ||
		ack.Lane != receipt.Lane || ack.Repository != receipt.Repository || ack.Target != receipt.Target || ack.ReceiptTargetSHA != receipt.TargetSHA ||
		ack.OriginalCandidate != receipt.Candidate || (ack.ObservedCandidateDescendantSHA != "" && ((receipt.Status != WorktreeMergeConflict && receipt.Status != WorktreeMergeValidationFailed) || ack.ObservedCandidateDescendantSHA == receipt.Candidate.SHA)) || ack.OriginalClaimBaseSHA == "" || ack.CurrentTargetSHA == "" || ack.Replacement.Task == "" || ack.Replacement.Worktree == "" || ack.Replacement.Branch == "" || ack.Replacement.SHA == "" || ack.ReplacementClaimBaseSHA == "" ||
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
	legacyValidation := validateLegacyValidationFailedReceiptShape(receipt, receipt.ReceiptPath) == nil
	legacyConflict := validateLegacyConflictReceiptShape(receipt, receipt.ReceiptPath) == nil
	if !legacyValidation && !legacyConflict {
		return WorktreeMergeValidationFailureSupersession{}, WorktreeMergeReceipt{}, err
	}

	identityPath := legacyValidationFailureIdentityPath(receipt.ReceiptPath)
	identityKind := "legacy validation-failed"
	if legacyConflict {
		identityPath = legacyConflictIdentityPath(receipt.ReceiptPath)
		identityKind = "legacy conflict"
	}
	contents, readErr := os.ReadFile(identityPath)
	if readErr != nil {
		// A legacy receipt with a supersession acknowledgement requires its
		// identity sidecar. Do not retain os.ErrNotExist here: the caller uses
		// that sentinel only for an absent supersession acknowledgement.
		return WorktreeMergeValidationFailureSupersession{}, WorktreeMergeReceipt{}, fmt.Errorf("read %s identity %s: %v", identityKind, identityPath, readErr)
	}
	var candidate WorktreeMergeCandidate
	if legacyValidation {
		var identity WorktreeMergeLegacyValidationFailureIdentity
		if readErr := json.Unmarshal(contents, &identity); readErr != nil {
			return WorktreeMergeValidationFailureSupersession{}, WorktreeMergeReceipt{}, fmt.Errorf("decode legacy validation-failed identity %s: %w", identityPath, readErr)
		}
		candidate = identity.Candidate
		if candidate.Task != receipt.Candidate.Task || filepath.Clean(candidate.Worktree) != filepath.Clean(receipt.Candidate.Worktree) ||
			candidate.Branch != receipt.Candidate.Branch || candidate.SHA == "" || candidate.SHA != receipt.Validation.Revision {
			return WorktreeMergeValidationFailureSupersession{}, WorktreeMergeReceipt{}, fmt.Errorf("legacy validation-failed identity %s has mismatched candidate identity", identityPath)
		}
		if _, readErr := readLegacyValidationFailureIdentity(identityPath, receipt, candidate); readErr != nil {
			return WorktreeMergeValidationFailureSupersession{}, WorktreeMergeReceipt{}, readErr
		}
	} else {
		var identity WorktreeMergeLegacyConflictIdentity
		if readErr := json.Unmarshal(contents, &identity); readErr != nil {
			return WorktreeMergeValidationFailureSupersession{}, WorktreeMergeReceipt{}, fmt.Errorf("decode legacy conflict identity %s: %w", identityPath, readErr)
		}
		candidate = identity.Candidate
		if candidate.Task != receipt.Candidate.Task || filepath.Clean(candidate.Worktree) != filepath.Clean(receipt.Candidate.Worktree) || candidate.Branch != receipt.Candidate.Branch || candidate.SHA == "" {
			return WorktreeMergeValidationFailureSupersession{}, WorktreeMergeReceipt{}, fmt.Errorf("legacy conflict identity %s has mismatched candidate identity", identityPath)
		}
		if _, readErr := readLegacyConflictIdentity(identityPath, receipt, candidate); readErr != nil {
			return WorktreeMergeValidationFailureSupersession{}, WorktreeMergeReceipt{}, readErr
		}
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
	var correction WorktreeMergeSelfSupersessionCorrection
	if err := readMergeAcknowledgement(path, "self-supersession correction", &correction); err != nil {
		return WorktreeMergeSelfSupersessionCorrection{}, err
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

// persistSelfSupersessionCorrectionInjected persists the correction with a
// test seam (task-9 PR-4): production call sites pass a nil
// *filewrite.Injector, so production behaviour is unchanged; a test passes
// its own Injector directly to reach a create/chmod/write/sync/close/link
// failure branch deterministically, and can use Injector.Hook to build a
// real concurrent-correction race at the final LinkPath call the way the
// package-level linkSelfSupersessionCorrection var this replaces used to
// let a test do by reassignment.
func persistSelfSupersessionCorrectionInjected(path string, correction WorktreeMergeSelfSupersessionCorrection, inj *filewrite.Injector) error {
	return persistMergeAcknowledgement(path, ".validation-failed-self-supersession-*.tmp", correction, filewrite.LinkPath, inj)
}
