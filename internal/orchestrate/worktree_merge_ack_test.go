package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcknowledgeLandedValidationFailureLeavesHistoricalReceiptUntouched(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "ack-source", "feature/ack", "ack.txt", "ack\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status = WorktreeMergeValidationFailed
	receipt.Failure = "candidate validation failed"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}

	// The candidate is the exact commit that the current target will contain.
	runEngineGit(t, fixture.canonical, "update-ref", "refs/heads/main", receipt.Candidate.SHA)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")

	dryRun, err := AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dryRun.Status != "landed_failure_acknowledged" || dryRun.CurrentTargetSHA != receipt.Candidate.SHA {
		t.Fatalf("dry-run acknowledgement = %+v", dryRun)
	}
	if _, err := os.Stat(dryRun.AcknowledgementPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote acknowledgement: %v", err)
	}

	ack, err := AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "verified candidate landed before validation receipt became terminal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ack.ID == "" || ack.AcknowledgementPath == "" {
		t.Fatalf("applied acknowledgement lacks identity: %+v", ack)
	}
	unchanged, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != string(original) {
		t.Fatal("historical merge receipt was rewritten")
	}
	if _, err := os.Stat(ack.AcknowledgementPath); err != nil {
		t.Fatalf("acknowledgement missing: %v", err)
	}
	ackAgain, err := AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "retry",
	})
	if err != nil || ackAgain.ID != ack.ID {
		t.Fatalf("idempotent acknowledgement = %+v err=%v", ackAgain, err)
	}
	if _, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	}); err == nil || !strings.Contains(err.Error(), "acknowledged as a historical landed failure") {
		t.Fatalf("acknowledged receipt was replayable: %v", err)
	}

	// The old non-terminal receipt no longer owns the lane, so a new source
	// revision can pass normal prepare preflight without branch-only evidence.
	newSource := createMergeSource(t, fixture, "ack-source-new", "feature/ack-new", "new.txt", "new\n")
	next, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{newSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.Status != WorktreeMergePrepared || next.Candidate.SHA == "" {
		t.Fatalf("new merge preflight = %+v", next)
	}
}

func TestAcknowledgeLandedPostTargetCIFailureLeavesReceiptForFreshForwardRepair(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "contactus-source", "feature/contactus", "contactus.txt", "contactus\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce Contactus: the candidate was directly landed, then the exact
	// target CI receipt failed and left its immutable land receipt nonterminal.
	runEngineGit(t, fixture.canonical, "update-ref", "refs/heads/main", receipt.Candidate.SHA)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	receipt.Phase = WorktreeMergePhaseLand
	receipt.Status = WorktreeMergePostTargetCIFailed
	receipt.LandingSHA = receipt.Candidate.SHA
	receipt.Checks = PullRequestWaitResult{Status: PullRequestWaitFailed, Head: receipt.LandingSHA, Reason: "required target check failed"}
	receipt.Failure = "post-target CI failed"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}

	ack, err := AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "Contactus candidate is in remote main and its post-target CI failure remains historical",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ack.ReceiptStatus != WorktreeMergePostTargetCIFailed || ack.ReceiptLandingSHA != receipt.LandingSHA || ack.CurrentTargetSHA != receipt.LandingSHA {
		t.Fatalf("post-target acknowledgement = %+v", ack)
	}
	unchanged, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil || string(unchanged) != string(original) {
		t.Fatalf("post-target receipt changed: err=%v", err)
	}

	// A repair starts from an additive source revision on a different receipt;
	// it cannot replay or overwrite the historical failed landing.
	writeEngineFile(t, filepath.Join(source.WorktreeDir, "repair.txt"), "repair\n")
	runEngineGit(t, source.WorktreeDir, "add", "repair.txt")
	runEngineGit(t, source.WorktreeDir, "commit", "-m", "fix: repair Contactus target CI")
	repair, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if repair.ReceiptPath == receipt.ReceiptPath || repair.Candidate.SHA == receipt.Candidate.SHA {
		t.Fatalf("forward repair reused immutable historical receipt: old=%+v new=%+v", receipt, repair)
	}
}

func TestAcknowledgeLandedValidationFailureRefusesMissingTargetContainment(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "ack-refuse", "feature/ack-refuse", "ack.txt", "ack\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status = WorktreeMergeValidationFailed
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	_, err = AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "unsafe test",
	})
	if err == nil || !strings.Contains(err.Error(), "does not contain receipted candidate") {
		t.Fatalf("missing target containment error = %v", err)
	}
	if _, statErr := os.Stat(landedFailureAcknowledgementPath(receipt.ReceiptPath)); !os.IsNotExist(statErr) {
		t.Fatalf("refusal left acknowledgement: %v", statErr)
	}
}

func TestAcknowledgeLandedValidationFailureRefusesChangedReceiptedSource(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "ack-source-drift", "feature/ack-source-drift", "ack.txt", "ack\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status = WorktreeMergeValidationFailed
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, fixture.canonical, "update-ref", "refs/heads/main", receipt.Candidate.SHA)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	writeEngineFile(t, filepath.Join(source.WorktreeDir, "changed-after-receipt.txt"), "changed\n")
	runEngineGit(t, source.WorktreeDir, "add", "changed-after-receipt.txt")
	runEngineGit(t, source.WorktreeDir, "commit", "-m", "fix: advance receipted source")

	_, err = AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "unsafe source drift",
	})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("changed receipted source acknowledgement error = %v", err)
	}
}
