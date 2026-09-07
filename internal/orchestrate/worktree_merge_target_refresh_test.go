package orchestrate

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/quality"
)

// publishMergeReceiptForTargetRefresh prepares a candidate, pushes it to the
// bare origin, and marks the receipt as an already-published pull request —
// the same shape recorded by receipt
// merge-sneat-dev-wb-main-1cbbf49dd60f-e69e39368098.json (2026-09-07) before
// WB refused to refresh it. It returns the receipt and the exact SHA that was
// published.
func publishMergeReceiptForTargetRefresh(t *testing.T, fixture engineFixture, sourceWorktree string) (WorktreeMergeReceipt, string) {
	t.Helper()
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{sourceWorktree}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/"+receipt.Candidate.Branch)
	receipt.Phase, receipt.Status = WorktreeMergePhaseLand, WorktreeMergePublished
	receipt.PullRequest, receipt.PublishedCandidateSHA = "https://example.test/acme/app/pull/41", receipt.Candidate.SHA
	receipt.Route.Requested = WorktreeMergeRoutePullRequest
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	return receipt, receipt.Candidate.SHA
}

func advanceMergeTarget(t *testing.T, fixture engineFixture, name, contents string) string {
	t.Helper()
	writeEngineFile(t, filepath.Join(fixture.canonical, name), contents)
	runEngineGit(t, fixture.canonical, "add", name)
	runEngineGit(t, fixture.canonical, "commit", "-m", "feat: advance target "+name)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	return strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
}

// TestLandWorktreeMergeRefreshAfterAdvancedSourceMergesOnTopOfDescendant
// covers refresh acceptance criterion (b): a source's additional commits
// landed directly in the published candidate worktree (the pre-existing
// advancePublishedWorktreeMergeCandidate path) before the target itself also
// advanced. The target refresh must merge the new target onto that advanced
// descendant, not onto the originally published SHA.
func TestLandWorktreeMergeRefreshAfterAdvancedSourceMergesOnTopOfDescendant(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "refresh-source-advance", "feature/refresh-source-advance", "published.txt", "candidate\n")
	receipt, originalCandidate := publishMergeReceiptForTargetRefresh(t, fixture, source.WorktreeDir)

	writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "descendant.txt"), "descendant\n")
	runEngineGit(t, receipt.Candidate.Worktree, "add", "descendant.txt")
	runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "feat: source advance lands directly in the published candidate")
	descendant := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))

	advancedTarget := advanceMergeTarget(t, fixture, "advanced.txt", "target\n")
	installWorktreeMergePublishOnlyPRGH(t)
	t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
	t.Setenv("WB_TEST_GH_LOG", filepath.Join(t.TempDir(), "gh.log"))
	t.Setenv("WB_TEST_CANDIDATE_SHA", originalCandidate)

	refreshed, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
		Progress: func(event progress.Event) {
			if event.Phase == "refresh_published_candidate" && event.State == progress.Completed {
				sha := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))
				t.Setenv("WB_TEST_CANDIDATE_SHA", sha)
			}
		},
	})
	if err == nil {
		t.Fatalf("expected the exact-head checks boundary past the refresh, got receipt=%+v", refreshed)
	}
	if len(refreshed.TargetRefreshes) != 1 {
		t.Fatalf("target_refreshes = %+v, want exactly one entry", refreshed.TargetRefreshes)
	}
	entry := refreshed.TargetRefreshes[0]
	if entry.PreviousCandidateSHA != descendant {
		t.Fatalf("target refresh merged onto %s, want the advanced source descendant %s", entry.PreviousCandidateSHA, descendant)
	}
	if entry.NewTargetSHA != advancedTarget {
		t.Fatalf("new_target_sha = %s, want %s", entry.NewTargetSHA, advancedTarget)
	}
	for _, ancestor := range []string{originalCandidate, descendant, advancedTarget} {
		contains, ancestorErr := isMergeAncestor(context.Background(), receipt.Candidate.Worktree, ancestor, refreshed.Candidate.SHA)
		if ancestorErr != nil || !contains {
			t.Fatalf("refreshed candidate %s does not contain %s: %v", refreshed.Candidate.SHA, ancestor, ancestorErr)
		}
	}
	if got := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "ls-remote", "origin", "refs/heads/"+receipt.Candidate.Branch)); !strings.HasPrefix(got, refreshed.Candidate.SHA+"\t") {
		t.Fatalf("PR branch was not fast-forwarded: %q, want head %s", got, refreshed.Candidate.SHA)
	}
}

