package diskprune

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// AC state-reported-never-pruned (classification): WB's own report
// directories are wb-written, other reports and backups are
// origin-not-proven, everything else is wb-state, and the retired home's
// worktrees are left to the worktrees category.
func TestStateReportClassifies(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, rel := range []string{"worklogs/a.md", "hub/db", "reports/worktree-merge/x.json", "reports/deps-graph-go/g", "reports/hand-made/big", "backups/x/bundle", "worktrees/task/f"} {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Repeat("x", 4096)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report, ok := stateReport(context.Background(), root, true)
	if !ok {
		t.Fatal("state report failed")
	}
	classes := map[string]string{}
	for _, entry := range report.Entries {
		rel, _ := filepath.Rel(root, entry.Path)
		classes[rel] = entry.Class
		if entry.Bytes <= 0 {
			t.Errorf("%s has no bytes", rel)
		}
	}
	for rel, want := range map[string]string{
		"worklogs": "wb-state", "hub": "wb-state", filepath.Join("reports", "worktree-merge"): "wb-written",
		filepath.Join("reports", "deps-graph-go"): "wb-written", filepath.Join("reports", "hand-made"): "origin-not-proven",
		filepath.Join("backups", "x"): "origin-not-proven",
	} {
		if classes[rel] != want {
			t.Errorf("%s = %q, want %q (%v)", rel, classes[rel], want, classes)
		}
	}
	if _, listed := classes["worktrees"]; listed {
		t.Error("the legacy home's worktrees were reported as state")
	}
	current, _ := stateReport(context.Background(), root, false)
	found := false
	for _, entry := range current.Entries {
		found = found || filepath.Base(entry.Path) == "worktrees"
	}
	if !found {
		t.Error("the current state's worktrees namespace is state")
	}
	if _, ok := stateReport(context.Background(), filepath.Join(root, "absent"), false); ok {
		t.Fatal("a missing state directory reported")
	}
	if reclaimOf(context.Background(), filepath.Join(root, "absent")) != 0 {
		t.Fatal("a missing path has bytes")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if reclaimOf(cancelled, root) != 0 {
		t.Fatal("a cancelled measurement has bytes")
	}
}

// Only the ten largest second-level entries are listed.
func TestStateReportCapsSecondLevelEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for i := 0; i < 12; i++ {
		path := filepath.Join(root, "reports", "r"+strconv.Itoa(i), "f")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Repeat("x", 4096*(i+1))), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report, _ := stateReport(context.Background(), root, false)
	if len(report.Entries) != stateTopEntries || !strings.HasSuffix(report.Entries[0].Path, "r11") {
		t.Fatalf("entries = %#v", report.Entries)
	}
}

// Every static reports/<name> a WB writer joins under its state home must be
// classified, so a new writer's output is never called origin-not-proven.
func TestEveryStaticReportWriterIsClassified(t *testing.T) {
	t.Parallel()
	join := regexp.MustCompile(`filepath\.Join\([A-Za-z.]*[Hh]ome[A-Za-z.()]*, "reports", "([a-z0-9-]+)"`)
	for _, dir := range []string{"../../internal", "../../cmd"} {
		err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, match := range join.FindAllStringSubmatch(string(raw), -1) {
				if !isWBWrittenReport(match[1]) && !strings.HasSuffix(match[1], "-") {
					t.Errorf("%s writes reports/%s, which wbWrittenReports does not name", path, match[1])
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
