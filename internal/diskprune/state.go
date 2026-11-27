package diskprune

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/diskusage"
)

// wbWrittenReports are the reports/ directory names static WB writers use; a
// name ending in "-" is a prefix (deps-graph-<ecosystem>). A test fails when
// a writer under internal/ or cmd/ adds a name this list does not cover.
var wbWrittenReports = []string{
	"archive-clean", "branch-cleanup", "branch-quarantine", "default-branch", "deps-drift", "deps-graph-",
	"merge-policy", "pr-update", "repository-transfers", "sync-prune-archived", "worktree-cleanup",
	"worktree-gc", "worktree-lock-recovery", "worktree-merge", "worktree-rename", "worktree-retire",
	"worktree-stage-recovery", "cgxc2-npm-apply-fail", "cw-deps-absent", "cw-deps-bump", "cw-npm-apply",
	"cw-npm-apply-dispatch", "cw-npm-plan", "cw-npm-preflight",
}

func isWBWrittenReport(name string) bool {
	for _, known := range wbWrittenReports {
		if name == known || (strings.HasSuffix(known, "-") && strings.HasPrefix(name, known)) {
			return true
		}
	}
	return false
}

// stateTopEntries is how many of the largest second-level entries a state
// report lists.
const stateTopEntries = 10

// stateReport classifies a state directory's entries; nothing in it is ever
// pruned (disk-prune#req:state-is-report-only). In the retired home,
// worktrees/ is reported under the worktrees category, not here.
func stateReport(ctx context.Context, root string, legacy bool) (StateReport, bool) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return StateReport{}, false
	}
	report := StateReport{Root: root}
	var second []StateEntryReport
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(root, name)
		if legacy && name == "worktrees" {
			continue
		}
		if entry.IsDir() && (name == "reports" || name == "backups") {
			children, err := os.ReadDir(path)
			if err != nil {
				// Listed, not hidden: a subtree that cannot be read is in the
				// report as such, with whatever its size can be measured as.
				report.Entries = append(report.Entries, StateEntryReport{Path: path, Class: "unreadable", Bytes: reclaimOf(ctx, path)})
				continue
			}
			for _, child := range children {
				class := "origin-not-proven"
				if name == "reports" && isWBWrittenReport(child.Name()) {
					class = "wb-written"
				}
				childPath := filepath.Join(path, child.Name())
				second = append(second, StateEntryReport{Path: childPath, Class: class, Bytes: reclaimOf(ctx, childPath)})
			}
			continue
		}
		report.Entries = append(report.Entries, StateEntryReport{Path: path, Class: "wb-state", Bytes: reclaimOf(ctx, path)})
	}
	sort.Slice(second, func(i, j int) bool { return second[i].Bytes > second[j].Bytes })
	if len(second) > stateTopEntries {
		second = second[:stateTopEntries]
	}
	report.Entries = append(report.Entries, second...)
	return report, true
}

func reclaimOf(ctx context.Context, path string) int64 {
	usage, err := diskusage.NewWalk().Measure(ctx, path)
	if err != nil {
		return 0
	}
	return usage.UnsharedBytes
}
