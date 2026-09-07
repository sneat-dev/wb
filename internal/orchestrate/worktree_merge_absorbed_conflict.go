package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/mergeack"
)

// worktreeMergeAbsorbedConflictAcknowledgementSuffix is kept as a plain alias
// of mergeack.FileSuffix: internal/orchestrate/worktree_merge.go's report
// directory scans (which this file does not own) name it directly, and
// aliasing rather than duplicating the literal means the two can never
// diverge.
const worktreeMergeAbsorbedConflictAcknowledgementSuffix = mergeack.FileSuffix

// WorktreeMergeAbsorbedConflictSourceProof, WorktreeMergeAbsorbedConflictPathProof,
// and WorktreeMergeAbsorbedConflictAcknowledgement alias internal/mergeack's
// types directly (rather than duplicating them) so this package's on-disk
// format, identity hash, and validation are always exactly the ones
// internal/mergeack defines -- the single source of truth internal/worktrees
// also reads. See internal/mergeack's package doc for the full rationale.
type (
	WorktreeMergeAbsorbedConflictSourceProof     = mergeack.SourceProof
	WorktreeMergeAbsorbedConflictPathProof       = mergeack.PathProof
	WorktreeMergeAbsorbedConflictAcknowledgement = mergeack.Acknowledgement
)

// WorktreeMergeAbsorbedConflictAcknowledgementOptions configures
// AcknowledgeAbsorbedConflict.
type WorktreeMergeAbsorbedConflictAcknowledgementOptions struct {
	ProjectsRoot string
	Receipt      string
	Apply        bool
	Actor        string
	Reason       string
	// DerivedPaths audits an operator exclusion for derived/generated files
	// (repo-relative, repeatable) -- each must exist on the freshly fetched
	// target and match the built-in derived-index allowlist
	// (isAbsorbedConflictDerivedPathAllowed), or the whole call refuses.
	DerivedPaths []string
}

