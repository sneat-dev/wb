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
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// #586: a file review naming a head, followed by ONLY a WB-produced
// update-branch merge (the target advances and WB brings the candidate up
// to date), still lands: the advance is proved, not a foreign change.
//
//nolint:paralleltest // calls a fixture helper (newLandFixture) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestE2ELandFileReviewSurvivesOnlyAWBUpdateBranchMerge(t *testing.T) {
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

//nolint:paralleltest // The existing review parent/proof hooks are process-global and restored after this native witness.
func TestE2EReviewHeadAdvanceChainBoundsNativeProvenMerges(t *testing.T) {
	fixture := newExplicitRootEngineFixture(t)
	writeEngineFile(t, fixture.canonical+"/candidate.txt", "reviewed candidate\n")
	runEngineGit(t, fixture.canonical, "add", "candidate.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: reviewed candidate")
	reviewed := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, fixture.canonical, "checkout", "main")
	// Leave main at the seed while the candidate is a separate first parent.
	runEngineGit(t, fixture.canonical, "reset", "--hard", "HEAD^")
	head := reviewed
	heads := make([]string, 0, maxReviewAdvanceHops+1)
	for hop := 0; hop <= maxReviewAdvanceHops; hop++ {
		writeEngineFile(t, fixture.canonical+"/target.txt", fmt.Sprintf("target step %d\n", hop))
		runEngineGit(t, fixture.canonical, "add", "target.txt")
		runEngineGit(t, fixture.canonical, "commit", "-m", fmt.Sprintf("test: target advance %d", hop))
		targetParent := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
		tree := strings.TrimSpace(runEngineGit(t, fixture.canonical, "merge-tree", "--write-tree", head, targetParent))
		head = strings.TrimSpace(runEngineGit(t, fixture.canonical, "commit-tree", tree, "-p", head, "-p", targetParent, "-m", fmt.Sprintf("test: update-branch merge %d", hop)))
		heads = append(heads, head)
	}
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	previousParents, previousProof := reviewCommitParents, reviewHeadAdvanceProof
	t.Cleanup(func() { reviewCommitParents, reviewHeadAdvanceProof = previousParents, previousProof })
	parentsRead, proofs := 0, 0
	reviewCommitParents = func(ctx context.Context, repository, sha string) ([]string, error) {
		want := heads[len(heads)-1-parentsRead]
		if repository != "acme/app" || sha != want {
			t.Fatalf("parent read %d=%s/%s want %s", parentsRead, repository, sha, want)
		}
		parentsRead++
		fields := strings.Fields(runEngineGit(t, fixture.canonical, "rev-list", "--parents", "-n", "1", sha))
		if len(fields) != 3 || fields[0] != sha {
			t.Fatalf("native merge parents=%v", fields)
		}
		return fields[1:], nil
	}
	reviewHeadAdvanceProof = func(ctx context.Context, git Git, run runner.Runner, worktree, branch, target, repository, candidateSHA, targetParent, headSHA string) (bool, error) {
		if headSHA != heads[len(heads)-1-proofs] || parentsRead != proofs+1 {
			t.Fatalf("proof order %d head=%s reads=%d", proofs, headSHA, parentsRead)
		}
		proven, err := verifyUpdateBranchMergeProof(ctx, git, run, worktree, branch, target, repository, candidateSHA, targetParent, headSHA)
		if !proven || err != nil {
			t.Fatalf("actual native update-branch proof failed: %v/%v", proven, err)
		}
		proofs++
		return proven, err
	}
	advanced, unverifiable, cause := reviewedHeadAdvanceChain(t.Context(), defaultGit, defaultRunner, fixture.canonical, "feature", "acme/app", "main", reviewed, head)
	if advanced || unverifiable || cause != "" || parentsRead != maxReviewAdvanceHops || proofs != maxReviewAdvanceHops {
		t.Fatalf("bounded proof=%v/%v/%q reads=%d proofs=%d", advanced, unverifiable, cause, parentsRead, proofs)
	}
}
