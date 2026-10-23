package orchestrate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// worktreeMergeTargetAbsorbedAcknowledgementSuffix names the immutable
// sidecar recording that a receipt's work already reached its remote target.
//
// On 2026-10-22 receipt merge-datatug-datatug-core-main-12ea2602a26b-ebf34b0eddbc
// stayed `prepared` for ~12 hours after datatug/datatug-core#394 merged its
// candidate into main. Only `complete` released a merger lane, so every later
// lander was refused and a reviewed four-repository change could not land.
// The lane scan now proves such absorption itself, records it here, and lets
// the lane move on without rewriting the historical receipt.
//
// The file is named <receipt>.<receipt-sha256[:12]>.target-absorbed.ack.json:
// each acknowledgement binds one exact receipt revision, so a receipt that is
// later rewritten (resumed, landed) is judged afresh rather than released by
// evidence about an older revision.
const worktreeMergeTargetAbsorbedAcknowledgementSuffix = ".target-absorbed.ack.json"

const worktreeMergeTargetAbsorbedSchemaVersion = 1

// WorktreeMergeTargetAbsorbedScanActor is the actor recorded when the lane
// scan writes an acknowledgement on demand; a sweep records its operator.
const WorktreeMergeTargetAbsorbedScanActor = "wb merger-lane scan"

// WorktreeMergeTargetAbsorbedAcknowledgement is the audited proof that every
// commit a receipt recorded is an ancestor of its freshly fetched remote
// target, so the receipt has nothing left to land and no longer owns its lane.
type WorktreeMergeTargetAbsorbedAcknowledgement struct {
	SchemaVersion int                 `json:"schema_version"`
	ID            string              `json:"id"`
	ReceiptPath   string              `json:"receipt_path"`
	ReceiptSHA256 string              `json:"receipt_sha256"`
	ReceiptID     string              `json:"receipt_id"`
	ReceiptStatus WorktreeMergeStatus `json:"receipt_status"`
	Lane          string              `json:"lane"`
	Repository    string              `json:"repository"`
	Target        string              `json:"target"`
	TargetSHA     string              `json:"target_sha"`
	ProvedCommits []string            `json:"proved_commits"`
	Actor         string              `json:"actor"`
	Reason        string              `json:"reason"`
	RecordedAt    time.Time           `json:"recorded_at"`
}

// worktreeMergeTargetAbsorptionEligible lists the statuses a proved
// absorption may release. `preparing` is excluded because a live prepare may
// not have recorded its candidate yet; landed_post_target_ci_failed is
// excluded because its forward repair still needs the lane.
func worktreeMergeTargetAbsorptionEligible(status WorktreeMergeStatus) bool {
	switch status {
	case WorktreeMergePrepared, WorktreeMergePublished, WorktreeMergeChecksPending, WorktreeMergeChecksFailed,
		WorktreeMergeValidationFailed, WorktreeMergeConflict, WorktreeMergeLanded, WorktreeMergeCanonicalSyncBlocked:
		return true
	default:
		return false
	}
}

// worktreeMergeTargetAbsorptionQuietPeriod is how long a landed receipt must
// sit untouched before absorption may release it, so a lander still running
// its own cleanup or canonical sync finishes before the lane moves on.
const worktreeMergeTargetAbsorptionQuietPeriod = 15 * time.Minute

// worktreeMergeTargetAbsorptionFetchTimeout bounds the target fetch the lane
// scan runs; a slow remote keeps the receipt blocking rather than stalling
// the prepare that scanned it.
const worktreeMergeTargetAbsorptionFetchTimeout = 2 * time.Minute

// worktreeMergeTargetAbsorptionGate refuses a receipt whose remaining work
// still needs its lane. A landed receipt is saved before its post-target CI
// is awaited (and its checks are cleared for that wait), so it qualifies only
// once that CI passed for its exact landing commit, under the same rule land
// itself uses to skip the wait, and only after the quiet period.
func worktreeMergeTargetAbsorptionGate(receipt WorktreeMergeReceipt, now time.Time) error {
	if !worktreeMergeTargetAbsorptionEligible(receipt.Status) {
		return fmt.Errorf("receipt status %s is not eligible for target absorption", receipt.Status)
	}
	if receipt.LandingSHA == "" {
		return nil
	}
	if receipt.Checks.Status != githubchecks.PullRequestWaitPassed || receipt.Checks.Head != receipt.LandingSHA ||
		receipt.ValidationDeferral != nil && receipt.ValidationDeferral.Route == WorktreeMergeRouteDirect {
		return fmt.Errorf("landing %s has no passed post-target CI evidence", receipt.LandingSHA)
	}
	if now.Sub(receipt.UpdatedAt) < worktreeMergeTargetAbsorptionQuietPeriod {
		return fmt.Errorf("landed receipt was updated less than %s ago", worktreeMergeTargetAbsorptionQuietPeriod)
	}
	return nil
}

