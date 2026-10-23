package cmdbranch

import (
	"bytes"
	"github.com/sneat-dev/wb/internal/worktrees"
	"strings"
	"testing"
	"time"
)

func TestBranchListShowsSortedDispositionTotals(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

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
	if err := printBranchList(&out, outcome); err != nil {
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
	if err := printBranchList(&out, worktrees.BranchListOutcome{}); err != nil {
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

func TestBranchCleanupShowsEveryOutcome(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
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
	if err := printBranchCleanup(&out, outcome); err != nil {
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
	if err := printBranchCleanup(&out, worktrees.BranchCleanupOutcome{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no branches matched") {
		t.Errorf("empty cleanup = %q", out.String())
	}
}
