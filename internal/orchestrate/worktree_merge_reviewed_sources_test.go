package orchestrate

import (
	"strings"
	"testing"
)

func TestReviewedSourceEvidenceRejectsMalformedAndForeignIdentities(t *testing.T) {
	t.Parallel()
	old := strings.Repeat("0", 40)
	a := strings.Repeat("a", 40)
	b := strings.Repeat("b", 40)
	r := WorktreeMergeReceipt{TargetSHA: old, Phase: WorktreeMergePhasePrepare, Status: WorktreeMergeValidationFailed, Sources: []WorktreeMergeSource{{Branch: "a", Task: "a", Worktree: "/a", SHA: old}, {Branch: "b", Task: "b", Worktree: "/b", SHA: old}}}
	replacement := WorktreeMergeCandidate{Task: "b", Worktree: "/b", Branch: "b", SHA: b}
	for _, heads := range [][]string{{a}, {a, "bad"}, {old, b}, {a, a}} {
		if err := validateReviewedSourceEvidence(r, replacement, old, heads); err == nil {
			t.Errorf("accepted invalid heads %v", heads)
		}
	}
	for _, modify := range []func(*WorktreeMergeReceipt){func(x *WorktreeMergeReceipt) { x.Status = WorktreeMergeConflict }, func(x *WorktreeMergeReceipt) { x.PublishedCandidateSHA = a }, func(x *WorktreeMergeReceipt) { x.PullRequest = "3" }, func(x *WorktreeMergeReceipt) { x.TargetSHA = b }} {
		other := r
		modify(&other)
		if err := validateReviewedSourceEvidence(other, replacement, old, []string{a, b}); err == nil {
			t.Error("accepted wrong failure or target")
		}
	}
}
