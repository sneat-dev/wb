package orchestrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

const (
	worktreeMergeUnpublishedValidationFailureAcknowledgementSchemaVersion = 1
	worktreeMergeUnpublishedValidationFailureAcknowledgementSuffix        = ".unpublished-validation-failure.ack.json"
)

// WorktreeMergeUnpublishedValidationFailureAcknowledgement retires one
// unpublished prepare attempt without discarding its source work. The
// historical receipt and Work Logs remain immutable; the sidecar records fresh
// proof that the candidate never reached the remote target and that every exact
// receipted source remains clean and actively claimed. An interrupted preparing
// attempt, or a validated prepared attempt that was never landed, is accepted
// only when WB's private cleanup backlog proves its exact candidate was
// deliberately discarded.
type WorktreeMergeUnpublishedValidationFailureAcknowledgement struct {
	SchemaVersion           int                               `json:"schema_version"`
	ID                      string                            `json:"id"`
	Status                  string                            `json:"status"`
	ReceiptPath             string                            `json:"receipt_path"`
	AcknowledgementPath     string                            `json:"acknowledgement_path"`
	ReceiptID               string                            `json:"receipt_id"`
	ReceiptSHA256           string                            `json:"receipt_sha256"`
	Lane                    string                            `json:"lane"`
	Repository              string                            `json:"repository"`
	Target                  string                            `json:"target"`
	ReceiptTargetSHA        string                            `json:"receipt_target_sha"`
	CurrentTargetSHA        string                            `json:"current_target_sha"`
	Candidate               WorktreeMergeCandidate            `json:"candidate"`
	CandidateCleanupBacklog string                            `json:"candidate_cleanup_backlog,omitempty"`
	Sources                 []WorktreeMergeSource             `json:"sources"`
	PreservedSources        []WorktreeMergeSource             `json:"preserved_sources,omitempty"`
	LandedSources           []WorktreeMergeRetiredSourceProof `json:"landed_sources,omitempty"`
	Actor                   string                            `json:"actor"`
	Reason                  string                            `json:"reason"`
	RecordedAt              time.Time                         `json:"recorded_at"`
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
	return acknowledgeUnpublishedValidationFailure(ctx, options, readWorktreeMergeReceipt, defaultRunner, worktreeMergeReceiptSHA256, persistUnpublishedValidationFailureAcknowledgement, worktrees.CanonicalRepositoryPath)
}