// worktreeMergeTargetAbsorptionCommits returns every commit the receipt
// recorded: all source heads plus any candidate, published candidate, and
// landing commit. A receipt with no sources or an unrecorded source head
// cannot be proved absorbed.
func worktreeMergeTargetAbsorptionCommits(receipt WorktreeMergeReceipt) ([]string, bool) {
	if len(receipt.Sources) == 0 {
		return nil, false
	}
	var commits []string
	for _, source := range receipt.Sources {
		sha := strings.TrimSpace(source.SHA)
		if sha == "" {
			return nil, false
		}
		commits = append(commits, sha)
	}
	for _, sha := range []string{receipt.Candidate.SHA, receipt.PublishedCandidateSHA, receipt.LandingSHA} {
		if sha = strings.TrimSpace(sha); sha != "" {
			commits = append(commits, sha)
		}
	}
	slices.Sort(commits)
	return slices.Compact(commits), true
}

func worktreeMergeTargetAbsorbedAcknowledgementPath(receiptPath, receiptSHA256 string) string {
	return receiptPath + "." + receiptSHA256[:12] + worktreeMergeTargetAbsorbedAcknowledgementSuffix
}

func worktreeMergeTargetAbsorbedAcknowledgementID(ack WorktreeMergeTargetAbsorbedAcknowledgement) string {
	hash := sha256.New()
	for _, value := range []string{ack.ReceiptPath, ack.ReceiptSHA256, ack.ReceiptID, string(ack.ReceiptStatus), ack.Lane, ack.Repository, ack.Target, ack.TargetSHA, ack.Actor, ack.Reason, ack.RecordedAt.UTC().Format(time.RFC3339Nano)} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	for _, commit := range ack.ProvedCommits {
		_, _ = hash.Write([]byte(commit))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// readWorktreeMergeTargetAbsorbedAcknowledgement returns the acknowledgement
// bound to the receipt's current bytes. os.ErrNotExist means none exists for
// this revision; any other error means one exists but does not prove this
// exact receipt, and must never release the lane.
func readWorktreeMergeTargetAbsorbedAcknowledgement(receipt WorktreeMergeReceipt) (WorktreeMergeTargetAbsorbedAcknowledgement, string, error) {
	receiptSHA256, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		return WorktreeMergeTargetAbsorbedAcknowledgement{}, "", err
	}
	path := worktreeMergeTargetAbsorbedAcknowledgementPath(receipt.ReceiptPath, receiptSHA256)
	contents, err := os.ReadFile(path)
	if err != nil {
		return WorktreeMergeTargetAbsorbedAcknowledgement{}, path, err
	}
	var ack WorktreeMergeTargetAbsorbedAcknowledgement
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ack); err != nil {
		return WorktreeMergeTargetAbsorbedAcknowledgement{}, path, fmt.Errorf("read target-absorbed acknowledgement %s: %w", path, err)
	}
	commits, _ := worktreeMergeTargetAbsorptionCommits(receipt)
	if ack.SchemaVersion != worktreeMergeTargetAbsorbedSchemaVersion || ack.ReceiptPath != receipt.ReceiptPath ||
		ack.ReceiptSHA256 != receiptSHA256 || ack.ReceiptID != receipt.ID || ack.ReceiptStatus != receipt.Status ||
		ack.Lane != receipt.Lane || ack.Repository != receipt.Repository || ack.Target != receipt.Target ||
		ack.TargetSHA == "" || len(commits) == 0 || !slices.Equal(ack.ProvedCommits, commits) ||
		ack.Actor == "" || ack.Reason == "" || ack.RecordedAt.IsZero() || ack.ID != worktreeMergeTargetAbsorbedAcknowledgementID(ack) {
		return WorktreeMergeTargetAbsorbedAcknowledgement{}, path, fmt.Errorf("target-absorbed acknowledgement %s does not prove receipt %s", path, receipt.ReceiptPath)
	}
	return ack, path, nil
}