// AcknowledgeAbsorbedConflict proves, for every receipted source of a
// prepare-phase conflict receipt whose source worktrees are all gone, that
// the source's exact content is already reachable from the freshly fetched
// current remote target -- either because the source SHA is a graph ancestor
// of that target, or because every path it changed relative to its
// merge-base with the target now carries an identical blob there -- then
// records a separate audited acknowledgement so a fresh candidate can own the
// merger lane. It never reads or requires a receipted source worktree (that
// infrastructure being gone is exactly the failure this recovers from), never
// rewrites the historical receipt or any Work Log, and never deletes the
// preserved, unpublished candidate worktree. This is a dry-run by default;
// --apply requires --actor and --reason and writes only the new
// acknowledgement artifact.
func AcknowledgeAbsorbedConflict(ctx context.Context, options WorktreeMergeAbsorbedConflictAcknowledgementOptions) (WorktreeMergeAbsorbedConflictAcknowledgement, error) {
	receiptPath, err := resolveWorktreeMergeReceiptPath(options.ProjectsRoot, options.Receipt)
	if err != nil {
		return WorktreeMergeAbsorbedConflictAcknowledgement{}, err
	}
	receipt, err := readWorktreeMergeReceipt(receiptPath)
	if err != nil {
		return WorktreeMergeAbsorbedConflictAcknowledgement{}, err
	}
	if err := validateAbsorbedConflictReceipt(receipt, receiptPath); err != nil {
		return WorktreeMergeAbsorbedConflictAcknowledgement{}, err
	}
	if options.Apply && (strings.TrimSpace(options.Actor) == "" || strings.TrimSpace(options.Reason) == "") {
		return WorktreeMergeAbsorbedConflictAcknowledgement{}, errors.New("--actor and --reason are required with --apply")
	}
	lockID := receipt.Lane
	if lockID == "" {
		lockID = worktreeMergeLaneID(receipt.Repository, receipt.Target)
	}
	lock, err := AcquireOperationLock(options.ProjectsRoot, lockID, true)
	if err != nil {
		return WorktreeMergeAbsorbedConflictAcknowledgement{}, err
	}
	defer func() { _ = lock.Release() }()

	// Re-read and re-validate beneath the lane lock: every proof below must
	// run against evidence observed while this acknowledgement exclusively
	// owns the lane, never against values captured before it.
	receipt, err = readWorktreeMergeReceipt(receiptPath)
	if err != nil {
		return WorktreeMergeAbsorbedConflictAcknowledgement{}, err
	}
	if err := validateAbsorbedConflictReceipt(receipt, receiptPath); err != nil {
		return WorktreeMergeAbsorbedConflictAcknowledgement{}, err
	}
	for _, source := range receipt.Sources {
		if _, statErr := os.Stat(source.Worktree); statErr == nil {
			return WorktreeMergeAbsorbedConflictAcknowledgement{}, fmt.Errorf("receipted source worktree %s still exists; use resume, supersede-validation-failed, or prepare-conflict-replacement instead", source.Worktree)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return WorktreeMergeAbsorbedConflictAcknowledgement{}, fmt.Errorf("inspect receipted source %s: %w", source.Worktree, statErr)
		}
	}
	candidateInfo, statErr := os.Stat(receipt.Candidate.Worktree)
	if statErr != nil || !candidateInfo.IsDir() {
		return WorktreeMergeAbsorbedConflictAcknowledgement{}, fmt.Errorf("candidate worktree %s is required for read-only git object resolution and is missing: %v", receipt.Candidate.Worktree, statErr)
	}
	if err := requireAbsorbedConflictCandidateUnpublished(ctx, receipt); err != nil {
		return WorktreeMergeAbsorbedConflictAcknowledgement{}, err
	}
	currentTarget, err := fetchExactMergeTarget(ctx, receipt.Candidate.Worktree, receipt.Target)
	if err != nil {
		return WorktreeMergeAbsorbedConflictAcknowledgement{}, err
	}
	excusedDerivedPaths, derivedPathSet, err := validateAbsorbedConflictDerivedPaths(ctx, receipt.Candidate.Worktree, currentTarget, options.DerivedPaths)
	if err != nil {
		return WorktreeMergeAbsorbedConflictAcknowledgement{}, err
	}
	proofs := make([]WorktreeMergeAbsorbedConflictSourceProof, 0, len(receipt.Sources))
	for _, source := range receipt.Sources {
		result, proofErr := proveAbsorbedConflictSource(ctx, receipt.Candidate.Worktree, currentTarget, source, derivedPathSet)
		if proofErr != nil {
			return WorktreeMergeAbsorbedConflictAcknowledgement{}, fmt.Errorf("receipted source %s: %w", source.Branch, proofErr)
		}
		proofs = append(proofs, WorktreeMergeAbsorbedConflictSourceProof{
			Task: source.Task, Worktree: source.Worktree, Branch: source.Branch, SHA: source.SHA,
			Method: result.method, MergeBaseSHA: result.mergeBaseSHA, PathCount: result.pathCount, PathProofs: result.pathProofs,
		})
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receiptPath)
	if err != nil {
		return WorktreeMergeAbsorbedConflictAcknowledgement{}, err
	}
	ackPath := absorbedConflictAcknowledgementPath(receiptPath)
	ack := WorktreeMergeAbsorbedConflictAcknowledgement{
		SchemaVersion: mergeack.SchemaVersion, Status: mergeack.Status,
		ReceiptPath: receiptPath, AcknowledgementPath: ackPath,
		ReceiptID: receipt.ID, ReceiptSHA256: receiptHash, ReceiptStatus: string(receipt.Status), Lane: receipt.Lane,
		Repository: receipt.Repository, Target: receipt.Target, ReceiptTargetSHA: receipt.TargetSHA, CurrentTargetSHA: currentTarget,
		CandidateTask: receipt.Candidate.Task, CandidateWorktree: receipt.Candidate.Worktree, CandidateBranch: receipt.Candidate.Branch, CandidateSHA: receipt.Candidate.SHA,
		Sources: mergeAckSources(receipt.Sources), SourceProofs: proofs, ExcusedDerivedPaths: excusedDerivedPaths,
		Actor: strings.TrimSpace(options.Actor), Reason: strings.TrimSpace(options.Reason), RecordedAt: time.Now().UTC(),
	}
	ack.ID = mergeack.ComputeID(ack)
	if existing, readErr := readAbsorbedConflictAcknowledgement(ackPath, receipt); readErr == nil {
		if !mergeack.Same(existing, ack) {
			return WorktreeMergeAbsorbedConflictAcknowledgement{}, fmt.Errorf("absorbed-conflict acknowledgement %s binds different immutable evidence", ackPath)
		}
		return existing, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return WorktreeMergeAbsorbedConflictAcknowledgement{}, readErr
	}
	if !options.Apply {
		return ack, nil
	}
	if err := mergeack.Persist(ackPath, ack); err != nil {
		return WorktreeMergeAbsorbedConflictAcknowledgement{}, err
	}
	return ack, nil
}