// TestLandWorktreeMergeRefreshedTargetConflictReportsPathsWithoutPushing
// covers refresh acceptance criterion (c): a target advance that textually
// conflicts with the published candidate must land in prepare/conflict,
// name the conflicting path and the exact recovery command, and never push
// or otherwise mutate the published branch.
func TestLandWorktreeMergeRefreshedTargetConflictReportsPathsWithoutPushing(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "refresh-conflict-source", "feature/refresh-conflict", "shared.txt", "source\n")
	receipt, originalCandidate := publishMergeReceiptForTargetRefresh(t, fixture, source.WorktreeDir)

	writeEngineFile(t, filepath.Join(fixture.canonical, "shared.txt"), "target\n")
	runEngineGit(t, fixture.canonical, "add", "shared.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "feat: conflicting target advance")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	advancedTarget := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	installWorktreeMergePublishOnlyPRGH(t)
	t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
	t.Setenv("WB_TEST_GH_LOG", filepath.Join(t.TempDir(), "gh.log"))
	t.Setenv("WB_TEST_CANDIDATE_SHA", originalCandidate)

	failed, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err == nil || failed.Status != WorktreeMergeConflict {
		t.Fatalf("conflicting target refresh receipt=%+v err=%v", failed, err)
	}
	if !strings.Contains(err.Error(), "shared.txt") {
		t.Fatalf("conflict error did not name the conflicting path: %v", err)
	}
	if !strings.Contains(err.Error(), "wb worktree merge resume "+receipt.ReceiptPath) {
		t.Fatalf("conflict error did not name the exact recovery command: %v", err)
	}
	if got := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD")); got != originalCandidate {
		t.Fatalf("candidate worktree changed after aborted refresh merge: got %s want %s", got, originalCandidate)
	}
	if status := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "status", "--porcelain")); status != "" {
		t.Fatalf("candidate worktree retained merge conflict state: %q", status)
	}
	if got := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "ls-remote", "origin", "refs/heads/"+receipt.Candidate.Branch)); !strings.HasPrefix(got, originalCandidate+"\t") {
		t.Fatalf("published branch was pushed despite the conflicting refresh: %q, want unchanged head %s", got, originalCandidate)
	}
	if len(failed.TargetRefreshes) != 0 {
		t.Fatalf("target_refreshes recorded despite the aborted merge: %+v", failed.TargetRefreshes)
	}
	if failed.TargetSHA != receipt.TargetSHA {
		t.Fatalf("target_sha changed despite the aborted merge: got %s want %s", failed.TargetSHA, receipt.TargetSHA)
	}
	_ = advancedTarget
}

