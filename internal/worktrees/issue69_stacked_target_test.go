package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A worktree's target is part of its immutable creation record. Cleanup must
// use that target even when the caller leaves the global --base fallback at
// main; this is the regression for stacked feature-target tasks.
func TestCleanupUsesRecordedStackedTargetInsteadOfGlobalMain(t *testing.T) {
	fixture := newGitFixture(t)

	// Publish a feature target containing one commit, while leaving the
	// canonical checkout on main so Create can safely branch from the target.
	gitTest(t, fixture.canonical, "checkout", "-b", "feature/base")
	gitTest(t, fixture.canonical, "commit", "--allow-empty", "-m", "feature target")
	gitTest(t, fixture.canonical, "push", "-u", "origin", "feature/base")
	gitTest(t, fixture.canonical, "checkout", "main")

	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "stacked-target",
		Base:         "feature/base",
		WorkLog:      WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 || created[0].Base != "feature/base" {
		t.Fatalf("created target = %#v, want feature/base", created)
	}
	gitTest(t, created[0].WorktreeDir, "commit", "--allow-empty", "-m", "stacked work")
	head := gitTestOutput(t, created[0].WorktreeDir, "rev-parse", "HEAD")
	gitTest(t, created[0].WorktreeDir, "push", "-u", "origin", created[0].Branch)

	// Fast-forward only the feature target to the source head. It is a real
	// remote receipt, but the source head is intentionally absent from main.
	gitTest(t, fixture.canonical, "update-ref", "refs/heads/feature/base", head)
	gitTest(t, fixture.canonical, "push", "origin", "refs/heads/feature/base")
	installMergedPullRequestFixtures(t, nil, time.Time{})

	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "stacked-target",
		OlderThan:    0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Results) != 1 {
		t.Fatalf("cleanup plan = %#v", planned.Results)
	}
	entry := planned.Results[0]
	if entry.Base != "feature/base" {
		t.Fatalf("cleanup target = %q, want recorded feature/base", entry.Base)
	}
	if entry.RemoteTargetSHA == "" || entry.RemoteTargetSHA != head || !entry.IntegratedAtOrigin || !entry.Eligible {
		t.Fatalf("stacked target was not used for cleanup: %#v", entry)
	}
	if strings.Contains(entry.Reason, "origin/main") {
		t.Fatalf("cleanup unexpectedly used global main target: %#v", entry)
	}
}

func TestRecordedWorktreeBaseFallsBackToWorkLogClaim(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "claim-target",
		WorkLog:      WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(created[0].WorktreeDir, ".wb", "local", "manifest.yaml")); err != nil {
		t.Fatal(err)
	}
	target, err := resolveRecordedWorktreeBase(context.Background(), fixture.home, created[0].WorktreeDir, "feature/wrong-fallback")
	if err != nil {
		t.Fatal(err)
	}
	if target != "main" {
		t.Fatalf("target = %q, want Work Log claim target main", target)
	}
}
