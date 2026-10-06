//go:build e2e

package orchestrate

import (
	"context"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// Prepare is the genuine default engine: the invalid Go source produces a real
// validation_failed receipt. Each TOP shares only its private native baseline.
func unpublishedOwnerFixture(t *testing.T) (engineFixture, WorktreeMergeReceipt) {
	t.Helper()
	f := newExplicitRootEngineFixture(t)
	writeEngineGoModule(t, f.canonical, "package app\n")
	runEngineGit(t, f.canonical, "add", "go.mod", "app.go")
	runEngineGit(t, f.canonical, "commit", "-m", "test: native unpublished validation baseline")
	runEngineGit(t, f.canonical, "push", "origin", "main")
	source := createMergeSource(t, f, "unpublished-owner-source", "feature/unpublished-owner", "candidate.go", "package app\n\nfunc Candidate() { missingCandidate }\n")
	r, err := PrepareWorktreeMerge(t.Context(), WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err == nil || r.Status != WorktreeMergeValidationFailed || r.Phase != WorktreeMergePhasePrepare || r.PullRequest != "" {
		t.Fatalf("native failed prepare: %+v %v", r, err)
	}
	return f, r
}

func unpublishedOwnerOptions(f engineFixture, r WorktreeMergeReceipt) WorktreeMergeUnpublishedValidationFailureAcknowledgementOptions {
	return WorktreeMergeUnpublishedValidationFailureAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "actual unpublished candidate and preserved sources"}
}

func unpublishedOwnerCall(ctx context.Context, o WorktreeMergeUnpublishedValidationFailureAcknowledgementOptions, run runner.Runner) (WorktreeMergeUnpublishedValidationFailureAcknowledgement, error) {
	return acknowledgeUnpublishedValidationFailure(ctx, o, readWorktreeMergeReceipt, run, worktreeMergeReceiptSHA256, persistUnpublishedValidationFailureAcknowledgement, worktrees.CanonicalRepositoryPath)
}
