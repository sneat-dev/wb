package orchestrate

import (
	"github.com/sneat-dev/wb/internal/worktrees"
	"testing"
)

func TestCandidateCustodySharedIdentityKeepsBasePolicySeparate(t *testing.T) {
	t.Parallel()
	claim := worktrees.WorkLogClaimView{Repository: "acme/app", Task: "candidate", Worktree: "/private/candidate", Branch: "wb/candidate", Lifecycle: "active", Base: "main", BaseSHA: "historical"}
	for _, tc := range []struct {
		name   string
		mutate func(*worktrees.WorkLogClaimView)
		want   bool
	}{
		{"exact", func(*worktrees.WorkLogClaimView) {}, true},
		{"base remains owner policy", func(c *worktrees.WorkLogClaimView) { c.Base = "different"; c.BaseSHA = "" }, true},
		{"repository", func(c *worktrees.WorkLogClaimView) { c.Repository = "acme/other" }, false},
		{"task", func(c *worktrees.WorkLogClaimView) { c.Task = "other" }, false},
		{"path", func(c *worktrees.WorkLogClaimView) { c.Worktree = "/private/other" }, false},
		{"branch", func(c *worktrees.WorkLogClaimView) { c.Branch = "other" }, false},
		{"lifecycle", func(c *worktrees.WorkLogClaimView) { c.Lifecycle = "terminal" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := claim
			tc.mutate(&c)
			if got := mergeClaimMatchesIdentity(&c, "acme/app", "candidate", "/private/candidate/.", "wb/candidate"); got != tc.want {
				t.Fatalf("identity=%v want=%v", got, tc.want)
			}
		})
	}
	if mergeClaimMatchesIdentity(nil, "acme/app", "candidate", "/private/candidate", "wb/candidate") {
		t.Fatal("nil claim accepted")
	}
	receipt := WorktreeMergeReceipt{Repository: claim.Repository, Target: claim.Base, Candidate: WorktreeMergeCandidate{Task: claim.Task, Branch: claim.Branch}}
	if !recoveryClaimMatches(&claim, receipt, claim.Worktree) {
		t.Fatal("historical base identity refused")
	}
	claim.BaseSHA = ""
	if !recoveryClaimMatches(&claim, receipt, claim.Worktree) {
		t.Fatal("recovery adaptor silently imposed acknowledgement base-SHA policy")
	}
	claim.Base = "different"
	if recoveryClaimMatches(&claim, receipt, claim.Worktree) {
		t.Fatal("recovery base name mismatch accepted")
	}
}
