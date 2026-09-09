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
)

const (
	worktreeMergeRetiredPublicationAcknowledgementSchemaVersion = 1
	worktreeMergeRetiredPublicationAcknowledgementSuffix        = ".retired-publication.ack.json"
)

// WorktreeMergeRetiredPublicationAcknowledgement is a separate, append-only
// acknowledgement for a prepare- or land-phase conflict/validation_failed/
// checks_failed receipt whose exact published pull request was later retired
// -- closed without ever merging, and its remote candidate branch deleted --
// after the receipted target advanced past the candidate, leaving WB's own
// "refusing to rewrite the published branch without force-push" refusal (or
// an equivalent validation/checks failure) as a dead end: resume repeats the
// same refusal forever, and every other conflict-recovery verb requires an
// unpublished receipt. It proves, using a fresh read of GitHub's own PR state
// and a freshly fetched current remote target, that the publication is
// genuinely gone and nothing from it landed, then records a separate audited
// acknowledgement so a fresh candidate can own the lane. It never rewrites
// the historical receipt or any Work Log, and never deletes the preserved
// candidate worktree.
type WorktreeMergeRetiredPublicationAcknowledgement struct {
	SchemaVersion         int                   `json:"schema_version"`
	ID                    string                `json:"id"`
	Status                string                `json:"status"`
	ReceiptPath           string                `json:"receipt_path"`
	AcknowledgementPath   string                `json:"acknowledgement_path"`
	ReceiptID             string                `json:"receipt_id"`
	ReceiptSHA256         string                `json:"receipt_sha256"`
	ReceiptPhase          WorktreeMergePhase    `json:"receipt_phase"`
	ReceiptStatus         WorktreeMergeStatus   `json:"receipt_status"`
	Lane                  string                `json:"lane"`
	Repository            string                `json:"repository"`
	Target                string                `json:"target"`
	ReceiptTargetSHA      string                `json:"receipt_target_sha"`
	CurrentTargetSHA      string                `json:"current_target_sha"`
	CandidateTask         string                `json:"candidate_task"`
	CandidateWorktree     string                `json:"candidate_worktree"`
	CandidateBranch       string                `json:"candidate_branch"`
	CandidateSHA          string                `json:"candidate_sha,omitempty"`
	PublishedCandidateSHA string                `json:"published_candidate_sha,omitempty"`
	PullRequest           string                `json:"pull_request"`
	PullRequestState      string                `json:"pull_request_state"`
	PullRequestClosedAt   string                `json:"pull_request_closed_at,omitempty"`
	PullRequestHeadSHA    string                `json:"pull_request_head_sha"`
	Sources               []WorktreeMergeSource `json:"sources"`
	Actor                 string                `json:"actor"`
	Reason                string                `json:"reason"`
	RecordedAt            time.Time             `json:"recorded_at"`
}

// WorktreeMergeRetiredPublicationAcknowledgementOptions configures
// AcknowledgeRetiredPublication.
type WorktreeMergeRetiredPublicationAcknowledgementOptions struct {
	ProjectsRoot string
	Receipt      string
	Apply        bool
	Actor        string
	Reason       string
}

