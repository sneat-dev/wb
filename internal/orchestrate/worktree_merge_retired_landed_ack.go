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
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/worktrees"
)

const worktreeMergeRetiredLandedAcknowledgementSuffix = ".retired-landed.ack.json"

// WorktreeMergeRetiredLandedAcknowledgement is the append-only terminal
// evidence for one already-merged published candidate whose WB worktrees were
// removed before its historical receipt became terminal.
type WorktreeMergeRetiredLandedAcknowledgement struct {
	SchemaVersion       int                                    `json:"schema_version"`
	ID                  string                                 `json:"id"`
	Status              string                                 `json:"status"`
	ReceiptPath         string                                 `json:"receipt_path"`
	AcknowledgementPath string                                 `json:"acknowledgement_path"`
	ReceiptID           string                                 `json:"receipt_id"`
	ReceiptSHA256       string                                 `json:"receipt_sha256"`
	Repository          string                                 `json:"repository"`
	Target              string                                 `json:"target"`
	ReceiptTargetSHA    string                                 `json:"receipt_target_sha"`
	Candidate           WorktreeMergeCandidate                 `json:"candidate"`
	Sources             []WorktreeMergeSource                  `json:"sources"`
	ClaimSHA256         []worktrees.TerminalWorkLogClaimDigest `json:"claim_sha256"`
	PullRequest         string                                 `json:"pull_request"`
	LandingSHA          string                                 `json:"landing_sha"`
	CurrentTargetSHA    string                                 `json:"current_target_sha"`
	Actor               string                                 `json:"actor"`
	Reason              string                                 `json:"reason"`
	RecordedAt          time.Time                              `json:"recorded_at"`
}

type WorktreeMergeRetiredLandedAcknowledgementOptions struct {
	ProjectsRoot, Receipt, ExpectedReceiptSHA256, ExpectedLandingSHA string
	ExpectedClaimSHA256                                              []string
	Apply                                                            bool
	Actor, Reason                                                    string
}

