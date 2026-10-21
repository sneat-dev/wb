//go:build e2e

package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestE2ECampaignMaterializedRepositoriesRemainDiscoverable(t *testing.T) {
	t.Parallel()
	f := newExplicitRootEngineFixture(t)
	ctx := context.Background()
	home, err := wbhome.EnsureRoot(f.githubDir)
	if err != nil {
		t.Fatal(err)
	}
	repositories := []Repository{f.repository}
	second := filepath.Join(f.githubDir, "acme", "widget")
	if err := os.MkdirAll(filepath.Dir(second), 0755); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, f.githubDir, "clone", "--no-hardlinks", f.repository.CloneURL, second)
	repositories = append(repositories, Repository{Slug: "acme/widget", Path: second, CloneURL: f.repository.CloneURL})
	for _, repository := range repositories {
		options := f.options()
		options.Operation = "campaign-test"
		options.DependencyCampaign = true
		options, err = Normalize(options)
		if err != nil {
			t.Fatal(err)
		}
		path, placement, base, resumed, err := operationWorktreePath(ctx, repository.Path, repository.Slug, options, ResolvedBase{Ref: "main"})
		if err != nil {
			t.Fatal(err)
		}
		created, err := prepareWorktree(ctx, repository.Path, repository.Slug, path, placement, base, resumed, options.Branch, "origin/main", options)
		if err != nil {
			t.Fatal(err)
		}
		if err := recordWorktreeManifest(ctx, home, repository.Path, path, repository, ResolvedBase{Ref: "main"}, base, created.BranchCreated, options); err != nil {
			created.Close()
			t.Fatal(err)
		}
		created.Close()
	}
	outcome, err := worktrees.ListWithDiagnostics(ctx, worktrees.ListOptions{ProjectsRoot: f.githubDir, Filter: "acme/", Tasks: []string{"campaign-test.acme-app", "campaign-test.acme-widget"}})
	entries, diagnostics := outcome.Results, outcome.Diagnostics
	if err != nil || len(diagnostics) != 0 || len(entries) != 2 {
		t.Fatalf("campaign inventory: entries=%+v diagnostics=%+v err=%v", entries, diagnostics, err)
	}
	for _, entry := range entries {
		if entry.Task != "campaign-test.acme-app" && entry.Task != "campaign-test.acme-widget" {
			t.Fatalf("repository effort identity lost: %+v", entry)
		}
	}
}