// mergeAckSources converts a receipt's []WorktreeMergeSource into the
// []mergeack.Source shape an Acknowledgement carries, dropping the Merged
// flag mergeack has no use for.
func mergeAckSources(sources []WorktreeMergeSource) []mergeack.Source {
	converted := make([]mergeack.Source, 0, len(sources))
	for _, source := range sources {
		converted = append(converted, mergeack.Source{Task: source.Task, Worktree: source.Worktree, Branch: source.Branch, SHA: source.SHA})
	}
	return converted
}

// mergeAckReceiptIdentity narrows receipt down to the fields mergeack.Load
// validates an acknowledgement against.
func mergeAckReceiptIdentity(receipt WorktreeMergeReceipt) mergeack.ReceiptIdentity {
	return mergeack.ReceiptIdentity{
		Path: receipt.ReceiptPath, ID: receipt.ID, Status: string(receipt.Status), Lane: receipt.Lane,
		Repository: receipt.Repository, Target: receipt.Target, TargetSHA: receipt.TargetSHA,
		Candidate: mergeack.Source{Task: receipt.Candidate.Task, Worktree: receipt.Candidate.Worktree, Branch: receipt.Candidate.Branch, SHA: receipt.Candidate.SHA},
		Sources:   mergeAckSources(receipt.Sources),
	}
}

