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
	worktreeMergeAbsorbedConflictAcknowledgementSchemaVersion = 1
	worktreeMergeAbsorbedConflictAcknowledgementSuffix        = ".absorbed-conflict.ack.json"
)

// WorktreeMergeAbsorbedConflictSourceProof records, for one receipted source,
// how its content was proved already reachable from the current remote
// target: either the receipted source SHA is a direct ancestor of the
// freshly fetched target head ("ancestor"), or every path it changed
// relative to its merge-base with the target now carries an identical blob
// on the target ("content_absorbed") -- typically because an unrelated later
// commit landed the same content. MergeBaseSHA and PathCount are populated
// only for the content_absorbed method.
type WorktreeMergeAbsorbedConflictSourceProof struct {
	Task         string `json:"task"`
	Worktree     string `json:"worktree"`
	Branch       string `json:"branch"`
	SHA          string `json:"sha"`
	Method       string `json:"method"`
	MergeBaseSHA string `json:"merge_base_sha,omitempty"`
	PathCount    int    `json:"path_count,omitempty"`
}

// WorktreeMergeAbsorbedConflictAcknowledgement is a separate, append-only
// acknowledgement for an unpublished prepare-phase conflict receipt whose
// every receipted source worktree is already gone from disk -- so neither an
// ordinary resume nor prepare-conflict-replacement/supersede-validation-failed
// can recover it -- yet whose exact receipted content is proved, source by
// source, to already be reachable from the freshly fetched current remote
// target either by graph ancestry or by identical-blob content absorption.
// It never rewrites the historical receipt or any Work Log, and it never
// deletes the preserved, unpublished candidate worktree; it only frees the
// merger lane so a fresh candidate can be prepared.
type WorktreeMergeAbsorbedConflictAcknowledgement struct {
	SchemaVersion       int                                        `json:"schema_version"`
	ID                  string                                     `json:"id"`
	Status              string                                     `json:"status"`
	ReceiptPath         string                                     `json:"receipt_path"`
	AcknowledgementPath string                                     `json:"acknowledgement_path"`
	ReceiptID           string                                     `json:"receipt_id"`
	ReceiptSHA256       string                                     `json:"receipt_sha256"`
	ReceiptStatus       WorktreeMergeStatus                        `json:"receipt_status"`
	Lane                string                                     `json:"lane"`
	Repository          string                                     `json:"repository"`
	Target              string                                     `json:"target"`
	ReceiptTargetSHA    string                                     `json:"receipt_target_sha"`
	CurrentTargetSHA    string                                     `json:"current_target_sha"`
	CandidateTask       string                                     `json:"candidate_task"`
	CandidateWorktree   string                                     `json:"candidate_worktree"`
	CandidateBranch     string                                     `json:"candidate_branch"`
	CandidateSHA        string                                     `json:"candidate_sha,omitempty"`
	Sources             []WorktreeMergeSource                      `json:"sources"`
	SourceProofs        []WorktreeMergeAbsorbedConflictSourceProof `json:"source_proofs"`
	Actor               string                                     `json:"actor"`
	Reason              string                                     `json:"reason"`
	RecordedAt          time.Time                                  `json:"recorded_at"`
}

