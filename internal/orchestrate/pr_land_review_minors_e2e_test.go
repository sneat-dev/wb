//go:build e2e

// This file holds pr_land_review_minors_test.go's real-git cases
// (spec/plans/coverage-to-100 task-17): the local-sync fast-forward path and
// verifyUpdateBranchMergeProof now run through orchestrateGit
// (internal/runner), which task-24's runtime guard blocks outside the e2e
// tier. Moving them here, rather than calling runnertest.AllowRealProcess in
// the default tier, keeps internal/quality/testdata/unit_tier.pending's
// cross-PR total from rising.
package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLandRecordsLocalSyncEvenWhenTheWaitFailsAfterUpdate is required test
// M1 (pr_land.go:498-501): result.LocalSync must be recorded even when a
// later step in the same landing attempt (the post-update-branch wait) fails
// hard, not only on the success path. Before the fix, LandPullRequest set
// result.LocalSync AFTER its own `if err != nil { return }` check, so a
// post-update failure silently dropped the fast-forward note the operator
// most needs right when something went wrong.
//
//nolint:paralleltest // calls a fixture helper (newLandFixture/newEngineFixture/createMergeSource) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestLandRecordsLocalSyncEvenWhenTheWaitFailsAfterUpdate(t *testing.T) {
	fixture := newLandFixture(t, "feature")
	worktree := addLandWorktree(t, fixture, "feature")
	advanceLandTarget(t, fixture)
	fixture.writeState(t, "fail-compare-after-update", "1")

	result, err := LandPullRequest(context.Background(), landOptions(fixture))
	if err == nil {
		t.Fatalf("want a hard error once the post-update behind-check read failed, got result=%+v", result)
	}
	if !strings.Contains(result.LocalSync, "fast-forwarded worktree") {
		t.Fatalf("LocalSync = %q, want the update-branch fast-forward note even though the later wait errored (M1)", result.LocalSync)
	}
	if got := runEngineGit(t, worktree, "rev-parse", "HEAD"); strings.TrimSpace(got) == "" {
		t.Fatal("worktree HEAD unreadable")
	}
}

// TestLandDoesNotLeakLocalSyncIntoEvidence is required test M2
// (pr_land_engine.go:127): the update-branch fast-forward note must reach
// the typed LocalSync field only, not also survive as a stray
// evidence["local_sync"] key that would leak into `wb pr land --json`
// alongside it.
//
//nolint:paralleltest // calls a fixture helper (newLandFixture/newEngineFixture/createMergeSource) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestLandDoesNotLeakLocalSyncIntoEvidence(t *testing.T) {
	fixture := newLandFixture(t, "feature")
	_ = addLandWorktree(t, fixture, "feature")
	advanceLandTarget(t, fixture)

	result, err := LandPullRequest(context.Background(), landOptions(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s, want success; reason = %s", result.Outcome, result.Reason)
	}
	if result.LocalSync == "" {
		t.Fatal("fixture invariant broken: expected an update-branch fast-forward to have run")
	}
	if _, leaked := result.Evidence["local_sync"]; leaked {
		t.Fatalf("evidence[\"local_sync\"] leaked alongside the typed LocalSync field (M2): %q", result.Evidence["local_sync"])
	}
}

// TestAdoptWorktreeMergeUpdateBranchAdvanceSurfacesATransientProofFailureAsRetryable
// is required test Minor 5 (review round on #614,
// verifyUpdateBranchMergeProof's commitTreeSHA fallback): a transient
// GitHub read failure while computing the update-branch merge proof must
// surface as a retryable error (IsTransientReadFailure), not be flattened
// into the same "not proved, refuse" outcome a genuine mismatch produces -
// landWorktreeMergePullRequest's own IsTransientReadFailure check then
// classifies it as WorktreeMergeChecksPending, never Conflict.
//
//nolint:paralleltest // calls t.Setenv("WB_TEST_COMMIT_TREE_TRANSIENT", ...), which Go's testing package forbids combined with t.Parallel
func TestAdoptWorktreeMergeUpdateBranchAdvanceSurfacesATransientProofFailureAsRetryable(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "m5-transient-proof-source", "feature/m5-transient-proof", "m5.txt", "m5\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	installWorktreeMergeEngineGH(t, fixture, receipt.Candidate.SHA, receipt.Candidate.Branch)
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/"+receipt.Candidate.Branch)

	// Build the update-branch merge commit directly in the bare remote,
	// under no ref at all - reachable by exact SHA (as GitHub's commits API
	// would serve it) but never fetchable via the candidate branch name, so
	// verifyUpdateBranchMergeProof's headLocal stays false and it falls
	// through to the commitTreeSHA read this test targets.
	tree := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", receipt.Candidate.SHA+"^{tree}"))
	updated := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "commit-tree", tree, "-p", receipt.Candidate.SHA, "-p", receipt.TargetSHA, "-m", "merge main"))

	marker := filepath.Join(t.TempDir(), "commit-tree-transient")
	if err := os.WriteFile(marker, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_COMMIT_TREE_TRANSIENT", marker)

	originalCandidateSHA := receipt.Candidate.SHA
	adoptErr := adoptWorktreeMergeUpdateBranchAdvance(context.Background(), defaultGit, defaultRunner, &receipt, receipt.Candidate.SHA, updated)
	if adoptErr == nil {
		t.Fatal("want an error once the proof's own GitHub read fails transiently on every attempt")
	}
	if !IsTransientReadFailure(adoptErr) {
		t.Fatalf("error = %v, want IsTransientReadFailure to recognize it as retryable, not a definitive refusal", adoptErr)
	}
	if receipt.Candidate.SHA != originalCandidateSHA || len(receipt.TargetRefreshes) != 0 {
		t.Fatalf("receipt was mutated by a transient proof failure: candidate=%s refreshes=%d", receipt.Candidate.SHA, len(receipt.TargetRefreshes))
	}
}
