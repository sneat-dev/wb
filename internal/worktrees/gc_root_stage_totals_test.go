package worktrees

import "testing"

// A repository-root stage is swept by retireEmptyUnscopedLocalStages under
// --apply, not by the task-scoped purge that feeds purged_artefacts. Before
// these totals existed, gc printed one row per such stage above a footer
// reading "0 terminal artefacts purged" — true of the other counter, and read
// by every reader as "gc will not touch these".
func TestSummarizeGCCountsRootStagesSeparatelyFromTaskArtefacts(t *testing.T) {
	t.Parallel()
	outcome := &GCOutcome{
		Totals: map[string]int{},
		Artifacts: []LifecycleArtifact{
			{Disposition: dispositionEmptyUnscopedLocalRetiredStage, Eligible: true},
			{Disposition: dispositionEmptyUnscopedLocalRetiredStage, Eligible: true},
			{Disposition: dispositionRetiredEmptyUnscopedLocalStage, Applied: true},
			// An ineligible root stage is neither planned nor done.
			{Disposition: dispositionEmptyUnscopedLocalRetiredStage, Eligible: false},
			// A stage kept for its owning task is not this sweep's business.
			{Disposition: dispositionUnscopedLocalStage},
		},
	}
	summarizeGC(outcome)

	if got := outcome.Totals["eligible_root_stages"]; got != 2 {
		t.Fatalf("eligible root stages = %d, want 2", got)
	}
	if got := outcome.Totals["retired_root_stages"]; got != 1 {
		t.Fatalf("retired root stages = %d, want 1", got)
	}
	// The task-scoped counter must not absorb them: the two sweeps have
	// different authorities and different apply paths.
	if got := outcome.Totals["purged_artefacts"]; got != 0 {
		t.Fatalf("purged artefacts = %d, want 0", got)
	}
}