// WorktreeMergeAbsorbedConflictAcknowledgementOptions configures
// AcknowledgeAbsorbedConflict.
type WorktreeMergeAbsorbedConflictAcknowledgementOptions struct {
	ProjectsRoot string
	Receipt      string
	Apply        bool
	Actor        string
	Reason       string
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
	proofs := make([]WorktreeMergeAbsorbedConflictSourceProof, 0, len(receipt.Sources))
	for _, source := range receipt.Sources {
		result, proofErr := proveAbsorbedConflictSource(ctx, receipt.Candidate.Worktree, currentTarget, source)
		if proofErr != nil {
			return WorktreeMergeAbsorbedConflictAcknowledgement{}, fmt.Errorf("receipted source %s: %w", source.Branch, proofErr)
		}
		proofs = append(proofs, WorktreeMergeAbsorbedConflictSourceProof{
			Task: source.Task, Worktree: source.Worktree, Branch: source.Branch, SHA: source.SHA,
			Method: result.method, MergeBaseSHA: result.mergeBaseSHA, PathCount: result.pathCount,
		})
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receiptPath)
	if err != nil {
		return WorktreeMergeAbsorbedConflictAcknowledgement{}, err
	}
	ackPath := absorbedConflictAcknowledgementPath(receiptPath)
	ack := WorktreeMergeAbsorbedConflictAcknowledgement{
		SchemaVersion: worktreeMergeAbsorbedConflictAcknowledgementSchemaVersion, Status: "absorbed_conflict_acknowledged",
		ReceiptPath: receiptPath, AcknowledgementPath: ackPath,
		ReceiptID: receipt.ID, ReceiptSHA256: receiptHash, ReceiptStatus: receipt.Status, Lane: receipt.Lane,
		Repository: receipt.Repository, Target: receipt.Target, ReceiptTargetSHA: receipt.TargetSHA, CurrentTargetSHA: currentTarget,
		CandidateTask: receipt.Candidate.Task, CandidateWorktree: receipt.Candidate.Worktree, CandidateBranch: receipt.Candidate.Branch, CandidateSHA: receipt.Candidate.SHA,
		Sources: append([]WorktreeMergeSource(nil), receipt.Sources...), SourceProofs: proofs,
		Actor: strings.TrimSpace(options.Actor), Reason: strings.TrimSpace(options.Reason), RecordedAt: time.Now().UTC(),
	}
	ack.ID = absorbedConflictAcknowledgementID(ack)
	if existing, readErr := readAbsorbedConflictAcknowledgement(ackPath, receipt); readErr == nil {
		if !sameAbsorbedConflictAcknowledgement(existing, ack) {
			return WorktreeMergeAbsorbedConflictAcknowledgement{}, fmt.Errorf("absorbed-conflict acknowledgement %s binds different immutable evidence", ackPath)
		}
		return existing, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return WorktreeMergeAbsorbedConflictAcknowledgement{}, readErr
	}
	if !options.Apply {
		return ack, nil
	}
	if err := persistAbsorbedConflictAcknowledgement(ackPath, ack); err != nil {
		return WorktreeMergeAbsorbedConflictAcknowledgement{}, err
	}
	return ack, nil
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
	if receipt.PullRequest != "" || receipt.PublishedCandidateSHA != "" {
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
}

// proveAbsorbedConflictSource resolves one receipted source's commit object
// (fetching the origin branch only if the object is not already local, since
// the source worktree that would normally hold it is gone) and proves its
// content already reachable from currentTarget, either by graph ancestry or
// by exact blob equality on every path it changed relative to its
// merge-base with currentTarget.
func proveAbsorbedConflictSource(ctx context.Context, worktree, currentTarget string, source WorktreeMergeSource) (absorbedConflictProof, error) {
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
	for _, path := range paths {
		sourceBlob, sourcePresent := gitBlobAtPath(ctx, worktree, source.SHA, path)
		targetBlob, targetPresent := gitBlobAtPath(ctx, worktree, currentTarget, path)
		if sourcePresent != targetPresent || sourceBlob != targetBlob {
			return absorbedConflictProof{}, fmt.Errorf("path %q is not content-absorbed: current target %s does not carry the exact blob source %s carries", path, currentTarget, source.SHA)
		}
	}
	return absorbedConflictProof{method: "content_absorbed", mergeBaseSHA: mergeBase, pathCount: len(paths)}, nil
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
	return receiptPath + worktreeMergeAbsorbedConflictAcknowledgementSuffix
}

func absorbedConflictAcknowledgementID(ack WorktreeMergeAbsorbedConflictAcknowledgement) string {
	hash := sha256.New()
	for _, value := range []string{
		ack.ReceiptID, ack.ReceiptPath, ack.ReceiptSHA256, string(ack.ReceiptStatus), ack.Lane, ack.Repository, ack.Target,
		ack.ReceiptTargetSHA, ack.CurrentTargetSHA, ack.CandidateTask, ack.CandidateWorktree, ack.CandidateBranch, ack.CandidateSHA,
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
	_, _ = hash.Write([]byte{0xfe})
	for _, proof := range ack.SourceProofs {
		for _, value := range []string{proof.Task, proof.Worktree, proof.Branch, proof.SHA, proof.Method, proof.MergeBaseSHA} {
			_, _ = hash.Write([]byte(value))
			_, _ = hash.Write([]byte{0})
		}
		_, _ = hash.Write([]byte{byte(proof.PathCount)})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// sameAbsorbedConflictAcknowledgement compares only the immutable proof
// evidence, deliberately excluding Actor/Reason/ID/RecordedAt: a retry that
// repeats the same receipt, target, and per-source proof is idempotent even
// when the operator supplies a different actor or reason on the replay. The
// first successful write is authoritative and is never silently overwritten.
func sameAbsorbedConflictAcknowledgement(left, right WorktreeMergeAbsorbedConflictAcknowledgement) bool {
	if left.ReceiptPath != right.ReceiptPath || left.AcknowledgementPath != right.AcknowledgementPath ||
		left.ReceiptID != right.ReceiptID || left.ReceiptSHA256 != right.ReceiptSHA256 || left.ReceiptStatus != right.ReceiptStatus ||
		left.Lane != right.Lane || left.Repository != right.Repository || left.Target != right.Target ||
		left.ReceiptTargetSHA != right.ReceiptTargetSHA || left.CurrentTargetSHA != right.CurrentTargetSHA ||
		left.CandidateTask != right.CandidateTask || left.CandidateWorktree != right.CandidateWorktree ||
		left.CandidateBranch != right.CandidateBranch || left.CandidateSHA != right.CandidateSHA ||
		!sameWorktreeMergeSources(left.Sources, right.Sources) || len(left.SourceProofs) != len(right.SourceProofs) {
		return false
	}
	for index := range left.SourceProofs {
		if left.SourceProofs[index] != right.SourceProofs[index] {
			return false
		}
	}
	return true
}

func persistAbsorbedConflictAcknowledgement(path string, ack WorktreeMergeAbsorbedConflictAcknowledgement) error {
	contents, err := json.MarshalIndent(ack, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".absorbed-conflict-ack-*.tmp")
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

func readAbsorbedConflictAcknowledgement(path string, receipt WorktreeMergeReceipt) (WorktreeMergeAbsorbedConflictAcknowledgement, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return WorktreeMergeAbsorbedConflictAcknowledgement{}, err
	}
	var ack WorktreeMergeAbsorbedConflictAcknowledgement
	if err := json.Unmarshal(contents, &ack); err != nil {
		return WorktreeMergeAbsorbedConflictAcknowledgement{}, fmt.Errorf("decode absorbed-conflict acknowledgement %s: %w", path, err)
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		return WorktreeMergeAbsorbedConflictAcknowledgement{}, err
	}
	if ack.SchemaVersion != worktreeMergeAbsorbedConflictAcknowledgementSchemaVersion || ack.Status != "absorbed_conflict_acknowledged" ||
		ack.AcknowledgementPath != path || ack.ReceiptPath != receipt.ReceiptPath || ack.ReceiptID != receipt.ID || ack.ReceiptSHA256 != receiptHash ||
		ack.ReceiptStatus != receipt.Status || ack.Lane != receipt.Lane || ack.Repository != receipt.Repository || ack.Target != receipt.Target ||
		ack.ReceiptTargetSHA != receipt.TargetSHA || ack.CandidateTask != receipt.Candidate.Task || ack.CandidateWorktree != receipt.Candidate.Worktree ||
		ack.CandidateBranch != receipt.Candidate.Branch || ack.CandidateSHA != receipt.Candidate.SHA || !sameWorktreeMergeSources(ack.Sources, receipt.Sources) ||
		len(ack.SourceProofs) != len(receipt.Sources) || ack.CurrentTargetSHA == "" ||
		ack.Actor == "" || ack.Reason == "" || ack.RecordedAt.IsZero() || ack.ID != absorbedConflictAcknowledgementID(ack) {
		return WorktreeMergeAbsorbedConflictAcknowledgement{}, fmt.Errorf("absorbed-conflict acknowledgement %s has invalid immutable identity", path)
	}
	for index, proof := range ack.SourceProofs {
		source := receipt.Sources[index]
		if proof.Task != source.Task || proof.Worktree != source.Worktree || proof.Branch != source.Branch || proof.SHA != source.SHA {
			return WorktreeMergeAbsorbedConflictAcknowledgement{}, fmt.Errorf("absorbed-conflict acknowledgement %s source proof identity drift", path)
		}
		if proof.Method != "ancestor" && proof.Method != "content_absorbed" {
			return WorktreeMergeAbsorbedConflictAcknowledgement{}, fmt.Errorf("absorbed-conflict acknowledgement %s has an unknown proof method %q", path, proof.Method)
		}
		if proof.Method == "content_absorbed" && (proof.MergeBaseSHA == "" || proof.PathCount == 0) {
			return WorktreeMergeAbsorbedConflictAcknowledgement{}, fmt.Errorf("absorbed-conflict acknowledgement %s content-absorbed proof lacks its merge base or path count", path)
		}
	}
	return ack, nil
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
