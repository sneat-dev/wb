//go:build e2e

package worktrees

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/testenv"
)

//nolint:paralleltest // Native Git fixture and gh process environment are task-wide.
func TestE2EReviewedBranchForkProofPrecedesRemoteDeletion(t *testing.T) {
	fixture := newGitFixture(t)
	ctx := context.Background()
	installMergedPullRequestFixturesWithMerge(t, nil, nil, time.Time{})
	gitTest(t, fixture.canonical, "checkout", "-b", "feature/contained-reject")
	containedHead := writeAndCommit(t, fixture.canonical, "contained.txt", "landed\n", "contained work")
	gitTest(t, fixture.canonical, "checkout", "main")
	gitTest(t, fixture.canonical, "merge", "--no-ff", "-m", "merge contained work", "feature/contained-reject")
	gitTest(t, fixture.canonical, "push", "origin", "main", "feature/contained-reject")
	gitTest(t, fixture.canonical, "checkout", "-b", "feature/reviewed-fork")
	reviewedHead := writeAndCommit(t, fixture.canonical, "reviewed.txt", "unmerged\n", "reviewed residual")
	gitTest(t, fixture.canonical, "push", "origin", "feature/reviewed-fork")
	gitTest(t, fixture.canonical, "checkout", "main")
	target := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	receipt := SupersessionReceipt{
		Version: 1, Repository: "acme/app", Branch: "feature/reviewed-fork", OriginalHead: reviewedHead,
		Target: "main", TargetHead: target,
		Replacements:      []SupersessionReplacement{{Kind: "commit", Ref: "replacement", SHA: target}},
		Residuals:         []SupersessionResidual{{Commit: reviewedHead, Classification: "obsolete", Reason: "reviewed obsolete source", Reviewed: true}},
		ResidualsComplete: true,
		Approval:          SupersessionApproval{Actor: "reviewer@example.test", Trusted: true, Decision: "approved", ReceiptID: "reviewed-fork", ApprovedAt: time.Now().UTC()},
	}
	receiptBytes, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(t.TempDir(), "supersession.json")
	if err := os.WriteFile(receiptPath, receiptBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	receiptDigest, err := supersessionFileSHA256(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	reviewed := BranchCleanupResult{BranchEntry: BranchEntry{
		Repository: "acme/app", Branch: "feature/reviewed-fork", Base: "main", Scope: BranchScopeRemote,
		SHA: reviewedHead, Disposition: BranchSuperseded, SupersededAtOrigin: true,
		SupersessionReceipt: receiptPath, SupersessionSHA256: receiptDigest,
	}}
	applyRemoteBranchDeletion(ctx, fixture.canonical, &reviewed, BranchCleanupOptions{ProjectsRoot: fixture.projectsRoot})
	if reviewed.Applied || reviewed.Outcome != "failed" || !strings.Contains(reviewed.Error, "fork status") || remoteBranchForTest(t, fixture.canonical, reviewed.Branch) != reviewedHead {
		t.Fatalf("reviewed fork-evidence refusal = %#v", reviewed)
	}
	binDir := t.TempDir()
	if err := testenv.WriteExecutableFile(filepath.Join(binDir, "gh"), []byte("#!/bin/sh\nset -eu\nif [ \"$3\" = 'repos/acme/app' ]; then printf '{\"fork\":false}\\n'; else printf '[]\\n'; fi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	hook := filepath.Join(fixture.remote, "hooks", "pre-receive")
	if err := testenv.WriteExecutableFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	contained := BranchCleanupResult{BranchEntry: BranchEntry{
		Repository: "acme/app", Branch: "feature/contained-reject", Base: "main", Scope: BranchScopeRemote,
		SHA: containedHead, Disposition: BranchContained,
	}}
	applyRemoteBranchDeletion(ctx, fixture.canonical, &contained, BranchCleanupOptions{ProjectsRoot: fixture.projectsRoot})
	if contained.Applied || contained.Outcome != "failed" || !strings.Contains(contained.Error, "force-with-lease") || remoteBranchForTest(t, fixture.canonical, contained.Branch) != containedHead {
		t.Fatalf("contained remote lease refusal = %#v", contained)
	}
	reviewed.Outcome, reviewed.Error = "", ""
	applyRemoteBranchDeletion(ctx, fixture.canonical, &reviewed, BranchCleanupOptions{ProjectsRoot: fixture.projectsRoot})
	if reviewed.Applied || reviewed.Outcome != "failed" || !strings.Contains(reviewed.Error, "force-with-lease") || remoteBranchForTest(t, fixture.canonical, reviewed.Branch) != reviewedHead {
		t.Fatalf("reviewed nonfork lease refusal = %#v", reviewed)
	}
}