// AcknowledgeRetiredPublication proves, from a fresh read of GitHub's own
// pull-request state and a freshly fetched current remote target, that a
// conflict/validation_failed/checks_failed receipt's exact published pull
// request was closed without ever merging, that its candidate branch carries
// no remote ref any more, and that neither its published candidate nor its
// preserved candidate landed on the target -- then records a separate
// audited acknowledgement so a fresh candidate can own the merger lane. It
// never rewrites the historical receipt or any Work Log, and never deletes
// the preserved candidate worktree (which this proof requires, for read-only
// git object resolution against the candidate's own remote). This is a
// dry-run by default; --apply requires --actor and --reason and writes only
// the new acknowledgement artifact.
func AcknowledgeRetiredPublication(ctx context.Context, options WorktreeMergeRetiredPublicationAcknowledgementOptions) (WorktreeMergeRetiredPublicationAcknowledgement, error) {
	receiptPath, err := resolveWorktreeMergeReceiptPath(options.ProjectsRoot, options.Receipt)
	if err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	receipt, err := readWorktreeMergeReceipt(receiptPath)
	if err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	if err := validateRetiredPublicationReceipt(receipt, receiptPath); err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	if options.Apply && (strings.TrimSpace(options.Actor) == "" || strings.TrimSpace(options.Reason) == "") {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, errors.New("--actor and --reason are required with --apply")
	}
	lockID := receipt.Lane
	if lockID == "" {
		lockID = worktreeMergeLaneID(receipt.Repository, receipt.Target)
	}
	lock, err := AcquireOperationLock(options.ProjectsRoot, lockID, true)
	if err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	defer func() { _ = lock.Release() }()

	// Re-read and re-validate beneath the lane lock: every proof below must
	// run against evidence observed while this acknowledgement exclusively
	// owns the lane, never against values captured before it.
	receipt, err = readWorktreeMergeReceipt(receiptPath)
	if err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	if err := validateRetiredPublicationReceipt(receipt, receiptPath); err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	candidateInfo, statErr := os.Stat(receipt.Candidate.Worktree)
	if statErr != nil || !candidateInfo.IsDir() {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, fmt.Errorf("candidate worktree %s is required for read-only git object resolution and is missing: %v", receipt.Candidate.Worktree, statErr)
	}

	view, err := proveRetiredPullRequest(ctx, receipt)
	if err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	remote, _, err := runCommand(ctx, 0, 0, receipt.Candidate.Worktree, "git", "ls-remote", "--heads", "origin", "refs/heads/"+receipt.Candidate.Branch)
	if err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, fmt.Errorf("inspect candidate publication state: %w", err)
	}
	if strings.TrimSpace(remote) != "" {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, fmt.Errorf("candidate branch %s still carries a remote ref; this recovery requires it to be gone", receipt.Candidate.Branch)
	}
	currentTarget, err := fetchExactMergeTarget(ctx, receipt.Candidate.Worktree, receipt.Target)
	if err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	for _, sha := range []string{receipt.PublishedCandidateSHA, receipt.Candidate.SHA} {
		if sha == "" {
			continue
		}
		landed, ancestorErr := isMergeAncestor(ctx, receipt.Candidate.Worktree, sha, currentTarget)
		if ancestorErr != nil {
			return WorktreeMergeRetiredPublicationAcknowledgement{}, fmt.Errorf("verify candidate %s against current target %s: %w", sha, currentTarget, ancestorErr)
		}
		if landed {
			return WorktreeMergeRetiredPublicationAcknowledgement{}, fmt.Errorf("candidate %s is already reachable from current target %s; the retired publication still landed, use acknowledge-stranded-landing or resume instead", sha, currentTarget)
		}
	}

	receiptHash, err := worktreeMergeReceiptSHA256(receiptPath)
	if err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	ackPath := retiredPublicationAcknowledgementPath(receiptPath)
	ack := WorktreeMergeRetiredPublicationAcknowledgement{
		SchemaVersion: worktreeMergeRetiredPublicationAcknowledgementSchemaVersion, Status: "retired_publication_acknowledged",
		ReceiptPath: receiptPath, AcknowledgementPath: ackPath,
		ReceiptID: receipt.ID, ReceiptSHA256: receiptHash, ReceiptPhase: receipt.Phase, ReceiptStatus: receipt.Status, Lane: receipt.Lane,
		Repository: receipt.Repository, Target: receipt.Target, ReceiptTargetSHA: receipt.TargetSHA, CurrentTargetSHA: currentTarget,
		CandidateTask: receipt.Candidate.Task, CandidateWorktree: receipt.Candidate.Worktree, CandidateBranch: receipt.Candidate.Branch,
		CandidateSHA: receipt.Candidate.SHA, PublishedCandidateSHA: receipt.PublishedCandidateSHA,
		PullRequest: receipt.PullRequest, PullRequestState: view.State, PullRequestClosedAt: view.ClosedAt, PullRequestHeadSHA: view.HeadRefOID,
		Sources: append([]WorktreeMergeSource(nil), receipt.Sources...),
		Actor:   strings.TrimSpace(options.Actor), Reason: strings.TrimSpace(options.Reason), RecordedAt: time.Now().UTC(),
	}
	ack.ID = retiredPublicationAcknowledgementID(ack)
	if existing, readErr := readRetiredPublicationAcknowledgement(ackPath, receipt); readErr == nil {
		if !sameRetiredPublicationAcknowledgement(existing, ack) {
			return WorktreeMergeRetiredPublicationAcknowledgement{}, fmt.Errorf("retired-publication acknowledgement %s binds different immutable evidence", ackPath)
		}
		return existing, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, readErr
	}
	if !options.Apply {
		return ack, nil
	}
	if err := persistRetiredPublicationAcknowledgement(ackPath, ack); err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	return ack, nil
}

