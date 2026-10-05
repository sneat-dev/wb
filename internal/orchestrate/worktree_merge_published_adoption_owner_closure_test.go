package orchestrate

import (
	"strings"
	"testing"
)

func TestPublishedAdoptionClosureReceiptShapeIsExact(t *testing.T) {
	t.Parallel()
	r := WorktreeMergeReceipt{ReceiptPath: "receipt", ID: "operation", Lane: worktreeMergeLaneID("acme/app", "main"), Repository: "acme/app", Target: "main", Phase: WorktreeMergePhasePrepare, Status: WorktreeMergeConflict, Candidate: WorktreeMergeCandidate{Task: "task", Worktree: "worktree", Branch: "branch", SHA: "sha"}, Sources: []WorktreeMergeSource{{Task: "source"}}}
	if err := validatePublishedCandidateAdoptionReceipt(r, r.ReceiptPath); err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		name  string
		apply func(*WorktreeMergeReceipt)
	}{
		{"path", func(r *WorktreeMergeReceipt) { r.ReceiptPath = "different" }},
		{"phase", func(r *WorktreeMergeReceipt) { r.Phase = WorktreeMergePhaseLand }},
		{"status", func(r *WorktreeMergeReceipt) { r.Status = WorktreeMergePrepared }},
		{"ID", func(r *WorktreeMergeReceipt) { r.ID = "" }},
		{"lane", func(r *WorktreeMergeReceipt) { r.Lane = "other" }},
		{"repository", func(r *WorktreeMergeReceipt) { r.Repository = "" }},
		{"target", func(r *WorktreeMergeReceipt) { r.Target = "" }},
		{"candidate task", func(r *WorktreeMergeReceipt) { r.Candidate.Task = "" }},
		{"candidate path", func(r *WorktreeMergeReceipt) { r.Candidate.Worktree = "" }},
		{"candidate branch", func(r *WorktreeMergeReceipt) { r.Candidate.Branch = "" }},
		{"candidate SHA", func(r *WorktreeMergeReceipt) { r.Candidate.SHA = "" }},
		{"sources", func(r *WorktreeMergeReceipt) { r.Sources = nil }},
		{"PR", func(r *WorktreeMergeReceipt) { r.PullRequest = "7" }},
		{"publication", func(r *WorktreeMergeReceipt) { r.PublishedCandidateSHA = "published" }},
		{"landing", func(r *WorktreeMergeReceipt) { r.LandingSHA = "landed" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			t.Parallel()
			current := r
			change.apply(&current)
			if err := validatePublishedCandidateAdoptionReceipt(current, r.ReceiptPath); err == nil || !strings.Contains(err.Error(), "not an exact unlanded") {
				t.Fatalf("shape %s err=%v", change.name, err)
			}
		})
	}
}
