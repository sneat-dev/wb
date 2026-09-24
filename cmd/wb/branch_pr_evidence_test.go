package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestPrintBranchListExplainsRemotePullRequestEvidence(t *testing.T) {
	for _, test := range []struct {
		name  string
		entry worktrees.BranchEntry
		want  string
	}{
		{
			name:  "query failure",
			entry: worktrees.BranchEntry{PullRequestQueryFailed: true, PullRequestQueryError: "GitHub unavailable"},
			want:  "; PR query failed: GitHub unavailable",
		},
		{
			name:  "no matching PR",
			entry: worktrees.BranchEntry{PullRequestQueried: true},
			want:  "; PRs: none",
		},
		{
			name: "head and base PRs",
			entry: worktrees.BranchEntry{PullRequestQueried: true, PullRequests: []worktrees.BranchPullRequest{
				{Role: "head", Number: 7, State: "merged", URL: "https://example.test/7"},
				{Role: "base", Number: 9, State: "open", URL: "https://example.test/9"},
			}},
			want: "head #7 merged https://example.test/7, base #9 open https://example.test/9",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry := test.entry
			entry.Repository, entry.Branch, entry.Scope = "acme/app", "feature/pr-evidence", worktrees.BranchScopeRemote
			entry.Disposition, entry.Evidence = worktrees.BranchContained, "ancestor of main"
			command := newBranchListCmd()
			var output bytes.Buffer
			command.SetOut(&output)
			if err := printBranchList(command, worktrees.BranchListOutcome{
				Entries: []worktrees.BranchEntry{entry}, Totals: map[string]int{worktrees.BranchContained: 1},
			}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), test.want) {
				t.Fatalf("output %q does not contain %q", output.String(), test.want)
			}
		})
	}
}
