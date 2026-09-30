package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktreebranches"
)

func TestBranchInventoryFacadeAdaptersAndRealGit(t *testing.T) {
	ctx := context.Background()
	fixture, result, commits, target := prepareSupersessionTask(t, "branch-inventory-adapter")
	service := branchInventoryService()
	uses, err := service.Ports.ListInUse(ctx, fixture.projectsRoot, "")
	if err != nil || len(uses) == 0 {
		t.Fatalf("live worktree inventory: %#v %v", uses, err)
	}
	found := false
	for _, use := range uses {
		if use.Repository == result.Repository && use.Branch == result.Branch {
			found = true
		}
	}
	if !found {
		t.Fatalf("created task absent from inventory: %#v", uses)
	}
	receipt := completeSupersessionReceipt(result, "", commits, target, "review-inventory")
	path := writeSupersessionReceipt(t, fixture, receipt)
	evidence, rejection := service.Ports.Supersession(ctx, path, worktreebranches.Repository{Slug: result.Repository, Path: fixture.canonical}, worktreebranches.BranchRef{Name: result.Branch, SHA: commits[len(commits)-1]}, "main", target)
	if rejection != "" || evidence == nil || evidence.Reviewer != "reviewer@example.test" || evidence.ReceiptID != "review-inventory" {
		t.Fatalf("supersession adapter: %#v %q", evidence, rejection)
	}
	repos, err := discoverBranchRepositories(fixture.projectsRoot, "acme/")
	if err != nil || len(repos) != 1 || repos[0].Slug() != "acme/app" {
		t.Fatalf("filtered discovery: %#v %v", repos, err)
	}
	if _, err := BranchList(ctx, BranchListOptions{ProjectsRoot: fixture.projectsRoot, Repository: "acme/missing"}); err == nil || !strings.Contains(err.Error(), "not discovered") {
		t.Fatalf("fleet selection: %v", err)
	}
	loop := filepath.Join(t.TempDir(), "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	if _, err := normalizeBranchListOptions(BranchListOptions{ProjectsRoot: loop}); err == nil {
		t.Fatal("symlink loop accepted")
	}
}
