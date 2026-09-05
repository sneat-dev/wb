package orchestrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestPrepareConflictWorktreeMergeReplacementBreaksReceiptOwnedLaneCycle(t *testing.T) {
	fixture := newEngineFixture(t)
	first := createMergeSource(t, fixture, "conflict-refresh-first", "feature/conflict-refresh-first", "shared.txt", "first\n")
	second := createMergeSource(t, fixture, "conflict-refresh-second", "feature/conflict-refresh-second", "shared.txt", "second\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{first.WorktreeDir, second.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err == nil || receipt.Status != WorktreeMergeConflict {
		t.Fatalf("initial prepare = %+v err=%v, want conflict", receipt, err)
	}
	receiptedCandidate := receipt.Candidate.SHA
	merge := exec.Command("git", "merge", "--no-commit", receipt.Sources[1].SHA)
	merge.Dir = receipt.Candidate.Worktree
	if output, mergeErr := merge.CombinedOutput(); mergeErr == nil {
		t.Fatalf("fixture merge unexpectedly succeeded: %s", output)
	}
	writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "shared.txt"), "resolved\n")
	runEngineGit(t, receipt.Candidate.Worktree, "add", "shared.txt")
	runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "test: resolve conflict for receipt-bound refresh")
	observed := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))
	if contains, ancestorErr := isMergeAncestor(context.Background(), receipt.Candidate.Worktree, receiptedCandidate, observed); ancestorErr != nil || !contains {
		t.Fatalf("resolved conflict must be a strict candidate descendant: contains=%t err=%v", contains, ancestorErr)
	}
	if contains, ancestorErr := isMergeAncestor(context.Background(), receipt.Candidate.Worktree, receipt.Sources[1].SHA, observed); ancestorErr != nil || !contains {
		t.Fatalf("resolved conflict must retain the missing receipted source: contains=%t err=%v", contains, ancestorErr)
	}
	receiptBytes, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	claim, observedFromValidator, err := validatePrepareFailureSupersessionCandidate(context.Background(), fixture.githubDir, receipt)
	if err != nil || observedFromValidator != observed {
		t.Fatalf("validate observed candidate = claim=%+v observed=%q err=%v", claim, observedFromValidator, err)
	}
	claimBytes, err := os.ReadFile(claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	claimHash := sha256Hex(claimBytes)
	target, err := fetchExactMergeTarget(context.Background(), receipt.Candidate.Worktree, receipt.Target)
	if err != nil {
		t.Fatal(err)
	}
	options := WorktreeMergeConflictCandidateRefreshOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath,
		Sources: []string{first.WorktreeDir, second.WorktreeDir}, ExpectedSourceSHAs: []string{receipt.Sources[0].SHA, receipt.Sources[1].SHA},
		ExpectedReceiptSHA256: receiptHash, ExpectedImmutableClaimSHA256: claimHash, ExpectedCurrentTargetSHA: target,
		Actor: "reviewer", Reason: "construct replacement while the failed receipt owns its lane",
	}
	dryRun, err := PrepareConflictWorktreeMergeReplacement(context.Background(), options)
	if err != nil || dryRun.Status != "conflict_candidate_refresh_planned" || dryRun.ObservedCandidateDescendant != observed {
		t.Fatalf("conflict refresh dry-run = %+v err=%v", dryRun, err)
	}
	assertNoConflictCandidateRefresh(t, fixture, receipt, options)
	if current, readErr := os.ReadFile(receipt.ReceiptPath); readErr != nil || string(current) != string(receiptBytes) {
		t.Fatalf("dry-run changed receipt: err=%v", readErr)
	}
	options.Apply = true
	refresh, err := PrepareConflictWorktreeMergeReplacement(context.Background(), options)
	if err != nil || refresh.Status != "conflict_candidate_refresh_prepared" || refresh.Candidate.SHA == "" {
		t.Fatalf("conflict refresh apply = %+v err=%v", refresh, err)
	}
	for _, root := range []string{claim.BaseSHA, receipt.TargetSHA, target, receiptedCandidate, observed, receipt.Sources[0].SHA, receipt.Sources[1].SHA} {
		contains, ancestorErr := isMergeAncestor(context.Background(), refresh.Candidate.Worktree, root, refresh.Candidate.SHA)
		if ancestorErr != nil || !contains {
			t.Fatalf("replacement lacks root %s: contains=%t err=%v", root, contains, ancestorErr)
		}
	}
	if current, readErr := os.ReadFile(receipt.ReceiptPath); readErr != nil || string(current) != string(receiptBytes) {
		t.Fatalf("apply changed receipt: err=%v", readErr)
	}
	if current, readErr := os.ReadFile(claim.ClaimPath); readErr != nil || string(current) != string(claimBytes) {
		t.Fatalf("apply changed immutable claim: err=%v", readErr)
	}
	if _, err := SupersedeValidationFailedWorktreeMerge(context.Background(), WorktreeMergeValidationFailureSupersessionOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: refresh.Candidate.Worktree, Apply: true, Actor: "reviewer", Reason: "consume receipt-bound conflict replacement",
	}); err != nil {
		t.Fatalf("supersede refreshed conflict candidate: %v", err)
	}
	if superseded, err := hasValidationFailureSupersession(context.Background(), fixture.githubDir, receipt); err != nil || !superseded {
		t.Fatalf("supersession after refresh = superseded=%t err=%v", superseded, err)
	}
}