// validateAbsorbedConflictReceipt narrowly scopes eligibility to an
// unpublished prepare-phase conflict: a real merge/validation conflict that
// never reached publication or landing. A published candidate is
// acknowledge-stranded-landing's or acknowledge-landed-failed's territory
// instead; a validation_failed receipt belongs to
// supersede-validation-failed/acknowledge-landed-failed.
func validateAbsorbedConflictReceipt(receipt WorktreeMergeReceipt, receiptPath string) error {
	if receipt.ReceiptPath != receiptPath || receipt.ID == "" || receipt.Lane == "" || receipt.Lane != worktreeMergeLaneID(receipt.Repository, receipt.Target) {
		return fmt.Errorf("receipt %s has inconsistent immutable receipt identity", receiptPath)
	}
	if receipt.Phase != WorktreeMergePhasePrepare || receipt.Status != WorktreeMergeConflict {
		return fmt.Errorf("receipt %s is %s/%s, want an unpublished prepare conflict", receiptPath, receipt.Phase, receipt.Status)
	}
	if receipt.LandingSHA != "" {
		return fmt.Errorf("receipt %s already recorded a landing SHA %s; use acknowledge-landed-failed instead", receiptPath, receipt.LandingSHA)
	}
	unpublished, unpublishedErr := effectiveUnpublishedConflict(receipt)
	if unpublishedErr != nil {
		return unpublishedErr
	}
	if !unpublished {
		return fmt.Errorf("receipt %s already published a candidate; use acknowledge-stranded-landing instead", receiptPath)
	}
	if receipt.Repository == "" || receipt.Target == "" || receipt.TargetSHA == "" {
		return fmt.Errorf("receipt %s lacks complete immutable repository or target identity", receiptPath)
	}
	if receipt.Candidate.Task == "" || receipt.Candidate.Worktree == "" || receipt.Candidate.Branch == "" {
		return fmt.Errorf("receipt %s lacks complete immutable candidate identity", receiptPath)
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

// requireAbsorbedConflictCandidateUnpublished proves the preserved candidate
// branch carries no remote publication. An empty candidate SHA (prepare
// failed before computing one) is trivially unpublished.
func requireAbsorbedConflictCandidateUnpublished(ctx context.Context, receipt WorktreeMergeReceipt) error {
	if receipt.Candidate.SHA == "" {
		return nil
	}
	remote, _, err := runCommand(ctx, 0, 0, receipt.Candidate.Worktree, "git", "ls-remote", "origin", "refs/heads/"+receipt.Candidate.Branch)
	if err != nil {
		return fmt.Errorf("inspect candidate publication state: %w", err)
	}
	if strings.TrimSpace(remote) != "" {
		return fmt.Errorf("candidate branch %s is published; this recovery requires it to remain unpublished", receipt.Candidate.Branch)
	}
	return nil
}

type absorbedConflictProof struct {
	method       string
	mergeBaseSHA string
	pathCount    int
	pathProofs   []WorktreeMergeAbsorbedConflictPathProof
}

// proveAbsorbedConflictSource resolves one receipted source's commit object
// (fetching the origin branch only if the object is not already local, since
// the source worktree that would normally hold it is gone) and proves its
// content already reachable from currentTarget, either by graph ancestry, or
// path by path relative to its merge-base with currentTarget: an exact blob
// match ("blob_absorbed"), every line a `*.jsonl` append-only ledger added
// present verbatim in the target's copy ("lines_absorbed"), or an
// operator-audited derived-index exclusion present in derivedPaths
// ("derived_excused").
func proveAbsorbedConflictSource(ctx context.Context, worktree, currentTarget string, source WorktreeMergeSource, derivedPaths map[string]bool) (absorbedConflictProof, error) {
	if err := resolveAbsorbedConflictSourceObject(ctx, worktree, source); err != nil {
		return absorbedConflictProof{}, err
	}
	ancestor, err := isMergeAncestor(ctx, worktree, source.SHA, currentTarget)
	if err != nil {
		return absorbedConflictProof{}, fmt.Errorf("verify source ancestry: %w", err)
	}
	if ancestor {
		return absorbedConflictProof{method: "ancestor"}, nil
	}
	mergeBaseOutput, _, err := runCommand(ctx, 0, 0, worktree, "git", "merge-base", source.SHA, currentTarget)
	if err != nil {
		return absorbedConflictProof{}, fmt.Errorf("resolve merge base with current target: %w", err)
	}
	mergeBase := strings.TrimSpace(mergeBaseOutput)
	diffOutput, _, err := runCommand(ctx, 0, 0, worktree, "git", "diff", "--name-only", mergeBase, source.SHA)
	if err != nil {
		return absorbedConflictProof{}, fmt.Errorf("diff source from its merge base with current target: %w", err)
	}
	paths := nonEmptyTrimmedLines(diffOutput)
	if len(paths) == 0 {
		return absorbedConflictProof{}, fmt.Errorf("source changed no path relative to its merge base %s with the current target; it is neither an ancestor nor content-absorbed", mergeBase)
	}
	pathProofs := make([]WorktreeMergeAbsorbedConflictPathProof, 0, len(paths))
	for _, path := range paths {
		if derivedPaths[path] {
			pathProofs = append(pathProofs, WorktreeMergeAbsorbedConflictPathProof{Path: path, Method: "derived_excused"})
			continue
		}
		if strings.HasSuffix(path, ".jsonl") {
			added, matched, linesErr := proveLinesAbsorbedPath(ctx, worktree, mergeBase, source.SHA, currentTarget, path)
			if linesErr != nil {
				return absorbedConflictProof{}, linesErr
			}
			pathProofs = append(pathProofs, WorktreeMergeAbsorbedConflictPathProof{Path: path, Method: "lines_absorbed", AddedLines: added, MatchedLines: matched})
			continue
		}
		sourceBlob, sourcePresent := gitBlobAtPath(ctx, worktree, source.SHA, path)
		targetBlob, targetPresent := gitBlobAtPath(ctx, worktree, currentTarget, path)
		if sourcePresent != targetPresent || sourceBlob != targetBlob {
			return absorbedConflictProof{}, fmt.Errorf("path %q is not content-absorbed: current target %s does not carry the exact blob source %s carries", path, currentTarget, source.SHA)
		}
		pathProofs = append(pathProofs, WorktreeMergeAbsorbedConflictPathProof{Path: path, Method: "blob_absorbed"})
	}
	return absorbedConflictProof{method: "content_absorbed", mergeBaseSHA: mergeBase, pathCount: len(paths), pathProofs: pathProofs}, nil
}

// proveLinesAbsorbedPath proves every line source added to path (relative to
// mergeBase), excluding the unified-diff file header, is present verbatim as
// a whole line in the target's copy of path. A path with zero added lines is
// trivially absorbed. A path the source added lines to but that is absent
// from the target refuses closed.
func proveLinesAbsorbedPath(ctx context.Context, worktree, mergeBase, sourceSHA, targetSHA, path string) (added, matched int, err error) {
	addedLines, err := gitDiffAddedLines(ctx, worktree, mergeBase, sourceSHA, path)
	if err != nil {
		return 0, 0, fmt.Errorf("diff added lines for %q: %w", path, err)
	}
	if len(addedLines) == 0 {
		return 0, 0, nil
	}
	targetLines, targetPresent := gitFileLines(ctx, worktree, targetSHA, path)
	if !targetPresent {
		return 0, 0, fmt.Errorf("path %q is not lines-absorbed: current target %s does not carry the file at all", path, targetSHA)
	}
	targetLineSet := make(map[string]bool, len(targetLines))
	for _, line := range targetLines {
		targetLineSet[line] = true
	}
	for _, line := range addedLines {
		if !targetLineSet[line] {
			return 0, 0, fmt.Errorf("path %q is not lines-absorbed: an added line is not present verbatim in the current target %s", path, targetSHA)
		}
	}
	return len(addedLines), len(addedLines), nil
}

// gitDiffAddedLines returns every line added by sourceSHA to path relative
// to mergeBase, excluding the unified-diff file header (the "+++" line) and
// hunk metadata, with the leading "+" stripped.
func gitDiffAddedLines(ctx context.Context, worktree, mergeBase, sourceSHA, path string) ([]string, error) {
	output, _, err := runCommand(ctx, 0, 0, worktree, "git", "diff", "--no-color", "-U0", mergeBase, sourceSHA, "--", path)
	if err != nil {
		return nil, err
	}
	var added []string
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "+") || strings.HasPrefix(line, "+++") {
			continue
		}
		added = append(added, strings.TrimPrefix(line, "+"))
	}
	return added, nil
}

