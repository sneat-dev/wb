package cmdfleet

import (
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/fleetinspect"
	"github.com/sneat-dev/wb/internal/repostatus"
)

func TestCwCovFleetSummarySentences(t *testing.T) {
	t.Parallel()
	remote := fleetinspect.RemoteStats{
		WouldClone: 1, WouldPull: 2, SkippedDirty: 3, Ignored: 4, EmptyRemote: 5,
		ArchivedUnlandable: 6, LocalOnly: 7, RemoteOnly: 8, NoOp: 9, Error: 10,
	}
	sentences := map[string]string{
		"inventory": fleetInventorySummary(fleetinspect.InventoryStats{Organizations: 1, Repositories: 2}),
		"git":       fleetGitSummary(fleetinspect.GitStats{Attention: 1, Clean: 2, Error: 3, Inspected: 6}),
		"layout":    fleetLayoutSummary(fleetinspect.LayoutStats{OK: 1, TopLevel: 2, Misowned: 3, NoOrigin: 4, Unreadable: 5}),
		"remote":    fleetRemoteSummary(remote),
		"hooks":     fleetHooksSummary(fleetinspect.HooksStats{Findings: 1, Repositories: 1, Errors: 1}),
		"worktrees": fleetWorktreeSummary(fleetinspect.WorktreeStats{Tasks: 1, Checkouts: 2, Dirty: 3, Locked: 4}),
	}
	for name, sentence := range sentences {
		if strings.TrimSpace(sentence) == "" {
			t.Errorf("%s summary is empty", name)
		}
	}
	if !strings.Contains(sentences["inventory"], "1 organization · 2 local repositorys") {
		t.Errorf("singular/plural inventory = %q", sentences["inventory"])
	}
	for _, want := range []string{"1 would-clone", "2 would-pull", "10 error"} {
		if !strings.Contains(sentences["remote"], want) {
			t.Errorf("remote summary missing %q: %q", want, sentences["remote"])
		}
	}
	if !strings.Contains(sentences["hooks"], "1 finding across 1 repository (1 error)") {
		t.Errorf("hooks summary = %q", sentences["hooks"])
	}
	if !strings.Contains(sentences["worktrees"], "1 task · 2 checkouts · 3 dirty · 4 locked") {
		t.Errorf("worktree summary = %q", sentences["worktrees"])
	}
}
func TestCwCovFleetMarkdownRendersOptionalSections(t *testing.T) {
	t.Parallel()
	stats := fleetinspect.StatsReport{
		SchemaVersion: 1,
		Inventory:     fleetinspect.InventoryStats{Organizations: 1, Repositories: 3},
		Git:           fleetinspect.GitStats{Inspected: 3, Clean: 2, Attention: 1},
		Layout:        fleetinspect.LayoutStats{OK: 3},
		Worktrees:     fleetinspect.WorktreeStats{Tasks: 1, Checkouts: 1},
	}
	withoutOptional := fleetStatsMarkdown(stats)
	if strings.Contains(withoutOptional, "- remote:") || strings.Contains(withoutOptional, "- hooks:") {
		t.Fatalf("optional sections rendered without data:\n%s", withoutOptional)
	}
	stats.Remote = &fleetinspect.RemoteStats{NoOp: 3}
	stats.Hooks = &fleetinspect.HooksStats{Repositories: 3, Findings: 2}
	withOptional := fleetStatsMarkdown(stats)
	for _, want := range []string{"# WB fleet stats", "- remote:", "- hooks:", "2 findings across 3 repositorys"} {
		if !strings.Contains(withOptional, want) {
			t.Errorf("stats markdown missing %q:\n%s", want, withOptional)
		}
	}

	overview := fleetOverviewMarkdown(fleetinspect.OverviewReport{
		SchemaVersion: 1,
		Stats:         stats,
		Status: repostatus.Index{SchemaVersion: 1, Repositories: []repostatus.Row{
			{Repository: "acme/dirty", Status: "attention", Summary: "modified"},
		}},
	}, true)
	for _, want := range []string{"# WB fleet overview", "## Stats", "## Attention", "acme/dirty"} {
		if !strings.Contains(overview, want) {
			t.Errorf("overview markdown missing %q:\n%s", want, overview)
		}
	}
}
