//go:build e2e

// This file holds pr_land_keep_coverage_test.go's real-git cases
// (spec/plans/coverage-to-100 task-17): each one exercises a function that
// now calls real git through orchestrateGit/orchestrateRunner
// (internal/runner), which task-24's runtime guard blocks outside the e2e
// tier. Moving them here, rather than calling runnertest.AllowRealProcess in
// the default tier, keeps internal/quality/testdata/unit_tier.pending's
// cross-PR total from rising (decision 20's "only a happy-path journey...
// moves to the e2e tier"; these already were happy-path/refusal journeys
// against a real fixture, not one-off failure probes).
package orchestrate

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrchCovBuildAtReportsAFailedBuildAndAcceptsAPassingOne(t *testing.T) {
	t.Parallel()
	if refusal := buildAt(context.Background(), t.TempDir(), SourceCommit{SHA: "0123456789abcdef"}, []string{"sh", "-c", "exit 0"}); refusal != nil {
		t.Fatalf("passing build refusal = %+v", refusal)
	}
	refusal := buildAt(context.Background(), t.TempDir(),
		SourceCommit{SHA: "0123456789abcdef", Subject: "add the thing"},
		[]string{"sh", "-c", "echo compile exploded >&2; exit 1"})
	if refusal == nil || refusal.code != LandRefusalKeepDoesNotBuild {
		t.Fatalf("failing build refusal = %+v", refusal)
	}
	if !strings.Contains(refusal.reason, "add the thing") || !strings.Contains(refusal.reason, "compile exploded") {
		t.Fatalf("failing build refusal = %+v", refusal)
	}
	if !strings.Contains(refusal.command, "0123456789ab") {
		t.Fatalf("failing build refusal command = %q", refusal.command)
	}
}

//nolint:paralleltest // calls a fixture helper (newLandFixture/newEngineFixture/createMergeSource) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestOrchCovCommitsBetweenAndPatchIdentityDescribeOneCommit(t *testing.T) {
	fixture := newLandFixture(t, "candidate", "a.txt", "b.txt", "c.txt")
	commits, err := commitsBetween(context.Background(), fixture.canonical, fixture.baseSHA, fixture.headSHA)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 3 || commits[0] != fixture.commitSHAs[0] || commits[2] != fixture.commitSHAs[2] {
		t.Fatalf("commits between = %v, want %v", commits, fixture.commitSHAs)
	}
	first, err := patchIdentity(context.Background(), fixture.canonical, commits[0])
	if err != nil || first == "" {
		t.Fatalf("patch identity = %q, err %v", first, err)
	}
	second, err := patchIdentity(context.Background(), fixture.canonical, commits[1])
	if err != nil || second == first {
		t.Fatalf("distinct commits shared a patch identity: %q vs %q (err %v)", first, second, err)
	}
}

//nolint:paralleltest // calls a fixture helper (newLandFixture/newEngineFixture/createMergeSource) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestOrchCovPatchIdentityHasNoIdentityForAMergeCommit(t *testing.T) {
	fixture := newLandFixture(t, "candidate", "a.txt")
	runEngineGit(t, fixture.canonical, "checkout", "-b", "side", fixture.baseSHA)
	writeEngineFile(t, filepath.Join(fixture.canonical, "side.txt"), "side\n")
	runEngineGit(t, fixture.canonical, "add", "-A")
	runEngineGit(t, fixture.canonical, "commit", "-m", "side work")
	sideSHA := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, fixture.canonical, "checkout", "main")
	writeEngineFile(t, filepath.Join(fixture.canonical, "main.txt"), "main\n")
	runEngineGit(t, fixture.canonical, "add", "-A")
	runEngineGit(t, fixture.canonical, "commit", "-m", "main work")
	runEngineGit(t, fixture.canonical, "merge", "--no-ff", "-m", "merge side", sideSHA)
	mergeSHA := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))

	identity, err := patchIdentity(context.Background(), fixture.canonical, mergeSHA)
	if err != nil || identity != "" {
		t.Fatalf("merge commit patch identity = %q, err %v", identity, err)
	}

	// A source WB cannot fingerprint is left without a pairing rather than
	// guessed at, and the unclaimed landed commit still carries the aggregate.
	landed := []LandedCommit{
		{SourceSHA: mergeSHA, Subject: "merge side", Kept: true},
		{SourceSHA: sideSHA, Subject: "side work"},
	}
	mapped, err := MapLandedCommits(context.Background(), fixture.canonical, "main", fixture.baseSHA, landed)
	if err != nil {
		t.Fatal(err)
	}
	if mapped[0].LandedSHA != "" {
		t.Fatalf("unfingerprintable kept source was paired with %q", mapped[0].LandedSHA)
	}
	if mapped[1].LandedSHA == "" {
		t.Fatalf("aggregated source was left unpaired: %+v", mapped)
	}
}

