package orchestrate

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestUnpublishedOwnerReceiptPolicyContracts(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipt.json")
	r := WorktreeMergeReceipt{ReceiptPath: path, Repository: "acme/app", Target: "main", TargetSHA: strings.Repeat("a", 40), Phase: WorktreeMergePhasePrepare, Status: WorktreeMergeValidationFailed, Candidate: WorktreeMergeCandidate{Worktree: "/candidate", Branch: "wb/candidate", SHA: strings.Repeat("c", 40)}, Sources: []WorktreeMergeSource{{Task: "source", Worktree: "/source", Branch: "feature/source", SHA: strings.Repeat("b", 40)}}}
	r.Lane = worktreeMergeLaneID(r.Repository, r.Target)
	r.ID = worktreeMergeOperationID(r.Lane, r.Sources)
	r.Candidate.Task = r.ID
	for _, status := range []WorktreeMergeStatus{WorktreeMergeValidationFailed, WorktreeMergePreparing} {
		copy := r
		copy.Status = status
		if err := validateUnpublishedValidationFailureReceipt(copy, path); err != nil {
			t.Fatalf("supported policy: %v", err)
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*WorktreeMergeReceipt)
		want   string
	}{
		{"path", func(r *WorktreeMergeReceipt) { r.ReceiptPath = "different" }, "exact unpublished"},
		{"lane", func(r *WorktreeMergeReceipt) { r.Lane = "different" }, "exact unpublished"},
		{"phase", func(r *WorktreeMergeReceipt) { r.Phase = WorktreeMergePhaseLand }, "exact unpublished"},
		{"status", func(r *WorktreeMergeReceipt) { r.Status = WorktreeMergePrepared }, "exact unpublished"},
		{"landing", func(r *WorktreeMergeReceipt) { r.LandingSHA = r.TargetSHA }, "exact unpublished"},
		{"pr", func(r *WorktreeMergeReceipt) { r.PullRequest = "17" }, "exact unpublished"},
		{"published", func(r *WorktreeMergeReceipt) { r.PublishedCandidateSHA = r.Candidate.SHA }, "exact unpublished"},
		{"target sha", func(r *WorktreeMergeReceipt) { r.TargetSHA = "" }, "exact unpublished"},
		{"candidate task", func(r *WorktreeMergeReceipt) { r.Candidate.Task = "different" }, "exact unpublished"},
		{"candidate sha", func(r *WorktreeMergeReceipt) { r.Candidate.SHA = "" }, "exact unpublished"},
		{"operation id", func(r *WorktreeMergeReceipt) { r.ID = "different"; r.Candidate.Task = r.ID }, "exact unpublished"},
		{"source task", func(r *WorktreeMergeReceipt) { r.Sources[0].Task = "" }, "incomplete immutable"},
		{"source worktree", func(r *WorktreeMergeReceipt) { r.Sources[0].Worktree = "" }, "incomplete immutable"},
		{"source branch", func(r *WorktreeMergeReceipt) { r.Sources[0].Branch = "" }, "incomplete immutable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			copy := r
			copy.Sources = append([]WorktreeMergeSource(nil), r.Sources...)
			tc.mutate(&copy)
			if err := validateUnpublishedValidationFailureReceipt(copy, path); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("native DTO refusal: %v", err)
			}
		})
	}
}
