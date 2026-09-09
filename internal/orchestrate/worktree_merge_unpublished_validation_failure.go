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
	worktreeMergeUnpublishedValidationFailureAcknowledgementSchemaVersion = 1
	worktreeMergeUnpublishedValidationFailureAcknowledgementSuffix        = ".unpublished-validation-failure.ack.json"
)

// WorktreeMergeUnpublishedValidationFailureAcknowledgement retires one
// unpublished prepare/validation_failed attempt without discarding its source
// work. The historical receipt and Work Logs remain immutable; the sidecar
// records fresh proof that the candidate never reached the remote target and
// that every exact receipted source remains clean and actively claimed.
type WorktreeMergeUnpublishedValidationFailureAcknowledgement struct {
	SchemaVersion       int                    `json:"schema_version"`
	ID                  string                 `json:"id"`
	Status              string                 `json:"status"`
	ReceiptPath         string                 `json:"receipt_path"`
	AcknowledgementPath string                 `json:"acknowledgement_path"`
	ReceiptID           string                 `json:"receipt_id"`
	ReceiptSHA256       string                 `json:"receipt_sha256"`
	Lane                string                 `json:"lane"`
	Repository          string                 `json:"repository"`
	Target              string                 `json:"target"`
	ReceiptTargetSHA    string                 `json:"receipt_target_sha"`
	CurrentTargetSHA    string                 `json:"current_target_sha"`
	Candidate           WorktreeMergeCandidate `json:"candidate"`
	Sources             []WorktreeMergeSource  `json:"sources"`
	Actor               string                 `json:"actor"`
	Reason              string                 `json:"reason"`
	RecordedAt          time.Time              `json:"recorded_at"`
}

type WorktreeMergeUnpublishedValidationFailureAcknowledgementOptions struct {
	ProjectsRoot string
	Receipt      string
	Apply        bool
	Actor        string
	Reason       string
}