// validateRetiredPublicationReceipt narrowly scopes eligibility to a receipt
// that once published a candidate and then failed non-terminally without
// ever landing: prepare or land phase, conflict/validation_failed/
// checks_failed status, no recorded landing SHA, and an exact published pull
// request. A receipt that never published anything is
// acknowledge-absorbed-conflict's or supersede-validation-failed's territory
// instead; one that recorded a landing SHA belongs to
// acknowledge-landed-failed.
func validateRetiredPublicationReceipt(receipt WorktreeMergeReceipt, receiptPath string) error {
	if receipt.ReceiptPath != receiptPath || receipt.ID == "" || receipt.Lane == "" || receipt.Lane != worktreeMergeLaneID(receipt.Repository, receipt.Target) {
		return fmt.Errorf("receipt %s has inconsistent immutable receipt identity", receiptPath)
	}
	if receipt.Phase != WorktreeMergePhasePrepare && receipt.Phase != WorktreeMergePhaseLand {
		return fmt.Errorf("receipt %s is phase %s, want prepare or land", receiptPath, receipt.Phase)
	}
	switch receipt.Status {
	case WorktreeMergeConflict, WorktreeMergeValidationFailed, WorktreeMergeChecksFailed:
	default:
		return fmt.Errorf("receipt %s is %s, want conflict, validation_failed, or checks_failed", receiptPath, receipt.Status)
	}
	if receipt.LandingSHA != "" {
		return fmt.Errorf("receipt %s already recorded a landing SHA %s; use acknowledge-landed-failed instead", receiptPath, receipt.LandingSHA)
	}
	if receipt.PullRequest == "" {
		return fmt.Errorf("receipt %s has no published pull request to prove retired", receiptPath)
	}
	if receipt.Repository == "" || receipt.Target == "" || receipt.TargetSHA == "" {
		return fmt.Errorf("receipt %s lacks complete immutable repository or target identity", receiptPath)
	}
	if receipt.Candidate.Task == "" || receipt.Candidate.Worktree == "" || receipt.Candidate.Branch == "" {
		return fmt.Errorf("receipt %s lacks complete immutable candidate identity", receiptPath)
	}
	if receipt.PublishedCandidateSHA == "" && receipt.Candidate.SHA == "" {
		return fmt.Errorf("receipt %s has no published or preserved candidate SHA to prove unlanded", receiptPath)
	}
	if len(receipt.Sources) == 0 {
		return fmt.Errorf("receipt %s has no receipted sources", receiptPath)
	}
	for _, source := range receipt.Sources {
		if source.Task == "" || source.Worktree == "" || source.Branch == "" || source.SHA == "" {
			return fmt.Errorf("receipt %s has an incomplete immutable source identity", receiptPath)
		}
	}
	return nil
}

