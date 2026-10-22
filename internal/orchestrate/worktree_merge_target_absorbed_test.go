package orchestrate

import (
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubchecks"
)

func TestTargetAbsorptionGateHoldsLandedReceiptsUntilPostTargetCIPassedAndSettled(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	landed := WorktreeMergeReceipt{Status: WorktreeMergeLanded, LandingSHA: "abc", UpdatedAt: now.Add(-time.Hour),
		Checks: githubchecks.PullRequestWaitResult{Status: githubchecks.PullRequestWaitPassed, Head: "abc"}}
	if err := worktreeMergeTargetAbsorptionGate(landed, now); err != nil {
		t.Fatalf("settled landed receipt with passed target CI = %v", err)
	}
	if err := worktreeMergeTargetAbsorptionGate(WorktreeMergeReceipt{Status: WorktreeMergePrepared}, now); err != nil {
		t.Fatalf("unlanded prepared receipt = %v", err)
	}
	for name, edit := range map[string]func(*WorktreeMergeReceipt){
		"ineligible status":     func(r *WorktreeMergeReceipt) { r.Status = WorktreeMergePostTargetCIFailed },
		"target CI pending":     func(r *WorktreeMergeReceipt) { r.Checks.Status = githubchecks.PullRequestWaitPending },
		"checks for other head": func(r *WorktreeMergeReceipt) { r.Checks.Head = "other" },
		"direct CI deferral": func(r *WorktreeMergeReceipt) {
			r.ValidationDeferral = &WorktreeMergeValidationDeferral{Route: WorktreeMergeRouteDirect}
		},
		"recently updated": func(r *WorktreeMergeReceipt) { r.UpdatedAt = now.Add(-time.Minute) },
	} {
		receipt := landed
		edit(&receipt)
		if err := worktreeMergeTargetAbsorptionGate(receipt, now); err == nil {
			t.Errorf("%s: landed receipt passed the gate", name)
		}
	}
}

func TestTargetAbsorptionEligibilityAndRecordedCommits(t *testing.T) {
	t.Parallel()
	for status, want := range map[WorktreeMergeStatus]bool{
		WorktreeMergePrepared: true, WorktreeMergeLanded: true, WorktreeMergeCanonicalSyncBlocked: true,
		WorktreeMergePreparing: false, WorktreeMergePostTargetCIFailed: false, WorktreeMergeComplete: false,
	} {
		if got := worktreeMergeTargetAbsorptionEligible(status); got != want {
			t.Errorf("eligible(%s) = %t, want %t", status, got, want)
		}
	}
	if _, ok := worktreeMergeTargetAbsorptionCommits(WorktreeMergeReceipt{}); ok {
		t.Error("a receipt without sources yielded commits")
	}
	commits, ok := worktreeMergeTargetAbsorptionCommits(WorktreeMergeReceipt{
		Sources:   []WorktreeMergeSource{{SHA: "b"}, {SHA: "a"}},
		Candidate: WorktreeMergeCandidate{SHA: "c"}, PublishedCandidateSHA: "c", LandingSHA: "d",
	})
	if !ok || strings.Join(commits, ",") != "a,b,c,d" {
		t.Errorf("commits = %v, %t; want sorted unique a,b,c,d", commits, ok)
	}
}