func TestPrepareConflictWorktreeMergeReplacementAcceptsDeterministicSupersededSuccessor(t *testing.T) {
	fixture := newEngineFixture(t)
	first := createMergeSource(t, fixture, "successor-conflict-first", "feature/successor-conflict-first", "shared.txt", "first\n")
	second := createMergeSource(t, fixture, "successor-conflict-second", "feature/successor-conflict-second", "shared.txt", "second\n")
	original, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{first.WorktreeDir, second.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err == nil || original.Status != WorktreeMergeConflict {
		t.Fatalf("initial prepare = %+v err=%v, want conflict", original, err)
	}

	originalCandidate := original.Candidate.Worktree
	merge := exec.Command("git", "merge", "--no-commit", original.Sources[1].SHA)
	merge.Dir = originalCandidate
	if output, mergeErr := merge.CombinedOutput(); mergeErr == nil {
		t.Fatalf("fixture merge unexpectedly succeeded: %s", output)
	}
	writeEngineFile(t, filepath.Join(originalCandidate, "shared.txt"), "resolved\n")
	runEngineGit(t, originalCandidate, "add", "shared.txt")
	runEngineGit(t, originalCandidate, "commit", "-m", "test: resolve original successor conflict")
	claim, _, err := validatePrepareFailureSupersessionCandidate(context.Background(), fixture.githubDir, original)
	if err != nil {
		t.Fatal(err)
	}
	claimBytes, err := os.ReadFile(claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	receiptHash, err := worktreeMergeReceiptSHA256(original.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	target, err := fetchExactMergeTarget(context.Background(), original.Candidate.Worktree, original.Target)
	if err != nil {
		t.Fatal(err)
	}
	refresh, err := PrepareConflictWorktreeMergeReplacement(context.Background(), WorktreeMergeConflictCandidateRefreshOptions{
		ProjectsRoot: fixture.githubDir, Receipt: original.ReceiptPath, Sources: []string{first.WorktreeDir, second.WorktreeDir},
		ExpectedSourceSHAs: []string{original.Sources[0].SHA, original.Sources[1].SHA}, ExpectedReceiptSHA256: receiptHash,
		ExpectedImmutableClaimSHA256: sha256Hex(claimBytes), ExpectedCurrentTargetSHA: target, Actor: "reviewer", Reason: "construct the original replacement",
	})
	if err != nil || refresh.Status != "conflict_candidate_refresh_planned" {
		t.Fatalf("initial conflict refresh dry-run = %+v err=%v", refresh, err)
	}
	refresh, err = PrepareConflictWorktreeMergeReplacement(context.Background(), WorktreeMergeConflictCandidateRefreshOptions{
		ProjectsRoot: fixture.githubDir, Receipt: original.ReceiptPath, Sources: []string{first.WorktreeDir, second.WorktreeDir}, Apply: true,
		ExpectedSourceSHAs: []string{original.Sources[0].SHA, original.Sources[1].SHA}, ExpectedReceiptSHA256: receiptHash,
		ExpectedImmutableClaimSHA256: sha256Hex(claimBytes), ExpectedCurrentTargetSHA: target, Actor: "reviewer", Reason: "construct the original replacement",
	})
	if err != nil || refresh.Status != "conflict_candidate_refresh_prepared" {
		t.Fatalf("initial conflict refresh apply = %+v err=%v", refresh, err)
	}
	if _, err := SupersedeValidationFailedWorktreeMerge(context.Background(), WorktreeMergeValidationFailureSupersessionOptions{
		ProjectsRoot: fixture.githubDir, Receipt: original.ReceiptPath, ReplacementWorktree: refresh.Candidate.Worktree, Apply: true, Actor: "reviewer", Reason: "supersede initial conflict",
	}); err != nil {
		t.Fatal(err)
	}

	successor, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{first.WorktreeDir, second.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err == nil || successor.Status != WorktreeMergeConflict {
		t.Fatalf("successor prepare = %+v err=%v, want conflict", successor, err)
	}
	if want := worktreeMergeSupersededOperationID(original.ID, original.ReceiptPath); successor.ID != want || successor.Candidate.Task != want {
		t.Fatalf("successor identity = id=%s task=%s, want %s", successor.ID, successor.Candidate.Task, want)
	}
	successorBefore, err := os.ReadFile(successor.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	successorClaim, _, err := validatePrepareFailureSupersessionCandidate(context.Background(), fixture.githubDir, successor)
	if err != nil {
		t.Fatal(err)
	}
	successorClaimBefore, err := os.ReadFile(successorClaim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	successorHash, err := worktreeMergeReceiptSHA256(successor.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	successorTarget, err := fetchExactMergeTarget(context.Background(), successor.Candidate.Worktree, successor.Target)
	if err != nil {
		t.Fatal(err)
	}
	options := WorktreeMergeConflictCandidateRefreshOptions{
		ProjectsRoot: fixture.githubDir, Receipt: successor.ReceiptPath, Sources: []string{first.WorktreeDir, second.WorktreeDir},
		ExpectedSourceSHAs: []string{successor.Sources[0].SHA, successor.Sources[1].SHA}, ExpectedReceiptSHA256: successorHash,
		ExpectedImmutableClaimSHA256: sha256Hex(successorClaimBefore), ExpectedCurrentTargetSHA: successorTarget, Actor: "reviewer", Reason: "plan successor replacement",
	}
	dryRun, err := PrepareConflictWorktreeMergeReplacement(context.Background(), options)
	if err != nil || dryRun.Status != "conflict_candidate_refresh_planned" {
		t.Fatalf("successor conflict refresh dry-run = %+v err=%v", dryRun, err)
	}
	if current, readErr := os.ReadFile(successor.ReceiptPath); readErr != nil || string(current) != string(successorBefore) {
		t.Fatalf("dry-run changed successor receipt: err=%v", readErr)
	}
	if current, readErr := os.ReadFile(successorClaim.ClaimPath); readErr != nil || string(current) != string(successorClaimBefore) {
		t.Fatalf("dry-run changed successor claim: err=%v", readErr)
	}

	tampered := successor
	tampered.ID = successor.ID + "-tampered"
	tampered.Candidate.Task = tampered.ID
	if err := persistWorktreeMergeReceipt(tampered); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareConflictWorktreeMergeReplacement(context.Background(), options); err == nil {
		t.Fatal("tampered successor ID was accepted")
	}
}

func TestPrepareConflictWorktreeMergeReplacementRefusesPublishedAndMismatchedSources(t *testing.T) {
	fixture, receipt, _ := supersessionFixture(t)
	receipt.Status, receipt.Failure = WorktreeMergeConflict, "historical conflict"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	claim, _, err := validatePrepareFailureSupersessionCandidate(context.Background(), fixture.githubDir, receipt)
	if err != nil {
		t.Fatal(err)
	}
	claimBytes, err := os.ReadFile(claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	target, err := fetchExactMergeTarget(context.Background(), receipt.Candidate.Worktree, receipt.Target)
	if err != nil {
		t.Fatal(err)
	}
	options := WorktreeMergeConflictCandidateRefreshOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Sources: []string{receipt.Sources[0].Worktree}, ExpectedSourceSHAs: []string{receipt.Sources[0].SHA}, ExpectedReceiptSHA256: receiptHash, ExpectedImmutableClaimSHA256: sha256Hex(claimBytes), ExpectedCurrentTargetSHA: target, Apply: true, Actor: "reviewer", Reason: "refuse unsafe conflict evidence"}
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", receipt.Candidate.Branch)
	if result, err := PrepareConflictWorktreeMergeReplacement(context.Background(), options); err == nil || result.Candidate.Worktree != "" || !strings.Contains(err.Error(), "published") {
		t.Fatalf("published conflict candidate = %+v err=%v", result, err)
	}
	assertNoConflictCandidateRefresh(t, fixture, receipt, options)
	options.Sources = []string{fixture.canonical}
	options.ExpectedSourceSHAs = []string{receipt.Sources[0].SHA}
	if result, err := PrepareConflictWorktreeMergeReplacement(context.Background(), options); err == nil || result.Candidate.Worktree != "" {
		t.Fatalf("wrong source identity = %+v err=%v", result, err)
	}
}

func TestPrepareConflictWorktreeMergeReplacementRetiresNewCandidateAfterEvidenceDrift(t *testing.T) {
	fixture, receipt, _ := supersessionFixture(t)
	receipt.Status, receipt.Failure = WorktreeMergeConflict, "historical conflict"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	claim, _, err := validatePrepareFailureSupersessionCandidate(context.Background(), fixture.githubDir, receipt)
	if err != nil {
		t.Fatal(err)
	}
	claimBytes, err := os.ReadFile(claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	target, err := fetchExactMergeTarget(context.Background(), receipt.Candidate.Worktree, receipt.Target)
	if err != nil {
		t.Fatal(err)
	}
	options := WorktreeMergeConflictCandidateRefreshOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Sources: []string{receipt.Sources[0].Worktree}, ExpectedSourceSHAs: []string{receipt.Sources[0].SHA},
		ExpectedReceiptSHA256: receiptHash, ExpectedImmutableClaimSHA256: sha256Hex(claimBytes), ExpectedCurrentTargetSHA: target,
		Apply: true, Actor: "reviewer", Reason: "prove failed construction retires its unconsumed candidate",
	}
	previous := beforeConflictCandidateRefreshFinalRevalidation
	beforeConflictCandidateRefreshFinalRevalidation = func() {
		writeEngineFile(t, filepath.Join(receipt.Sources[0].Worktree, "late-drift.txt"), "drift\n")
	}
	defer func() { beforeConflictCandidateRefreshFinalRevalidation = previous }()
	if result, err := PrepareConflictWorktreeMergeReplacement(context.Background(), options); err == nil || result.Candidate.Worktree != "" || !strings.Contains(err.Error(), "source") {
		t.Fatalf("final evidence drift = %+v err=%v", result, err)
	}
	assertNoConflictCandidateRefresh(t, fixture, receipt, options)
}

func assertNoConflictCandidateRefresh(t *testing.T, fixture engineFixture, receipt WorktreeMergeReceipt, options WorktreeMergeConflictCandidateRefreshOptions) {
	t.Helper()
	listed, err := worktrees.List(context.Background(), worktrees.ListOptions{ProjectsRoot: fixture.githubDir, Task: options.RefreshTask(), Base: receipt.Target, Workers: 1})
	if err != nil || len(listed) != 0 {
		t.Fatalf("refusal leaked conflict replacement: listed=%+v err=%v", listed, err)
	}
}

func sha256Hex(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}
