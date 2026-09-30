package worktrees

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/worktreelanding"
	"os"
	"strings"
	"testing"
	"time"
)

func TestProofAdaptersPreserveFacadeInputs(t *testing.T) {
	sha := strings.Repeat("a", 40)
	if !isGitObjectID(sha) || !isGitRevisionID("abcd") {
		t.Fatal("SHA facade")
	}
	if sameAbsorbedPullRequest(nil, &PullRequest{}) || !sameAbsorbedPullRequest(nil, nil) {
		t.Fatal("PR facade")
	}
	if !isMissingRemoteTargetError(errors.New("could not find remote ref")) || isMissingRemoteTargetError(errors.New("timeout")) {
		t.Fatal("missing target facade")
	}
	if got := detachedRefusal(ListResult{HeadSHA: sha, Base: "main", HeadUnknownToRemote: true}); !strings.Contains(got, "never pushed") {
		t.Fatalf("refusal %q", got)
	}
	landing := &LandingEvidence{LandedSHA: "landed", LandingSHA: "target", Residue: []ResidualCommit{{SHA: sha, Subject: "fix"}}}
	result := ListResult{Landing: landing}
	if !result.landedWithResidue() || !strings.Contains(result.residueReason(), "1 residual commit") {
		t.Fatal("residue facade")
	}
	if got := landing.ResidueSummary(); !strings.Contains(got, "fix") {
		t.Fatalf("summary %q", got)
	}
	var absorbed absorbedConflictReceipt
	absorbed.Candidate.Task = "task"
	if got := absorbed.identity(); got.Candidate.Task != "task" {
		t.Fatalf("absorbed identity %#v", got)
	}
	retired := retiredPrepareCandidateReceipt{TargetSHA: sha}
	if got := retired.identity(); got.Candidate.SHA != sha {
		t.Fatalf("retired identity %#v", got)
	}
}

func TestProofAgeAndGitQueryAdapters(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	path := t.TempDir()
	modified := now.Add(-time.Hour)
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
	entry := &ListResult{OwnerState: "orphaned"}
	applyWorktreeAge(entry, path, inspectPolicy{ttl: 30 * time.Minute, now: func() time.Time { return now }})
	if entry.Owner != "orphaned" || entry.AgeSeconds != 3600 || !entry.Expired {
		t.Fatalf("age %#v", entry)
	}
	absent := &ListResult{}
	applyWorktreeAge(absent, path+"/missing", inspectPolicy{now: func() time.Time { t.Fatal("clock on missing age"); return now }})
	if !absent.CreatedAt.IsZero() {
		t.Fatalf("absent age %#v", absent)
	}
	ctx := lifecycleGitContext(t, "repo", lifecycleGitReply{operation: "merge-base", output: ""})
	yes, err := isAncestor(ctx, "repo", "a", "b")
	if err != nil || !yes {
		t.Fatalf("ancestor %v %v", yes, err)
	}

}

func TestProofUncachedTargetAdapterUsesPrivateRef(t *testing.T) {
	fixture := newGitFixture(t)
	want := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	got, err := worktreelanding.FetchRemoteTargetHeadUncached(context.Background(), fixture.canonical, "main", remoteTargetFetchTimeout, fetchRemoteTargetPrivate)
	if err != nil || got != want {
		t.Fatalf("uncached target = %q, %v; want %q", got, err, want)
	}
}

func TestLandingEvidenceAdapterKeepsGitHubVerifierFailures(t *testing.T) {
	t.Run("unavailable", func(t *testing.T) {
		installFailingGitHubFixture(t)
		ctx := lifecycleGitContext(t, "repo", lifecycleGitReply{operation: "rev-list", output: "head\nancestor\n"})
		if _, err := landingEvidence(ctx, t.TempDir(), "repo", "acme/app", "head", "main", "target", 2); err == nil {
			t.Fatal("GitHub failure hidden")
		}
	})
	t.Run("no receipt", func(t *testing.T) {
		installPerCommitPullRequestFixture(t, nil)
		ctx := lifecycleGitContext(t, "repo", lifecycleGitReply{operation: "rev-list", output: "head\nancestor\n"})
		if got, err := landingEvidence(ctx, t.TempDir(), "repo", "acme/app", "head", "main", "target", 2); err != nil || got != nil {
			t.Fatalf("unverified landing = %#v, %v", got, err)
		}
	})
}
