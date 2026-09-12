package orchestrate

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestAcknowledgeDiscardedInterruptedPreparingFreesLane(t *testing.T) {
	fixture := newEngineFixture(t)
	writeEngineGoModule(t, fixture.canonical, "package app\n")
	runEngineGit(t, fixture.canonical, "add", "go.mod", "app.go")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: add Go validation fixture")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	source := createMergeSource(t, fixture, "retained-interrupted-source", "feature/retained-interrupted", "candidate.go", "package app\n\nfunc Candidate() {}\n")

	prepared, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir,
		Sources:      []string{source.WorktreeDir},
		Target:       "main",
		Model:        "test-model",
		AgentRuntime: "test",
	})
	if err != nil || prepared.Status != WorktreeMergePrepared {
		t.Fatalf("prepare = %+v err=%v", prepared, err)
	}
	prepared.Status = WorktreeMergePreparing
	if err := persistWorktreeMergeReceipt(prepared); err != nil {
		t.Fatal(err)
	}
	aborted, err := worktrees.Abort(context.Background(), worktrees.AbortOptions{
		ProjectsRoot: fixture.githubDir,
		Task:         prepared.Candidate.Task,
		Base:         "main",
		Disposition:  worktrees.AbortDiscarded,
		DeleteRemote: true,
		Apply:        true,
	})
	if err != nil || len(aborted) != 1 || !aborted[0].Applied || !aborted[0].WorktreeGone || !aborted[0].BranchDeleted {
		t.Fatalf("abort candidate = %+v err=%v", aborted, err)
	}
	proof, err := worktrees.FindDiscardedLifecycleBacklogProof(context.Background(), fixture.githubDir, prepared.Repository, prepared.Target,
		prepared.Candidate.Task, prepared.Candidate.Worktree, prepared.Candidate.Branch, prepared.Candidate.SHA)
	if err != nil {
		t.Fatal(err)
	}
	backlogBytes, err := os.ReadFile(proof.Path)
	if err != nil {
		t.Fatal(err)
	}
	tamperedBacklog := bytes.Replace(backlogBytes, []byte(`"stage": "complete"`), []byte(`"stage": "worktree_removed"`), 1)
	if err := os.WriteFile(proof.Path, tamperedBacklog, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AcknowledgeUnpublishedValidationFailure(context.Background(), WorktreeMergeUnpublishedValidationFailureAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: prepared.ReceiptPath,
	}); err == nil || !strings.Contains(err.Error(), "is not complete") {
		t.Fatalf("incomplete discarded cleanup proof was accepted: %v", err)
	}
	if err := os.WriteFile(proof.Path, backlogBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source.WorktreeDir+"/repair.go", []byte("package app\n\nfunc Repair() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, source.WorktreeDir, "add", "repair.go")
	runEngineGit(t, source.WorktreeDir, "commit", "-m", "fix: advance preserved source")
	advancedSourceHead := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))

	ack, err := AcknowledgeUnpublishedValidationFailure(context.Background(), WorktreeMergeUnpublishedValidationFailureAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir,
		Receipt:      prepared.ReceiptPath,
		Apply:        true,
		Actor:        "reviewer",
		Reason:       "interrupted unpublished candidate was deliberately discarded",
	})
	if err != nil || ack.CandidateCleanupBacklog == "" {
		t.Fatalf("acknowledge discarded prepare = %+v err=%v", ack, err)
	}
	if len(ack.PreservedSources) != 1 || ack.PreservedSources[0].SHA != advancedSourceHead || ack.PreservedSources[0].SHA == prepared.Sources[0].SHA {
		t.Fatalf("preserved source evidence = %+v", ack.PreservedSources)
	}
	next, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir,
		Sources:      []string{source.WorktreeDir},
		Target:       "main",
		Model:        "test-model",
		AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.Status != WorktreeMergePrepared || next.ReceiptPath == prepared.ReceiptPath {
		t.Fatalf("fresh prepare after discarded acknowledgement = %+v", next)
	}
}

func TestAcknowledgeUnpublishedValidationFailureFreesLane(t *testing.T) {
	fixture := newEngineFixture(t)
	writeEngineGoModule(t, fixture.canonical, "package app\n")
	runEngineGit(t, fixture.canonical, "add", "go.mod", "app.go")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: add Go validation fixture")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	failedSource := createMergeSource(t, fixture, "retained-failed-source", "feature/retained-failed", "candidate.go", "package app\n\nfunc Candidate() { missingCandidate }\n")

	failed, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir,
		Sources:      []string{failedSource.WorktreeDir},
		Target:       "main",
		Model:        "test-model",
		AgentRuntime: "test",
	})
	if err == nil || failed.Status != WorktreeMergeValidationFailed || failed.PullRequest != "" {
		t.Fatalf("initial validation failure = receipt %+v err %v", failed, err)
	}

	dryRun, err := AcknowledgeUnpublishedValidationFailure(context.Background(), WorktreeMergeUnpublishedValidationFailureAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir,
		Receipt:      failed.ReceiptPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dryRun.AcknowledgementPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote acknowledgement: %v", err)
	}

	ack, err := AcknowledgeUnpublishedValidationFailure(context.Background(), WorktreeMergeUnpublishedValidationFailureAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir,
		Receipt:      failed.ReceiptPath,
		Apply:        true,
		Actor:        "reviewer",
		Reason:       "failed attempt is unpublished and every source is preserved",
	})
	if err != nil || ack.ID == "" {
		t.Fatalf("applied acknowledgement = %+v err=%v", ack, err)
	}

	passingSource := createMergeSource(t, fixture, "different-passing-source", "feature/different-passing", "passing.go", "package app\n\nfunc Passing() {}\n")
	ackBytes, err := os.ReadFile(ack.AcknowledgementPath)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(ackBytes, []byte(failed.Candidate.SHA), []byte(strings.Repeat("0", len(failed.Candidate.SHA))), 1)
	if err := os.WriteFile(ack.AcknowledgementPath, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{passingSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	}); err == nil || !strings.Contains(err.Error(), "invalid immutable identity") {
		t.Fatalf("tampered acknowledgement did not keep the lane blocked: %v", err)
	}
	if err := os.WriteFile(ack.AcknowledgementPath, ackBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	next, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir,
		Sources:      []string{passingSource.WorktreeDir},
		Target:       "main",
		Model:        "test-model",
		AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.Status != WorktreeMergePrepared || next.ReceiptPath == failed.ReceiptPath {
		t.Fatalf("new source did not receive a fresh lane receipt: %+v", next)
	}
}