//nolint:paralleltest // calls a fixture helper (newLandFixture/newEngineFixture/createMergeSource) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestOrchCovMapLandedCommitsPairsKeptSourcesByPatchIdentity(t *testing.T) {
	fixture := newLandFixture(t, "candidate", "a.txt", "b.txt", "c.txt")
	runEngineGit(t, fixture.canonical, "checkout", "-b", "landed", fixture.baseSHA)
	runEngineGit(t, fixture.canonical, "cherry-pick", fixture.commitSHAs[1])
	keptLanded := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, fixture.canonical, "cherry-pick", "--no-commit", fixture.commitSHAs[0], fixture.commitSHAs[2])
	runEngineGit(t, fixture.canonical, "commit", "-m", "aggregate remaining")
	aggregate := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, fixture.canonical, "checkout", "main")

	landed := []LandedCommit{
		{SourceSHA: fixture.commitSHAs[1], Subject: "change b.txt", Kept: true},
		{SourceSHA: fixture.commitSHAs[0], Subject: "change a.txt"},
		{SourceSHA: fixture.commitSHAs[2], Subject: "change c.txt"},
	}
	mapped, err := MapLandedCommits(context.Background(), fixture.canonical, "landed", fixture.baseSHA, landed)
	if err != nil {
		t.Fatal(err)
	}
	if mapped[0].LandedSHA != keptLanded {
		t.Fatalf("kept source paired with %q, want %q", mapped[0].LandedSHA, keptLanded)
	}
	if mapped[1].LandedSHA != aggregate || mapped[2].LandedSHA != aggregate {
		t.Fatalf("aggregated sources = %q/%q, want %q", mapped[1].LandedSHA, mapped[2].LandedSHA, aggregate)
	}
	if !mapped[0].Kept || mapped[1].Kept || mapped[2].Kept {
		t.Fatalf("kept flags changed: %+v", mapped)
	}
}

//nolint:paralleltest // calls a fixture helper (newLandFixture/newEngineFixture/createMergeSource) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestOrchCovMapLandedCommitsLeavesEverythingUnpairedWithoutAnAggregate(t *testing.T) {
	fixture := newLandFixture(t, "candidate", "a.txt", "b.txt")
	runEngineGit(t, fixture.canonical, "checkout", "-b", "landed", fixture.baseSHA)
	runEngineGit(t, fixture.canonical, "cherry-pick", fixture.commitSHAs[0])
	keptLanded := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, fixture.canonical, "checkout", "main")

	landed := []LandedCommit{{SourceSHA: fixture.commitSHAs[0], Subject: "change a.txt", Kept: true}}
	mapped, err := MapLandedCommits(context.Background(), fixture.canonical, "landed", fixture.baseSHA, landed)
	if err != nil {
		t.Fatal(err)
	}
	if mapped[0].LandedSHA != keptLanded {
		t.Fatalf("kept source paired with %q, want %q", mapped[0].LandedSHA, keptLanded)
	}
}

//nolint:paralleltest // calls a fixture helper (newLandFixture/newEngineFixture/createMergeSource) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestOrchCovRewriteBranchForKeptCommitsLandsKeptAndAggregatedCommits(t *testing.T) {
	fixture := newLandFixture(t, "candidate", "a.txt", "b.txt", "c.txt")
	commits := orchCovSourceCommits(t, fixture.canonical, fixture.baseSHA, fixture.headSHA)
	if len(commits) != 3 {
		t.Fatalf("fixture commits = %+v", commits)
	}
	plan, refusal := planKeptCommits(commits, []string{commits[1].SHA})
	if refusal != nil {
		t.Fatal(refusal.reason)
	}
	view := PullRequestView{Number: 7, Title: "feat: the change"}
	view.Head.Ref, view.Head.SHA, view.Base.Ref = "candidate", fixture.headSHA, "main"

	landed, head, refusal, err := rewriteBranchForKeptCommits(context.Background(), fixture.canonical,
		"acme/app", "candidate", fixture.baseSHA, plan, view, commits, "reviewer@example.test",
		"these two stand alone", []string{"sh", "-c", "exit 0"})
	if err != nil {
		t.Fatal(err)
	}
	if refusal != nil {
		t.Fatalf("rewrite refusal = %+v", refusal)
	}
	if head == "" || head == fixture.headSHA || len(landed) != 3 {
		t.Fatalf("rewrite head=%q landed=%+v", head, landed)
	}
	if !landed[0].Kept || landed[0].SourceSHA != commits[1].SHA {
		t.Fatalf("kept pairing = %+v", landed[0])
	}
	if landed[1].Kept || landed[2].Kept {
		t.Fatalf("aggregated pairings marked kept: %+v", landed)
	}
	published := strings.TrimSpace(runEngineGit(t, fixture.root,
		"--git-dir="+fixture.remote, "rev-parse", "refs/heads/candidate"))
	if published != head {
		t.Fatalf("published head = %q, want %q", published, head)
	}
	subjects := strings.TrimSpace(runEngineGit(t, fixture.root,
		"--git-dir="+fixture.remote, "log", "--format=%s", fixture.baseSHA+"..candidate"))
	lines := strings.Split(subjects, "\n")
	if len(lines) != 2 || lines[0] != "feat: the change" || lines[1] != "change b.txt" {
		t.Fatalf("landed subjects = %q", subjects)
	}
}