// proveWorktreeMergeTargetAbsorbed fetches the receipt's exact remote target
// into the canonical clone and proves every recorded commit, and every live
// head of the candidate branch, is its ancestor. A candidate branch that
// advanced past its recorded commit (a resolution or follow-up committed on
// it) therefore keeps the lane until that work lands too.
func proveWorktreeMergeTargetAbsorbed(ctx context.Context, run runner.Runner, projectsRoot string, receipt WorktreeMergeReceipt, now time.Time) (string, []string, error) {
	if err := worktreeMergeTargetAbsorptionGate(receipt, now); err != nil {
		return "", nil, err
	}
	commits, ok := worktreeMergeTargetAbsorptionCommits(receipt)
	if !ok {
		return "", nil, errors.New("receipt does not record every source head")
	}
	canonical, err := worktrees.CanonicalRepositoryPath(projectsRoot, receipt.Repository)
	if err != nil {
		return "", nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, worktreeMergeTargetAbsorptionFetchTimeout)
	defer cancel()
	targetSHA, err := fetchExactMergeTargetWithRunner(ctx, run, canonical, receipt.Target)
	if err != nil {
		return "", nil, err
	}
	heads := append([]string(nil), commits...)
	if branch := strings.TrimSpace(receipt.Candidate.Branch); branch != "" {
		local, _, localErr := runCommand(ctx, run, 0, 0, canonical, "git", "for-each-ref", "--format=%(objectname)", "refs/heads/"+branch)
		if localErr != nil {
			return "", nil, localErr
		}
		remote, _, remoteErr := runCommand(ctx, run, 0, 0, canonical, "git", "ls-remote", "--heads", "origin", "refs/heads/"+branch)
		if remoteErr != nil {
			return "", nil, remoteErr
		}
		for _, line := range strings.Split(strings.TrimSpace(local)+"\n"+strings.TrimSpace(remote), "\n") {
			if fields := strings.Fields(line); len(fields) > 0 {
				heads = append(heads, fields[0])
			}
		}
	}
	for _, head := range heads {
		absorbed, ancestorErr := isMergeAncestorWithRunner(ctx, run, canonical, head, targetSHA)
		if ancestorErr != nil {
			return "", nil, ancestorErr
		}
		if !absorbed {
			return "", nil, fmt.Errorf("commit %s is not in %s at %s", head, receipt.Target, targetSHA)
		}
	}
	return targetSHA, commits, nil
}

// recordWorktreeMergeTargetAbsorbed binds proved evidence to the receipt's
// current bytes and publishes it without replacing an existing file. A
// receipt rewritten after the proof was read is refused rather than bound to
// evidence about a different revision; a concurrent writer that published
// first wins when its acknowledgement proves this exact revision too.
func recordWorktreeMergeTargetAbsorbed(proved WorktreeMergeReceipt, targetSHA string, commits []string, actor, reason string, now time.Time) (WorktreeMergeTargetAbsorbedAcknowledgement, string, error) {
	receiptSHA256, err := worktreeMergeReceiptSHA256(proved.ReceiptPath)
	if err != nil {
		return WorktreeMergeTargetAbsorbedAcknowledgement{}, "", err
	}
	current, err := readWorktreeMergeReceipt(proved.ReceiptPath)
	if err != nil {
		return WorktreeMergeTargetAbsorbedAcknowledgement{}, "", err
	}
	// Hash, parse, and hash again: the acknowledgement binds only bytes that
	// did not change while they were checked against the proof. A receipt
	// that vanished meanwhile hashes empty and so counts as changed.
	settledSHA256, _ := worktreeMergeReceiptSHA256(proved.ReceiptPath)
	currentCommits, _ := worktreeMergeTargetAbsorptionCommits(current)
	if settledSHA256 != receiptSHA256 || current.Status != proved.Status || current.ID != proved.ID || !slices.Equal(currentCommits, commits) {
		return WorktreeMergeTargetAbsorbedAcknowledgement{}, "", fmt.Errorf("receipt %s changed while its target absorption was proved", proved.ReceiptPath)
	}
	ack := WorktreeMergeTargetAbsorbedAcknowledgement{
		SchemaVersion: worktreeMergeTargetAbsorbedSchemaVersion,
		ReceiptPath:   current.ReceiptPath, ReceiptSHA256: receiptSHA256, ReceiptID: current.ID, ReceiptStatus: current.Status,
		Lane: current.Lane, Repository: current.Repository, Target: current.Target,
		TargetSHA: targetSHA, ProvedCommits: commits,
		Actor: actor, Reason: reason, RecordedAt: now.UTC(),
	}
	ack.ID = worktreeMergeTargetAbsorbedAcknowledgementID(ack)
	path := worktreeMergeTargetAbsorbedAcknowledgementPath(current.ReceiptPath, receiptSHA256)
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return WorktreeMergeTargetAbsorbedAcknowledgement{}, "", err
	}
	defer func() { _ = directory.Close() }()
	if writeErr := filewrite.WriteJSONImmutableAt(directory, filepath.Base(path), ack, false, nil); writeErr != nil {
		if existing, _, readErr := readWorktreeMergeTargetAbsorbedAcknowledgement(current); readErr == nil {
			return existing, path, nil
		}
		return WorktreeMergeTargetAbsorbedAcknowledgement{}, "", writeErr
	}
	return ack, path, nil
}

