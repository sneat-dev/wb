package orchestrate

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRetiredPrepareOwnerReceiptPolicyContracts(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipt.json")
	r := WorktreeMergeReceipt{ReceiptPath: path, ID: "legacy", Repository: "acme/app", Target: "gone", TargetSHA: strings.Repeat("a", 40), Phase: WorktreeMergePhasePrepare, Status: WorktreeMergeConflict, Candidate: WorktreeMergeCandidate{Task: "candidate", Worktree: "/candidate", Branch: "wb/candidate"}, Sources: []WorktreeMergeSource{{Task: "source", Worktree: "/source", Branch: "feature/source", SHA: strings.Repeat("b", 40)}}}
	r.Lane = worktreeMergeLaneID(r.Repository, r.Target)
	if err := validateRetiredPrepareCandidateReceipt(r, path); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, want string
		mutate     func(*WorktreeMergeReceipt)
	}{
		{"path", "inconsistent", func(r *WorktreeMergeReceipt) { r.ReceiptPath = "different" }},
		{"id", "inconsistent", func(r *WorktreeMergeReceipt) { r.ID = "" }},
		{"lane", "inconsistent", func(r *WorktreeMergeReceipt) { r.Lane = "" }},
		{"phase", "failed prepare conflict", func(r *WorktreeMergeReceipt) { r.Phase = WorktreeMergePhaseLand }},
		{"status", "failed prepare conflict", func(r *WorktreeMergeReceipt) { r.Status = WorktreeMergePrepared }},
		{"landing", "failed prepare conflict", func(r *WorktreeMergeReceipt) { r.LandingSHA = r.TargetSHA }},
		{"pr", "failed prepare conflict", func(r *WorktreeMergeReceipt) { r.PullRequest = "17" }},
		{"published", "failed prepare conflict", func(r *WorktreeMergeReceipt) { r.PublishedCandidateSHA = r.TargetSHA }},
		{"target sha", "empty-candidate", func(r *WorktreeMergeReceipt) { r.TargetSHA = "" }},
		{"candidate sha", "empty-candidate", func(r *WorktreeMergeReceipt) { r.Candidate.SHA = r.TargetSHA }},
		{"confused source", "candidate-confused", func(r *WorktreeMergeReceipt) { r.Sources[0].Worktree = r.Candidate.Worktree }},
		{"missing source", "candidate-confused", func(r *WorktreeMergeReceipt) { r.Sources = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			copy := r
			copy.Sources = append([]WorktreeMergeSource(nil), r.Sources...)
			tc.mutate(&copy)
			if err := validateRetiredPrepareCandidateReceipt(copy, path); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("actual schema refusal: %v want %s", err, tc.want)
			}
		})
	}
}