// gitFileLines returns revision's copy of path split into lines, or
// present=false if the path does not exist at that revision.
func gitFileLines(ctx context.Context, worktree, revision, path string) (lines []string, present bool) {
	output, _, err := runCommand(ctx, 0, 0, worktree, "git", "show", revision+":"+path)
	if err != nil {
		return nil, false
	}
	trimmed := strings.TrimSuffix(output, "\n")
	if trimmed == "" {
		return []string{}, true
	}
	return strings.Split(trimmed, "\n"), true
}

// validateAbsorbedConflictDerivedPaths normalizes and validates the
// operator-supplied --derived-path exclusions: each must match the built-in
// derived-index allowlist and must exist on the freshly fetched
// currentTarget (a path excused this way is never permitted to be one the
// target deleted). Returns the deduplicated, sorted, validated paths for the
// sidecar record alongside a lookup set for per-path proof.
func validateAbsorbedConflictDerivedPaths(ctx context.Context, worktree, currentTarget string, rawPaths []string) ([]string, map[string]bool, error) {
	if len(rawPaths) == 0 {
		return nil, nil, nil
	}
	seen := make(map[string]bool, len(rawPaths))
	var excused []string
	for _, raw := range rawPaths {
		path := strings.TrimSpace(raw)
		path = strings.TrimPrefix(path, "./")
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		if !mergeack.IsDerivedPathAllowed(path) {
			return nil, nil, fmt.Errorf("--derived-path %q is not an allowed derived-index shape (spec/**/README.md)", path)
		}
		if _, present := gitBlobAtPath(ctx, worktree, currentTarget, path); !present {
			return nil, nil, fmt.Errorf("--derived-path %q does not exist on the current target %s", path, currentTarget)
		}
		excused = append(excused, path)
	}
	slices.Sort(excused)
	derivedSet := make(map[string]bool, len(excused))
	for _, path := range excused {
		derivedSet[path] = true
	}
	return excused, derivedSet, nil
}

