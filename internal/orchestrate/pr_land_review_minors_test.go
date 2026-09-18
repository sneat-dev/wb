package orchestrate

import (
	"context"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/progress"
)

// TestLandRecordsLocalSyncEvenWhenTheWaitFailsAfterUpdate is required test
// M1 (pr_land.go:498-501): result.LocalSync must be recorded even when a
// later step in the same landing attempt (the post-update-branch wait) fails
// hard, not only on the success path. Before the fix, LandPullRequest set
// result.LocalSync AFTER its own `if err != nil { return }` check, so a
// post-update failure silently dropped the fast-forward note the operator
// most needs right when something went wrong.
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

// TestLandRefusesWhenTheReReadHeadDiffersFromTheUpdateBranchResult is
// required test M3 (pr_land_engine.go:117, worktree_merge_pr_land.go:238):
// a crafted force-push landing in the exact window between the
// update-branch write and the engine's own re-read must be refused, not
// silently adopted as a trusted update-branch advance. Before the fix, the
// hook was handed updatedView.Head.SHA (the re-read value) rather than
// updatedHead (what the update-branch write itself reported), so the two
// were never compared against each other at all.
func TestLandRefusesWhenTheReReadHeadDiffersFromTheUpdateBranchResult(t *testing.T) {
	fixture := newLandFixture(t, "feature")
	advanceLandTarget(t, fixture)
	const racedHead = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	fixture.writeState(t, "force-push-race", racedHead)

	result, err := LandPullRequest(context.Background(), landOptions(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandRefused || result.RefusalCode != LandRefusalHeadMoved {
		t.Fatalf("outcome/refusal = %s/%s, want %s/%s: %+v", result.Outcome, result.RefusalCode, LandRefused, LandRefusalHeadMoved, result)
	}
}

// TestAdvancePublishedWorktreeMergeCandidateClearsAStaleLocalSyncNote is
// required test M6 (worktree_merge.go:1771-1773): once the local worktree's
// HEAD already matches the recorded candidate, an earlier fast-forward
// failure note left on receipt.LocalSync no longer describes anything live
// and must be cleared, not left to confuse the next read of the receipt.
func TestAdvancePublishedWorktreeMergeCandidateClearsAStaleLocalSyncNote(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "m6-clear-source", "feature/m6-clear", "m6-clear.txt", "clear\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.LocalSync = "local worktree not fast-forwarded: stale failure note from an earlier resume"
	advanced, err := advancePublishedWorktreeMergeCandidate(context.Background(), &receipt)
	if err != nil {
		t.Fatal(err)
	}
	if advanced {
		t.Fatalf("HEAD already matches the recorded candidate; want no advance, got advanced=%v", advanced)
	}
	if receipt.LocalSync != "" {
		t.Fatalf("LocalSync = %q, want it cleared once HEAD matches the recorded candidate (M6)", receipt.LocalSync)
	}
}

// TestAdvancePublishedWorktreeMergeCandidateAppendsLocalSyncToDriftError is
// required test M6's second half (worktree_merge_pr_land.go:261,364): a
// generic candidate-drift error must include whatever receipt.LocalSync
// already recorded about why the local worktree fell out of step, not just
// the drift itself.
func TestAdvancePublishedWorktreeMergeCandidateAppendsLocalSyncToDriftError(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "m6-append-source", "feature/m6-append", "m6-append.txt", "append\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	const note = "local worktree not fast-forwarded: fetched head does not match updated head"
	receipt.LocalSync = note
	// Drift the worktree's own HEAD without recording it anywhere on the
	// receipt (no TargetRefreshes, no PublishedCandidateSHA), so
	// advancePublishedWorktreeMergeCandidate falls straight into the
	// "drifted without an exact published predecessor" error this test
	// targets.
	writeEngineFile(t, receipt.Candidate.Worktree+"/m6-append-extra.txt", "extra\n")
	runEngineGit(t, receipt.Candidate.Worktree, "add", "-A")
	runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "unrecorded local drift")

	_, err = advancePublishedWorktreeMergeCandidate(context.Background(), &receipt)
	if err == nil {
		t.Fatal("want a drift error for an unrecorded candidate advance")
	}
	if !strings.Contains(err.Error(), note) {
		t.Fatalf("drift error = %q, want it to include the recorded LocalSync note (M6): %q", err.Error(), note)
	}
}

// TestCandidateChecksProgressReportsGitHubAutoMergeNotFailed is the required
// label test (#600/#614): when the candidate-checks wait ends because the
// target moved under the head and the re-read shows GitHub's own armed
// auto-merge already merged the pull request, the phase's progress detail
// must say so - never the generic wait-result status string ("failed"),
// which every one of GitHub's own green checks contradicts.
func TestCandidateChecksProgressReportsGitHubAutoMergeNotFailed(t *testing.T) {
	fixture := newLandFixture(t, "feature")
	fixture.writeState(t, "auto-merge-lands-on-checks", "1")

	var candidateChecksDetail string
	var sawFailedDetail bool
	options := landOptions(fixture)
	options.OperationProgress = progress.Reporter(func(event progress.Event) {
		if event.Phase == "candidate_checks" && event.State == progress.Completed {
			candidateChecksDetail = event.Detail
			if event.Detail == "failed" {
				sawFailedDetail = true
			}
		}
	})

	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s, want success; reason = %s", result.Outcome, result.Reason)
	}
	if sawFailedDetail {
		t.Fatalf("candidate_checks progress reported %q for a pull request GitHub auto-merge already landed with every check green", "failed")
	}
	if candidateChecksDetail != mergedByGitHubAutoMergeDetail {
		t.Fatalf("candidate_checks completed detail = %q, want %q", candidateChecksDetail, mergedByGitHubAutoMergeDetail)
	}
}
