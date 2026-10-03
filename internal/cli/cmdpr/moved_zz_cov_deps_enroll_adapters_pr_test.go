package cmdpr

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/orchestrate"
)

func TestCwDepsPrintPullRequestLandShapes(t *testing.T) {
	t.Parallel()
	deps := testDependencies()
	_ = deps
	success := orchestrate.PullRequestLandResult{
		Repository: "acme/app", PullRequest: 42, Title: "fix the thing", Outcome: orchestrate.LandSuccess,
		MergeSHA: strings.Repeat("a", 40), BaseRef: "main", HeadRef: "feature/fix", BranchDeleted: true,
		CleanedTasks: []string{"task-1"}, Kept: true,
		Commits: []orchestrate.LandedCommit{
			{SourceSHA: strings.Repeat("b", 40), Subject: "keep me", Kept: true},
			{SourceSHA: strings.Repeat("c", 40), Subject: "squash me", Kept: false},
		},
	}
	var out bytes.Buffer
	if err := printPullRequestLand(outputCommand(&out), success); err != nil {
		t.Fatalf("success print: %v", err)
	}
	for _, want := range []string{
		"acme/app#42 fix the thing (needs review)",
		"landed " + strings.Repeat("a", 12) + " on main",
		"retired origin/feature/fix",
		"retired worktree for task task-1",
		"kept the worktree (--keep)",
		"kept commit " + strings.Repeat("b", 12) + " keep me",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("success output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "squash me") {
		t.Errorf("an unkept commit must not be listed:\n%s", out.String())
	}
	// A mechanical bump is classified as such.
	out.Reset()
	success.Mechanical = true
	if err := printPullRequestLand(outputCommand(&out), success); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "(mechanical bump)") {
		t.Errorf("mechanical classification missing:\n%s", out.String())
	}
	// A refusal names its code and the command that satisfies the guard.
	out.Reset()
	refused := orchestrate.PullRequestLandResult{
		Repository: "acme/app", PullRequest: 7, Title: "wip", Outcome: orchestrate.LandRefused,
		Reason: "checks are red", RefusalCode: "checks-not-green", SanctionedCommand: "wb pr land acme/app#8",
	}
	if err := printPullRequestLand(outputCommand(&out), refused); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"refused: checks are red", "refusal: checks-not-green", "resolve with: wb pr land acme/app#8"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("refusal output missing %q:\n%s", want, out.String())
		}
	}
	// A short SHA is printed whole, and a failed write is surfaced.
	if got := shortSHAForDisplay("abc"); got != "abc" {
		t.Errorf("shortSHAForDisplay(abc) = %q", got)
	}
	if err := printPullRequestLand(outputCommand(failingWriter{}), success); err == nil {
		t.Error("a failed print must be surfaced")
	}
}

func TestCwDepsPRLandCommandUsageRefusals(t *testing.T) {
	t.Parallel()
	deps := testDependencies()
	_ = deps

	tests := map[string]struct {
		args []string
		want string
		code int
	}{
		"unknown format":        {[]string{"pr", "land", "acme/app#1", "--format", "toml"}, "unsupported format", exitFindings},
		"keep commits alone":    {[]string{"pr", "land", "acme/app#1", "--keep-commits", "4f2a1c9"}, "--keep-commits requires", exitUsage},
		"take over without why": {[]string{"pr", "land", "acme/app#1", "--take-over-lane"}, "--take-over-lane requires", exitUsage},
		"bad selector":          {[]string{"pr", "land", "not-a-selector"}, "owner/repository#number", exitUsage},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var out, errOut strings.Builder
			code := executeTest(New(testRuntime(), deps), test.args[1:], &out, &errOut)
			stderr := errOut.String()
			if code != test.code {
				t.Fatalf("%v exit = %d, want %d\nstderr: %s", test.args, code, test.code, stderr)
			}
			if !strings.Contains(strings.ToLower(stderr), strings.ToLower(test.want)) {
				t.Errorf("%v stderr = %q, want %q", test.args, stderr, test.want)
			}
		})
	}
}