func AcknowledgeRetiredLandedWorktreeMerge(ctx context.Context, options WorktreeMergeRetiredLandedAcknowledgementOptions) (WorktreeMergeRetiredLandedAcknowledgement, error) {
	if strings.TrimSpace(options.ExpectedReceiptSHA256) == "" || strings.TrimSpace(options.ExpectedLandingSHA) == "" || len(options.ExpectedClaimSHA256) == 0 {
		return WorktreeMergeRetiredLandedAcknowledgement{}, errors.New("expected receipt, landing, and every claim SHA256 are required")
	}
	if options.Apply && (strings.TrimSpace(options.Actor) == "" || strings.TrimSpace(options.Reason) == "") {
		return WorktreeMergeRetiredLandedAcknowledgement{}, errors.New("--actor and --reason are required with --apply")
	}
	receiptPath, err := resolveWorktreeMergeReceiptPath(options.ProjectsRoot, options.Receipt)
	if err != nil {
		return WorktreeMergeRetiredLandedAcknowledgement{}, err
	}
	receipt, err := readWorktreeMergeReceipt(receiptPath)
	if err != nil {
		return WorktreeMergeRetiredLandedAcknowledgement{}, err
	}
	if err := validateRetiredLandedReceipt(receipt, receiptPath); err != nil {
		return WorktreeMergeRetiredLandedAcknowledgement{}, err
	}
	lock, err := AcquireOperationLock(options.ProjectsRoot, receipt.Lane, true)
	if err != nil {
		return WorktreeMergeRetiredLandedAcknowledgement{}, err
	}
	defer func() { _ = lock.Release() }()
	// Re-read after the lane lock. An acknowledgement never releases a lane on
	// the authority of a receipt or terminal claim observed before admission.
	receipt, err = readWorktreeMergeReceipt(receiptPath)
	if err != nil {
		return WorktreeMergeRetiredLandedAcknowledgement{}, err
	}
	if err := validateRetiredLandedReceipt(receipt, receiptPath); err != nil {
		return WorktreeMergeRetiredLandedAcknowledgement{}, err
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receiptPath)
	if err != nil || receiptHash != options.ExpectedReceiptSHA256 {
		return WorktreeMergeRetiredLandedAcknowledgement{}, fmt.Errorf("receipt SHA256 %s does not match expected %s", receiptHash, options.ExpectedReceiptSHA256)
	}
	canonical := filepath.Join(options.ProjectsRoot, filepath.FromSlash(receipt.Repository))
	guard, err := worktrees.Guard(ctx, canonical, worktrees.GuardOptions{ProjectsRoot: options.ProjectsRoot, Base: receipt.Target})
	if err != nil || guard.Kind != "canonical" {
		return WorktreeMergeRetiredLandedAcknowledgement{}, fmt.Errorf("canonical repository is not a clean canonical checkout: %w", err)
	}
	if slug, err := worktrees.OriginSlug(ctx, canonical); err != nil || slug != receipt.Repository {
		return WorktreeMergeRetiredLandedAcknowledgement{}, fmt.Errorf("canonical origin identity does not match receipt repository %s", receipt.Repository)
	}
	expectations, err := terminalWorkLogExpectations(receipt)
	if err != nil {
		return WorktreeMergeRetiredLandedAcknowledgement{}, err
	}
	digests, err := worktrees.RemovedTerminalWorkLogClaimDigestsAllowingAdvancedFinalCommit(options.ProjectsRoot, expectations)
	if err != nil {
		return WorktreeMergeRetiredLandedAcknowledgement{}, fmt.Errorf("validate retired Work Logs: %w", err)
	}
	if !sameClaimDigestValues(digests, options.ExpectedClaimSHA256) {
		return WorktreeMergeRetiredLandedAcknowledgement{}, errors.New("terminal Work Log claim SHA256 values do not match expected evidence")
	}
	ackPath := retiredLandedAcknowledgementPath(receiptPath)
	if existing, err := readRetiredLandedAcknowledgement(ackPath, receipt); err == nil {
		if err := validateRetiredLandedAcknowledgementEvidence(ctx, options.ProjectsRoot, receipt, receiptHash, options.ExpectedLandingSHA, digests, existing); err != nil {
			return WorktreeMergeRetiredLandedAcknowledgement{}, err
		}
		if existing.ReceiptSHA256 != receiptHash || existing.LandingSHA != options.ExpectedLandingSHA || !sameClaimDigests(existing.ClaimSHA256, digests) {
			return WorktreeMergeRetiredLandedAcknowledgement{}, errors.New("existing retired landed acknowledgement binds different immutable evidence")
		}
		return existing, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return WorktreeMergeRetiredLandedAcknowledgement{}, err
	}
	if err := requireRetiredLandedWorktreeCleanup(ctx, options.ProjectsRoot, receipt, expectations); err != nil {
		return WorktreeMergeRetiredLandedAcknowledgement{}, err
	}
	observed := receipt
	observed.Candidate.Worktree = canonical
	landing, merged, err := pullRequestLandingReceipt(ctx, observed, WorktreeMergeLandOptions{})
	if err != nil || !merged || landing != options.ExpectedLandingSHA {
		return WorktreeMergeRetiredLandedAcknowledgement{}, fmt.Errorf("read exact merged pull-request receipt: landing=%s merged=%t: %w", landing, merged, err)
	}
	prospective := WorktreeMergeRetiredLandedAcknowledgement{LandingSHA: landing}
	if err := validateRetiredLandedAcknowledgementEvidence(ctx, options.ProjectsRoot, receipt, receiptHash, landing, digests, prospective); err != nil {
		return WorktreeMergeRetiredLandedAcknowledgement{}, err
	}
	currentTarget, err := fetchExactMergeTarget(ctx, canonical, receipt.Target)
	if err != nil {
		return WorktreeMergeRetiredLandedAcknowledgement{}, err
	}
	if err := requireRetiredAncestor(ctx, canonical, landing, currentTarget, "current remote target"); err != nil {
		return WorktreeMergeRetiredLandedAcknowledgement{}, err
	}
	ack := WorktreeMergeRetiredLandedAcknowledgement{SchemaVersion: 1, Status: "retired_landed_acknowledged", ReceiptPath: receiptPath, AcknowledgementPath: ackPath, ReceiptID: receipt.ID, ReceiptSHA256: receiptHash, Repository: receipt.Repository, Target: receipt.Target, ReceiptTargetSHA: receipt.TargetSHA, Candidate: receipt.Candidate, Sources: append([]WorktreeMergeSource(nil), receipt.Sources...), ClaimSHA256: digests, PullRequest: receipt.PullRequest, LandingSHA: landing, CurrentTargetSHA: currentTarget, Actor: strings.TrimSpace(options.Actor), Reason: strings.TrimSpace(options.Reason), RecordedAt: time.Now().UTC()}
	ack.ID = retiredLandedAcknowledgementID(ack)
	if !options.Apply {
		ack.ID = ""
		ack.Status = "retired_landed_acknowledgement_planned"
		return ack, nil
	}
	if err := persistRetiredLandedAcknowledgement(ack.AcknowledgementPath, ack); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return WorktreeMergeRetiredLandedAcknowledgement{}, err
		}
		existing, readErr := readRetiredLandedAcknowledgement(ack.AcknowledgementPath, receipt)
		if readErr != nil || validateRetiredLandedAcknowledgementEvidence(ctx, options.ProjectsRoot, receipt, receiptHash, options.ExpectedLandingSHA, digests, existing) != nil || existing.ReceiptSHA256 != receiptHash || existing.LandingSHA != options.ExpectedLandingSHA || !sameClaimDigests(existing.ClaimSHA256, digests) {
			return WorktreeMergeRetiredLandedAcknowledgement{}, errors.New("concurrent retired landed acknowledgement binds different immutable evidence")
		}
		return existing, nil
	}
	return ack, nil
}