type retiredPullRequestView struct {
	State       string `json:"state"`
	ClosedAt    string `json:"closedAt"`
	MergedAt    string `json:"mergedAt"`
	HeadRefName string `json:"headRefName"`
	HeadRefOID  string `json:"headRefOid"`
	MergeCommit struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
}

// proveRetiredPullRequest reads the receipted pull request fresh from GitHub
// and proves it was retired: closed without ever merging. A pull request
// that reports MERGED points the caller at acknowledge-stranded-landing
// instead; one that is still OPEN, or any other non-CLOSED state, refuses.
func proveRetiredPullRequest(ctx context.Context, receipt WorktreeMergeReceipt) (retiredPullRequestView, error) {
	output, err := githubRead(ctx, "", "pr", "view", receipt.PullRequest, "--repo", receipt.Repository,
		"--json", "state,closedAt,mergedAt,mergeCommit,headRefName,headRefOid")
	if err != nil {
		return retiredPullRequestView{}, fmt.Errorf("read pull-request retirement state: %w", err)
	}
	var view retiredPullRequestView
	if jsonErr := json.Unmarshal([]byte(output), &view); jsonErr != nil {
		return retiredPullRequestView{}, fmt.Errorf("decode pull-request retirement state: %w", jsonErr)
	}
	if view.State == "MERGED" || view.MergeCommit.OID != "" || view.MergedAt != "" {
		return retiredPullRequestView{}, fmt.Errorf("pull request %s is MERGED; use acknowledge-stranded-landing instead", receipt.PullRequest)
	}
	if view.State != "CLOSED" {
		return retiredPullRequestView{}, fmt.Errorf("pull request %s is %s, want CLOSED without a merge commit", receipt.PullRequest, view.State)
	}
	if view.HeadRefName != "" && view.HeadRefName != receipt.Candidate.Branch {
		return retiredPullRequestView{}, fmt.Errorf("pull request %s head branch %s does not match exact receipted candidate branch %s", receipt.PullRequest, view.HeadRefName, receipt.Candidate.Branch)
	}
	return view, nil
}

func retiredPublicationAcknowledgementPath(receiptPath string) string {
	return receiptPath + worktreeMergeRetiredPublicationAcknowledgementSuffix
}

