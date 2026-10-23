//go:build e2e

package orchestrate

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Protocol fixtures share only the existing immutable Git seed. Every claim,
// worktree, bare remote and append-only record belongs to its private row.
func protocolCollisionFixture(t *testing.T) (prepareOwnerFixture, WorktreeMergeReceiptCollisionAcknowledgementOptions) {
	t.Helper()
	f := newPrepareOwnerFixture(t)
	f.createInterruptedCandidate(t)
	history := append([]WorktreeMergeSource(nil), f.receipt.Sources...)
	writeEngineFile(t, filepath.Join(f.source.WorktreeDir, "protocol-advance.txt"), "native source advance\n")
	runEngineGit(t, f.source.WorktreeDir, "add", "protocol-advance.txt")
	runEngineGit(t, f.source.WorktreeDir, "commit", "-m", "private protocol source advance")
	head := strings.TrimSpace(runEngineGit(t, f.source.WorktreeDir, "rev-parse", "HEAD"))
	runEngineGit(t, f.receipt.Candidate.Worktree, "merge", "--no-edit", head)
	f.receipt.Sources[0].SHA = head
	f.receipt.Sources[0].Merged = true
	f.receipt.SourceRefreshes = []WorktreeMergeSourceRefresh{{Sources: history, RecordedAt: time.Now().UTC()}}
	f.receipt.Candidate.SHA = strings.TrimSpace(runEngineGit(t, f.receipt.Candidate.Worktree, "rev-parse", "HEAD"))
	f.receipt.Failure = "historical collision failure is an explicit operator assertion"
	if err := persistWorktreeMergeReceipt(f.receipt); err != nil {
		t.Fatal(err)
	}
	claim, err := validateMergeAcknowledgementCandidate(t.Context(), f.engine.githubDir, f.receipt, f.receipt.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	claimHash, err := worktreeMergeReceiptSHA256(claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	receiptHash, err := worktreeMergeReceiptSHA256(f.receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	return f, WorktreeMergeReceiptCollisionAcknowledgementOptions{ProjectsRoot: f.engine.githubDir, Receipt: f.receipt.ReceiptPath, ExpectedReceiptSHA256: receiptHash, ExpectedImmutableClaimSHA256: claimHash, ExpectedTargetSHA: f.receipt.TargetSHA, ExpectedCandidateSHA: f.receipt.Candidate.SHA, ExpectedCurrentSourceSHA: head, ExpectedHistoricalRefreshSourceSHA: history[0].SHA}
}

func protocolAssertLaneHeld(t *testing.T, root, lane string) {
	t.Helper()
	lock, err := AcquireOperationLock(root, lane, true)
	if err == nil {
		_ = lock.Release()
		t.Fatal("negative observation ran without the actual lane lock")
	}
	if !strings.Contains(err.Error(), "already active") {
		t.Fatalf("lane lock was not held: %v", err)
	}
}