func validateRetiredLandedReceipt(receipt WorktreeMergeReceipt, path string) error {
	if receipt.ReceiptPath != path || receipt.ID == "" || receipt.Lane != worktreeMergeLaneID(receipt.Repository, receipt.Target) || receipt.Phase != WorktreeMergePhaseLand || (receipt.Status != WorktreeMergePrepared && receipt.Status != WorktreeMergeConflict) || receipt.PullRequest == "" || receipt.PublishedCandidateSHA != receipt.Candidate.SHA || receipt.LandingSHA != "" {
		return errors.New("receipt is not a published retired land candidate")
	}
	if _, err := terminalWorkLogExpectations(receipt); err != nil {
		return err
	}
	return nil
}
func requireRetiredAncestor(ctx context.Context, path, ancestor, descendant, name string) error {
	if !validRetiredLandedGitSHA(ancestor) || !validRetiredLandedGitSHA(descendant) {
		return fmt.Errorf("%s has invalid Git object identity", name)
	}
	ok, err := isMergeAncestor(ctx, path, ancestor, descendant)
	if err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("%s %s is not an ancestor of %s", name, ancestor, descendant)
		}
		return err
	}
	return nil
}

// requireRetiredLandedWorktreeCleanup proves the retired path and branch state
// without reusing the ordinary recovery helper: source terminals may have an
// explicitly proven later final commit, which ordinary cleanup intentionally
// refuses. Claim and terminal identity is validated before this helper runs.
func requireRetiredLandedWorktreeCleanup(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt, expectations []worktrees.TerminalWorkLogExpectation) error {
	for _, expectation := range expectations {
		if _, err := os.Lstat(expectation.Worktree); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				err = errors.New("path exists")
			}
			return fmt.Errorf("retired worktree cleanup refuses task %s because worktree path is not absent: %w", expectation.Task, err)
		}
	}
	return requireTerminalCleanupBranchesAbsent(ctx, projectsRoot, receipt, expectations, 0, 0)
}