// worktreeMergeReceiptReleasedByTargetAbsorption reports whether receipt no
// longer owns its lane because its recorded work already reached the remote
// target. It releases only with an audited acknowledgement for the receipt's
// exact bytes: an existing one, or one it proves and records now. Every
// failure (offline fetch, missing objects, a tampered acknowledgement, a
// failed write) keeps the receipt blocking exactly as before.
func worktreeMergeReceiptReleasedByTargetAbsorption(ctx context.Context, run runner.Runner, projectsRoot string, receipt WorktreeMergeReceipt) bool {
	_, _, err := readWorktreeMergeTargetAbsorbedAcknowledgement(receipt)
	if err == nil {
		return true
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false
	}
	targetSHA, commits, err := proveWorktreeMergeTargetAbsorbed(ctx, run, projectsRoot, receipt, time.Now())
	if err != nil {
		return false
	}
	reason := fmt.Sprintf("every recorded commit is an ancestor of freshly fetched %s at %s; nothing is left to land", receipt.Target, targetSHA)
	_, _, err = recordWorktreeMergeTargetAbsorbed(receipt, targetSHA, commits, WorktreeMergeTargetAbsorbedScanActor, reason, time.Now())
	return err == nil
}

// Outcomes reported by SweepTargetAbsorbedWorktreeMergeReceipts.
const (
	WorktreeMergeTargetAbsorbedReleased        = "released"
	WorktreeMergeTargetAbsorbedWouldRelease    = "would_release"
	WorktreeMergeTargetAbsorbedAlreadyReleased = "already_released"
	WorktreeMergeTargetAbsorbedNotAbsorbed     = "not_absorbed"
	WorktreeMergeTargetAbsorbedIneligible      = "ineligible"
)

// WorktreeMergeTargetAbsorbedSweepOptions configures
// SweepTargetAbsorbedWorktreeMergeReceipts. Empty Receipts sweeps every
// receipt in the reports directory.
type WorktreeMergeTargetAbsorbedSweepOptions struct {
	ProjectsRoot string
	Receipts     []string
	Apply        bool
	// Actor and Reason are required with Apply and recorded on every
	// acknowledgement the sweep writes.
	Actor  string
	Reason string
}

// WorktreeMergeTargetAbsorbedSweepResult is one receipt's sweep outcome.
type WorktreeMergeTargetAbsorbedSweepResult struct {
	Receipt             string `json:"receipt"`
	Lane                string `json:"lane"`
	Status              string `json:"status"`
	Outcome             string `json:"outcome"`
	TargetSHA           string `json:"target_sha,omitempty"`
	AcknowledgementPath string `json:"acknowledgement_path,omitempty"`
	Detail              string `json:"detail,omitempty"`
}

// WorktreeMergeTargetAbsorbedSweep is one sweep's per-receipt outcomes.
type WorktreeMergeTargetAbsorbedSweep struct {
	Results []WorktreeMergeTargetAbsorbedSweepResult `json:"results"`
}

// SweepTargetAbsorbedWorktreeMergeReceipts proves, for every eligible
// non-complete receipt, whether its recorded work already reached the remote
// target, and with Apply records the same audited acknowledgement the lane
// scan writes on demand. It never rewrites a receipt, worktree, or branch.
func SweepTargetAbsorbedWorktreeMergeReceipts(ctx context.Context, options WorktreeMergeTargetAbsorbedSweepOptions) (WorktreeMergeTargetAbsorbedSweep, error) {
	results, err := sweepTargetAbsorbedWorktreeMergeReceipts(ctx, defaultRunner, options, time.Now)
	return WorktreeMergeTargetAbsorbedSweep{Results: results}, err
}

