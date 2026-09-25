package orchestrate

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/progress"
)

// TestLandRecordsLocalSyncEvenWhenTheWaitFailsAfterUpdate and
// TestLandDoesNotLeakLocalSyncIntoEvidence moved to
// pr_land_review_minors_e2e_test.go (spec/plans/coverage-to-100 task-17):
// the local-sync fast-forward path now runs through orchestrateGit
// (internal/runner), which task-24's runtime guard blocks outside the e2e
// tier.

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
	advanced, err := advancePublishedWorktreeMergeCandidate(context.Background(), nil, &receipt)
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

	_, err = advancePublishedWorktreeMergeCandidate(context.Background(), nil, &receipt)
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

// TestPrepareWorktreeMergeRebatchRecoversATransientVerifyReadAfterClose is
// required test M5's first half (worktree_merge_ack.go:826-836): the PATCH
// that closes a superseded pull request already succeeded by the time its
// own post-close verification re-read runs. A single transient GitHub read
// failure on that re-read is not a verdict on the close - it is exactly the
// kind of blip every other read in this area retries - and the rebatch must
// proceed once the in-process retry recovers, not surface the blip as a
// hard failure that leaves an already-closed pull request looking stuck.
func TestPrepareWorktreeMergeRebatchRecoversATransientVerifyReadAfterClose(t *testing.T) {
	fixture := newEngineFixture(t)
	firstSource := createMergeSource(t, fixture, "m5-transient-verify-first", "feature/m5-transient-verify-first", "first.txt", "first\n")
	secondSource := createMergeSource(t, fixture, "m5-transient-verify-second", "feature/m5-transient-verify-second", "second.txt", "second\n")
	first, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{firstSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, first.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/"+first.Candidate.Branch)
	first.Phase = WorktreeMergePhaseLand
	first.Status = WorktreeMergeChecksFailed
	first.PullRequest = "41"
	first.PublishedCandidateSHA = first.Candidate.SHA
	first.Failure = "strict required-check fence unavailable"
	first.AutoMergeArmed = true
	if err := persistWorktreeMergeReceipt(first); err != nil {
		t.Fatal(err)
	}
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_CANDIDATE_SHA", first.Candidate.SHA)
	closedLog := filepath.Join(t.TempDir(), "closed-pr.log")
	t.Setenv("WB_TEST_CLOSED_PR_LOG", closedLog)
	marker := filepath.Join(t.TempDir(), "verify-read-fail-once")
	if err := os.WriteFile(marker, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_VERIFY_READ_FAIL_ONCE", marker)

	replacement, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{firstSource.WorktreeDir, secondSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", RebatchReceipt: first.ReceiptPath,
	})
	if err != nil {
		t.Fatalf("rebatch failed despite a recoverable transient verify-read failure: %v", err)
	}
	if replacement.SupersededPullRequest != "41" {
		t.Fatalf("replacement receipt did not record the closed superseded pull request: %+v", replacement)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("verify-read marker was never consumed; the transient failure this test injects was never exercised: statErr=%v", statErr)
	}
	closedCalls, readErr := os.ReadFile(closedLog)
	if readErr != nil || !strings.Contains(string(closedCalls), "pulls/41") {
		t.Fatalf("superseded pull request was not closed: err=%v calls=%q", readErr, string(closedCalls))
	}
}

// TestPrepareWorktreeMergeRebatchResumeAcceptsClosedNotMergedAfterCrashBeforeAck
// is required test M5's second half (worktree_merge_ack.go:879-895): the
// replacement receipt's SupersededPullRequest is persisted BEFORE the PATCH
// that closes the original candidate's pull request, so a crash between a
// successful close and this function's own rebatch acknowledgement leaves a
// durable trail. A resume must re-run the (idempotent) close, accept the
// pull request it re-reads as "closed, not merged by us" as the retired
// state, and complete the rebatch acknowledgement - never strand the lane
// on a pull request that is already exactly what WB wanted.
func TestPrepareWorktreeMergeRebatchResumeAcceptsClosedNotMergedAfterCrashBeforeAck(t *testing.T) {
	fixture := newEngineFixture(t)
	firstSource := createMergeSource(t, fixture, "m5-resume-crash-first", "feature/m5-resume-crash-first", "first.txt", "first\n")
	secondSource := createMergeSource(t, fixture, "m5-resume-crash-second", "feature/m5-resume-crash-second", "second.txt", "second\n")
	first, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{firstSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, first.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/"+first.Candidate.Branch)
	first.Phase = WorktreeMergePhaseLand
	first.Status = WorktreeMergeChecksFailed
	first.PullRequest = "41"
	first.PublishedCandidateSHA = first.Candidate.SHA
	first.Failure = "strict required-check fence unavailable"
	first.AutoMergeArmed = true
	if err := persistWorktreeMergeReceipt(first); err != nil {
		t.Fatal(err)
	}
	originalReceipt, err := os.ReadFile(first.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_CANDIDATE_SHA", first.Candidate.SHA)
	closedLog := filepath.Join(t.TempDir(), "closed-pr.log")
	t.Setenv("WB_TEST_CLOSED_PR_LOG", closedLog)

	// Simulate a crash between the close (which the fixture's PATCH case
	// makes durable on the fake remote by flipping the recorded PR state to
	// "closed") and this rebatch's own acknowledgement write.
	previousPersist := persistPreparedWorktreeMergeRebatchForPrepare
	persistPreparedWorktreeMergeRebatchForPrepare = func(string, WorktreeMergePreparedRebatch) error { return os.ErrPermission }
	partial, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{firstSource.WorktreeDir, secondSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", RebatchReceipt: first.ReceiptPath,
	})
	persistPreparedWorktreeMergeRebatchForPrepare = previousPersist
	if err == nil {
		t.Fatalf("want the injected post-close acknowledgement write failure to surface, got a clean rebatch: %+v", partial)
	}
	closedCalls, readErr := os.ReadFile(closedLog)
	if readErr != nil || !strings.Contains(string(closedCalls), "pulls/41") {
		t.Fatalf("close never reached the API before the simulated crash: err=%v calls=%q", readErr, string(closedCalls))
	}
	if current, readErr := os.ReadFile(first.ReceiptPath); readErr != nil || !bytes.Equal(current, originalReceipt) {
		t.Fatalf("original receipt changed despite the crash before acknowledgement: err=%v", readErr)
	}
	if _, statErr := os.Stat(rebatchPath(first.ReceiptPath)); !os.IsNotExist(statErr) {
		t.Fatal("a crashed rebatch left behind an acknowledgement")
	}

	// Resume: the pull request is already closed (and never merged) on the
	// fake remote from the pre-crash close. The resume must accept that as
	// the retired state, complete the idempotent close, and finish the
	// acknowledgement rather than stranding the lane.
	replacement, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{firstSource.WorktreeDir, secondSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", RebatchReceipt: first.ReceiptPath,
	})
	if err != nil {
		t.Fatalf("resume after the crash did not accept the already-closed, not-merged pull request as retired: %v", err)
	}
	if replacement.SupersededPullRequest != "41" {
		t.Fatalf("resumed replacement receipt did not record the closed superseded pull request: %+v", replacement)
	}
	rebatch, err := readPreparedWorktreeMergeRebatch(rebatchPath(first.ReceiptPath), first)
	if err != nil {
		t.Fatal(err)
	}
	if rebatch.ClosedPullRequest != "41" {
		t.Fatalf("resumed rebatch acknowledgement did not record the closed pull request: %+v", rebatch)
	}
}

// TestAdoptWorktreeMergeUpdateBranchAdvanceRefusesAHeadItDidNotHold is
// required test Minor 3 (review round on #614, worktree_merge_pr_land.go's
// adoptWorktreeMergeUpdateBranchAdvance): the engine hands this hook
// whatever head it itself last observed as "previous". Before this fix, the
// hook never checked that value against the receipt's own recorded
// candidate before using it - a stale or mismatched "previous" would still
// be accepted, and "updated" silently adopted as this receipt's own advance
// even though the receipt never actually held "previous" as its candidate.
// It must refuse instead, and never touch the receipt or reach the network.
func TestAdoptWorktreeMergeUpdateBranchAdvanceRefusesAHeadItDidNotHold(t *testing.T) {
	receipt := WorktreeMergeReceipt{
		Repository: "acme/app", Target: "main",
		Candidate: WorktreeMergeCandidate{Worktree: "/nonexistent", Branch: "candidate", SHA: "cccccccccccccccccccccccccccccccccccccccc"},
		TargetSHA: "tttttttttttttttttttttttttttttttttttttttt",
	}
	originalCandidateSHA := receipt.Candidate.SHA
	originalTargetSHA := receipt.TargetSHA
	const staleHead = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	const updated = "1234567890123456789012345678901234567890"

	err := adoptWorktreeMergeUpdateBranchAdvance(context.Background(), nil, &receipt, staleHead, updated)
	if err == nil {
		t.Fatalf("want a refusal when previous (%s) does not match the receipt's own candidate (%s)", staleHead, receipt.Candidate.SHA)
	}
	if !strings.Contains(err.Error(), "did not hold") {
		t.Fatalf("error = %q, want it to name refusing an advance from a head the receipt did not hold", err.Error())
	}
	if receipt.Candidate.SHA != originalCandidateSHA || receipt.TargetSHA != originalTargetSHA || len(receipt.TargetRefreshes) != 0 {
		t.Fatalf("receipt was mutated by a refused adoption: candidate=%s target=%s refreshes=%d",
			receipt.Candidate.SHA, receipt.TargetSHA, len(receipt.TargetRefreshes))
	}
}

// TestAdoptWorktreeMergeUpdateBranchAdvanceSurfacesATransientProofFailureAsRetryable
// moved to pr_land_review_minors_e2e_test.go (spec/plans/coverage-to-100
// task-17): verifyUpdateBranchMergeProof now resolves through orchestrateGit
// (internal/runner), which task-24's runtime guard blocks outside the e2e
// tier.

// TestValidatePublishedUnlandedRebatchRefusesASupersededPullRequestNotBoundToThisOriginal
// is required test Minor 6 (review round on #614,
// worktreeMergeReplacementRecordsSupersession): SupersededPullRequest alone
// is just a string field on a receipt WB itself wrote to disk - nothing
// about the string forces it to name the pull request THIS original
// receipt is being rebatched away from. This test reaches the exact crash
// state M5's second test recovers from (a replacement whose
// SupersededPullRequest already names the original's own closed pull
// request), then corrupts that replacement's RebatchOf to point somewhere
// else - simulating a receipt that was not, in fact, produced as a
// replacement for this original. A resume must then refuse the pull
// request as not open/unmerged, exactly as it would an externally closed
// one, rather than trust a SupersededPullRequest that is not bound back to
// this original via RebatchOf.
func TestValidatePublishedUnlandedRebatchRefusesASupersededPullRequestNotBoundToThisOriginal(t *testing.T) {
	fixture := newEngineFixture(t)
	firstSource := createMergeSource(t, fixture, "m6-bind-first", "feature/m6-bind-first", "first.txt", "first\n")
	secondSource := createMergeSource(t, fixture, "m6-bind-second", "feature/m6-bind-second", "second.txt", "second\n")
	first, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{firstSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, first.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/"+first.Candidate.Branch)
	first.Phase = WorktreeMergePhaseLand
	first.Status = WorktreeMergeChecksFailed
	first.PullRequest = "41"
	first.PublishedCandidateSHA = first.Candidate.SHA
	first.Failure = "strict required-check fence unavailable"
	first.AutoMergeArmed = true
	if err := persistWorktreeMergeReceipt(first); err != nil {
		t.Fatal(err)
	}
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_CANDIDATE_SHA", first.Candidate.SHA)
	closedLog := filepath.Join(t.TempDir(), "closed-pr.log")
	t.Setenv("WB_TEST_CLOSED_PR_LOG", closedLog)

	// Reach the exact crash state: close succeeds and the replacement's
	// SupersededPullRequest is durably persisted, but the acknowledgement
	// write fails (simulating a crash before it).
	previousPersist := persistPreparedWorktreeMergeRebatchForPrepare
	persistPreparedWorktreeMergeRebatchForPrepare = func(string, WorktreeMergePreparedRebatch) error { return os.ErrPermission }
	partial, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{firstSource.WorktreeDir, secondSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", RebatchReceipt: first.ReceiptPath,
	})
	persistPreparedWorktreeMergeRebatchForPrepare = previousPersist
	if err == nil || partial.ReceiptPath == "" {
		t.Fatalf("want the injected acknowledgement write failure to surface with a persisted replacement path: partial=%+v err=%v", partial, err)
	}
	if partial.SupersededPullRequest != "41" {
		t.Fatalf("replacement was not persisted with the superseded pull request before the simulated crash: %+v", partial)
	}

	// Corrupt the persisted replacement's binding: it no longer names this
	// original as the receipt it rebatches, even though its
	// SupersededPullRequest still names pull request 41.
	corrupted, err := readWorktreeMergeReceipt(partial.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	corrupted.RebatchOf = first.ReceiptPath + "-not-this-original"
	if err := persistWorktreeMergeReceipt(corrupted); err != nil {
		t.Fatal(err)
	}

	// Resume: the pull request is closed-and-not-merged on the fake remote
	// from the pre-crash close, exactly as in M5's second test - but this
	// time the replacement that would otherwise vouch for it is not bound
	// to this original, so it must refuse rather than recover.
	_, err = PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{firstSource.WorktreeDir, secondSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", RebatchReceipt: first.ReceiptPath,
	})
	if err == nil || !strings.Contains(err.Error(), "not the exact open unmerged candidate") {
		t.Fatalf("resume with an unbound SupersededPullRequest = %v, want a refusal naming the pull request as not open/unmerged", err)
	}
}