// resolveAbsorbedConflictSourceObject proves the receipted source commit is
// reachable in worktree's shared object store, fetching the receipted origin
// branch once if it is not already local. A source SHA reachable from
// neither the local store nor the current tip of its own origin branch
// refuses closed.
func resolveAbsorbedConflictSourceObject(ctx context.Context, worktree string, source WorktreeMergeSource) error {
	if _, _, err := runCommand(ctx, 0, 0, worktree, "git", "cat-file", "-e", source.SHA+"^{commit}"); err == nil {
		return nil
	}
	if _, _, err := runCommand(ctx, 0, 0, worktree, "git", "fetch", "--no-tags", "origin", source.Branch); err != nil {
		return fmt.Errorf("fetch origin %s to resolve receipted source %s: %w", source.Branch, source.SHA, err)
	}
	if _, _, err := runCommand(ctx, 0, 0, worktree, "git", "cat-file", "-e", source.SHA+"^{commit}"); err != nil {
		return fmt.Errorf("receipted source commit %s is reachable from neither the local object store nor the current origin %s", source.SHA, source.Branch)
	}
	return nil
}

// gitBlobAtPath resolves the blob at path in revision. Any failure --
// including the path genuinely being absent at that revision -- is treated
// as "absent" so a real infrastructure error fails the equality comparison
// closed rather than silently passing it.
func gitBlobAtPath(ctx context.Context, worktree, revision, path string) (blob string, present bool) {
	output, _, err := runCommand(ctx, 0, 0, worktree, "git", "rev-parse", "--verify", "-q", revision+":"+path)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(output), true
}

func nonEmptyTrimmedLines(value string) []string {
	var lines []string
	for _, line := range strings.Split(value, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

func absorbedConflictAcknowledgementPath(receiptPath string) string {
	return mergeack.Path(receiptPath)
}

// readAbsorbedConflictAcknowledgement reads and fully validates the
// acknowledgement sidecar at path against receipt, delegating the on-disk
// format, identity hash, and validation to internal/mergeack -- the single
// source of truth internal/worktrees's cleanup proof also reads.
func readAbsorbedConflictAcknowledgement(path string, receipt WorktreeMergeReceipt) (WorktreeMergeAbsorbedConflictAcknowledgement, error) {
	return mergeack.Load(path, mergeAckReceiptIdentity(receipt))
}

func hasAbsorbedConflictAcknowledgement(receipt WorktreeMergeReceipt) (bool, error) {
	_, err := readAbsorbedConflictAcknowledgement(absorbedConflictAcknowledgementPath(receipt.ReceiptPath), receipt)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