// AcknowledgeUnpublishedValidationFailure proves that a prepare-time
// validation failure was never published or landed and that all receipted
// source work is still preserved. It is a dry-run unless Apply is true.
func AcknowledgeUnpublishedValidationFailure(ctx context.Context, options WorktreeMergeUnpublishedValidationFailureAcknowledgementOptions) (WorktreeMergeUnpublishedValidationFailureAcknowledgement, error) {
	receiptPath, err := resolveWorktreeMergeReceiptPath(options.ProjectsRoot, options.Receipt)
	if err != nil {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, err
	}
	receipt, err := readWorktreeMergeReceipt(receiptPath)
	if err != nil {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, err
	}
	if err := validateUnpublishedValidationFailureReceipt(receipt, receiptPath); err != nil {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, err
	}
	if options.Apply && (strings.TrimSpace(options.Actor) == "" || strings.TrimSpace(options.Reason) == "") {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, errors.New("--actor and --reason are required with --apply")
	}

	lock, err := AcquireOperationLock(options.ProjectsRoot, receipt.Lane, true)
	if err != nil {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, err
	}
	defer func() { _ = lock.Release() }()

	receipt, err = readWorktreeMergeReceipt(receiptPath)
	if err != nil {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, err
	}
	if err := validateUnpublishedValidationFailureReceipt(receipt, receiptPath); err != nil {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, err
	}
	guard, err := worktrees.Guard(ctx, receipt.Candidate.Worktree, worktrees.GuardOptions{ProjectsRoot: options.ProjectsRoot, Base: receipt.Target})
	if err != nil {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("guard candidate worktree: %w", err)
	}
	if guard.Kind != "linked" || guard.Branch != receipt.Candidate.Branch || filepath.Clean(guard.Path) != filepath.Clean(receipt.Candidate.Worktree) {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("candidate worktree %s no longer has its exact linked-worktree identity", receipt.Candidate.Worktree)
	}
	if err := requireCleanMergeWorktree(ctx, receipt.Candidate.Worktree); err != nil {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("candidate worktree is not clean: %w", err)
	}
	if head, headErr := mergeRevision(ctx, receipt.Candidate.Worktree, "HEAD"); headErr != nil || head != receipt.Candidate.SHA {
		if headErr != nil {
			return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("read candidate HEAD: %w", headErr)
		}
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("candidate HEAD %s does not match receipted SHA %s", head, receipt.Candidate.SHA)
	}
	for _, source := range receipt.Sources {
		if err := validateLandedFailureAcknowledgementSource(ctx, options.ProjectsRoot, receipt, source, ""); err != nil {
			return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("prove preserved source: %w", err)
		}
	}
	remote, _, err := runCommand(ctx, 0, 0, receipt.Candidate.Worktree, "git", "ls-remote", "--heads", "origin", "refs/heads/"+receipt.Candidate.Branch)
	if err != nil {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("inspect candidate publication state: %w", err)
	}
	if strings.TrimSpace(remote) != "" {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("candidate branch %s is published; use a published-candidate recovery", receipt.Candidate.Branch)
	}
	currentTarget, err := fetchExactMergeTarget(ctx, receipt.Candidate.Worktree, receipt.Target)
	if err != nil {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, err
	}
	landed, err := isMergeAncestor(ctx, receipt.Candidate.Worktree, receipt.Candidate.SHA, currentTarget)
	if err != nil {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("verify candidate against current target: %w", err)
	}
	if landed {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("candidate %s is already reachable from current target %s; use acknowledge-landed-failed", receipt.Candidate.SHA, currentTarget)
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receiptPath)
	if err != nil {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, err
	}
	ack := WorktreeMergeUnpublishedValidationFailureAcknowledgement{
		SchemaVersion: worktreeMergeUnpublishedValidationFailureAcknowledgementSchemaVersion,
		Status:        "unpublished_validation_failure_acknowledged", ReceiptPath: receiptPath,
		AcknowledgementPath: unpublishedValidationFailureAcknowledgementPath(receiptPath),
		ReceiptID:           receipt.ID, ReceiptSHA256: receiptHash, Lane: receipt.Lane,
		Repository: receipt.Repository, Target: receipt.Target, ReceiptTargetSHA: receipt.TargetSHA,
		CurrentTargetSHA: currentTarget, Candidate: receipt.Candidate,
		Sources: append([]WorktreeMergeSource(nil), receipt.Sources...),
		Actor:   strings.TrimSpace(options.Actor), Reason: strings.TrimSpace(options.Reason), RecordedAt: time.Now().UTC(),
	}
	ack.ID = unpublishedValidationFailureAcknowledgementID(ack)
	if existing, readErr := readUnpublishedValidationFailureAcknowledgement(ack.AcknowledgementPath, receipt); readErr == nil {
		if !sameUnpublishedValidationFailureAcknowledgement(existing, ack) {
			return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("unpublished-validation-failure acknowledgement %s binds different immutable evidence", ack.AcknowledgementPath)
		}
		return existing, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, readErr
	}
	if !options.Apply {
		return ack, nil
	}
	if err := persistUnpublishedValidationFailureAcknowledgement(ack.AcknowledgementPath, ack); err != nil {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, err
	}
	return ack, nil
}

func validateUnpublishedValidationFailureReceipt(receipt WorktreeMergeReceipt, receiptPath string) error {
	if receipt.ReceiptPath != receiptPath || receipt.ID == "" || receipt.Lane == "" || receipt.Lane != worktreeMergeLaneID(receipt.Repository, receipt.Target) ||
		receipt.Phase != WorktreeMergePhasePrepare || receipt.Status != WorktreeMergeValidationFailed || receipt.LandingSHA != "" || receipt.PullRequest != "" || receipt.PublishedCandidateSHA != "" ||
		receipt.Repository == "" || receipt.Target == "" || receipt.TargetSHA == "" || receipt.Candidate.Task == "" || receipt.Candidate.Worktree == "" || receipt.Candidate.Branch == "" || receipt.Candidate.SHA == "" ||
		!worktreeMergeOperationIDMatchesRecordedSourceSet(receipt) || receipt.Candidate.Task != receipt.ID || len(receipt.Sources) == 0 {
		return fmt.Errorf("receipt %s is not an exact unpublished prepare/validation_failed receipt", receiptPath)
	}
	for _, source := range receipt.Sources {
		if source.Task == "" || source.Worktree == "" || source.Branch == "" || source.SHA == "" {
			return fmt.Errorf("receipt %s has an incomplete immutable source identity", receiptPath)
		}
	}
	return nil
}

