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
	worktreeMergeStrandedLandingAcknowledgementSchemaVersion = 1
	worktreeMergeStrandedLandingAcknowledgementSuffix        = ".stranded-landing.ack.json"
)

// WorktreeMergeStrandedLandingAcknowledgement is a separate, append-only
// acknowledgement for a land/conflict receipt whose exact published pull
// request is proved MERGED, and whose merge commit and preserved candidate
// are both proved contained in the freshly fetched current remote target,
// using only GitHub's remote state. This receipt shape exists precisely
// because the candidate (and every receipted source) worktree is already
// gone -- typically because a resume's landing-result read failed on pure
// I/O after infrastructure cleanup raced ahead of it -- so no local git
// ancestry check against that worktree is possible. The historical merge
// receipt and every Work Log remain untouched; this acknowledgement only
// frees the merger lane for the next candidate.
type WorktreeMergeStrandedLandingAcknowledgement struct {
	SchemaVersion       int                 `json:"schema_version"`
	ID                  string              `json:"id"`
	Status              string              `json:"status"`
	ReceiptPath         string              `json:"receipt_path"`
	AcknowledgementPath string              `json:"acknowledgement_path"`
	ReceiptID           string              `json:"receipt_id"`
	ReceiptSHA256       string              `json:"receipt_sha256"`
	ReceiptStatus       WorktreeMergeStatus `json:"receipt_status"`
	Lane                string              `json:"lane"`
	Repository          string              `json:"repository"`
	Target              string              `json:"target"`
	ReceiptTargetSHA    string              `json:"receipt_target_sha"`
	CandidateSHA        string              `json:"candidate_sha"`
	PullRequest         string              `json:"pull_request"`
	// ProvedLandingSHA is GitHub's own server merge-result commit for
	// PullRequest, discovered live by this acknowledgement. The historical
	// receipt's own LandingSHA is deliberately left empty by this recovery
	// path: the tool itself never observed the landing at the time, and this
	// field records the later, separately audited proof instead.
	ProvedLandingSHA string                `json:"proved_landing_sha"`
	CurrentTargetSHA string                `json:"current_target_sha"`
	Sources          []WorktreeMergeSource `json:"sources"`
	Actor            string                `json:"actor"`
	Reason           string                `json:"reason"`
	RecordedAt       time.Time             `json:"recorded_at"`
}

// WorktreeMergeStrandedLandingAcknowledgementOptions configures
// AcknowledgeStrandedPullRequestLanding.
type WorktreeMergeStrandedLandingAcknowledgementOptions struct {
	ProjectsRoot string
	Receipt      string
	Apply        bool
	Actor        string
	Reason       string
}