// validateRetiredLandedAcknowledgementEvidence is the shared local admission
// proof for first publication, an existing-acknowledgement replay, and lane
// release. It deliberately makes no fetch or GitHub call: a replay can work
// offline only after the immutable receipt, claims, terminals, retired paths,
// and locally available graph all corroborate the stored exact landing.
func validateRetiredLandedAcknowledgementEvidence(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt, receiptHash, landing string, digests []worktrees.TerminalWorkLogClaimDigest, ack WorktreeMergeRetiredLandedAcknowledgement) error {
	if receiptHash == "" || landing == "" || ack.LandingSHA != landing {
		return errors.New("retired landed acknowledgement lacks exact landing evidence")
	}
	expectations, err := terminalWorkLogExpectations(receipt)
	if err != nil {
		return err
	}
	if err := requireRetiredLandedWorktreePathsAbsent(expectations); err != nil {
		return err
	}
	canonical := filepath.Join(projectsRoot, filepath.FromSlash(receipt.Repository))
	for _, root := range append([]string{receipt.TargetSHA}, sourceSHAs(receipt.Sources)...) {
		if err := requireRetiredAncestor(ctx, canonical, root, receipt.Candidate.SHA, "receipted candidate"); err != nil {
			return err
		}
	}
	for _, digest := range digests {
		if err := requireRetiredAncestor(ctx, canonical, digest.BaseSHA, receipt.Candidate.SHA, "immutable claim base"); err != nil {
			return err
		}
	}
	if err := requireRetiredAncestor(ctx, canonical, receipt.Candidate.SHA, landing, "acknowledged server landing"); err != nil {
		return err
	}
	return validateRetiredLandedTerminalFinals(ctx, canonical, receipt, digests, landing)
}

func validateRetiredLandedTerminalFinals(ctx context.Context, canonical string, receipt WorktreeMergeReceipt, digests []worktrees.TerminalWorkLogClaimDigest, landing string) error {
	for _, digest := range digests {
		if digest.Task == receipt.Candidate.Task {
			if digest.FinalCommit != receipt.Candidate.SHA {
				return errors.New("candidate terminal final does not match receipted candidate")
			}
			continue
		}
		var source *WorktreeMergeSource
		for i := range receipt.Sources {
			if receipt.Sources[i].Task == digest.Task {
				source = &receipt.Sources[i]
				break
			}
		}
		if source == nil {
			return fmt.Errorf("terminal claim task %s is not a receipted source", digest.Task)
		}
		if err := requireRetiredAncestor(ctx, canonical, source.SHA, digest.FinalCommit, "receipted source to terminal final"); err != nil {
			return err
		}
		if err := requireRetiredAncestor(ctx, canonical, digest.FinalCommit, landing, "terminal source final to exact server landing"); err != nil {
			return err
		}
	}
	return nil
}
func retiredLandedAcknowledgementPath(path string) string {
	return path + worktreeMergeRetiredLandedAcknowledgementSuffix
}
func retiredLandedAcknowledgementID(ack WorktreeMergeRetiredLandedAcknowledgement) string {
	ack.ID = ""
	contents, _ := json.Marshal(ack)
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}
func sameClaimDigestValues(digests []worktrees.TerminalWorkLogClaimDigest, expected []string) bool {
	got := make([]string, len(digests))
	for i, d := range digests {
		got[i] = d.Task + "=" + d.SHA256
	}
	sort.Strings(got)
	want := append([]string(nil), expected...)
	sort.Strings(want)
	return strings.Join(got, "\x00") == strings.Join(want, "\x00")
}
func sameClaimDigests(left, right []worktrees.TerminalWorkLogClaimDigest) bool {
	if len(left) != len(right) {
		return false
	}
	copyLeft, copyRight := append([]worktrees.TerminalWorkLogClaimDigest(nil), left...), append([]worktrees.TerminalWorkLogClaimDigest(nil), right...)
	sort.Slice(copyLeft, func(i, j int) bool { return copyLeft[i].Task < copyLeft[j].Task })
	sort.Slice(copyRight, func(i, j int) bool { return copyRight[i].Task < copyRight[j].Task })
	for i := range copyLeft {
		if copyLeft[i] != copyRight[i] {
			return false
		}
	}
	return true
}
func persistRetiredLandedAcknowledgement(path string, ack WorktreeMergeRetiredLandedAcknowledgement) error {
	data, err := json.MarshalIndent(ack, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".retired-landed-ack-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err = temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err = temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err = temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	// link is create-if-absent on this directory. Unlike Rename it cannot
	// replace another actor's complete acknowledgement after our preflight.
	if err = os.Link(temporaryPath, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return err
	}
	return directory.Close()
}
func readRetiredLandedAcknowledgement(path string, receipt WorktreeMergeReceipt) (WorktreeMergeRetiredLandedAcknowledgement, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return WorktreeMergeRetiredLandedAcknowledgement{}, err
	}
	var ack WorktreeMergeRetiredLandedAcknowledgement
	if err = json.Unmarshal(data, &ack); err != nil {
		return ack, err
	}
	if ack.SchemaVersion != 1 || ack.Status != "retired_landed_acknowledged" || ack.AcknowledgementPath != path || ack.ReceiptPath != receipt.ReceiptPath || ack.ReceiptID != receipt.ID || ack.ReceiptSHA256 == "" || ack.Repository != receipt.Repository || ack.Target != receipt.Target || ack.ReceiptTargetSHA != receipt.TargetSHA || ack.Candidate != receipt.Candidate || !sameWorktreeMergeSources(ack.Sources, receipt.Sources) || ack.PullRequest != receipt.PullRequest || ack.LandingSHA == "" || ack.CurrentTargetSHA == "" || ack.Actor == "" || ack.Reason == "" || ack.RecordedAt.IsZero() || !validRetiredLandedClaimDigests(ack.ClaimSHA256) || ack.ID != retiredLandedAcknowledgementID(ack) {
		return ack, errors.New("retired landed acknowledgement has invalid immutable identity")
	}
	return ack, nil
}