func unpublishedValidationFailureAcknowledgementPath(receiptPath string) string {
	return receiptPath + worktreeMergeUnpublishedValidationFailureAcknowledgementSuffix
}

func unpublishedValidationFailureAcknowledgementID(ack WorktreeMergeUnpublishedValidationFailureAcknowledgement) string {
	hash := sha256.New()
	for _, value := range []string{ack.ReceiptID, ack.ReceiptPath, ack.ReceiptSHA256, ack.Lane, ack.Repository, ack.Target, ack.ReceiptTargetSHA, ack.CurrentTargetSHA,
		ack.Candidate.Task, ack.Candidate.Worktree, ack.Candidate.Branch, ack.Candidate.SHA, ack.Actor, ack.Reason} {
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

func sameUnpublishedValidationFailureAcknowledgement(left, right WorktreeMergeUnpublishedValidationFailureAcknowledgement) bool {
	return left.ReceiptPath == right.ReceiptPath && left.AcknowledgementPath == right.AcknowledgementPath && left.ReceiptID == right.ReceiptID &&
		left.ReceiptSHA256 == right.ReceiptSHA256 && left.Lane == right.Lane && left.Repository == right.Repository && left.Target == right.Target &&
		left.ReceiptTargetSHA == right.ReceiptTargetSHA && left.CurrentTargetSHA == right.CurrentTargetSHA && left.Candidate == right.Candidate &&
		sameWorktreeMergeSources(left.Sources, right.Sources)
}

func persistUnpublishedValidationFailureAcknowledgement(path string, ack WorktreeMergeUnpublishedValidationFailureAcknowledgement) error {
	contents, err := json.MarshalIndent(ack, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".unpublished-validation-failure-ack-*.tmp")
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

func readUnpublishedValidationFailureAcknowledgement(path string, receipt WorktreeMergeReceipt) (WorktreeMergeUnpublishedValidationFailureAcknowledgement, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, err
	}
	var ack WorktreeMergeUnpublishedValidationFailureAcknowledgement
	if err := json.Unmarshal(contents, &ack); err != nil {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("decode unpublished-validation-failure acknowledgement %s: %w", path, err)
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, err
	}
	if ack.SchemaVersion != worktreeMergeUnpublishedValidationFailureAcknowledgementSchemaVersion || ack.Status != "unpublished_validation_failure_acknowledged" ||
		ack.AcknowledgementPath != path || ack.ReceiptPath != receipt.ReceiptPath || ack.ReceiptID != receipt.ID || ack.ReceiptSHA256 != receiptHash ||
		ack.Lane != receipt.Lane || ack.Repository != receipt.Repository || ack.Target != receipt.Target || ack.ReceiptTargetSHA != receipt.TargetSHA ||
		ack.Candidate != receipt.Candidate || !sameWorktreeMergeSources(ack.Sources, receipt.Sources) || ack.CurrentTargetSHA == "" || ack.Actor == "" || ack.Reason == "" || ack.RecordedAt.IsZero() || ack.ID != unpublishedValidationFailureAcknowledgementID(ack) {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("unpublished-validation-failure acknowledgement %s has invalid immutable identity", path)
	}
	return ack, nil
}

func hasUnpublishedValidationFailureAcknowledgement(receipt WorktreeMergeReceipt) (bool, error) {
	_, err := readUnpublishedValidationFailureAcknowledgement(unpublishedValidationFailureAcknowledgementPath(receipt.ReceiptPath), receipt)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
