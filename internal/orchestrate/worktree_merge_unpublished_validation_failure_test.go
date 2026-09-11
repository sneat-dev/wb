package orchestrate

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

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