func sweepTargetAbsorbedWorktreeMergeReceipts(ctx context.Context, run runner.Runner, options WorktreeMergeTargetAbsorbedSweepOptions, now func() time.Time) ([]WorktreeMergeTargetAbsorbedSweepResult, error) {
	if strings.TrimSpace(options.ProjectsRoot) == "" {
		return nil, errors.New("projects root is required")
	}
	if options.Apply && (strings.TrimSpace(options.Actor) == "" || strings.TrimSpace(options.Reason) == "") {
		return nil, errors.New("--actor and --reason are required with --apply")
	}
	options.ProjectsRoot, _ = filepath.Abs(strings.TrimSpace(options.ProjectsRoot))
	paths, err := targetAbsorbedSweepReceiptPaths(options, wbhome.Root)
	if err != nil {
		return nil, err
	}
	var results []WorktreeMergeTargetAbsorbedSweepResult
	for _, path := range paths {
		receipt, err := readWorktreeMergeReceipt(path)
		if err != nil {
			// One unreadable file must not hide the rest of the sweep.
			results = append(results, WorktreeMergeTargetAbsorbedSweepResult{Receipt: path, Outcome: WorktreeMergeTargetAbsorbedNotAbsorbed, Detail: err.Error()})
			continue
		}
		result := WorktreeMergeTargetAbsorbedSweepResult{Receipt: path, Lane: receipt.Lane, Status: string(receipt.Status)}
		if receipt.Status == WorktreeMergeComplete {
			continue
		}
		if released, releaseErr := worktreeMergeReceiptReleasedByRecordedEvidence(ctx, options.ProjectsRoot, run, receipt); releaseErr != nil {
			result.Outcome, result.Detail = WorktreeMergeTargetAbsorbedNotAbsorbed, releaseErr.Error()
			results = append(results, result)
			continue
		} else if released {
			result.Outcome, result.Detail = WorktreeMergeTargetAbsorbedAlreadyReleased, "released by earlier recorded evidence"
			results = append(results, result)
			continue
		}
		if ack, ackPath, ackErr := readWorktreeMergeTargetAbsorbedAcknowledgement(receipt); ackErr == nil {
			result.Outcome, result.TargetSHA, result.AcknowledgementPath = WorktreeMergeTargetAbsorbedAlreadyReleased, ack.TargetSHA, ackPath
			results = append(results, result)
			continue
		} else if !errors.Is(ackErr, os.ErrNotExist) {
			result.Outcome, result.Detail = WorktreeMergeTargetAbsorbedNotAbsorbed, ackErr.Error()
			results = append(results, result)
			continue
		}
		if gateErr := worktreeMergeTargetAbsorptionGate(receipt, now()); gateErr != nil {
			result.Outcome, result.Detail = WorktreeMergeTargetAbsorbedIneligible, gateErr.Error()
			results = append(results, result)
			continue
		}
		targetSHA, commits, proveErr := proveWorktreeMergeTargetAbsorbed(ctx, run, options.ProjectsRoot, receipt, now())
		if proveErr != nil {
			result.Outcome, result.Detail = WorktreeMergeTargetAbsorbedNotAbsorbed, proveErr.Error()
			results = append(results, result)
			continue
		}
		result.TargetSHA = targetSHA
		if !options.Apply {
			result.Outcome = WorktreeMergeTargetAbsorbedWouldRelease
			results = append(results, result)
			continue
		}
		reason := fmt.Sprintf("%s: every recorded commit is an ancestor of freshly fetched %s at %s; nothing is left to land", strings.TrimSpace(options.Reason), receipt.Target, targetSHA)
		_, ackPath, recordErr := recordWorktreeMergeTargetAbsorbed(receipt, targetSHA, commits, strings.TrimSpace(options.Actor), reason, now())
		if recordErr != nil {
			result.Outcome, result.Detail = WorktreeMergeTargetAbsorbedNotAbsorbed, recordErr.Error()
			results = append(results, result)
			continue
		}
		result.Outcome, result.AcknowledgementPath = WorktreeMergeTargetAbsorbedReleased, ackPath
		results = append(results, result)
	}
	return results, nil
}

// targetAbsorbedSweepReceiptPaths takes rootFor so the WB-home resolution
// failure, unreachable for an absolute projects root, stays testable.
func targetAbsorbedSweepReceiptPaths(options WorktreeMergeTargetAbsorbedSweepOptions, rootFor func(string) (string, error)) ([]string, error) {
	if len(options.Receipts) > 0 {
		paths := make([]string, 0, len(options.Receipts))
		for _, input := range options.Receipts {
			path, err := resolveWorktreeMergeReceiptPath(options.ProjectsRoot, input)
			if err != nil {
				return nil, err
			}
			paths = append(paths, path)
		}
		return paths, nil
	}
	home, err := rootFor(options.ProjectsRoot)
	if err != nil {
		return nil, err
	}
	reportsDir := filepath.Join(home, "reports", "worktree-merge")
	entries, err := os.ReadDir(reportsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" || isWorktreeMergeReportSidecar(entry.Name()) {
			continue
		}
		paths = append(paths, filepath.Join(reportsDir, entry.Name()))
	}
	return paths, nil
}