func acknowledgeUnpublishedValidationFailure(ctx context.Context, options WorktreeMergeUnpublishedValidationFailureAcknowledgementOptions, readReceipt func(string) (WorktreeMergeReceipt, error), run runner.Runner, hash func(string) (string, error), persist func(string, WorktreeMergeUnpublishedValidationFailureAcknowledgement) error, resolveCanonical func(string, string) (string, error)) (WorktreeMergeUnpublishedValidationFailureAcknowledgement, error) {
	receiptPath, err := resolveWorktreeMergeReceiptPath(options.ProjectsRoot, options.Receipt)
	if err != nil {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, err
	}
	receipt, err := readReceipt(receiptPath)
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

	receipt, err = readReceipt(receiptPath)
	if err != nil {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, err
	}
	if err := validateUnpublishedValidationFailureReceipt(receipt, receiptPath); err != nil {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, err
	}
	gitRoot := receipt.Candidate.Worktree
	candidateCleanupBacklog := ""
	if receipt.Phase == WorktreeMergePhaseLand {
		gitRoot, err = requireRetiredUnpublishedLandCandidate(ctx, options.ProjectsRoot, receipt, run, resolveCanonical)
		if err != nil {
			return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, err
		}
	} else if discardedCandidateRequired(receipt) {
		proof, proofErr := worktrees.FindDiscardedLifecycleBacklogProof(ctx, options.ProjectsRoot, receipt.Repository, receipt.Target,
			receipt.Candidate.Task, receipt.Candidate.Worktree, receipt.Candidate.Branch, receipt.Candidate.SHA)
		if proofErr != nil {
			return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("prove discarded candidate: %w", proofErr)
		}
		candidateCleanupBacklog = proof.Path
		resolvedRoot, canonicalErr := resolveCanonical(options.ProjectsRoot, receipt.Repository)
		if canonicalErr != nil {
			return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, canonicalErr
		}
		gitRoot = resolvedRoot
	} else {
		guard, guardErr := worktrees.Guard(ctx, receipt.Candidate.Worktree, worktrees.GuardOptions{ProjectsRoot: options.ProjectsRoot, Base: receipt.Target})
		if guardErr != nil {
			return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("guard candidate worktree: %w", guardErr)
		}
		if guard.Kind != "linked" || guard.Branch != receipt.Candidate.Branch || filepath.Clean(guard.Path) != filepath.Clean(receipt.Candidate.Worktree) {
			return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("candidate worktree %s no longer has its exact linked-worktree identity", receipt.Candidate.Worktree)
		}
		if err := requireCleanMergeWorktreeWithRunner(ctx, run, receipt.Candidate.Worktree); err != nil {
			return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("candidate worktree is not clean: %w", err)
		}
		if head, headErr := mergeRevision(ctx, run, receipt.Candidate.Worktree, "HEAD"); headErr != nil || head != receipt.Candidate.SHA {
			if headErr != nil {
				return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("read candidate HEAD: %w", headErr)
			}
			return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("candidate HEAD %s does not match receipted SHA %s", head, receipt.Candidate.SHA)
		}
	}
	preservedSources := make([]WorktreeMergeSource, 0, len(receipt.Sources))
	var landedSources []WorktreeMergeRetiredSourceProof
	for _, source := range receipt.Sources {
		if _, statErr := os.Lstat(source.Worktree); receipt.Phase == WorktreeMergePhaseLand && errors.Is(statErr, os.ErrNotExist) {
			proof, proofErr := proveRetiredUnpublishedFailureSource(ctx, options.ProjectsRoot, receipt, source, gitRoot, run)
			if proofErr != nil {
				return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("prove independently landed source: %w", proofErr)
			}
			landedSources = append(landedSources, proof)
			continue
		}
		preservedSHA, validationErr := validatePreservedLandedFailureAcknowledgementSourceWithHead(ctx, options.ProjectsRoot, receipt, source)
		if validationErr != nil {
			return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("prove preserved source: %w", validationErr)
		}
		preserved := source
		preserved.SHA = preservedSHA
		preservedSources = append(preservedSources, preserved)
	}
	remote, _, err := runCommand(ctx, run, 0, 0, gitRoot, "git", "ls-remote", "--heads", "origin", "refs/heads/"+receipt.Candidate.Branch)
	if err != nil {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("inspect candidate publication state: %w", err)
	}
	if strings.TrimSpace(remote) != "" {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("candidate branch %s is published; use a published-candidate recovery", receipt.Candidate.Branch)
	}
	currentTarget, err := fetchExactMergeTargetWithRunner(ctx, run, gitRoot, receipt.Target)
	if err != nil {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, err
	}
	landed, err := isMergeAncestorWithRunner(ctx, run, gitRoot, receipt.Candidate.SHA, currentTarget)
	if err != nil {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("verify candidate against current target: %w", err)
	}
	if landed {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("candidate %s is already reachable from current target %s; use acknowledge-landed-failed", receipt.Candidate.SHA, currentTarget)
	}
	for _, proof := range landedSources {
		if proof.CurrentTargetSHA != currentTarget {
			return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, errors.New("target changed during independently landed source proof")
		}
	}
	receiptHash, err := hash(receiptPath)
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
		CandidateCleanupBacklog: candidateCleanupBacklog,
		Sources:                 append([]WorktreeMergeSource(nil), receipt.Sources...),
		PreservedSources:        preservedSources,
		LandedSources:           landedSources,
		Actor:                   strings.TrimSpace(options.Actor), Reason: strings.TrimSpace(options.Reason), RecordedAt: time.Now().UTC(),
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
	for _, proof := range landedSources {
		current, proofErr := proveRetiredUnpublishedFailureSource(ctx, options.ProjectsRoot, receipt, proof.Source, gitRoot, run)
		if proofErr != nil || !sameRetiredSourceProofs([]WorktreeMergeRetiredSourceProof{proof}, []WorktreeMergeRetiredSourceProof{current}) {
			return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("independently landed source evidence changed before acknowledgement: %v", proofErr)
		}
	}
	if err := persist(ack.AcknowledgementPath, ack); err != nil {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, err
	}
	return ack, nil
}

