package prmeta

import (
	"strings"
	"testing"
)

func TestAppendAddsStableEffortAndStreamWithoutBranchOrWorktree(t *testing.T) {
	got := Append("Summary.\n", Provenance{Effort: "checkout-rewrite", Stream: "cli-helpers"})
	for _, want := range []string{
		"WB effort: `checkout-rewrite`",
		"WB stream: `cli-helpers`",
		`<!-- wb:provenance:v1 {"effort":"checkout-rewrite","stream":"cli-helpers"} -->`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("body lacks %q:\n%s", want, got)
		}
	}
	for _, absent := range []string{"worktree", "Branch:"} {
		if strings.Contains(got, absent) {
			t.Fatalf("body unexpectedly duplicates machine or GitHub metadata %q:\n%s", absent, got)
		}
	}
	if retried := Append(got, Provenance{Effort: "checkout-rewrite", Stream: "cli-helpers"}); retried != got {
		t.Fatalf("retry changed body:\n%s", retried)
	}
}