// AcknowledgeStrandedPullRequestLanding proves, using only GitHub's remote
// state, that a land/conflict receipt's exact published candidate merged and
// remains reachable from the current remote target, then records a separate
// audited acknowledgement so a fresh forward candidate can own the lane. It
// never reads or requires the candidate or any receipted source worktree:
// that infrastructure being gone is exactly the failure this recovers from.
// It never rewrites the historical receipt or any Work Log. This is a
// dry-run by default; --apply requires --actor and --reason.
func AcknowledgeStrandedPullRequestLanding(ctx context.Context, options WorktreeMergeStrandedLandingAcknowledgementOptions) (WorktreeMergeStrandedLandingAcknowledgement, error) {
	receiptPath, err := resolveWorktreeMergeReceiptPath(options.ProjectsRoot, options.Receipt)
	if err != nil {
		return WorktreeMergeStrandedLandingAcknowledgement{}, err
	}
	receipt, err := readWorktreeMergeReceipt(receiptPath)
	if err != nil {
		return WorktreeMergeStrandedLandingAcknowledgement{}, err
	}
	if err := validateStrandedLandingReceipt(receipt, receiptPath); err != nil {
		return WorktreeMergeStrandedLandingAcknowledgement{}, err
	}
	if options.Apply && (strings.TrimSpace(options.Actor) == "" || strings.TrimSpace(options.Reason) == "") {
		return WorktreeMergeStrandedLandingAcknowledgement{}, errors.New("--actor and --reason are required with --apply")
	}
	lockID := receipt.Lane
	if lockID == "" {
		lockID = worktreeMergeLaneID(receipt.Repository, receipt.Target)
	}
	lock, err := AcquireOperationLock(options.ProjectsRoot, lockID, true)
	if err != nil {
		return WorktreeMergeStrandedLandingAcknowledgement{}, err
	}
	defer func() { _ = lock.Release() }()

	// Every dynamic proof is queried live, under the lane lock: GitHub, never
	// a local worktree or any evidence gathered before this lock, is
	// authoritative for whether this candidate landed and still does.
	landingSHA, currentTarget, proofErr := proveStrandedPullRequestLanding(ctx, receipt)
	if proofErr != nil {
		return WorktreeMergeStrandedLandingAcknowledgement{}, proofErr
	}

	receiptHash, err := worktreeMergeReceiptSHA256(receiptPath)
	if err != nil {
		return WorktreeMergeStrandedLandingAcknowledgement{}, err
	}
	ackPath := strandedLandingAcknowledgementPath(receiptPath)
	ack := WorktreeMergeStrandedLandingAcknowledgement{
		SchemaVersion: worktreeMergeStrandedLandingAcknowledgementSchemaVersion,
		Status:        "stranded_landing_acknowledged",
		ReceiptPath:   receiptPath, AcknowledgementPath: ackPath,
		ReceiptID: receipt.ID, ReceiptSHA256: receiptHash, ReceiptStatus: receipt.Status, Lane: receipt.Lane,
		Repository: receipt.Repository, Target: receipt.Target, ReceiptTargetSHA: receipt.TargetSHA,
		CandidateSHA: receipt.Candidate.SHA, PullRequest: receipt.PullRequest,
		ProvedLandingSHA: landingSHA, CurrentTargetSHA: currentTarget,
		Sources: append([]WorktreeMergeSource(nil), receipt.Sources...),
		Actor:   strings.TrimSpace(options.Actor), Reason: strings.TrimSpace(options.Reason), RecordedAt: time.Now().UTC(),
	}
	ack.ID = strandedLandingAcknowledgementID(ack)
	if existing, readErr := readStrandedLandingAcknowledgement(ackPath, receipt); readErr == nil {
		if existing.ProvedLandingSHA != landingSHA || existing.CurrentTargetSHA != currentTarget {
			return WorktreeMergeStrandedLandingAcknowledgement{}, fmt.Errorf("acknowledgement %s binds different landing or target evidence", ackPath)
		}
		return existing, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return WorktreeMergeStrandedLandingAcknowledgement{}, readErr
	}
	if !options.Apply {
		return ack, nil
	}
	if err := persistStrandedLandingAcknowledgement(ackPath, ack); err != nil {
		return WorktreeMergeStrandedLandingAcknowledgement{}, err
	}
	return ack, nil
}

