//go:build e2e

// This file holds pr_land_review_test.go's real-git case
// (spec/plans/coverage-to-100 task-17): its own doc comment already said
// the review-stale proof "needs a real local checkout... to run git
// merge-tree/ancestor checks against" -- that proof now resolves through
// PullRequestLandOptions' git seam (pr_land.go), which defaults to
// production's real gitcli/runner adapters and so runs through task-24's
// guarded runner, blocked outside the e2e tier. Moving it here, rather than
// calling runnertest.AllowRealProcess in the default tier, keeps
// internal/quality/testdata/unit_tier.pending's cross-PR total from rising:
// pr_land_review_test.go carries zero pending entries.
package orchestrate

import (
	"context"
	"os"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// #586: a file review naming a head, followed by ONLY a WB-produced
// update-branch merge (the target advances and WB brings the candidate up
// to date), still lands: the advance is proved, not a foreign change.
//
//nolint:paralleltest // calls a fixture helper (newLandFixture) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestLandFileReviewSurvivesOnlyAWBUpdateBranchMerge(t *testing.T) {
	// "feature" (no slash) matches the fixture's fake update-branch script,
	// which republishes the merge onto a hardcoded "refs/heads/feature"
	// when no "head-ref" state override is written (see pr_land_test.go's
	// update-branch case) — the same branch name
	// TestLandUpdatesABehindCandidateInsteadOfRefusing and friends use.
	fixture := newLandFixture(t, "feature", "main.go")
	// The review-stale proof needs a real local checkout of the branch to
	// run `git merge-tree`/ancestor checks against — the same requirement
	// --keep-commits already has (locateBranchCheckout). A real linked
	// worktree, not just the bare canonical push newLandFixture leaves on
	// main, is what makes it findable.
	if _, err := worktrees.Create(context.Background(), []string{"acme/app"}, worktrees.CreateOptions{
		ProjectsRoot: fixture.projects, Operation: "file-update-branch",
		Branch: "feature", BranchChosen: true, Resume: true,
		WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := dir + "/review.md"
	contents := "looks good\n\nReviewed-Head: " + fixture.headSHA + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	advanceLandTarget(t, fixture)

	options := landOptions(fixture)
	options.ApprovedBy = path
	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if !fixtureHasMarker(fixture, "update-branch") {
		t.Fatal("expected the candidate to have been brought up to date via update-branch")
	}
	if !reviewBound(result) {
		t.Fatal("the review must still be recorded as bound")
	}
}
