package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestCwCovStatusMarkdownTitleChoosesTheRightHeading(t *testing.T) {
	for _, test := range []struct {
		kind  statusTitleKind
		fleet bool
		want  string
	}{
		{statusTitleFleet, true, "# WB fleet status\n\n"},
		{statusTitleFleet, false, "# WB fleet status\n\n"},
		{statusTitleRepo, true, "# WB repository status\n\n"},
		{statusTitleRepo, false, "# WB repository status\n\n"},
		{statusTitleAuto, true, "# WB local repository status\n\n"},
		{statusTitleAuto, false, "# WB repository status\n\n"},
	} {
		if got := statusMarkdownTitle(test.kind, test.fleet); got != test.want {
			t.Errorf("statusMarkdownTitle(%d, fleet=%t) = %q, want %q", test.kind, test.fleet, got, test.want)
		}
	}
}

func TestCwCovStatusIndexHelpers(t *testing.T) {
	report := statusIndex{SchemaVersion: 1, Repositories: []repositoryStatusInfo{
		{Repository: "acme/clean", Status: "clean"},
		{Repository: "acme/dirty", Status: "attention", Summary: "1 modified file"},
		{Repository: "acme/broken", Status: "error", Error: "not a repository"},
	}}
	if !statusFailed(report) {
		t.Fatal("an error row must fail the report")
	}
	hidden := hideCleanRepositories(report)
	if hidden.HiddenClean != 1 || len(hidden.Repositories) != 2 {
		t.Fatalf("hidden = %+v, want the clean row dropped and counted", hidden)
	}
	for _, repository := range hidden.Repositories {
		if repository.Repository == "acme/clean" {
			t.Fatal("the clean row survived filtering")
		}
	}
	if hidden.HiddenClean+len(hidden.Repositories) != len(report.Repositories) {
		t.Fatal("filtering lost rows")
	}
	if statusFailed(statusIndex{Repositories: []repositoryStatusInfo{{Status: "clean"}, {Status: "attention"}}}) {
		t.Fatal("a report without error rows must not fail")
	}
}

func TestCwCovStatusMarkdownAndOutput(t *testing.T) {
	clean := statusIndex{SchemaVersion: 1, HiddenClean: 3}
	markdown := statusMarkdown(clean, false, "# WB fleet status\n\n")
	if !strings.Contains(markdown, "All 3 inspected repositories are clean.") {
		t.Errorf("all-clean markdown = %q", markdown)
	}
	single := statusMarkdown(statusIndex{SchemaVersion: 1, HiddenClean: 1}, false, "# WB fleet status\n\n")
	if !strings.Contains(single, "The inspected repository is clean.") {
		t.Errorf("single-clean markdown = %q", single)
	}

	report := statusIndex{SchemaVersion: 1, HiddenClean: 1, Repositories: []repositoryStatusInfo{
		{Repository: "acme/dirty", Status: "attention", Summary: "2 modified", Modified: []string{"a.go", "b.go"}},
		{Repository: "acme/broken", Status: "error", Error: "not a repository"},
		{Repository: "acme/quiet", Status: "attention"},
	}}
	detailed := statusMarkdown(report, true, "# WB status\n\n")
	for _, want := range []string{
		"# WB status", "| `acme/dirty` | `attention` | 2 modified |",
		"| `acme/broken` | `error` | not a repository |",
		"| `acme/quiet` | `attention` | — |",
		"clean repository hidden; pass `--all` to include it._",
	} {
		if !strings.Contains(detailed, want) {
			t.Errorf("status markdown missing %q:\n%s", want, detailed)
		}
	}
	brief := statusMarkdown(report, false, "# WB status\n\n")
	if strings.Contains(brief, "a.go") {
		t.Errorf("details rendered without --details:\n%s", brief)
	}

	reportDir := filepath.Join(t.TempDir(), "reports")
	var err error
	out := cwCovCaptureStdout(t, func() {
		err = writeStatusOutput(report, "json", reportDir, false, "")
	})
	if err != nil {
		t.Fatalf("status json: %v", err)
	}
	if !strings.Contains(out, `"repository": "acme/dirty"`) {
		t.Errorf("status json = %s", out)
	}
	for _, name := range []string{"status.md", "status.yaml"} {
		if _, statErr := os.Stat(filepath.Join(reportDir, name)); statErr != nil {
			t.Errorf("status report did not write %s: %v", name, statErr)
		}
	}
	if err := writeStatusOutput(report, "toml", "", false, ""); err == nil ||
		!strings.Contains(err.Error(), `unknown --format "toml"`) {
		t.Fatalf("unknown status format = %v", err)
	}
	// An empty title falls back to the local-repository heading.
	out = cwCovCaptureStdout(t, func() {
		err = writeStatusOutput(report, "markdown", "", false, "")
	})
	if err != nil || !strings.Contains(out, "# WB local repository status") {
		t.Fatalf("default title output = %q (%v)", out, err)
	}
}

