package fleetsync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// RemovalReceipt records one archived clone that `wb sync --prune-archived`
// deleted, or was about to.
//
// Sync is the only WB command that removes a canonical clone without leaving
// evidence: `wb archive clean` performs the same os.RemoveAll and writes a
// receipt for it, while sync printed a line to a terminal and forgot. A
// deleted clone is not recoverable from WB, so "what was removed, from where,
// at which commit, and on what grounds" has to outlive the terminal it
// scrolled past.
//
// HeadSHA is the point of the record. The repository still exists on GitHub —
// archived, read-only, but intact — so the clone can be restored, and the
// commit it stood at is the difference between restoring the right state and
// guessing.
type RemovalReceipt struct {
	SchemaVersion int        `json:"schema_version"`
	Phase         string     `json:"phase"`
	Repository    string     `json:"repository"`
	ClonePath     string     `json:"clone_path"`
	HeadSHA       string     `json:"head_sha"`
	Reason        string     `json:"reason"`
	CreatedAt     time.Time  `json:"created_at"`
	RemovedAt     *time.Time `json:"removed_at,omitempty"`
	Error         string     `json:"error,omitempty"`
}

// Receipt phases. A receipt is written as `planned` before anything is
// deleted, so an interrupted or crashed run still leaves proof of intent, and
// updated to `removed` or `failed` afterwards. A receipt left at `planned` is
// itself a finding: something stopped mid-deletion.
const (
	PhasePlanned = "planned"
	PhaseRemoved = "removed"
	PhaseFailed  = "failed"
)

const removalReceiptSchemaVersion = 1

// writeRemovalReceipt writes or replaces a receipt and returns its path. The
// path is derived from CreatedAt and the repository, so the planned write and
// every later update land on the same file.
func writeRemovalReceipt(projectsRoot string, receipt RemovalReceipt) (string, error) {
	home, err := wbhome.EnsureRoot(projectsRoot)
	if err != nil {
		return "", err
	}
	// 0o700 matches archive-clean's receipt directory: a receipt names local
	// paths and commit ids, and WB already treats operation evidence as
	// private.
	directory := filepath.Join(home, "reports", "sync-prune-archived")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create prune receipt directory: %w", err)
	}
	path := filepath.Join(directory, removalReceiptName(receipt))
	if err := overwriteRemovalReceipt(path, receipt); err != nil {
		return "", err
	}
	return path, nil
}

// removalReceiptName is deterministic in (CreatedAt, Repository) so a phase
// update rewrites the same file rather than accumulating one per phase.
func removalReceiptName(receipt RemovalReceipt) string {
	return fmt.Sprintf("%s-%s.json",
		receipt.CreatedAt.UTC().Format("20060102T150405.000000000Z"),
		strings.ReplaceAll(receipt.Repository, "/", "--"))
}

// overwriteRemovalReceipt replaces the receipt atomically. A half-written
// receipt is worse than none: it is the only record that a clone existed, and
// it is read by a human reconstructing what happened after the fact.
func overwriteRemovalReceipt(path string, receipt RemovalReceipt) error {
	raw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return fmt.Errorf("encode prune receipt: %w", err)
	}
	raw = append(raw, '\n')
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".prune-receipt-*")
	if err != nil {
		return fmt.Errorf("stage prune receipt: %w", err)
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return fmt.Errorf("set permissions on %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
