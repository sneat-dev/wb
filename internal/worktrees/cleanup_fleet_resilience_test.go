package worktrees

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// A fleet sweep is a different question from a named task. One task that cannot
// be corroborated at preflight says nothing about the tasks already proven
// eligible beside it, and each of those cost a `git fetch` to prove. This is the
// regression for a live `--all-merged --apply` that verified ninety-nine
// eligible tasks, applied ten, and then threw the remaining eighty-nine away
// because a single worktree sat on a branch its Work Log claim did not name:
//
//	error: preflight Work Log for sneat-co/sneat-go: live branch
//	"renovate/3d-parties-minor-patch" does not match private claim "fix-failing-prs"
//
// CleanupOutcome already documents the intended contract — a bad candidate
// "blocks eligibility only for its own coordinated task ... Every other task in
// the run proceeds normally" — and the task loop contradicted it.
func TestCleanupAllMergedKeepsSweepingWhenOneTaskCannotBeCorroborated(t *testing.T) {
	fixture := newGitFixture(t)
	healthy, healthyHead, mergedAt := prepareMergedTaskInFixture(t, fixture, "cleanup-healthy")
	broken, brokenHead, _ := prepareMergedTaskInFixture(t, fixture, "cleanup-broken")
	installMergedPullRequestFixtures(t, []string{healthyHead, brokenHead}, mergedAt)

	// Move the checkout onto a branch the private claim does not name, which is
	// exactly what the sweep met in the wild.
	gitTest(t, broken.WorktreeDir, "checkout", "-b", "renovate/unclaimed-by-the-work-log")

	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		AllMerged:    true,
		Apply:        true,
		Now:          func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatalf("one uncorroborated task aborted the whole sweep: %v", err)
	}
	if !cleanupApplied(outcome, "cleanup-healthy") {
		t.Fatalf("an uncorroborated sibling stopped a healthy task being cleaned: %#v", outcome.Results)
	}
	if cleanupApplied(outcome, "cleanup-broken") {
		t.Fatal("an uncorroborated task must never be applied")
	}
	if !cleanupReported(outcome, "cleanup-broken") {
		t.Fatalf("an uncorroborated task must be reported, not silently skipped: %#v", outcome.Diagnostics)
	}
	if _, statErr := os.Stat(healthy.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("the healthy task's worktree survived a sweep that reported it applied: %v", statErr)
	}
}

// A named task remains the operator's exact subject, so its failure is still
// the answer to what they asked and must still end the command.
func TestCleanupNamedTaskStillFailsWhenItCannotBeCorroborated(t *testing.T) {
	fixture := newGitFixture(t)
	broken, brokenHead, mergedAt := prepareMergedTaskInFixture(t, fixture, "cleanup-broken")
	installMergedPullRequestFixtures(t, []string{brokenHead}, mergedAt)
	gitTest(t, broken.WorktreeDir, "checkout", "-b", "renovate/unclaimed-by-the-work-log")

	_, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "cleanup-broken",
		Apply:        true,
		DeleteRemote: true,
		Now:          func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err == nil {
		t.Fatal("a named task that cannot be corroborated must fail the command")
	}
}

func cleanupApplied(outcome CleanupOutcome, task string) bool {
	for _, result := range outcome.Results {
		if result.Task == task && result.Applied {
			return true
		}
	}
	return false
}

func cleanupReported(outcome CleanupOutcome, task string) bool {
	for _, diagnostic := range outcome.Diagnostics {
		if diagnostic.Task == task {
			return true
		}
	}
	for _, result := range outcome.Results {
		if result.Task == task && strings.TrimSpace(result.Reason) != "" {
			return true
		}
	}
	return false
}
