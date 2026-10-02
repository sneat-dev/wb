package worktrees

import (
	"testing"
)

func TestLifecycleNextCleanupProofPriorityAndNilClaim(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		entry ListResult
		want  string
	}{
		{"none", ListResult{}, ""},
		{"absorbed", ListResult{AbsorbedConflictAcknowledgementPath: "retained-ack"}, "absorbed_conflict_acknowledgement"},
		{"retired dominates absorbed", ListResult{RetiredPrepareCandidateAcknowledgementPath: "retained-retired", AbsorbedConflictAcknowledgementPath: "retained-ack"}, "retired_prepare_candidate_acknowledgement"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := cleanupResultProof(tc.entry); got != tc.want {
				t.Fatalf("proof=%q want%q", got, tc.want)
			}
		})
	}
	if got := cleanupTaskToClaim(nil); got != nil {
		t.Fatalf("absent cleanup authority became claim: %+v", got)
	}
}