// validateStrandedLandingReceipt narrowly scopes eligibility to the exact
// shape of a stranded publish: a land-phase receipt marked conflict (WB's
// catch-all failure status) that never recorded a landing SHA but did
// publish its exact candidate in a pull request. A real merge conflict
// during prepare (wrong phase), a land conflict before any PR existed (no
// PullRequest), and an already-landed post-target CI failure (LandingSHA
// set; that is acknowledge-landed-failed's territory) are all excluded.
func validateStrandedLandingReceipt(receipt WorktreeMergeReceipt, receiptPath string) error {
	if receipt.ReceiptPath != receiptPath || receipt.ID == "" || receipt.Lane == "" || receipt.Lane != worktreeMergeLaneID(receipt.Repository, receipt.Target) {
		return fmt.Errorf("receipt %s has inconsistent immutable receipt identity", receiptPath)
	}
	if receipt.Phase != WorktreeMergePhaseLand || receipt.Status != WorktreeMergeConflict {
		return fmt.Errorf("receipt %s is %s/%s, want land conflict with a stranded published pull request", receiptPath, receipt.Phase, receipt.Status)
	}
	if receipt.LandingSHA != "" {
		return fmt.Errorf("receipt %s already recorded a landing SHA %s; use acknowledge-landed-failed for a landed_post_target_ci_failed receipt instead", receiptPath, receipt.LandingSHA)
	}
	if receipt.PullRequest == "" {
		return fmt.Errorf("receipt %s has no published pull request to prove", receiptPath)
	}
	if receipt.Repository == "" || receipt.Target == "" || receipt.TargetSHA == "" || receipt.Candidate.SHA == "" {
		return fmt.Errorf("receipt %s lacks complete immutable repository, target, or candidate identity", receiptPath)
	}
	if receipt.PublishedCandidateSHA == "" || receipt.PublishedCandidateSHA != receipt.Candidate.SHA {
		return fmt.Errorf("receipt %s published candidate %s does not match its exact preserved candidate %s", receiptPath, receipt.PublishedCandidateSHA, receipt.Candidate.SHA)
	}
	return nil
}

type strandedPullRequestLandingView struct {
	State       string `json:"state"`
	MergedAt    string `json:"mergedAt"`
	HeadRefOID  string `json:"headRefOid"`
	BaseRefName string `json:"baseRefName"`
	MergeCommit struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
}

// proveStrandedPullRequestLanding proves a stranded receipt's landing using
// only GitHub's remote state: the exact published pull request reports
// MERGED at the receipted candidate head, the exact server merge commit and
// the receipted candidate are both reachable from the freshly fetched current
// target, and the receipted candidate still contains its own recorded
// pre-merge target. Every call passes an empty working directory: none of
// this proof may depend on a local worktree, because this receipt shape is
// defined by that worktree already being gone.
func proveStrandedPullRequestLanding(ctx context.Context, receipt WorktreeMergeReceipt) (landingSHA, currentTargetSHA string, err error) {
	output, readErr := githubRead(ctx, "", "pr", "view", receipt.PullRequest, "--repo", receipt.Repository,
		"--json", "state,mergedAt,mergeCommit,headRefOid,baseRefName")
	if readErr != nil {
		return "", "", fmt.Errorf("read pull-request landing state: %w", readErr)
	}
	var view strandedPullRequestLandingView
	if jsonErr := json.Unmarshal([]byte(output), &view); jsonErr != nil {
		return "", "", fmt.Errorf("decode pull-request landing state: %w", jsonErr)
	}
	if view.BaseRefName != receipt.Target {
		return "", "", fmt.Errorf("pull request %s targets %s, not receipted target %s", receipt.PullRequest, view.BaseRefName, receipt.Target)
	}
	if view.HeadRefOID != receipt.Candidate.SHA {
		return "", "", fmt.Errorf("pull request %s head %s does not match exact receipted candidate %s", receipt.PullRequest, view.HeadRefOID, receipt.Candidate.SHA)
	}
	if view.State != "MERGED" {
		return "", "", fmt.Errorf("pull request %s is %s, not MERGED", receipt.PullRequest, view.State)
	}
	if view.MergedAt == "" || view.MergeCommit.OID == "" {
		return "", "", fmt.Errorf("pull request %s reports MERGED without a merge time or server merge commit", receipt.PullRequest)
	}
	currentTarget, headReason := targetHead(ctx, receipt.Repository, receipt.Target)
	if currentTarget == "" {
		return "", "", fmt.Errorf("read current remote target %s: %s", receipt.Target, headReason)
	}
	if contains, reason := candidateContainsTarget(ctx, receipt.Repository, view.MergeCommit.OID, currentTarget); !contains {
		if reason == "" {
			reason = fmt.Sprintf("current remote target %s does not contain proved merge commit %s", currentTarget, view.MergeCommit.OID)
		}
		return "", "", errors.New(reason)
	}
	if contains, reason := candidateContainsTarget(ctx, receipt.Repository, receipt.Candidate.SHA, currentTarget); !contains {
		if reason == "" {
			reason = fmt.Sprintf("current remote target %s does not contain receipted candidate %s", currentTarget, receipt.Candidate.SHA)
		}
		return "", "", errors.New(reason)
	}
	if contains, reason := candidateContainsTarget(ctx, receipt.Repository, receipt.TargetSHA, receipt.Candidate.SHA); !contains {
		if reason == "" {
			reason = fmt.Sprintf("receipted candidate %s no longer contains its own recorded pre-merge target %s", receipt.Candidate.SHA, receipt.TargetSHA)
		}
		return "", "", errors.New(reason)
	}
	return view.MergeCommit.OID, currentTarget, nil
}