func retiredPublicationAcknowledgementID(ack WorktreeMergeRetiredPublicationAcknowledgement) string {
	hash := sha256.New()
	for _, value := range []string{
		ack.ReceiptID, ack.ReceiptPath, ack.ReceiptSHA256, string(ack.ReceiptPhase), string(ack.ReceiptStatus), ack.Lane, ack.Repository, ack.Target,
		ack.ReceiptTargetSHA, ack.CurrentTargetSHA, ack.CandidateTask, ack.CandidateWorktree, ack.CandidateBranch, ack.CandidateSHA, ack.PublishedCandidateSHA,
		ack.PullRequest, ack.PullRequestState, ack.PullRequestClosedAt, ack.PullRequestHeadSHA,
		ack.Actor, ack.Reason,
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

// sameRetiredPublicationAcknowledgement compares only the immutable proof
// evidence, deliberately excluding Actor/Reason/ID/RecordedAt: a retry that
// repeats the same receipt, target, and pull-request proof is idempotent even
// when the operator supplies a different actor or reason on the replay.
func sameRetiredPublicationAcknowledgement(left, right WorktreeMergeRetiredPublicationAcknowledgement) bool {
	return left.ReceiptPath == right.ReceiptPath && left.AcknowledgementPath == right.AcknowledgementPath &&
		left.ReceiptID == right.ReceiptID && left.ReceiptSHA256 == right.ReceiptSHA256 &&
		left.ReceiptPhase == right.ReceiptPhase && left.ReceiptStatus == right.ReceiptStatus &&
		left.Lane == right.Lane && left.Repository == right.Repository && left.Target == right.Target &&
		left.ReceiptTargetSHA == right.ReceiptTargetSHA && left.CurrentTargetSHA == right.CurrentTargetSHA &&
		left.CandidateTask == right.CandidateTask && left.CandidateWorktree == right.CandidateWorktree &&
		left.CandidateBranch == right.CandidateBranch && left.CandidateSHA == right.CandidateSHA &&
		left.PublishedCandidateSHA == right.PublishedCandidateSHA &&
		left.PullRequest == right.PullRequest && left.PullRequestState == right.PullRequestState &&
		left.PullRequestClosedAt == right.PullRequestClosedAt && left.PullRequestHeadSHA == right.PullRequestHeadSHA &&
		sameWorktreeMergeSources(left.Sources, right.Sources)
}

func persistRetiredPublicationAcknowledgement(path string, ack WorktreeMergeRetiredPublicationAcknowledgement) error {
	contents, err := json.MarshalIndent(ack, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".retired-publication-ack-*.tmp")
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

func readRetiredPublicationAcknowledgement(path string, receipt WorktreeMergeReceipt) (WorktreeMergeRetiredPublicationAcknowledgement, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	var ack WorktreeMergeRetiredPublicationAcknowledgement
	if err := json.Unmarshal(contents, &ack); err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, fmt.Errorf("decode retired-publication acknowledgement %s: %w", path, err)
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	if ack.SchemaVersion != worktreeMergeRetiredPublicationAcknowledgementSchemaVersion || ack.Status != "retired_publication_acknowledged" ||
		ack.AcknowledgementPath != path || ack.ReceiptPath != receipt.ReceiptPath || ack.ReceiptID != receipt.ID || ack.ReceiptSHA256 != receiptHash ||
		ack.ReceiptPhase != receipt.Phase || ack.ReceiptStatus != receipt.Status || ack.Lane != receipt.Lane || ack.Repository != receipt.Repository || ack.Target != receipt.Target ||
		ack.ReceiptTargetSHA != receipt.TargetSHA || ack.CandidateTask != receipt.Candidate.Task || ack.CandidateWorktree != receipt.Candidate.Worktree ||
		ack.CandidateBranch != receipt.Candidate.Branch || ack.CandidateSHA != receipt.Candidate.SHA || ack.PublishedCandidateSHA != receipt.PublishedCandidateSHA ||
		ack.PullRequest != receipt.PullRequest || !sameWorktreeMergeSources(ack.Sources, receipt.Sources) ||
		ack.PullRequestState == "" || ack.CurrentTargetSHA == "" ||
		ack.Actor == "" || ack.Reason == "" || ack.RecordedAt.IsZero() || ack.ID != retiredPublicationAcknowledgementID(ack) {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, fmt.Errorf("retired-publication acknowledgement %s has invalid immutable identity", path)
	}
	return ack, nil
}

func hasRetiredPublicationAcknowledgement(receipt WorktreeMergeReceipt) (bool, error) {
	_, err := readRetiredPublicationAcknowledgement(retiredPublicationAcknowledgementPath(receipt.ReceiptPath), receipt)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// effectiveUnpublishedConflict reports whether receipt should be treated, for
// conflict-recovery eligibility purposes, as never having published a
// candidate: either its own PullRequest and PublishedCandidateSHA fields are
// genuinely both empty, or a valid retired-publication acknowledgement proves
// the sole publication this receipt ever recorded was later retired -- its
// pull request closed without merging and its remote candidate branch
// deleted -- with nothing from it landed on the current target. A malformed
// or absent acknowledgement never widens eligibility: this fails closed to
// "still published" whenever the sidecar cannot be authenticated.
func effectiveUnpublishedConflict(receipt WorktreeMergeReceipt) (bool, error) {
	if receipt.PullRequest == "" && receipt.PublishedCandidateSHA == "" {
		return true, nil
	}
	return hasRetiredPublicationAcknowledgement(receipt)
}
