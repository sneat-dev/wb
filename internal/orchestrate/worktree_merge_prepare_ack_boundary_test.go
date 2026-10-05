//go:build e2e

package orchestrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This is an actual native claim/history acknowledgement. The historical
// failure text is an operator record, not simulated validation execution.
func prepareOwnerCollisionBoundary(t *testing.T, exactPath bool) prepareOwnerFixture {
	t.Helper()
	f := newPrepareOwnerFixture(t)
	f.createInterruptedCandidate(t)
	historical := append([]WorktreeMergeSource(nil), f.sources...)
	writeEngineFile(t, filepath.Join(f.source.WorktreeDir, "collision-advance.txt"), "native collision source advance\n")
	runEngineGit(t, f.source.WorktreeDir, "add", "collision-advance.txt")
	runEngineGit(t, f.source.WorktreeDir, "commit", "-m", "native collision source advance")
	head := strings.TrimSpace(runEngineGit(t, f.source.WorktreeDir, "rev-parse", "HEAD"))
	runEngineGit(t, f.receipt.Candidate.Worktree, "merge", "--no-edit", head)
	f.receipt.Sources[0].SHA = head
	f.receipt.SourceRefreshes = []WorktreeMergeSourceRefresh{{RecordedAt: time.Now().UTC(), Sources: historical}}
	f.receipt.Candidate.SHA = strings.TrimSpace(runEngineGit(t, f.receipt.Candidate.Worktree, "rev-parse", "HEAD"))
	f.receipt.Failure = "historical operator-reported collision"
	if exactPath {
		old := f.receipt.ReceiptPath
		operation := worktreeMergeOperationID(f.receipt.Lane, f.receipt.Sources)
		f.receipt.ReceiptPath = filepath.Join(filepath.Dir(old), operation+".json")
		if err := os.Remove(old); err != nil {
			t.Fatal(err)
		}
	}
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
	_, err = AcknowledgeWorktreeMergeReceiptCollision(t.Context(), WorktreeMergeReceiptCollisionAcknowledgementOptions{
		ProjectsRoot: f.engine.githubDir, Receipt: f.receipt.ReceiptPath, Apply: true, Actor: "private-fixture", Reason: "native exact collision custody",
		ExpectedReceiptSHA256: receiptHash, ExpectedImmutableClaimSHA256: claimHash, ExpectedTargetSHA: f.receipt.TargetSHA,
		ExpectedCandidateSHA: f.receipt.Candidate.SHA, ExpectedCurrentSourceSHA: head, ExpectedHistoricalRefreshSourceSHA: historical[0].SHA,
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}