// A failed land call can have sealed and retired its unpublished candidate.
// Authenticate that exact removal before using the canonical repository to
// observe publication and target state; empty receipt fields are not proof.
func requireRetiredUnpublishedLandCandidate(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt, run runner.Runner, resolveCanonical func(string, string) (string, error)) (string, error) {
	if _, err := os.Lstat(receipt.Candidate.Worktree); !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("retired candidate checkout must be absent: %v", err)
	}
	expectation := worktrees.TerminalWorkLogExpectation{Task: receipt.Candidate.Task, Repository: receipt.Repository,
		Worktree: receipt.Candidate.Worktree, Branch: receipt.Candidate.Branch, FinalCommit: receipt.Candidate.SHA}
	if err := worktrees.ValidateRemovedTerminalWorkLogs(projectsRoot, []worktrees.TerminalWorkLogExpectation{expectation}); err != nil {
		return "", fmt.Errorf("authenticate retired candidate terminal: %w", err)
	}
	canonical, err := resolveCanonical(projectsRoot, receipt.Repository)
	if err != nil {
		return "", err
	}
	registered, _, err := runCommand(ctx, run, 0, 0, canonical, "git", "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return "", fmt.Errorf("inspect retired candidate registration: %w", err)
	}
	for _, field := range strings.Split(registered, "\x00") {
		if strings.HasPrefix(field, "worktree ") && filepath.Clean(strings.TrimPrefix(field, "worktree ")) == filepath.Clean(receipt.Candidate.Worktree) {
			return "", errors.New("retired candidate checkout remains registered")
		}
	}
	branch, _, err := runCommand(ctx, run, 0, 0, canonical, "git", "for-each-ref", "--format=%(refname)", "refs/heads/"+receipt.Candidate.Branch)
	if err != nil {
		return "", fmt.Errorf("inspect retired candidate local branch: %w", err)
	}
	if strings.TrimSpace(branch) != "" {
		return "", errors.New("retired candidate branch still exists locally")
	}
	if err := requireZeroDeltaCandidateWithoutPullRequest(ctx, canonical, receipt); err != nil {
		return "", err
	}
	return canonical, nil
}

func validateUnpublishedValidationFailureReceipt(receipt WorktreeMergeReceipt, receiptPath string) error {
	validPhase := receipt.Phase == WorktreeMergePhasePrepare || receipt.Phase == WorktreeMergePhaseLand && receipt.Status == WorktreeMergeValidationFailed
	if receipt.ReceiptPath != receiptPath || receipt.ID == "" || receipt.Lane == "" || receipt.Lane != worktreeMergeLaneID(receipt.Repository, receipt.Target) ||
		!validPhase || (receipt.Status != WorktreeMergeValidationFailed && !discardedCandidateRequired(receipt)) || receipt.LandingSHA != "" || receipt.PullRequest != "" || receipt.PublishedCandidateSHA != "" ||
		receipt.Repository == "" || receipt.Target == "" || receipt.TargetSHA == "" || receipt.Candidate.Task == "" || receipt.Candidate.Worktree == "" || receipt.Candidate.Branch == "" || receipt.Candidate.SHA == "" ||
		!worktreeMergeOperationIDMatchesRecordedSourceSet(receipt) || receipt.Candidate.Task != receipt.ID || len(receipt.Sources) == 0 {
		return fmt.Errorf("receipt %s is not an exact unpublished prepare or failed-land receipt", receiptPath)
	}
	for _, source := range receipt.Sources {
		if source.Task == "" || source.Worktree == "" || source.Branch == "" || source.SHA == "" {
			return fmt.Errorf("receipt %s has an incomplete immutable source identity", receiptPath)
		}
	}
	return nil
}

