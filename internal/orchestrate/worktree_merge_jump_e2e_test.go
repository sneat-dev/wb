//go:build e2e

package orchestrate

import (
	"context"
	"strings"
	"testing"
)

//nolint:paralleltest // newEngineFixture changes the process environment with t.Setenv
func TestE2EPrepareWorktreeMergeUnpublishedConflictDoesNotBlockIndependentSource(t *testing.T) {
	fixture := newEngineFixture(t)
	sourceA := createMergeSource(t, fixture, "queue-conflict-a", "feature/queue-a", "shared.txt", "a\n")
	sourceB := createMergeSource(t, fixture, "queue-conflict-b", "feature/queue-b", "shared.txt", "b\n")
	conflict, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{sourceA.WorktreeDir, sourceB.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err == nil || conflict.Status != WorktreeMergeConflict || conflict.Phase != WorktreeMergePhasePrepare {
		t.Fatalf("first prepare = %+v, %v; want prepare conflict", conflict, err)
	}
	independent := createMergeSource(t, fixture, "queue-independent", "feature/queue-independent", "independent.txt", "ready\n")
	runEngineGit(t, conflict.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/"+conflict.Candidate.Branch)
	_, err = PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{independent.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err == nil || !strings.Contains(err.Error(), "still owned") {
		t.Fatalf("published conflict should hold the lane: %v", err)
	}
	runEngineGit(t, conflict.Candidate.Worktree, "push", "origin", ":refs/heads/"+conflict.Candidate.Branch)
	prepared, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{independent.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil || prepared.Status != WorktreeMergePrepared {
		t.Fatalf("independent prepare = %+v, %v; want prepared", prepared, err)
	}
	if prepared.ReceiptPath == conflict.ReceiptPath {
		t.Fatalf("independent prepare reused conflict receipt %s", conflict.ReceiptPath)
	}
	retained, err := readWorktreeMergeReceipt(conflict.ReceiptPath)
	if err != nil || retained.Status != WorktreeMergeConflict {
		t.Fatalf("old conflict receipt = %+v, %v; want preserved conflict", retained, err)
	}
}
