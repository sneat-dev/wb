package cmdpr

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/orchestrate"
)

func TestCwCovShortSHAForDisplay(t *testing.T) {
	t.Parallel()
	deps := testDependencies()
	_ = deps
	if got := shortSHAForDisplay("0123456789abcdef0123"); got != "0123456789ab" {
		t.Errorf("long sha = %q", got)
	}
	if got := shortSHAForDisplay("abc123"); got != "abc123" {
		t.Errorf("short sha = %q", got)
	}
}

func TestCwCovPrintPullRequestLand(t *testing.T) {
	t.Parallel()
	deps := testDependencies()
	_ = deps
	command := NewLand(testRuntime(), deps)
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
	t.Parallel()
	deps := testDependencies()
	_ = deps
	got := splitCommaSeparated([]string{"a,b", " c ", "", "d,,e"})
	if strings.Join(got, "|") != "a|b|c|d|e" {
		t.Fatalf("splitCommaSeparated = %v", got)
	}
	if values := splitCommaSeparated(nil); len(values) != 0 {
		t.Fatalf("nil input = %v", values)
	}
}
