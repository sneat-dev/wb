package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestCwCovShortSHAForDisplay(t *testing.T) {
	if got := shortSHAForDisplay("0123456789abcdef0123"); got != "0123456789ab" {
		t.Errorf("long sha = %q", got)
	}
	if got := shortSHAForDisplay("abc123"); got != "abc123" {
		t.Errorf("short sha = %q", got)
	}
}

func TestCwCovPrintPullRequestLand(t *testing.T) {
	command := newPRLandCmd(&invocation{})
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
	command := newBranchListCmd(&invocation{})
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
	// The table prints a repository column on every row for copyable fleet output.
	if strings.Count(text, "acme/app") != 3 {
		t.Errorf("repository column occurrence count changed:\n%s", text)
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
	command := newBranchCleanupCmd(&invocation{})
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
