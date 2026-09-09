package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCreateLocalPlacementRejectsUnsafeRoot(t *testing.T) {
	for _, kind := range []string{"symlink", "tracked"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newGitFixture(t)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			root := filepath.Join(fixture.canonical, ".worktrees")
			var protected string
			if kind == "symlink" {
				outside := t.TempDir()
				protected = filepath.Join(outside, "existing.txt")
				if err := os.Symlink(outside, root); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(root, 0o755); err != nil {
					t.Fatal(err)
				}
				protected = filepath.Join(root, "existing.txt")
			}
			if err := os.WriteFile(protected, []byte("preserve me\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if kind == "tracked" {
				gitTest(t, fixture.canonical, "add", "-f", ".worktrees/existing.txt")
				gitTest(t, fixture.canonical, "commit", "-m", "existing tracked worktrees content")
				gitTest(t, fixture.canonical, "push", "origin", "main")
			}
			if _, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
				ProjectsRoot: fixture.projectsRoot, Operation: "unsafe-local-root", WorkLog: WorkLogOptions{Model: "unknown"},
			}); err == nil {
				t.Fatal("unsafe local root was accepted")
			}
			if data, err := os.ReadFile(protected); err != nil || string(data) != "preserve me\n" {
				t.Fatalf("protected file changed: %q, %v", data, err)
			}
			if exists, err := localBranchExists(context.Background(), fixture.canonical, "unsafe-local-root"); err != nil || exists {
				t.Fatalf("refused create left branch: %t, %v", exists, err)
			}
		})
	}
}

func TestCleanupResumesRemovedCheckoutAfterSharedRootChanges(t *testing.T) {
	fixture := newGitFixture(t)
	config := filepath.Join(t.TempDir(), "wb", "worktrees.yaml")
	t.Setenv("XDG_CONFIG_HOME", filepath.Dir(filepath.Dir(config)))
	rootA := filepath.Join(t.TempDir(), "root-a")
	mustWriteBranchConfig(t, config, "version: 1\nworktrees:\n  root: "+rootA+"\n")
	created, head, mergedAt := prepareMergedTaskInFixture(t, fixture, "root-drift-backlog")
	installMergedPullRequestFixture(t, head, mergedAt)
	injected := errors.New("interrupted after physical checkout removal")
	options := CleanupOptions{ProjectsRoot: fixture.projectsRoot, Task: "root-drift-backlog",
		Apply: true, DeleteRemote: true, OlderThan: 0, Now: func() time.Time { return mergedAt.Add(time.Hour) },
		afterCleanupWorktreeRemoval: func(string) error { return injected }}
	first, err := Cleanup(context.Background(), options)
	if !errors.Is(err, injected) || len(first.Results) != 1 || !first.Results[0].WorktreeGone {
		t.Fatalf("interrupted cleanup = %#v, %v", first.Results, err)
	}
	if _, err := os.Stat(created.WorktreeDir); !os.IsNotExist(err) {
		t.Fatalf("old checkout remains: %v", err)
	}
	mustWriteBranchConfig(t, config, "version: 1\nworktrees:\n  root: "+filepath.Join(t.TempDir(), "root-b")+"\n")
	options.afterCleanupWorktreeRemoval = nil
	resumed, err := Cleanup(context.Background(), options)
	if err != nil || len(resumed.Results) != 1 || !resumed.Results[0].Applied || !resumed.Results[0].BranchDeleted {
		t.Fatalf("root-drift resume = %#v, %v", resumed.Results, err)
	}
	if exists, err := localBranchExists(context.Background(), fixture.canonical, created.Branch); err != nil || exists {
		t.Fatalf("old branch remains: exists=%t err=%v", exists, err)
	}
}

func TestFilteredLocalCleanupPreservesActiveSiblingCoordination(t *testing.T) {
	fixture := newGitFixture(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	storage := addRepositoryToFixture(t, fixture, "storage")
	created, heads := prepareMergedTaskInRepositories(t, fixture, "local-sibling", "app", "storage")
	var sibling CreateResult
	for _, result := range created {
		if result.Repository == "acme/storage" {
			sibling = result
		}
	}
	if sibling.WorktreeDir == "" {
		t.Fatal("missing sibling")
	}
	if err := os.WriteFile(filepath.Join(sibling.WorktreeDir, "active.txt"), []byte("still working\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, sibling.WorktreeDir, "add", "active.txt")
	gitTest(t, sibling.WorktreeDir, "commit", "-m", "active sibling work")
	activeHead := gitTestOutput(t, sibling.WorktreeDir, "rev-parse", "HEAD")
	mergedAt := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
	installMergedPullRequestFixtures(t, heads, mergedAt)
	options := CleanupOptions{ProjectsRoot: fixture.projectsRoot, Task: "local-sibling", Filter: "acme/app",
		Apply: true, DeleteRemote: true, OlderThan: 0, Now: func() time.Time { return mergedAt.Add(time.Hour) }}
	first, err := Cleanup(context.Background(), options)
	if err != nil || len(first.Results) != 1 || !first.Results[0].Applied {
		t.Fatalf("filtered cleanup = %#v, %v", first.Results, err)
	}
	if got := gitTestOutput(t, sibling.WorktreeDir, "rev-parse", "HEAD"); got != activeHead {
		t.Fatalf("active sibling changed: %s", got)
	}
	logical := filepath.Join(fixture.home, "worktrees", "local-sibling")
	if _, err := os.Stat(logical); err != nil {
		t.Fatalf("active sibling lost coordination namespace: %v", err)
	}
	gitTest(t, sibling.WorktreeDir, "push", "origin", sibling.Branch)
	gitTest(t, storage, "merge", "--no-ff", sibling.Branch, "-m", "finish sibling")
	gitTest(t, storage, "push", "origin", "main")
	installMergedPullRequestFixture(t, activeHead, mergedAt)
	options.Filter = ""
	last, err := Cleanup(context.Background(), options)
	if err != nil || len(last.Results) != 1 || !last.Results[0].Applied {
		t.Fatalf("final sibling cleanup = %#v, %v", last.Results, err)
	}
	if _, err := os.Stat(logical); !os.IsNotExist(err) {
		t.Fatalf("terminal namespace remains: %v", err)
	}
}
