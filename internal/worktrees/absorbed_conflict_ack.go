package worktrees

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// This file lets cleanup and end recognize one extra, narrowly scoped piece
// of landing proof: a validated `wb worktree merge acknowledge-absorbed-conflict`
// acknowledgement for a candidate worktree that was never itself pushed
// anywhere. internal/orchestrate owns writing and richly proving that
// acknowledgement (see WorktreeMergeAbsorbedConflictAcknowledgement in
// internal/orchestrate/worktree_merge_absorbed_conflict.go); it already
// imports internal/worktrees for MergeReceiptCleanupProof, so the reverse
// import would cycle. Cleanup therefore reads the receipt and its sidecar
// directly, off disk, checking only the fields it needs: that the sidecar is
// bound (by a plain SHA-256 of the receipt file's own bytes) to the exact,
// unchanged receipt naming this worktree as its candidate, and that the
// target head it was proved against is an ancestor of the freshly fetched
// origin/<target>. It never writes, rewrites, or deletes either file.

const (
	absorbedConflictAckSuffix        = ".absorbed-conflict.ack.json"
	absorbedConflictAckSchemaVersion = 1
	absorbedConflictAckStatus        = "absorbed_conflict_acknowledged"
)

// absorbedConflictReceipt is the minimal subset of an
// internal/orchestrate.WorktreeMergeReceipt cleanup needs to recognize a
// candidate worktree as the receipt's own, and to name its receipted sources.
type absorbedConflictReceipt struct {
	ReceiptPath string `json:"receipt_path"`
	Candidate   struct {
		Task     string `json:"task"`
		Worktree string `json:"worktree"`
		Branch   string `json:"branch"`
		SHA      string `json:"sha"`
	} `json:"candidate"`
	Sources []struct {
		SHA string `json:"sha"`
	} `json:"sources"`
}

// absorbedConflictAcknowledgement is the minimal subset of
// internal/orchestrate.WorktreeMergeAbsorbedConflictAcknowledgement cleanup
// needs to accept it as landing proof for a never-pushed candidate.
type absorbedConflictAcknowledgement struct {
	SchemaVersion     int    `json:"schema_version"`
	Status            string `json:"status"`
	ReceiptPath       string `json:"receipt_path"`
	ReceiptSHA256     string `json:"receipt_sha256"`
	CandidateTask     string `json:"candidate_task"`
	CandidateWorktree string `json:"candidate_worktree"`
	CandidateBranch   string `json:"candidate_branch"`
	CandidateSHA      string `json:"candidate_sha"`
	CurrentTargetSHA  string `json:"current_target_sha"`
	Actor             string `json:"actor"`
	Reason            string `json:"reason"`
	RecordedAt        string `json:"recorded_at"`
}

// absorbedConflictCleanupProof is the landing proof cleanup accepted: the
// exact acknowledgement sidecar path, and the receipted source commits its
// proofs covered.
type absorbedConflictCleanupProof struct {
	AcknowledgementPath string
	SourceSHAs          []string
}

