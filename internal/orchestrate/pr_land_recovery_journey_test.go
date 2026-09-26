package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mergedLandFixture models a GitHub merge that happened while WB was away.
// The remote ref is real Git history; only GitHub's PR metadata is scripted.
func mergedLandFixture(t *testing.T, branch string) *landFixture {
	t.Helper()
	fixture := newLandFixture(t, branch, "go.mod")
	runEngineGit(t, fixture.canonical, "push", "origin", fixture.headSHA+":refs/heads/main")
	fixture.writeState(t, "merged", "true")
	fixture.writeState(t, "pr-state", "closed")
	return fixture
}

func TestLandPullRequestMergedRecoveryWaitsForVerifiedMergeCommit(t *testing.T) {
	fixture := mergedLandFixture(t, "bump/recover-unverified")
	fixture.writeState(t, "merge-sha-override", "")
	options := landOptions(fixture)
	options.Keep = false

	first, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if first.Outcome != LandFindings || first.RefusalCode != LandRefusalLandingUnverified || first.MergeSHA != "" {
		t.Fatalf("unverified merge was accepted: %+v", first)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); got != fixture.baseSHA {
		t.Fatalf("canonical target moved to %s before verification", got)
	}
	if err := os.Remove(filepath.Join(fixture.state, "merge-sha-override")); err != nil {
		t.Fatal(err)
	}

	resumed, err := LandPullRequest(context.Background(), options)
	if err != nil || resumed.Outcome != LandSuccess || resumed.MergeSHA != fixture.headSHA || !resumed.BranchDeleted {
		t.Fatalf("verified resume = %+v, err=%v", resumed, err)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); got != fixture.headSHA {
		t.Fatalf("canonical target = %s, want %s", got, fixture.headSHA)
	}
}

func TestLandPullRequestMergedRecoveryWaitsForReachableMergeCommit(t *testing.T) {
	fixture := newLandFixture(t, "bump/recover-unreachable", "go.mod")
	fixture.writeState(t, "merged", "true")
	fixture.writeState(t, "pr-state", "closed")
	fixture.writeState(t, "merge-sha-override", fixture.headSHA)
	options := landOptions(fixture)

	first, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if first.Outcome != LandFindings || first.RefusalCode != LandRefusalLandingUnverified || first.LandingOnBase {
		t.Fatalf("unreachable merge was accepted: %+v", first)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); got != fixture.baseSHA {
		t.Fatalf("canonical target moved to %s before reachability proof", got)
	}
	runEngineGit(t, fixture.canonical, "push", "origin", fixture.headSHA+":refs/heads/main")
	if err := os.Remove(filepath.Join(fixture.state, "merge-sha-override")); err != nil {
		t.Fatal(err)
	}

	resumed, err := LandPullRequest(context.Background(), options)
	if err != nil || resumed.Outcome != LandSuccess || !resumed.LandingOnBase {
		t.Fatalf("reachable resume = %+v, err=%v", resumed, err)
	}
}

func TestLandPullRequestMergedRecoveryKeepsRemoteReceiptWhenCanonicalDirty(t *testing.T) {
	fixture := mergedLandFixture(t, "bump/recover-dirty-canonical")
	path := filepath.Join(fixture.canonical, "go.mod")
	if err := os.WriteFile(path, []byte("local work in progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	options := landOptions(fixture)
	options.Keep = false

	first, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if first.Outcome != LandFindings || first.RefusalCode != LandRefusalCanonicalSync || first.CanonicalSync != "blocked_dirty" {
		t.Fatalf("dirty canonical checkout was accepted: %+v", first)
	}
	if !strings.Contains(first.SanctionedCommand, "wb sync") {
		t.Fatalf("missing canonical recovery command: %+v", first)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); got != fixture.baseSHA {
		t.Fatalf("dirty canonical target moved to %s", got)
	}
	runEngineGit(t, fixture.canonical, "checkout", "--", "go.mod")

	resumed, err := LandPullRequest(context.Background(), options)
	if err != nil || resumed.Outcome != LandSuccess || resumed.CanonicalSync != "fast_forwarded" || !resumed.BranchDeleted {
		t.Fatalf("clean canonical resume = %+v, err=%v", resumed, err)
	}
}
