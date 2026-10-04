package orchestrate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestConflictRecoverySharedIdentityAndResetContracts(t *testing.T) {
	t.Parallel()
	receipt := WorktreeMergeReceipt{Repository: "acme/app", Target: "main", Candidate: WorktreeMergeCandidate{Task: "candidate", Worktree: "/private/candidate", Branch: "wb/candidate"}}
	original := worktrees.WorkLogClaimView{Task: "candidate", Repository: "acme/app", Worktree: "/private/candidate", Branch: "wb/candidate", Base: "main", Lifecycle: "active", BaseSHA: "historical"}
	for _, tc := range []struct {
		name   string
		mutate func(*worktrees.WorkLogClaimView)
		match  bool
	}{
		{"historical base remains caller policy", func(c *worktrees.WorkLogClaimView) { c.BaseSHA = "different" }, true},
		{"task", func(c *worktrees.WorkLogClaimView) { c.Task = "other" }, false},
		{"repository", func(c *worktrees.WorkLogClaimView) { c.Repository = "acme/other" }, false},
		{"worktree", func(c *worktrees.WorkLogClaimView) { c.Worktree = "/private/other" }, false},
		{"branch", func(c *worktrees.WorkLogClaimView) { c.Branch = "other" }, false},
		{"base", func(c *worktrees.WorkLogClaimView) { c.Base = "other" }, false},
		{"lifecycle", func(c *worktrees.WorkLogClaimView) { c.Lifecycle = "retired" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			claim := original
			tc.mutate(&claim)
			if got := recoveryClaimMatches(&claim, receipt, receipt.Candidate.Worktree); got != tc.match {
				t.Fatalf("claim match=%v want=%v", got, tc.match)
			}
		})
	}
	if recoveryClaimMatches(nil, receipt, receipt.Candidate.Worktree) {
		t.Fatal("nil claim admitted")
	}
	before := time.Now()
	receipt.Failure = "old failure"
	recordRecoveredMergeCandidate(&receipt, "resolved")
	if receipt.Candidate.SHA != "resolved" || receipt.Status != WorktreeMergePreparing || receipt.Failure != "" || receipt.UpdatedAt.Before(before) || receipt.UpdatedAt.Location() != time.UTC {
		t.Fatalf("recovery reset=%+v", receipt)
	}
}

func TestConflictRecoveryCanonicalUsesPhysicalNativePaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	canonical := filepath.Join(root, "acme", "app")
	if err := os.MkdirAll(canonical, 0o700); err != nil {
		t.Fatal(err)
	}
	physicalCanonical, err := filepath.EvalSymlinks(canonical)
	if err != nil {
		t.Fatal(err)
	}
	expected, match, err := recoveryCanonicalMatches(root, "acme/app", canonical)
	if err != nil || !match || expected != physicalCanonical {
		t.Fatalf("canonical=%q matches=%v error=%v", expected, match, err)
	}
	if _, match, err := recoveryCanonicalMatches(root, "acme/app", filepath.Join(root, "other")); err != nil || match {
		t.Fatalf("different canonical matched: %v %v", match, err)
	}
	if _, _, err := recoveryCanonicalMatches(root, "invalid", canonical); err == nil {
		t.Fatal("invalid repository admitted")
	}
	missing := filepath.Join(root, "missing")
	if _, match, err := recoveryCanonicalMatches(missing, "acme/app", filepath.Join(missing, "acme", "app")); err != nil || !match {
		t.Fatalf("lexical missing paths changed policy: %v %v", match, err)
	}
	alias := filepath.Join(root, "canonical-alias")
	if err := os.Symlink(canonical, alias); err != nil {
		t.Fatal(err)
	}
	if _, match, err := recoveryCanonicalMatches(root, "acme/app", alias); err != nil || !match {
		t.Fatalf("native physical alias mismatch: %v %v", match, err)
	}
}