// hasRetiredLandedAcknowledgement is used by lane admission. It revalidates
// the receipt bytes and the sealed terminal claim bytes before an old receipt
// can stop blocking a new candidate.
func hasRetiredLandedAcknowledgement(projectsRoot string, receipt WorktreeMergeReceipt) (bool, error) {
	ack, err := readRetiredLandedAcknowledgement(retiredLandedAcknowledgementPath(receipt.ReceiptPath), receipt)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	hash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil || hash != ack.ReceiptSHA256 {
		return false, errors.New("retired landed acknowledgement receipt bytes changed")
	}
	expectations, err := terminalWorkLogExpectations(receipt)
	if err != nil {
		return false, err
	}
	digests, err := worktrees.RemovedTerminalWorkLogClaimDigestsAllowingAdvancedFinalCommit(projectsRoot, expectations)
	if err != nil {
		return false, err
	}
	if !sameClaimDigests(digests, ack.ClaimSHA256) {
		return false, errors.New("retired landed acknowledgement claim bytes changed")
	}
	if err := validateRetiredLandedAcknowledgementEvidence(context.Background(), projectsRoot, receipt, hash, ack.LandingSHA, digests, ack); err != nil {
		return false, err
	}
	return true, nil
}

func requireRetiredLandedWorktreePathsAbsent(expectations []worktrees.TerminalWorkLogExpectation) error {
	for _, expectation := range expectations {
		if _, err := os.Lstat(expectation.Worktree); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				err = errors.New("path exists")
			}
			return fmt.Errorf("retired landed acknowledgement refuses reused worktree path for task %s: %w", expectation.Task, err)
		}
	}
	return nil
}

func validRetiredLandedClaimDigests(digests []worktrees.TerminalWorkLogClaimDigest) bool {
	seen := make(map[string]bool, len(digests))
	for _, digest := range digests {
		if strings.TrimSpace(digest.Task) == "" || !validRetiredLandedGitSHA(digest.BaseSHA) || !validRetiredLandedGitSHA(digest.FinalCommit) || !validRetiredLandedSHA256(digest.SHA256) || !validRetiredLandedSHA256(digest.TerminalSHA256) || seen[digest.Task] {
			return false
		}
		seen[digest.Task] = true
	}
	return len(digests) > 0
}

func validRetiredLandedGitSHA(value string) bool {
	return (len(value) == 40 || len(value) == 64) && validRetiredLandedHex(value)
}

func validRetiredLandedSHA256(value string) bool {
	return len(value) == 64 && validRetiredLandedHex(value)
}

func validRetiredLandedHex(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}
