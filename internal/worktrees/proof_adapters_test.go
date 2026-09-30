package worktrees

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestProofAdaptersPreserveFacadeInputs(t *testing.T) {
	t.Parallel()
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

func TestProofAgeAdapters(t *testing.T) {
	t.Parallel()
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

}

//nolint:paralleltest // GitHub response fixtures mutate process-wide fake transport state.
func TestLandingEvidenceAdapterKeepsGitHubVerifierFailures(t *testing.T) {
	//nolint:paralleltest // GitHub fixture changes process-wide fake transport state.
	t.Run("unavailable", func(t *testing.T) {
		installFailingGitHubFixture(t)
		ctx := lifecycleGitContext(t, "repo", lifecycleGitReply{operation: "rev-list", output: "head\nancestor\n"})
		if _, err := landingEvidence(ctx, t.TempDir(), "repo", "acme/app", "head", "main", "target", 2); err == nil {
			t.Fatal("GitHub failure hidden")
		}
	})
	//nolint:paralleltest // GitHub fixture changes process-wide fake transport state.
	t.Run("no receipt", func(t *testing.T) {
		installPerCommitPullRequestFixture(t, nil)
		ctx := lifecycleGitContext(t, "repo", lifecycleGitReply{operation: "rev-list", output: "head\nancestor\n"})
		if got, err := landingEvidence(ctx, t.TempDir(), "repo", "acme/app", "head", "main", "target", 2); err != nil || got != nil {
			t.Fatalf("unverified landing = %#v, %v", got, err)
		}
	})
}