func TestCwCovRunStatusTargetsWithProgress(t *testing.T) {
	root := t.TempDir()
	cleanRepo := initTestRepository(t, filepath.Join(root, "clean"))
	dirtyRepo := initTestRepository(t, filepath.Join(root, "dirty"))
	if err := os.WriteFile(filepath.Join(dirtyRepo, "notes.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The completion sink is called from the parallel workers, so the slice
	// needs its own lock or the appends race and drop rows.
	var completedMu sync.Mutex
	var completed []string
	reports := runStatusTargetsWithProgress([]qualityTarget{
		{repository: "acme/clean", path: cleanRepo},
		{repository: "acme/dirty", path: dirtyRepo},
		{repository: "acme/missing", path: filepath.Join(root, "absent")},
	}, 2, func(target qualityTarget, info repositoryStatusInfo) {
		completedMu.Lock()
		defer completedMu.Unlock()
		completed = append(completed, target.repository+":"+info.Status)
	})
	if len(reports) != 3 {
		t.Fatalf("reports = %+v", reports)
	}
	byRepo := map[string]string{}
	for _, report := range reports {
		byRepo[report.Repository] = report.Status
	}
	if byRepo["acme/clean"] != "clean" || byRepo["acme/dirty"] != "attention" || byRepo["acme/missing"] != "error" {
		t.Fatalf("statuses = %+v", byRepo)
	}
	if len(completed) != 3 {
		t.Fatalf("completion callbacks = %v", completed)
	}
	// A nil completion sink is tolerated.
	if reports := runStatusTargets([]qualityTarget{{repository: "acme/clean", path: cleanRepo}}, 1); len(reports) != 1 {
		t.Fatalf("nil-progress reports = %+v", reports)
	}
	if reports := runStatusTargets(nil, 2); len(reports) != 0 {
		t.Fatalf("no targets = %+v", reports)
	}
}

func TestCwCovShortSHAForDisplay(t *testing.T) {
	if got := shortSHAForDisplay("0123456789abcdef0123"); got != "0123456789ab" {
		t.Errorf("long sha = %q", got)
	}
	if got := shortSHAForDisplay("abc123"); got != "abc123" {
		t.Errorf("short sha = %q", got)
	}
}

func TestCwCovPrintPullRequestLand(t *testing.T) {
	command := newPRLandCmd()
	var out bytes.Buffer
	command.SetOut(&out)

	success := orchestrate.PullRequestLandResult{
		Repository: "acme/app", PullRequest: 42, Title: "bump deps", Mechanical: true,
		Outcome: orchestrate.LandSuccess, MergeSHA: "0123456789abcdef", BaseRef: "main",
		HeadRef: "task/bump", BranchDeleted: true, CleanedTasks: []string{"bump-deps"}, Kept: true,
		Commits: []orchestrate.LandedCommit{
			{SourceSHA: "aaaaaaaaaaaaaaa", Subject: "kept change", Kept: true},
			{SourceSHA: "bbbbbbbbbbbbbbb", Subject: "aggregated", Kept: false},
		},
	}
	if err := printPullRequestLand(command, success); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"acme/app#42 bump deps (mechanical bump)",
		"landed 0123456789ab on main",
		"retired origin/task/bump",
		"retired worktree for task bump-deps",
		"kept the worktree (--keep)",
		"kept commit aaaaaaaaaaaa kept change",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("land output missing %q:\n%s", want, text)
		}
	}
	// An aggregated (not kept) commit is not listed.
	if strings.Contains(text, "aggregated") {
		t.Errorf("an unkept commit must not be listed:\n%s", text)
	}

	out.Reset()
	refused := orchestrate.PullRequestLandResult{
		Repository: "acme/app", PullRequest: 7, Title: "wip", Outcome: orchestrate.LandRefused,
		Reason: "required checks failed", RefusalCode: "checks-failed",
		SanctionedCommand: "wb pr land acme/app#7 --wait",
	}
	if err := printPullRequestLand(command, refused); err != nil {
		t.Fatal(err)
	}
	text = out.String()
	for _, want := range []string{
		"acme/app#7 wip (needs review)",
		"refused: required checks failed",
		"refusal: checks-failed",
		"resolve with: wb pr land acme/app#7 --wait",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("refusal output missing %q:\n%s", want, text)
		}
	}
}

