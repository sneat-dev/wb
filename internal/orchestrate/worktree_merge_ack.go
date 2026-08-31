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
	worktreeMergeLandedFailureAcknowledgementSchemaVersion = 1
	worktreeMergeLandedFailureAcknowledgementSuffix        = ".landed-validation-failed.ack.json"
)

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
		view.Claim.Base != receipt.Target || view.Claim.BaseSHA != receipt.TargetSHA {
		return WorktreeMergeLandedFailureAcknowledgement{}, errors.New("candidate has no active Work Log claim matching the immutable receipt base and identity")
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
