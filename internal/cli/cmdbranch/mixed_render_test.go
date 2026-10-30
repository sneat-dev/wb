package cmdbranch

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktrees"
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
