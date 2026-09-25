package main

import (
	"context"
	"testing"

	"github.com/sneat-dev/wb/internal/deps"
)

// TestRwi01SweepDirectivesSortsRowsByRepositoryThenModule covers
// sweepDirectives' final sort.Slice comparator, including its
// Repository-tiebreak-by-Module branch: two remote-only repositories (Path
// == "") short-circuit straight to a row without discovering modules or
// assessing anything, so the sort is exercised with no filesystem or
// process work at all.
func TestRwi01SweepDirectivesSortsRowsByRepositoryThenModule(t *testing.T) {
	t.Parallel()
	repositories := []deps.Repository{
		{Slug: "zeta/repo"},
		{Slug: "alpha/repo"},
		{Slug: "alpha/repo"},
	}
	rows := sweepDirectives(context.Background(), repositories, deps.DirectivePolicy{}, deps.Options{}, "")
	if len(rows) != 3 {
		t.Fatalf("sweepDirectives returned %d rows, want 3", len(rows))
	}
	for i, row := range rows {
		if row.Verdict != verdictNoModule {
			t.Fatalf("rows[%d].Verdict = %q, want %q for a remote-only repository", i, row.Verdict, verdictNoModule)
		}
	}
	if rows[0].Repository != "alpha/repo" || rows[1].Repository != "alpha/repo" || rows[2].Repository != "zeta/repo" {
		t.Fatalf("rows not sorted by repository: %+v", rows)
	}
}
