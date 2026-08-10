package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestAbortDiscardedUnusedWorktreesIsAudited covers the common storage-agent
// failure shape: two untouched worktrees were claimed but never started, so
// they cannot have merged PR evidence and must not become abandoned branches.
func TestAbortDiscardedUnusedWorktreesIsAudited(t *testing.T) {
	fixture := newGitFixture(t)
	otherCanonical := filepath.Join(fixture.projectsRoot, "acme", "storage")
	gitTest(t, fixture.projectsRoot, "clone", fixture.remote, otherCanonical)
	created, err := Create(context.Background(), []string{"acme/app", "acme/storage"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "unused-storage"})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 2 {
		t.Fatalf("created = %#v", created)
	}
	results, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "unused-storage", Disposition: AbortDiscarded, Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("abort = %#v", results)
	}
	for _, result := range results {
		if !result.Applied || !result.WorktreeGone || !result.BranchDeleted {
			t.Fatalf("abort result = %#v", result)
		}
	}
	for _, create := range created {
		if _, err := os.Stat(create.WorktreeDir); !os.IsNotExist(err) {
			t.Fatalf("discarded worktree remains: %v", err)
		}
		canonical := fixture.canonical
		if create.Repository == "acme/storage" {
			canonical = otherCanonical
		}
		if exists, err := localBranchExists(context.Background(), canonical, create.Branch); err != nil || exists {
			t.Fatalf("discarded branch exists=%t err=%v", exists, err)
		}
	}
	terminal := filepath.Join(fixture.home, "worklogs", "unused-storage", "runs")
	entries, err := os.ReadDir(terminal)
	if err != nil || len(entries) != 1 {
		t.Fatalf("terminal archive directory = %#v err=%v", entries, err)
	}
	for _, claim := range []string{"acme-app.json", "acme-storage.json"} {
		if _, err := os.Stat(filepath.Join(terminal, entries[0].Name(), "terminals", claim)); err != nil {
			t.Fatalf("sealed archive %s missing: %v", claim, err)
		}
	}
}

func TestAbortNotLandedSealsButRetainsResumableWorktree(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "resume-storage"})
	if err != nil {
		t.Fatal(err)
	}
	results, err := Abort(context.Background(), AbortOptions{ProjectsRoot: fixture.projectsRoot, Task: "resume-storage", Disposition: AbortNotLanded, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Applied || results[0].WorktreeGone || results[0].BranchDeleted {
		t.Fatalf("abort = %#v", results)
	}
	if _, err := os.Stat(created[0].WorktreeDir); err != nil {
		t.Fatalf("resumable worktree missing: %v", err)
	}
}