func TestConflictRecoveryEntryRefusalsPreserveReceipt(t *testing.T) {
	t.Parallel()
	if changed, err := recoverResolvedWorktreeMergeCandidate(t.Context(), "", nil, 0, 0); changed || err != nil {
		t.Fatalf("nil recovery=%v %v", changed, err)
	}
	if changed, err := advanceResolvedConflictWorktreeMergeCandidate(t.Context(), "", nil, 0, 0); changed || err != nil {
		t.Fatalf("nil advancement=%v %v", changed, err)
	}
	receipt := WorktreeMergeReceipt{Candidate: WorktreeMergeCandidate{SHA: "recorded"}, Status: WorktreeMergeConflict}
	if changed, err := recoverResolvedWorktreeMergeCandidate(t.Context(), "", &receipt, 0, 0); changed || err != nil {
		t.Fatalf("recorded recovery=%v %v", changed, err)
	}
	receipt.Candidate.SHA = ""
	if changed, err := recoverResolvedWorktreeMergeCandidate(t.Context(), "", &receipt, 0, 0); changed || err == nil {
		t.Fatalf("incomplete recovery=%v %v", changed, err)
	}
	if changed, err := advanceResolvedConflictWorktreeMergeCandidate(t.Context(), "", &receipt, 0, 0); changed || err != nil {
		t.Fatalf("empty advancement=%v %v", changed, err)
	}
	receipt.Candidate.SHA = "recorded"
	receipt.PullRequest = "pr"
	receipt.PublishedCandidateSHA = "published"
	if changed, err := advanceResolvedConflictWorktreeMergeCandidate(t.Context(), "", &receipt, 0, 0); changed || err != nil {
		t.Fatalf("published advancement=%v %v", changed, err)
	}
	receipt.PullRequest = ""
	receipt.PublishedCandidateSHA = ""
	if changed, err := advanceResolvedConflictWorktreeMergeCandidate(t.Context(), "", &receipt, 0, 0); changed || err == nil {
		t.Fatalf("incomplete advancement=%v %v", changed, err)
	}
}

func TestConflictRecoveryValidationConsumesOnlyMatchingAcknowledgement(t *testing.T) {
	t.Parallel()
	receipt := WorktreeMergeReceipt{ReceiptPath: "receipt", ID: "id", Lane: "lane", Repository: "acme/app", Target: "main", TargetSHA: "target", Status: WorktreeMergePreparing, Candidate: WorktreeMergeCandidate{Task: "task", Worktree: "candidate", Branch: "branch", SHA: "advanced"}, Sources: []WorktreeMergeSource{{SHA: "source"}}}
	ack := WorktreeMergeConflictCandidateAdvance{ReceiptPath: receipt.ReceiptPath, ReceiptID: receipt.ID, Lane: receipt.Lane, Repository: receipt.Repository, Target: receipt.Target, ReceiptTargetSHA: receipt.TargetSHA, CurrentTargetSHA: receipt.TargetSHA, OriginalCandidate: receipt.Candidate, AdvancedCandidateSHA: receipt.Candidate.SHA, Sources: receipt.Sources, AcknowledgementPath: "ack"}
	sentinel := errors.New("controlled acknowledgement read refused")
	for _, tc := range []struct {
		name    string
		readErr error
		mutate  func(*WorktreeMergeReceipt, *WorktreeMergeConflictCandidateAdvance)
		want    bool
		err     bool
	}{
		{"absent", os.ErrNotExist, nil, false, false},
		{"read refused", sentinel, nil, false, true},
		{"identity mismatch", nil, func(_ *WorktreeMergeReceipt, a *WorktreeMergeConflictCandidateAdvance) { a.Lane = "other" }, false, true},
		{"exact preparing", nil, nil, true, false},
		{"prepared missing validation", nil, func(r *WorktreeMergeReceipt, _ *WorktreeMergeConflictCandidateAdvance) {
			r.Status = WorktreeMergePrepared
		}, false, true},
		{"prepared exact validation", nil, func(r *WorktreeMergeReceipt, _ *WorktreeMergeConflictCandidateAdvance) {
			r.Status = WorktreeMergePrepared
			r.Validation.Revision = r.Candidate.SHA
			r.Validation.Status = quality.StatusPassed
		}, false, false},
		{"conflict", nil, func(r *WorktreeMergeReceipt, _ *WorktreeMergeConflictCandidateAdvance) {
			r.Status = WorktreeMergeConflict
		}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, a := receipt, ack
			if tc.mutate != nil {
				tc.mutate(&r, &a)
			}
			store := nativeConflictAdvanceStore()
			store.read = func(path string) (WorktreeMergeConflictCandidateAdvance, error) {
				if path != conflictCandidateAdvancePath(r.ReceiptPath) {
					t.Fatalf("ack path=%q", path)
				}
				return a, tc.readErr
			}
			got, err := conflictCandidateNeedsValidationWithStore(r, store)
			if got != tc.want || (err != nil) != tc.err {
				t.Fatalf("needs=%v error=%v", got, err)
			}
			if tc.readErr == sentinel && err != sentinel {
				t.Fatalf("read identity=%v", err)
			}
		})
	}
}
