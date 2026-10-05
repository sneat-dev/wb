package orchestrate

import (
	"errors"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
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

func protocolSupersessionFixture(t *testing.T) (engineFixture, WorktreeMergeReceipt, worktrees.CreateResult) {
	t.Helper()
	f := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, f, "protocol-source", "feature/protocol-source", "source.txt", "native source\n")
	r, err := PrepareWorktreeMerge(t.Context(), WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err != nil {
		t.Fatal(err)
	}
	// This is recorded failure eligibility, not a claim that the fixture's
	// successful native preparation itself produced a validation regression.
	r.Status = WorktreeMergeValidationFailed
	r.Failure = "recorded historical protocol failure"
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, source.WorktreeDir, "push", "origin", source.Branch)
	writeEngineFile(t, filepath.Join(f.canonical, "protocol-target.txt"), "native current target\n")
	runEngineGit(t, f.canonical, "add", "protocol-target.txt")
	runEngineGit(t, f.canonical, "commit", "-m", "private protocol target advance")
	runEngineGit(t, f.canonical, "push", "origin", "main")
	replacement := createMergeSource(t, f, "protocol-replacement", "feature/protocol-replacement", "replacement.txt", "native replacement\n")
	runEngineGit(t, replacement.WorktreeDir, "fetch", "origin")
	runEngineGit(t, replacement.WorktreeDir, "merge", "--no-edit", "origin/"+source.Branch)
	return f, r, replacement
}

func protocolCorrectionFixture(t *testing.T) (engineFixture, WorktreeMergeReceipt, worktrees.CreateResult, WorktreeMergeValidationFailureSupersession, WorktreeMergeSelfSupersessionCorrectionOptions) {
	t.Helper()
	f, r, replacement := protocolSupersessionFixture(t)
	claim, err := validateMergeAcknowledgementCandidate(t.Context(), f.githubDir, r, r.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	claimHash, err := worktreeMergeReceiptSHA256(claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	target, err := fetchExactMergeTarget(t.Context(), replacement.WorktreeDir, r.Target)
	if err != nil {
		t.Fatal(err)
	}
	receiptHash, err := worktreeMergeReceiptSHA256(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	path := validationFailureSupersessionPath(r.ReceiptPath)
	// Historical invalid self-supersession is the input being repaired. All
	// actual claim, source, target and replacement observations remain native.
	a := WorktreeMergeValidationFailureSupersession{SchemaVersion: worktreeMergeValidationFailureSupersessionSchemaVersion, Status: "validation_failure_superseded", ReceiptPath: r.ReceiptPath, AcknowledgementPath: path, ReceiptID: r.ID, ReceiptSHA256: receiptHash, ReceiptStatus: r.Status, Lane: r.Lane, Repository: r.Repository, Target: r.Target, ReceiptTargetSHA: r.TargetSHA, CurrentTargetSHA: target, OriginalCandidate: r.Candidate, OriginalClaimBaseSHA: claim.BaseSHA, Replacement: r.Candidate, ReplacementClaimBaseSHA: claim.BaseSHA, Sources: append([]WorktreeMergeSource(nil), r.Sources...), Actor: "historical operator", Reason: "pre-guard self-supersession", RecordedAt: time.Now().UTC()}
	a.ID = validationFailureSupersessionID(a)
	if err := persistValidationFailureSupersession(path, a); err != nil {
		t.Fatal(err)
	}
	ackHash, err := worktreeMergeReceiptSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	return f, r, replacement, a, WorktreeMergeSelfSupersessionCorrectionOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir, ExpectedSupersessionSHA256: ackHash, ExpectedImmutableClaimSHA256: claimHash}
}

func protocolUnchangedBytes(t *testing.T, path string, before []byte) {
	t.Helper()
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("immutable protocol bytes changed %s: %v", path, err)
	}
}

func TestReplacementProtocolEntryErrorsPrecedeNativeEffects(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"collision expected", "collision actor", "collision missing", "collision location", "correction expected", "correction replacement", "correction actor", "correction missing", "correction location", "supersession missing", "supersession location", "supersession replacement", "supersession actor", "supersession lane"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			r := protocolShapeReceipt(t)
			r.ReceiptPath = filepath.Join(root, r.ID+".json")
			if err := persistWorktreeMergeReceipt(r); err != nil {
				t.Fatal(err)
			}
			var err error
			collision := WorktreeMergeReceiptCollisionAcknowledgementOptions{ProjectsRoot: root, Receipt: r.ReceiptPath, ExpectedReceiptSHA256: "r", ExpectedImmutableClaimSHA256: "c", ExpectedTargetSHA: "t", ExpectedCandidateSHA: "h", ExpectedCurrentSourceSHA: "s", ExpectedHistoricalRefreshSourceSHA: "a"}
			correction := WorktreeMergeSelfSupersessionCorrectionOptions{ProjectsRoot: root, Receipt: r.ReceiptPath, ReplacementWorktree: "replacement", ExpectedSupersessionSHA256: "r", ExpectedImmutableClaimSHA256: "c"}
			replacement := WorktreeMergeValidationFailureSupersessionOptions{ProjectsRoot: root, Receipt: r.ReceiptPath, ReplacementWorktree: "replacement"}
			switch mode {
			case "collision expected":
				collision.ExpectedReceiptSHA256 = ""
			case "collision actor":
				collision.Apply = true
			case "collision location":
				collision.Receipt = ""
			case "collision missing":
				collision.Receipt = filepath.Join(root, "missing.json")
			case "correction expected":
				correction.ExpectedSupersessionSHA256 = ""
			case "correction replacement":
				correction.ReplacementWorktree = ""
			case "correction actor":
				correction.Apply = true
			case "correction location":
				correction.Receipt = ""
			case "correction missing":
				correction.Receipt = filepath.Join(root, "missing.json")
			case "supersession location":
				replacement.Receipt = ""
			case "supersession missing":
				replacement.Receipt = filepath.Join(root, "missing.json")
			case "supersession replacement":
				replacement.ReplacementWorktree = ""
			case "supersession actor":
				replacement.Apply = true
			case "supersession lane":
				r.Lane = ""
				if err := persistWorktreeMergeReceipt(r); err != nil {
					t.Fatal(err)
				}
			}
			switch {
			case strings.HasPrefix(mode, "collision"):
				_, err = AcknowledgeWorktreeMergeReceiptCollision(t.Context(), collision)
			case strings.HasPrefix(mode, "correction"):
				_, err = CorrectValidationFailedSelfSupersession(t.Context(), correction)
			default:
				_, err = SupersedeValidationFailedWorktreeMerge(t.Context(), replacement)
			}
			if err == nil {
				t.Fatalf("entry accepted %s", mode)
			}
			if strings.HasSuffix(mode, "missing") && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("native missing receipt identity lost: %v", err)
			}
		})
	}
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
