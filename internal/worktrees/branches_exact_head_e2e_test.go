//go:build e2e

package worktrees

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktreebranches"
)

// divergedExactHeadFixture is the datatug/websites shape: a branch
// squash-merged from its exact tip, after which main edited the same files, so
// the target no longer carries the branch's bytes and the plain receipt proof
// reports "diverged".
func divergedExactHeadFixture(t *testing.T) (fixture *gitFixture, head, landingSHA string) {
	t.Helper()
	fixture, head, landingSHA = squashLandedFixture(t)
	writeAndCommit(t, fixture.canonical, "landed-a.txt", "a, edited after the landing\n", "later edit of landed work")
	gitTest(t, fixture.canonical, "push", "origin", "main")
	return fixture, head, landingSHA
}

//nolint:paralleltest // the git fixture pins HOME and WB_PROJECTS_ROOT with t.Setenv.
func TestE2EBranchCleanupRetiresASquashMergedExactHeadAfterTheTargetMovedOn(t *testing.T) {
	fixture, head, landingSHA := divergedExactHeadFixture(t)
	installReceiptFixture(t, head, landingSHA)

	options := cleanupOptions(fixture, false, true)
	options.Only = CleanOnlyLanded
	plan, err := BranchCleanup(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	result := resultFor(t, plan, "feature/landed")
	if result.Disposition != BranchReceipted || !result.ExactHeadReceipt || !result.Eligible || !strings.Contains(result.Evidence, "exact head") {
		t.Fatalf("exact-head squash landing was not retirable: %#v", result)
	}
	options.Apply = true
	applied, err := BranchCleanup(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result := resultFor(t, applied, "feature/landed"); !result.Applied {
		t.Fatalf("exact-head receipt was not applied: %#v", result)
	}
	if _, err := gitTestRun(fixture.canonical, "rev-parse", "--verify", "refs/heads/feature/landed"); err == nil {
		t.Fatal("the retired branch still exists")
	}
}

//nolint:paralleltest // the git fixture pins HOME and WB_PROJECTS_ROOT with t.Setenv.
func TestE2EBranchCleanupOnlyNarrowsThePlan(t *testing.T) {
	fixture, head, landingSHA := divergedExactHeadFixture(t)
	installReceiptFixture(t, head, landingSHA)
	options := cleanupOptions(fixture, false, true)
	options.Only = BranchUnique
	outcome, err := BranchCleanup(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range outcome.Results {
		if result.Disposition != BranchUnique {
			t.Fatalf("--only unique kept %#v", result)
		}
	}
}

//nolint:paralleltest // the git fixture pins HOME and WB_PROJECTS_ROOT with t.Setenv.
func TestE2EBranchCleanupExactHeadRecheckRefusesARevertOrAMovedRef(t *testing.T) {
	for _, change := range []string{"revert", "moved", "restored"} {
		//nolint:paralleltest // each subcase builds its own process-wide Git/WB fixture environment.
		t.Run(change, func(t *testing.T) {
			fixture, head, landingSHA := divergedExactHeadFixture(t)
			installReceiptFixture(t, head, landingSHA)
			ctx := context.Background()
			options, err := normalizeBranchCleanupOptions(cleanupOptions(fixture, true, true))
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			sweep := branchSweepOptions{ProjectsRoot: options.ProjectsRoot, Base: options.Base, Scope: options.Scope, Receipts: true, Now: now}
			entries, _, paths, err := classifyFleetBranchesWithPaths(ctx, sweep)
			if err != nil {
				t.Fatal(err)
			}
			results := worktreebranches.PlanBranchCleanup(entries, sweep.branchPolicyOptions())
			switch change {
			case "revert":
				// The later edit makes a mechanical revert conflict, so the
				// operator resolved it by hand; Git's message still names it.
				gitTest(t, fixture.canonical, "commit", "-q", "--allow-empty", "-m", "Revert landed work", "-m", "This reverts commit "+landingSHA+".")
			case "restored":
				// Every landed path back to its pre-landing state, without
				// Git's revert message.
				gitTest(t, fixture.canonical, "rm", "-q", "landed-a.txt", "landed-b.txt")
				gitTest(t, fixture.canonical, "commit", "-q", "-m", "drop landed work")
			case "moved":
				gitTest(t, fixture.canonical, "checkout", "-q", "feature/landed")
				writeAndCommit(t, fixture.canonical, "after.txt", "after\n", "committed after the merge")
				gitTest(t, fixture.canonical, "checkout", "-q", "main")
			}
			gitTest(t, fixture.canonical, "push", "origin", "main")
			applyBranchCleanup(ctx, results, paths, options, now)
			for _, result := range results {
				if result.Branch == "feature/landed" && (result.Applied || result.Outcome != "failed") {
					t.Fatalf("recheck did not refuse after %s: %#v", change, result)
				}
			}
			if _, err := gitTestRun(fixture.canonical, "rev-parse", "--verify", "refs/heads/feature/landed"); err != nil {
				t.Fatalf("branch deleted after %s", change)
			}
		})
	}
}

//nolint:paralleltest // the git fixture pins HOME and WB_PROJECTS_ROOT with t.Setenv.
func TestE2ELandingRevertedReportsGitFailures(t *testing.T) {
	fixture, _, landingSHA := divergedExactHeadFixture(t)
	missing := strings.Repeat("f", 40)
	if _, err := landingReverted(context.Background(), fixture.canonical, missing, "main"); err == nil {
		t.Fatal("an unknown landing must fail the revert check")
	}
	root := gitTestOutput(t, fixture.canonical, "rev-list", "--max-parents=0", "HEAD")
	if _, err := landingReverted(context.Background(), fixture.canonical, root, "main"); err == nil {
		t.Fatal("a parentless landing must fail the revert check")
	}
	empty := gitTestOutput(t, fixture.canonical, "commit-tree", "-p", landingSHA, "-m", "empty", landingSHA+"^{tree}")
	if reverted, err := landingReverted(context.Background(), fixture.canonical, empty, "main"); err != nil || reverted {
		t.Fatalf("a landing that changed nothing is not reverted: %v %v", reverted, err)
	}
	if _, err := landingReverted(context.Background(), fixture.canonical, landingSHA, missing); err == nil {
		t.Fatal("an unknown target must fail the revert check")
	}
}

//nolint:paralleltest // the git fixture pins HOME and WB_PROJECTS_ROOT with t.Setenv.
func TestE2ERecheckExactHeadReceiptFailsClosedOnEveryMissingProof(t *testing.T) {
	fixture, head, landingSHA := divergedExactHeadFixture(t)
	ctx := context.Background()
	target := gitTestOutput(t, fixture.canonical, "rev-parse", "main")
	initial := gitTestOutput(t, fixture.canonical, "rev-list", "--max-parents=0", "HEAD")
	missing := strings.Repeat("f", 40)
	for name, tc := range map[string]struct {
		pr      *PullRequest
		landing string
		target  string
		want    string
	}{
		"no pull request":            {nil, landingSHA, target, "no longer the exact head"},
		"another head":               {&PullRequest{HeadSHA: initial}, landingSHA, target, "no longer the exact head"},
		"landing lacks the work":     {&PullRequest{HeadSHA: head}, initial, target, "no longer carries"},
		"landing unreadable":         {&PullRequest{HeadSHA: head}, missing, target, "no longer carries"},
		"revert check unreadable":    {&PullRequest{HeadSHA: head}, landingSHA, missing, "refusing: git"},
		"still landed, not reverted": {&PullRequest{HeadSHA: head}, landingSHA, target, ""},
	} {
		//nolint:paralleltest // subcase shares the parent's process-wide Git/WB fixture environment.
		t.Run(name, func(t *testing.T) {
			result := &BranchCleanupResult{BranchEntry: BranchEntry{LandingSHA: tc.landing, ReceiptPullRequest: tc.pr}}
			ok := recheckExactHeadReceipt(ctx, fixture.canonical, head, tc.target, result)
			if tc.want == "" {
				if !ok {
					t.Fatalf("a valid exact-head receipt was refused: %q", result.Error)
				}
				return
			}
			if ok || result.Outcome != "failed" || !strings.Contains(result.Error, tc.want) {
				t.Fatalf("got ok=%v %#v", ok, result)
			}
		})
	}
}