// TestLandWorktreeMergeRefreshValidationFailureLeavesNothingPushed covers
// refresh acceptance criterion (d): the target merges cleanly, but the
// resulting candidate fails validation in a way the target alone does not
// (a genuine regression). Nothing may be pushed, and the receipt records
// validation_failed without changing phase.
func TestLandWorktreeMergeRefreshValidationFailureLeavesNothingPushed(t *testing.T) {
	fixture := newEngineFixture(t)
	writeEngineGoModule(t, fixture.canonical, "package app\n\nfunc Value() int { return 1 }\n")
	runEngineGit(t, fixture.canonical, "add", "go.mod", "app.go")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: seed passing target validation")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	source := createMergeSource(t, fixture, "refresh-regression-source", "feature/refresh-regression", "value_test.go",
		"package app\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) {\n\tif Value() != 1 {\n\t\tt.Fatal(\"unexpected value\")\n\t}\n}\n")
	receipt, originalCandidate := publishMergeReceiptForTargetRefresh(t, fixture, source.WorktreeDir)
	if receipt.Validation.Status != quality.StatusPassed {
		t.Fatalf("prepared candidate did not pass before the target regression: %+v", receipt.Validation)
	}

	// The target alone still builds and has no tests of its own, so its
	// baseline validation passes; only the merged candidate, carrying the
	// source's test asserting the old value, regresses.
	advancedTarget := advanceMergeTarget(t, fixture, "app.go", "package app\n\nfunc Value() int { return 2 }\n")
	installWorktreeMergePublishOnlyPRGH(t)
	t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
	t.Setenv("WB_TEST_GH_LOG", filepath.Join(t.TempDir(), "gh.log"))
	t.Setenv("WB_TEST_CANDIDATE_SHA", originalCandidate)

	failed, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest,
		Timeout: 30 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err == nil || failed.Status != WorktreeMergeValidationFailed {
		t.Fatalf("refreshed candidate regression receipt=%+v err=%v", failed, err)
	}
	if failed.Phase != WorktreeMergePhaseLand {
		t.Fatalf("phase changed after validation failure: %s", failed.Phase)
	}
	if got := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "ls-remote", "origin", "refs/heads/"+receipt.Candidate.Branch)); !strings.HasPrefix(got, originalCandidate+"\t") {
		t.Fatalf("published branch was pushed despite the validation failure: %q, want unchanged head %s", got, originalCandidate)
	}
	if failed.TargetSHA != advancedTarget {
		t.Fatalf("target_sha = %s, want the refreshed %s even though push was withheld", failed.TargetSHA, advancedTarget)
	}
	if len(failed.TargetRefreshes) != 1 || failed.TargetRefreshes[0].PreviousCandidateSHA != originalCandidate {
		t.Fatalf("target_refreshes = %+v", failed.TargetRefreshes)
	}
	if failed.Validation.Status != quality.StatusFailed {
		t.Fatalf("validation report = %+v, want a recorded failure", failed.Validation)
	}
}

// TestLandWorktreeMergeRefreshIsNoOpWithoutTargetAdvance covers refresh
// acceptance criterion (e): resuming a published receipt whose target has
// not moved must not merge, revalidate, or push anything.
func TestLandWorktreeMergeRefreshIsNoOpWithoutTargetAdvance(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "refresh-noop-source", "feature/refresh-noop", "published.txt", "candidate\n")
	receipt, originalCandidate := publishMergeReceiptForTargetRefresh(t, fixture, source.WorktreeDir)
	installWorktreeMergePublishOnlyPRGH(t)
	t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
	t.Setenv("WB_TEST_GH_LOG", filepath.Join(t.TempDir(), "gh.log"))
	t.Setenv("WB_TEST_CANDIDATE_SHA", originalCandidate)

	var refreshEvents []progress.Event
	result, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
		Progress: func(event progress.Event) {
			if strings.Contains(event.Phase, "refresh_published_candidate") || strings.Contains(event.Phase, "validate_refreshed_candidate") {
				refreshEvents = append(refreshEvents, event)
			}
		},
	})
	// The resume still runs past the refresh boundary into the ordinary
	// exact-head checks wait (see the other tests in this file), which the
	// fixture's minimal gh mock cannot carry to a real landing. What this
	// test proves is narrower and unconditional on that outcome: no refresh
	// machinery ran, and neither the candidate nor the target moved.
	if len(refreshEvents) != 0 {
		t.Fatalf("refresh machinery ran despite no target advance: %+v", refreshEvents)
	}
	if len(result.TargetRefreshes) != 0 {
		t.Fatalf("target_refreshes recorded despite no target advance: %+v", result.TargetRefreshes)
	}
	if result.Candidate.SHA != originalCandidate || result.TargetSHA != receipt.TargetSHA {
		t.Fatalf("no-op resume changed candidate or target: %+v", result)
	}
	if got := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "ls-remote", "origin", "refs/heads/"+receipt.Candidate.Branch)); !strings.HasPrefix(got, originalCandidate+"\t") {
		t.Fatalf("published branch changed despite no target advance: %q", got)
	}
	_ = err
}