//nolint:paralleltest // calls a fixture helper (newLandFixture/newEngineFixture/createMergeSource) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestOrchCovRewriteBranchForKeptCommitsRefusesAStaleLease(t *testing.T) {
	fixture := newLandFixture(t, "candidate", "a.txt", "b.txt")
	commits := orchCovSourceCommits(t, fixture.canonical, fixture.baseSHA, fixture.headSHA)
	plan, refusal := planKeptCommits(commits, nil)
	if refusal != nil {
		t.Fatal(refusal.reason)
	}
	view := PullRequestView{Number: 7, Title: "feat: the change"}
	view.Head.Ref, view.Base.Ref = "candidate", "main"
	view.Head.SHA = strings.Repeat("b", 40)

	_, _, refusal, err := rewriteBranchForKeptCommits(context.Background(), fixture.canonical,
		"acme/app", "candidate", fixture.baseSHA, plan, view, commits, "", "", []string{"sh", "-c", "exit 0"})
	if err != nil {
		t.Fatal(err)
	}
	if refusal == nil || refusal.code != LandRefusalHeadMoved {
		t.Fatalf("stale lease refusal = %+v", refusal)
	}
	if !strings.Contains(refusal.command, "wb pr land acme/app#7") {
		t.Fatalf("stale lease command = %q", refusal.command)
	}
}

//nolint:paralleltest // calls a fixture helper (newLandFixture/newEngineFixture/createMergeSource) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestOrchCovRewriteBranchForKeptCommitsRefusesAConflictWithoutMovingTheBranch(t *testing.T) {
	fixture := newLandFixture(t, "candidate", "a.txt", "b.txt", "c.txt")
	// The base now carries its own a.txt, so the first kept commit cannot
	// replay. The kept commit is planned first, which is the path this proves.
	writeEngineFile(t, filepath.Join(fixture.canonical, "a.txt"), "base owns this file\n")
	runEngineGit(t, fixture.canonical, "add", "-A")
	runEngineGit(t, fixture.canonical, "commit", "-m", "base takes a.txt")
	advanced := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	commits := orchCovSourceCommits(t, fixture.canonical, fixture.baseSHA, fixture.headSHA)
	plan, planRefusal := planKeptCommits(commits, []string{commits[0].SHA})
	if planRefusal != nil {
		t.Fatal(planRefusal.reason)
	}
	if plan.steps[0].aggregate || plan.steps[0].sources[0].SHA != commits[0].SHA {
		t.Fatalf("plan does not lead with the kept commit: %+v", plan)
	}
	view := PullRequestView{Number: 7, Title: "feat: the change"}
	view.Head.Ref, view.Head.SHA, view.Base.Ref = "candidate", fixture.headSHA, "main"

	_, head, refusal, err := rewriteBranchForKeptCommits(context.Background(), fixture.canonical,
		"acme/app", "candidate", advanced, plan, view, commits, "", "", []string{"sh", "-c", "exit 0"})
	if err != nil {
		t.Fatal(err)
	}
	if refusal == nil || refusal.code != LandRefusalMergeRejected {
		t.Fatalf("conflicting kept commit refusal = %+v (head %q)", refusal, head)
	}
	if !strings.Contains(refusal.reason, "does not replay cleanly onto the base") {
		t.Fatalf("conflicting kept commit reason = %q", refusal.reason)
	}
	published := strings.TrimSpace(runEngineGit(t, fixture.root,
		"--git-dir="+fixture.remote, "rev-parse", "refs/heads/candidate"))
	if published != fixture.headSHA {
		t.Fatalf("a refused rewrite moved the published branch to %q", published)
	}
}

