package layout

import (
	"strings"
	"testing"
)

func TestMigrateReportMarkdownUndoDryRunEmptyClones(t *testing.T) {
	t.Parallel()
	report := MigrateReport{
		Undo:         true,
		ManifestID:   "m1",
		ProjectsRoot: "/projects",
		DryRun:       true,
	}
	out := report.Markdown()
	for _, want := range []string{
		"# WB layout migrate --undo\n",
		"- Manifest: `m1`\n",
		"- Projects root: `/projects`\n",
		"- Mode: `dry-run` (pass `--apply` to move)\n",
		"No legacy clones found under the projects root.\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("Markdown() = %q, want it to contain %q", out, want)
		}
	}
	if strings.Contains(out, "| Repository |") {
		t.Fatalf("Markdown() = %q, want no clone table for empty Clones", out)
	}
	if strings.Contains(out, "daemon must be restarted") {
		t.Fatalf("Markdown() = %q, want no daemon-restart note when DaemonRestartRequired is false", out)
	}
}

func TestMigrateReportMarkdownApplyWithClonesWorktreesAndRelocations(t *testing.T) {
	t.Parallel()
	report := MigrateReport{
		ProjectsRoot:          "/projects",
		DryRun:                false,
		ManifestPath:          "/projects/.wb-migrate/manifest.json",
		DaemonRestartRequired: true,
		Clones: []MigrateClone{
			{
				Repository:  "owner/repo",
				Source:      "/legacy/owner/repo",
				Destination: "/store/owner/repo",
				Status:      "done",
				Reason:      "a|b\nc",
				Worktrees: []MigrateWorktree{
					{Source: "/legacy/owner/repo-wt/w1", Destination: "/store/owner/repo-wt/w1"},
				},
				Relocations: []MigrateRelocation{
					{Task: "t1", Source: "/legacy/task/t1", Destination: "/store/task/t1", Status: "done", Reason: "r|s"},
				},
			},
		},
	}
	out := report.Markdown()
	for _, want := range []string{
		"# WB layout migrate\n\n",
		"- Mode: `apply`\n",
		"- Manifest: `/projects/.wb-migrate/manifest.json`\n",
		"- A running wb daemon must be restarted to see the moved paths.\n",
		"| Repository | Source | Destination | Status | Reason |\n|---|---|---|---|---|\n",
		"| `owner/repo` | `/legacy/owner/repo` | `/store/owner/repo` | `done` | a\\|b c |\n",
		"|  | `/legacy/owner/repo-wt/w1` | `/store/owner/repo-wt/w1` |  |  |\n",
		"|  relocate `t1` | `/legacy/task/t1` | `/store/task/t1` | `done` | r\\|s |\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("Markdown() = %q, want it to contain %q", out, want)
		}
	}
	if strings.Contains(out, "--undo") {
		t.Fatalf("Markdown() = %q, want no --undo heading when Undo is false", out)
	}
}
