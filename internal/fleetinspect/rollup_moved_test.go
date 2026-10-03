package fleetinspect

import (
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/reposelection"
	"github.com/sneat-dev/wb/internal/repostatus"
)

func TestCwCovSummarizeGitStatsCountsEveryStatus(t *testing.T) {
	t.Parallel()
	service := testService()
	_ = service

	stats := summarizeGitStats([]repostatus.Row{
		{Status: "clean"},
		{Status: "attention"},
		{Status: "attention"},
		{Status: "error"},
		{Status: "something-else"},
	})
	if stats.Inspected != 5 || stats.Clean != 1 || stats.Attention != 2 || stats.Error != 1 {
		t.Fatalf("stats = %+v, want inspected=5 clean=1 attention=2 error=1", stats)
	}
}
func TestCwCovFleetInventoryWorktreesAndLayout(t *testing.T) {
	t.Parallel()
	service := testService()
	_ = service

	root := projectsFixture(t, "acme/app", "acme/other", "beta/tool")
	options := Options{Parallel: 2}

	inventory, err := service.fleetInventory(root, "", options)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Organizations != 2 || inventory.Repositories != 3 {
		t.Fatalf("inventory = %+v, want 2 orgs / 3 repos", inventory)
	}
	filtered, err := service.fleetInventory(root, "acme/app", options)
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Repositories != 1 || filtered.Organizations != 1 {
		t.Fatalf("filtered inventory = %+v, want 1 org / 1 repo", filtered)
	}
	byRegex, err := service.fleetInventory(root, "", Options{Parallel: 2, Regex: `^beta/`})
	if err != nil {
		t.Fatal(err)
	}
	if byRegex.Repositories != 1 {
		t.Fatalf("regex inventory = %+v, want 1 repo", byRegex)
	}
	if _, err := service.fleetInventory(root, "", Options{Parallel: 2, Regex: `(`}); err == nil {
		t.Fatal("an invalid --regex must be refused")
	}

	worktreeStats, err := service.fleetWorktreeRollup(root, "", options)
	if err != nil {
		t.Fatal(err)
	}
	if worktreeStats.Checkouts != 0 || worktreeStats.Tasks != 0 {
		t.Fatalf("worktrees = %+v, want no WB-managed worktrees", worktreeStats)
	}

	layoutStats, err := service.fleetLayoutRollup(root)
	if err != nil {
		t.Fatal(err)
	}
	if layoutStats.NoOrigin+layoutStats.OK+layoutStats.TopLevel+layoutStats.Misowned+layoutStats.Unreadable != 3 {
		t.Fatalf("layout = %+v, want all three checkouts classified", layoutStats)
	}

	hooksStats := service.fleetHooksRollup(Scope{}, []reposelection.Target{{Repository: "acme/app", Path: filepath.Join(root, "acme", "app")}}, 1)
	if hooksStats.Repositories != 1 {
		t.Fatalf("hooks = %+v, want one repository inspected", hooksStats)
	}
}