//nolint:paralleltest // calls a fixture helper (newLandFixture/newEngineFixture/createMergeSource) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestOrchCovRewriteBranchForKeptCommitsRefusesAnAggregateConflict(t *testing.T) {
	fixture := newLandFixture(t, "candidate", "a.txt", "b.txt")
	writeEngineFile(t, filepath.Join(fixture.canonical, "a.txt"), "base owns this file\n")
	runEngineGit(t, fixture.canonical, "add", "-A")
	runEngineGit(t, fixture.canonical, "commit", "-m", "base takes a.txt")
	advanced := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	commits := orchCovSourceCommits(t, fixture.canonical, fixture.baseSHA, fixture.headSHA)
	plan, planRefusal := planKeptCommits(commits, nil)
	if planRefusal != nil {
		t.Fatal(planRefusal.reason)
	}
	view := PullRequestView{Number: 7, Title: "feat: the change"}
	view.Head.Ref, view.Head.SHA, view.Base.Ref = "candidate", fixture.headSHA, "main"

	_, _, refusal, err := rewriteBranchForKeptCommits(context.Background(), fixture.canonical,
		"acme/app", "candidate", advanced, plan, view, commits, "", "", []string{"sh", "-c", "exit 0"})
	if err != nil {
		t.Fatal(err)
	}
	if refusal == nil || refusal.code != LandRefusalMergeRejected ||
		!strings.Contains(refusal.reason, "aggregated commits do not replay cleanly") {
		t.Fatalf("aggregate conflict refusal = %+v", refusal)
	}
}

//nolint:paralleltest // calls a fixture helper (newLandFixture/newEngineFixture/createMergeSource) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestOrchCovRewriteBranchForKeptCommitsRefusesAKeptCommitThatDoesNotBuild(t *testing.T) {
	fixture := newLandFixture(t, "candidate", "a.txt", "b.txt")
	commits := orchCovSourceCommits(t, fixture.canonical, fixture.baseSHA, fixture.headSHA)
	plan, planRefusal := planKeptCommits(commits, []string{commits[0].SHA})
	if planRefusal != nil {
		t.Fatal(planRefusal.reason)
	}
	view := PullRequestView{Number: 7, Title: "feat: the change"}
	view.Head.Ref, view.Head.SHA, view.Base.Ref = "candidate", fixture.headSHA, "main"

	_, _, refusal, err := rewriteBranchForKeptCommits(context.Background(), fixture.canonical,
		"acme/app", "candidate", fixture.baseSHA, plan, view, commits, "", "",
		[]string{"sh", "-c", "exit 7"})
	if err != nil {
		t.Fatal(err)
	}
	if refusal == nil || refusal.code != LandRefusalKeepDoesNotBuild {
		t.Fatalf("unbuildable kept commit refusal = %+v", refusal)
	}
	published := strings.TrimSpace(runEngineGit(t, fixture.root,
		"--git-dir="+fixture.remote, "rev-parse", "refs/heads/candidate"))
	if published != fixture.headSHA {
		t.Fatalf("a refused rewrite moved the published branch to %q", published)
	}
}

//nolint:paralleltest // calls a fixture helper (newLandFixture/newEngineFixture/createMergeSource) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestOrchCovLandKeepingCommitsRewritesThePublishedBranch(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "keep-task", "wb/keep/candidate", "candidate.txt", "one\n")
	writeEngineFile(t, filepath.Join(source.WorktreeDir, "second.txt"), "two\n")
	runEngineGit(t, source.WorktreeDir, "add", "second.txt")
	runEngineGit(t, source.WorktreeDir, "commit", "-m", "add second")
	runEngineGit(t, source.WorktreeDir, "push", "-u", "origin", "wb/keep/candidate")
	head := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	commits := orchCovSourceCommits(t, source.WorktreeDir, "main", "HEAD")
	if len(commits) != 2 {
		t.Fatalf("source commits = %+v", commits)
	}
	view := PullRequestView{Number: 7, Title: "feat: the change"}
	view.Head.Ref, view.Head.SHA, view.Base.Ref = "wb/keep/candidate", head, "main"
	options := PullRequestLandOptions{
		Repository: "acme/app", ProjectsRoot: fixture.githubDir,
		KeepCommits: []string{commits[0].SHA}, Reason: "the first commit stands alone",
		BuildCommand: []string{"sh", "-c", "exit 0"},
	}

	landed, rewritten, refusal, err := landKeepingCommits(context.Background(), options, view, commits, "7", "reviewer@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if refusal != nil {
		t.Fatalf("keep refusal = %+v", refusal)
	}
	if rewritten == "" || rewritten == head || len(landed) != 2 {
		t.Fatalf("rewritten head=%q landed=%+v", rewritten, landed)
	}
	var kept int
	for _, commit := range landed {
		if commit.Kept {
			kept++
		}
	}
	if kept != 1 {
		t.Fatalf("kept commits = %+v", landed)
	}
	published := strings.TrimSpace(runEngineGit(t, source.WorktreeDir,
		"--git-dir="+fixture.repository.CloneURL, "rev-parse", "refs/heads/wb/keep/candidate"))
	if published != rewritten {
		t.Fatalf("published head = %q, want %q", published, rewritten)
	}
	// The rewrite happens in a throwaway worktree: the task's own checkout
	// must still be where the agent left it.
	if still := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD")); still != head {
		t.Fatalf("the source worktree moved to %q, want %q", still, head)
	}
}