func strandedLandingAcknowledgementID(ack WorktreeMergeStrandedLandingAcknowledgement) string {
	hash := sha256.New()
	for _, value := range []string{
		ack.ReceiptID, ack.ReceiptPath, ack.ReceiptSHA256, string(ack.ReceiptStatus), ack.ReceiptTargetSHA,
		ack.CandidateSHA, ack.PullRequest, ack.ProvedLandingSHA, ack.CurrentTargetSHA,
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

func strandedLandingAcknowledgementPath(receiptPath string) string {
	return receiptPath + worktreeMergeStrandedLandingAcknowledgementSuffix
}

func persistStrandedLandingAcknowledgement(path string, ack WorktreeMergeStrandedLandingAcknowledgement) error {
	contents, err := json.MarshalIndent(ack, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".stranded-landing-ack-*.tmp")
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

func readStrandedLandingAcknowledgement(path string, receipt WorktreeMergeReceipt) (WorktreeMergeStrandedLandingAcknowledgement, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return WorktreeMergeStrandedLandingAcknowledgement{}, err
	}
	var ack WorktreeMergeStrandedLandingAcknowledgement
	if err := json.Unmarshal(contents, &ack); err != nil {
		return WorktreeMergeStrandedLandingAcknowledgement{}, fmt.Errorf("decode stranded-landing acknowledgement %s: %w", path, err)
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		return WorktreeMergeStrandedLandingAcknowledgement{}, err
	}
	if ack.SchemaVersion != worktreeMergeStrandedLandingAcknowledgementSchemaVersion || ack.Status != "stranded_landing_acknowledged" ||
		ack.AcknowledgementPath != path || ack.ReceiptPath != receipt.ReceiptPath || ack.ReceiptID != receipt.ID || ack.ReceiptSHA256 != receiptHash ||
		ack.ReceiptStatus != receipt.Status || ack.Lane != receipt.Lane || ack.Repository != receipt.Repository || ack.Target != receipt.Target ||
		ack.ReceiptTargetSHA != receipt.TargetSHA || ack.CandidateSHA != receipt.Candidate.SHA || ack.PullRequest != receipt.PullRequest ||
		ack.ProvedLandingSHA == "" || ack.CurrentTargetSHA == "" || !sameWorktreeMergeSources(ack.Sources, receipt.Sources) ||
		ack.Actor == "" || ack.Reason == "" || ack.RecordedAt.IsZero() || ack.ID != strandedLandingAcknowledgementID(ack) {
		return WorktreeMergeStrandedLandingAcknowledgement{}, fmt.Errorf("stranded-landing acknowledgement %s has invalid immutable identity", path)
	}
	return ack, nil
}

func hasStrandedLandingAcknowledgement(receipt WorktreeMergeReceipt) (bool, error) {
	_, err := readStrandedLandingAcknowledgement(strandedLandingAcknowledgementPath(receipt.ReceiptPath), receipt)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