// discardedCandidateRequired reports whether a prepare receipt can be retired
// only by proving its candidate was discarded to completion: an interrupted
// preparing attempt, or a prepared attempt whose validated candidate was
// abandoned before landing. A validation_failed candidate is instead retained.
func discardedCandidateRequired(receipt WorktreeMergeReceipt) bool {
	return receipt.Phase == WorktreeMergePhasePrepare && (receipt.Status == WorktreeMergePreparing || receipt.Status == WorktreeMergePrepared)
}

func unpublishedValidationFailureAcknowledgementPath(receiptPath string) string {
	return receiptPath + worktreeMergeUnpublishedValidationFailureAcknowledgementSuffix
}

func unpublishedValidationFailureAcknowledgementID(ack WorktreeMergeUnpublishedValidationFailureAcknowledgement) string {
	hash := sha256.New()
	for _, value := range []string{ack.ReceiptID, ack.ReceiptPath, ack.ReceiptSHA256, ack.Lane, ack.Repository, ack.Target, ack.ReceiptTargetSHA, ack.CurrentTargetSHA,
		ack.Candidate.Task, ack.Candidate.Worktree, ack.Candidate.Branch, ack.Candidate.SHA, ack.CandidateCleanupBacklog, ack.Actor, ack.Reason} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	for _, source := range ack.Sources {
		for _, value := range []string{source.Task, source.Worktree, source.Branch, source.SHA} {
			_, _ = hash.Write([]byte(value))
			_, _ = hash.Write([]byte{0})
		}
	}
	for _, source := range ack.PreservedSources {
		for _, value := range []string{source.Task, source.Worktree, source.Branch, source.SHA} {
			_, _ = hash.Write([]byte(value))
			_, _ = hash.Write([]byte{0})
		}
	}
	for _, proof := range ack.LandedSources {
		for _, value := range []string{proof.Source.Task, proof.Source.Worktree, proof.Source.Branch, proof.Source.SHA, proof.ClaimBaseSHA, proof.CleanupReport, proof.CleanupSHA256, proof.PullRequest, proof.LandingSHA, proof.CurrentTargetSHA, proof.Kind, proof.TreeSHA} {
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
		left.CandidateCleanupBacklog == right.CandidateCleanupBacklog &&
		sameWorktreeMergeSources(left.Sources, right.Sources) && sameWorktreeMergeSources(left.PreservedSources, right.PreservedSources) && sameRetiredSourceProofs(left.LandedSources, right.LandedSources)
}

func persistUnpublishedValidationFailureAcknowledgement(path string, ack WorktreeMergeUnpublishedValidationFailureAcknowledgement) error {
	return persistUnpublishedValidationFailureAcknowledgementInjected(path, ack, nil)
}

// persistUnpublishedValidationFailureAcknowledgementInjected is
// persistUnpublishedValidationFailureAcknowledgement's test seam (task-9
// PR-4): every production call site reaches it only through
// persistUnpublishedValidationFailureAcknowledgement, which always passes a
// nil *filewrite.Injector, so production behaviour is unchanged; a test
// passes its own Injector directly to reach a
// create/chmod/write/sync/close/rename failure branch deterministically.
func persistUnpublishedValidationFailureAcknowledgementInjected(path string, ack WorktreeMergeUnpublishedValidationFailureAcknowledgement, inj *filewrite.Injector) error {
	return persistMergeAcknowledgement(path, ".unpublished-validation-failure-ack-*.tmp", ack, filewrite.Rename, inj)
}

func readUnpublishedValidationFailureAcknowledgement(path string, receipt WorktreeMergeReceipt) (WorktreeMergeUnpublishedValidationFailureAcknowledgement, error) {
	var ack WorktreeMergeUnpublishedValidationFailureAcknowledgement
	if err := readMergeAcknowledgement(path, "unpublished-validation-failure acknowledgement", &ack); err != nil {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, err
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
	if discardedCandidateRequired(receipt) && ack.CandidateCleanupBacklog == "" {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("unpublished-validation-failure acknowledgement %s lacks discarded candidate cleanup evidence", path)
	}
	if err := validateUnpublishedSourceProofCoverage(ack, receipt); err != nil {
		return WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, fmt.Errorf("unpublished-validation-failure acknowledgement %s lacks preserved descendant source evidence", path)
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