// findAbsorbedConflictCleanupProof looks under home's worktree-merge reports
// for a receipt whose recorded candidate is exactly this task and worktree
// directory. matchedReceiptPath is returned whenever such a receipt is found,
// whether or not its acknowledgement validates, so a refusal can name the
// exact receipt to acknowledge. proof is non-nil only when every check
// passes: the acknowledgement sidecar exists, is schema-valid, is bound by a
// plain SHA-256 of the receipt file's current bytes to this exact, unchanged
// receipt, names this exact candidate identity, and its recorded target head
// is an ancestor of remoteTargetSHA (the freshly fetched origin/<target>). A
// missing, tampered, identity-mismatched, or stale-target acknowledgement is
// treated exactly like no acknowledgement at all: proof is nil and err is
// nil, never a hard failure of the whole cleanup run.
func findAbsorbedConflictCleanupProof(
	ctx context.Context, home, canonicalDir, task, worktreeDir, remoteTargetSHA string,
) (proof *absorbedConflictCleanupProof, matchedReceiptPath string, err error) {
	if home == "" || remoteTargetSHA == "" {
		return nil, "", nil
	}
	reportsDir := filepath.Join(home, "reports", "worktree-merge")
	entries, readDirErr := os.ReadDir(reportsDir)
	if readDirErr != nil {
		if os.IsNotExist(readDirErr) {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("read worktree-merge reports %s: %w", reportsDir, readDirErr)
	}
	cleanWorktree := filepath.Clean(worktreeDir)
	for _, dirEntry := range entries {
		name := dirEntry.Name()
		if dirEntry.IsDir() || !strings.HasSuffix(name, ".json") || strings.Contains(name, ".ack.json") {
			continue
		}
		receiptPath := filepath.Join(reportsDir, name)
		receiptBytes, readErr := os.ReadFile(receiptPath)
		if readErr != nil {
			continue
		}
		var receipt absorbedConflictReceipt
		if jsonErr := json.Unmarshal(receiptBytes, &receipt); jsonErr != nil {
			continue
		}
		if receipt.ReceiptPath != receiptPath || receipt.Candidate.Task != task ||
			filepath.Clean(receipt.Candidate.Worktree) != cleanWorktree {
			continue
		}
		// Exactly one receipt can name this task/worktree as its candidate
		// (the merger lane is exclusive), so the first structural match is
		// the only one that matters.
		matchedReceiptPath = receiptPath
		proof, err = validateAbsorbedConflictAcknowledgementSidecar(ctx, canonicalDir, receiptPath, receiptBytes, receipt, task, cleanWorktree, remoteTargetSHA)
		return proof, matchedReceiptPath, err
	}
	return nil, "", nil
}

func validateAbsorbedConflictAcknowledgementSidecar(
	ctx context.Context, canonicalDir, receiptPath string, receiptBytes []byte, receipt absorbedConflictReceipt,
	task, cleanWorktree, remoteTargetSHA string,
) (*absorbedConflictCleanupProof, error) {
	ackPath := receiptPath + absorbedConflictAckSuffix
	ackBytes, ackErr := os.ReadFile(ackPath)
	if ackErr != nil {
		return nil, nil //nolint:nilerr // missing acknowledgement is a normal refusal, not a failure.
	}
	var ack absorbedConflictAcknowledgement
	if jsonErr := json.Unmarshal(ackBytes, &ack); jsonErr != nil {
		return nil, nil // a corrupt/tampered sidecar is treated as no acknowledgement.
	}
	digest := sha256.Sum256(receiptBytes)
	receiptHash := hex.EncodeToString(digest[:])
	if ack.SchemaVersion != absorbedConflictAckSchemaVersion || ack.Status != absorbedConflictAckStatus ||
		ack.ReceiptPath != receiptPath || ack.ReceiptSHA256 != receiptHash ||
		ack.CandidateTask != task || filepath.Clean(ack.CandidateWorktree) != cleanWorktree ||
		ack.CandidateBranch != receipt.Candidate.Branch || ack.CandidateSHA != receipt.Candidate.SHA ||
		strings.TrimSpace(ack.CurrentTargetSHA) == "" || strings.TrimSpace(ack.Actor) == "" ||
		strings.TrimSpace(ack.Reason) == "" || strings.TrimSpace(ack.RecordedAt) == "" {
		return nil, nil
	}
	ancestor, ancestorErr := isAncestor(ctx, canonicalDir, ack.CurrentTargetSHA, remoteTargetSHA)
	if ancestorErr != nil || !ancestor {
		// An unresolvable recorded target head (for example the object was
		// pruned) is exactly as inconclusive as a stale one: refuse, do not
		// abort the run over it.
		return nil, nil
	}
	sourceSHAs := make([]string, 0, len(receipt.Sources))
	for _, source := range receipt.Sources {
		if source.SHA != "" {
			sourceSHAs = append(sourceSHAs, source.SHA)
		}
	}
	return &absorbedConflictCleanupProof{AcknowledgementPath: ackPath, SourceSHAs: sourceSHAs}, nil
}

// applyAbsorbedConflictAcknowledgementCleanupProof grants no general
// absorption shortcut, mirroring applyMergeReceiptCleanupProof: it only
// recognizes an acknowledgement that exactly matches this candidate's task
// and worktree directory. When it validates, entry is marked integrated and
// absorbed at origin -- exactly what removes the "was never pushed" refusal
// -- and the acknowledgement path plus proven source SHAs are recorded for
// the cleanup report and terminal Work Log. It is always safe to call: a
// candidate that is already integrated, superseded, or has no matching
// receipt is left unchanged.
func applyAbsorbedConflictAcknowledgementCleanupProof(ctx context.Context, home string, entry *ListResult) error {
	if entry.IntegratedAtOrigin || entry.SupersededAtOrigin {
		return nil
	}
	proof, receiptPath, err := findAbsorbedConflictCleanupProof(ctx, home, entry.CanonicalDir, entry.Task, entry.WorktreeDir, entry.RemoteTargetSHA)
	if err != nil {
		return err
	}
	entry.AbsorbedConflictReceiptPath = receiptPath
	if proof == nil {
		return nil
	}
	entry.IntegratedAtOrigin = true
	entry.AbsorbedAtOrigin = true
	entry.AbsorbedConflictAcknowledgementPath = proof.AcknowledgementPath
	entry.AbsorbedConflictProvenSourceSHAs = proof.SourceSHAs
	return nil
}

// absorbedConflictAcknowledgementHint names the exact next command for an
// operator looking at a "was never pushed" refusal on a candidate that is (or
// might be) a WB integration lane's own unpublished candidate.
func absorbedConflictAcknowledgementHint(receiptPath string) string {
	if receiptPath == "" {
		return "if this worktree is a WB integration candidate whose target already absorbed its content through a resolved merge conflict, run `wb worktree merge acknowledge-absorbed-conflict <receipt>` first"
	}
	return "an absorbed-conflict acknowledgement was not found, is stale, or does not validate for receipt " +
		receiptPath + "; run `wb worktree merge acknowledge-absorbed-conflict " + receiptPath + "` to review whether this candidate's content already reached the target"
}
