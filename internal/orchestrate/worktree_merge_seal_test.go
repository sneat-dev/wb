package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestPrepareValidationFailedWorktreeMergeSealPreservesTargetTreeAndHistoricalRecords(t *testing.T) {
	fixture := newEngineFixture(t)
	writeEngineFile(t, filepath.Join(fixture.canonical, "historical-target.txt"), "historical target\n")
	runEngineGit(t, fixture.canonical, "add", "historical-target.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: historical receipt target")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	source := createMergeSource(t, fixture, "seal-source", "feature/seal-source", "source.txt", "source\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status = WorktreeMergeValidationFailed
	receipt.Failure = "historical validation failure"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	originalReceipt, originalCandidateClaim, sourceClaim := validationFailureSealImmutableBytes(t, fixture, receipt, source)

	claimBaseParent := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", receipt.TargetSHA+"^"))
	alternateTargetWorktree := filepath.Join(t.TempDir(), "alternate-target")
	runEngineGit(t, fixture.canonical, "worktree", "add", "-b", "test/seal-alternate-target", alternateTargetWorktree, claimBaseParent)
	writeEngineFile(t, filepath.Join(alternateTargetWorktree, "target.txt"), "current target\n")
	runEngineGit(t, alternateTargetWorktree, "add", "target.txt")
	runEngineGit(t, alternateTargetWorktree, "commit", "-m", "test: replace historical branch tip")
	runEngineGit(t, alternateTargetWorktree, "push", "--force", "origin", "HEAD:main")
	runEngineGit(t, source.WorktreeDir, "fetch", "origin", "main")
	runEngineGit(t, source.WorktreeDir, "merge", "--no-edit", "origin/main")
	advancedSource := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	runEngineGit(t, alternateTargetWorktree, "merge", "--squash", advancedSource)
	runEngineGit(t, alternateTargetWorktree, "commit", "-m", "test: squash land advanced source")
	runEngineGit(t, alternateTargetWorktree, "push", "--force", "origin", "HEAD:main")
	currentTarget := strings.TrimSpace(runEngineGit(t, alternateTargetWorktree, "rev-parse", "HEAD"))
	targetTree := strings.TrimSpace(runEngineGit(t, alternateTargetWorktree, "rev-parse", "HEAD^{tree}"))
	if contains, err := isMergeAncestor(context.Background(), fixture.canonical, receipt.TargetSHA, currentTarget); err != nil || contains {
		t.Fatalf("fixture receipt target unexpectedly contained in rewritten current target: contains=%t err=%v", contains, err)
	}
	if contains, err := isMergeAncestor(context.Background(), fixture.canonical, strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD")), currentTarget); err != nil || contains {
		t.Fatalf("fixture source unexpectedly contained in current target: contains=%t err=%v", contains, err)
	}
	_, err = PrepareValidationFailedWorktreeMergeSeal(context.Background(), WorktreeMergeValidationFailureSealOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true,
	})
	if err == nil || !strings.Contains(err.Error(), "--actor and --reason are required") {
		t.Fatalf("missing audit identity error = %v", err)
	}

	dryRun, err := PrepareValidationFailedWorktreeMergeSeal(context.Background(), WorktreeMergeValidationFailureSealOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dryRun.Status != "validation_failure_seal_planned" || dryRun.CurrentTargetSHA != currentTarget || dryRun.TargetTreeSHA != targetTree || dryRun.Candidate.Worktree != "" {
		t.Fatalf("dry-run seal = %+v", dryRun)
	}
	home, err := wbhome.Root(fixture.githubDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "worktrees", dryRun.Candidate.Task)); !os.IsNotExist(err) {
		t.Fatalf("dry-run created ancestry seal task: %v", err)
	}

	seal, err := PrepareValidationFailedWorktreeMergeSeal(context.Background(), WorktreeMergeValidationFailureSealOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true,
		Actor: "reviewer", Reason: "approved immutable ancestry recovery", Model: "test-model", AgentRuntime: "test",
		Timeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if seal.Status != "validation_failure_seal_prepared" || seal.Candidate.Worktree == "" || seal.Candidate.SHA == currentTarget {
		t.Fatalf("applied seal = %+v", seal)
	}
	if got := strings.TrimSpace(runEngineGit(t, seal.Candidate.Worktree, "rev-parse", "HEAD^{tree}")); got != targetTree {
		t.Fatalf("seal tree = %s, want target tree %s", got, targetTree)
	}
	for _, root := range seal.RequiredRoots {
		if contains, err := isMergeAncestor(context.Background(), seal.Candidate.Worktree, root.SHA, seal.Candidate.SHA); err != nil || !contains {
			t.Fatalf("seal does not contain %s %s: contains=%t err=%v", root.Kind, root.SHA, contains, err)
		}
	}
	if contains, err := isMergeAncestor(context.Background(), seal.Candidate.Worktree, advancedSource, seal.Candidate.SHA); err != nil || !contains {
		t.Fatalf("seal does not contain advanced source %s: contains=%t err=%v", advancedSource, contains, err)
	}
	if err := requireCleanMergeWorktree(context.Background(), seal.Candidate.Worktree); err != nil {
		t.Fatal(err)
	}
	assertValidationFailureSealImmutableBytes(t, fixture, receipt, source, originalReceipt, originalCandidateClaim, sourceClaim)

	ack, err := SupersedeValidationFailedWorktreeMerge(context.Background(), WorktreeMergeValidationFailureSupersessionOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: seal.Candidate.Worktree,
		Apply: true, Actor: "reviewer", Reason: "approved immutable ancestry recovery",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ack.Replacement != seal.Candidate || ack.CurrentTargetSHA != currentTarget {
		t.Fatalf("supersession did not bind seal: ack=%+v seal=%+v", ack, seal)
	}
	writeEngineFile(t, filepath.Join(seal.Candidate.Worktree, "drift.txt"), "semantic drift\n")
	runEngineGit(t, seal.Candidate.Worktree, "add", "drift.txt")
	runEngineGit(t, seal.Candidate.Worktree, "commit", "-m", "test: semantic drift")
	_, err = PrepareValidationFailedWorktreeMergeSeal(context.Background(), WorktreeMergeValidationFailureSealOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true,
		Actor: "reviewer", Reason: "approved immutable ancestry recovery", Model: "test-model", AgentRuntime: "test", Timeout: time.Minute,
	})
	if err == nil || !strings.Contains(err.Error(), "differs from fetched target tree") {
		t.Fatalf("candidate tree drift error = %v", err)
	}
}

func validationFailureSealImmutableBytes(t *testing.T, fixture engineFixture, receipt WorktreeMergeReceipt, source worktrees.CreateResult) ([]byte, []byte, []byte) {
	t.Helper()
	receiptBytes, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	candidateView, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: receipt.Candidate.Worktree})
	if err != nil || candidateView.Claim == nil {
		t.Fatalf("load candidate claim: %+v err=%v", candidateView, err)
	}
	candidateBytes, err := os.ReadFile(candidateView.Claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	sourceView, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: source.WorktreeDir})
	if err != nil || sourceView.Claim == nil {
		t.Fatalf("load source claim: %+v err=%v", sourceView, err)
	}
	sourceBytes, err := os.ReadFile(sourceView.Claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	return receiptBytes, candidateBytes, sourceBytes
}

func assertValidationFailureSealImmutableBytes(t *testing.T, fixture engineFixture, receipt WorktreeMergeReceipt, source worktrees.CreateResult, wantReceipt, wantCandidateClaim, wantSourceClaim []byte) {
	t.Helper()
	gotReceipt, gotCandidateClaim, gotSourceClaim := validationFailureSealImmutableBytes(t, fixture, receipt, source)
	if string(gotReceipt) != string(wantReceipt) {
		t.Fatal("historical receipt changed")
	}
	if string(gotCandidateClaim) != string(wantCandidateClaim) {
		t.Fatal("historical candidate Work Log changed")
	}
	if string(gotSourceClaim) != string(wantSourceClaim) {
		t.Fatal("historical source Work Log changed")
	}
}
