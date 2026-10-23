package layout

import (
	"strings"
	"testing"
)

// TestUnknownIncludeTaskError_Error drives the error formatting for an
// --include-task name that matches no live Work Log claim.
func TestUnknownIncludeTaskError_Error(t *testing.T) {
	t.Parallel()

	err := &UnknownIncludeTaskError{Task: "rename-thing"}
	const want = `--include-task "rename-thing" matches no live Work Log claim in any resolved home`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// TestUndoIncludeFlagsError_Error drives the fixed error text for combining
// --include-task/--include-active-tasks with --undo.
func TestUndoIncludeFlagsError_Error(t *testing.T) {
	t.Parallel()

	err := (&UndoIncludeFlagsError{}).Error()
	const want = "--include-task/--include-active-tasks have no effect with --undo: undo honours exactly the inclusions its manifest recorded when the clones were moved"
	if err != want {
		t.Errorf("Error() = %q, want %q", err, want)
	}
}

// TestMigrateReportMarkdown_UndoWithNoClones drives the Undo header branch
// together with the empty-Clones early return.
func TestMigrateReportMarkdown_UndoWithNoClones(t *testing.T) {
	t.Parallel()

	report := MigrateReport{Undo: true, ManifestID: "mig-1", ProjectsRoot: "/projects"}
	markdown := report.Markdown()

	for _, want := range []string{
		"# WB layout migrate --undo",
		"- Manifest: `mig-1`",
		"- Projects root: `/projects`",
		"No legacy clones found under the projects root.",
	} {
		if !strings.Contains(markdown, want) {
			t.Errorf("Markdown() = %q, want it to contain %q", markdown, want)
		}
	}
	if strings.Contains(markdown, "| Repository |") {
		t.Errorf("Markdown() = %q, want no clone table when Clones is empty", markdown)
	}
}

// TestMigrateReportMarkdown_DryRunApplyManifestAndDaemonRestart drives the
// non-undo header, the dry-run/apply mode lines, the ManifestPath line, and
// the DaemonRestartRequired line, plus the clone/worktree/relocation table
// rows.
func TestMigrateReportMarkdown_DryRunApplyManifestAndDaemonRestart(t *testing.T) {
	t.Parallel()

	dryRun := MigrateReport{
		ProjectsRoot:          "/projects",
		DryRun:                true,
		ManifestPath:          "/projects/.wb/manifest.json",
		DaemonRestartRequired: true,
		Clones: []MigrateClone{
			{
				Repository:  "owner/repo",
				Source:      "/legacy/owner/repo",
				Destination: "/store/owner/repo",
				Status:      "planned",
				Reason:      "legacy layout",
				Worktrees: []MigrateWorktree{
					{Source: "/legacy/owner/repo/wt", Destination: "/store/owner/repo/wt"},
				},
				Relocations: []MigrateRelocation{
					{Task: "task-1", Source: "/legacy/wt", Destination: "/store/wt", Status: "planned", Reason: "not at store placement"},
				},
			},
		},
	}
	markdown := dryRun.Markdown()

	for _, want := range []string{
		"# WB layout migrate\n",
		"- Mode: `dry-run` (pass `--apply` to move)",
		"- Manifest: `/projects/.wb/manifest.json`",
		"- A running wb daemon must be restarted to see the moved paths.",
		"| Repository | Source | Destination | Status | Reason |",
		"`owner/repo`",
		"/legacy/owner/repo/wt",
		"relocate `task-1`",
	} {
		if !strings.Contains(markdown, want) {
			t.Errorf("Markdown() dry-run = %q, want it to contain %q", markdown, want)
		}
	}

	apply := MigrateReport{ProjectsRoot: "/projects", DryRun: false, Clones: []MigrateClone{{Repository: "o/r", Source: "s", Status: "done"}}}
	applyMarkdown := apply.Markdown()
	if !strings.Contains(applyMarkdown, "- Mode: `apply`") {
		t.Errorf("Markdown() apply = %q, want it to contain the apply mode line", applyMarkdown)
	}
	if strings.Contains(applyMarkdown, "- Manifest:") {
		t.Errorf("Markdown() apply = %q, want no Manifest line when ManifestPath is empty", applyMarkdown)
	}
}