func TestCwCovSplitCommaSeparated(t *testing.T) {
	got := splitCommaSeparated([]string{"a,b", " c ", "", "d,,e"})
	if strings.Join(got, "|") != "a|b|c|d|e" {
		t.Fatalf("splitCommaSeparated = %v", got)
	}
	if values := splitCommaSeparated(nil); len(values) != 0 {
		t.Fatalf("nil input = %v", values)
	}
}

func TestCwCovPrintBranchListAndDispositionTotals(t *testing.T) {
	command := newBranchListCmd()
	var out bytes.Buffer
	command.SetOut(&out)

	now := time.Now()
	outcome := worktrees.BranchListOutcome{
		Base: "main", Scope: "all",
		Entries: []worktrees.BranchEntry{
			{Repository: "acme/app", Scope: "local", Branch: "task/one", ShortSHA: "abc1234", Disposition: "unmerged", CommitterDate: now.Add(-3 * time.Hour), Evidence: "not contained"},
			{Repository: "acme/app", Scope: "remote", Branch: "origin/task/two", ShortSHA: "def5678", Disposition: "landed", Evidence: "contained in origin/main"},
			{Repository: "beta/tool", Scope: "local", Branch: "task/three", ShortSHA: "0123456", Disposition: "unknown", Evidence: "no committer date"},
		},
		Totals: map[string]int{"landed": 1, "unknown": 1, "unmerged": 1},
	}
	if err := printBranchList(command, outcome); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"acme/app", "beta/tool", "task/one", "abc1234", "unmerged",
		"origin/task/two", "landed", "landed      1", "unknown     1", "unmerged    1",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("branch list missing %q:\n%s", want, text)
		}
	}
	// The repository header appears once per repository run.
	if strings.Count(text, "acme/app") != 1 {
		t.Errorf("repository header repeated:\n%s", text)
	}

	out.Reset()
	if err := printBranchList(command, worktrees.BranchListOutcome{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no branches matched") {
		t.Errorf("empty branch list = %q", out.String())
	}

	// Totals are printed in sorted order so a rerun reads the same.
	var totals bytes.Buffer
	if err := printDispositionTotals(&totals, map[string]int{"zeta": 1, "alpha": 2}); err != nil {
		t.Fatal(err)
	}
	if strings.Index(totals.String(), "alpha") > strings.Index(totals.String(), "zeta") {
		t.Errorf("totals are not sorted:\n%s", totals.String())
	}
}

func TestCwCovPrintBranchCleanup(t *testing.T) {
	command := newBranchCleanupCmd()
	var out bytes.Buffer
	command.SetOut(&out)
	outcome := worktrees.BranchCleanupOutcome{
		Base: "main", Scope: "all", Apply: true,
		Results: []worktrees.BranchCleanupResult{
			{BranchEntry: worktrees.BranchEntry{Repository: "acme/app", Scope: "local", Branch: "task/one", ShortSHA: "abc1234"}, Applied: true},
			{BranchEntry: worktrees.BranchEntry{Repository: "acme/app", Scope: "remote", Branch: "origin/task/two", ShortSHA: "def5678"}, Eligible: true},
			{BranchEntry: worktrees.BranchEntry{Repository: "acme/app", Scope: "local", Branch: "task/three", ShortSHA: "0123456"}, Error: "delete failed"},
			{BranchEntry: worktrees.BranchEntry{Repository: "beta/tool", Disposition: "unreadable"}, SkipReason: "target could not be fetched"},
		},
		Totals: map[string]int{"deleted": 1, "planned": 1, "skipped": 1, "failed": 1},
	}
	if err := printBranchCleanup(command, outcome); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"deleted      local task/one (abc1234)",
		"would delete remote origin/task/two (def5678)",
		"failed       local task/three: delete failed",
		"skip         beta/tool   (unreadable): target could not be fetched",
		"1 deleted",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("branch cleanup missing %q:\n%s", want, text)
		}
	}

	out.Reset()
	if err := printBranchCleanup(command, worktrees.BranchCleanupOutcome{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no branches matched") {
		t.Errorf("empty cleanup = %q", out.String())
	}
}
