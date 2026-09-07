package worktrees

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/mergeack"
)

// This file lets cleanup and end recognize one extra, narrowly scoped piece
// of landing proof: a validated `wb worktree merge acknowledge-absorbed-conflict`
// acknowledgement for a candidate worktree that was never itself pushed
// anywhere. internal/orchestrate owns writing that acknowledgement (see
// WorktreeMergeAbsorbedConflictAcknowledgement in
// internal/orchestrate/worktree_merge_absorbed_conflict.go); it already
// imports internal/worktrees for MergeReceiptCleanupProof, so the reverse
// import would cycle. Cleanup therefore reads the receipt and its sidecar
// directly, off disk, using internal/mergeack for the sidecar's on-disk
// format, identity hash, and full validation -- the same validation
// internal/orchestrate itself applies, so a hand-edited sidecar with
// fabricated or emptied proofs is rejected here exactly as it would be there.
// It never writes, rewrites, or deletes either file.

// absorbedConflictAckSuffix, absorbedConflictAckSchemaVersion, and
// absorbedConflictAckStatus alias internal/mergeack's constants directly
// (rather than duplicating the literals) so the two packages' notion of the
// sidecar's schema version, suffix, and status can never diverge.
const (
	absorbedConflictAckSuffix        = mergeack.FileSuffix
	absorbedConflictAckSchemaVersion = mergeack.SchemaVersion
	absorbedConflictAckStatus        = mergeack.Status
)

// absorbedConflictReceipt is the minimal subset of an
// internal/orchestrate.WorktreeMergeReceipt cleanup needs to recognize a
// candidate worktree as the receipt's own, to name its receipted sources, and
// to validate a sidecar acknowledgement fully via internal/mergeack.Load.
type absorbedConflictReceipt struct {
	ReceiptPath string `json:"receipt_path"`
	ID          string `json:"id"`
	Status      string `json:"status"`
	Lane        string `json:"lane"`
	Repository  string `json:"repository"`
	Target      string `json:"target"`
	TargetSHA   string `json:"target_sha"`
	Candidate   struct {
		Task     string `json:"task"`
		Worktree string `json:"worktree"`
		Branch   string `json:"branch"`
		SHA      string `json:"sha"`
	} `json:"candidate"`
	Sources []struct {
		Task     string `json:"task"`
		Worktree string `json:"worktree"`
		Branch   string `json:"branch"`
		SHA      string `json:"sha"`
	} `json:"sources"`
}

// identity converts receipt into the mergeack.ReceiptIdentity Load validates
// an acknowledgement sidecar against.
func (receipt absorbedConflictReceipt) identity() mergeack.ReceiptIdentity {
	sources := make([]mergeack.Source, 0, len(receipt.Sources))
	for _, source := range receipt.Sources {
		sources = append(sources, mergeack.Source{Task: source.Task, Worktree: source.Worktree, Branch: source.Branch, SHA: source.SHA})
	}
	return mergeack.ReceiptIdentity{
		Path: receipt.ReceiptPath, ID: receipt.ID, Status: receipt.Status, Lane: receipt.Lane,
		Repository: receipt.Repository, Target: receipt.Target, TargetSHA: receipt.TargetSHA,
		Candidate: mergeack.Source{
			Task: receipt.Candidate.Task, Worktree: receipt.Candidate.Worktree,
			Branch: receipt.Candidate.Branch, SHA: receipt.Candidate.SHA,
		},
		Sources: sources,
	}
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
// passes: the acknowledgement sidecar exists, is schema-valid per
// internal/mergeack's full validation, is bound by a plain SHA-256 of the
// receipt file's current bytes to this exact, unchanged receipt, names this
// exact candidate identity, matches the live worktree's own current branch
// and head SHA (entryBranch/entryHeadSHA) -- never just the task name and
// worktree path, which a recycled worktree slot could share with unrelated
// new work -- and its recorded target head is an ancestor of remoteTargetSHA
// (the freshly fetched origin/<target>). A missing, tampered,
// identity-mismatched, branch/SHA-mismatched, or stale-target
// acknowledgement is treated exactly like no acknowledgement at all: proof is
// nil and err is nil, never a hard failure of the whole cleanup run.
func findAbsorbedConflictCleanupProof(
	ctx context.Context, home, canonicalDir, task, worktreeDir, entryBranch, entryHeadSHA, remoteTargetSHA string,
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
		proof, err = validateAbsorbedConflictAcknowledgementSidecar(ctx, canonicalDir, receiptPath, receipt, entryBranch, entryHeadSHA, remoteTargetSHA)
		return proof, matchedReceiptPath, err
	}
	return nil, "", nil
}

func validateAbsorbedConflictAcknowledgementSidecar(
	ctx context.Context, canonicalDir, receiptPath string, receipt absorbedConflictReceipt,
	entryBranch, entryHeadSHA, remoteTargetSHA string,
) (*absorbedConflictCleanupProof, error) {
	// A candidate whose live branch/head no longer match exactly what the
	// receipt named is never this receipt's candidate any more -- whether
	// because the worktree/task slot was recycled for unrelated new work, or
	// because the candidate is a detached HEAD (entryBranch empty, which
	// cannot equal a receipt's always-non-empty candidate branch). Refuse
	// before even looking at the acknowledgement sidecar: an acknowledgement
	// proves the *receipted* content landed, never whatever content
	// currently happens to sit in that worktree.
	if entryBranch != receipt.Candidate.Branch || entryHeadSHA != receipt.Candidate.SHA {
		return nil, nil
	}
	ackPath := receiptPath + absorbedConflictAckSuffix
	ack, ackErr := mergeack.Load(ackPath, receipt.identity())
	if ackErr != nil {
		// A missing, corrupt, tampered, or identity-mismatched sidecar is a
		// normal refusal, not a failure of the whole cleanup run.
		return nil, nil //nolint:nilerr
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
// recognizes an acknowledgement that exactly matches this candidate's task,
// worktree directory, current branch, and current head SHA. When it
// validates, entry is marked integrated and absorbed at origin -- exactly
// what removes the "was never pushed" refusal -- and the acknowledgement path
// plus proven source SHAs are recorded for the cleanup report and terminal
// Work Log. It is always safe to call: a candidate that is already
// integrated, superseded, or has no matching receipt is left unchanged.
func applyAbsorbedConflictAcknowledgementCleanupProof(ctx context.Context, home string, entry *ListResult) error {
	if entry.IntegratedAtOrigin || entry.SupersededAtOrigin {
		return nil
	}
	proof, receiptPath, err := findAbsorbedConflictCleanupProof(ctx, home, entry.CanonicalDir, entry.Task, entry.WorktreeDir, entry.Branch, entry.HeadSHA, entry.RemoteTargetSHA)
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
