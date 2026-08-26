package worktrees

import (
	"context"
	"strings"
	"testing"
	"time"
)

// squashAbsorbedNoWorktreeFixture reproduces the exact shape reported against
// sneat-co/bookius (#182): several commits on a feature branch are
// squash-landed onto main as one commit, and the branch's own worktree and
// local branch are already gone — only the remote branch survives. GitHub
// never associates the landing with a pull request (a manual `git merge
// --squash` + commit, exactly as an operator retiring a batch by hand would
// do), so `--receipts` auto-discovery has nothing to find and only an
// explicit `--absorbed-by <commit>` pointer can prove the content is safe to
// retire.
func squashAbsorbedNoWorktreeFixture(t *testing.T) (fixture *gitFixture, head, landingSHA string) {
	t.Helper()
	fixture = newGitFixture(t)

	gitTest(t, fixture.canonical, "checkout", "-b", "codex/bookius-extraction")
	writeAndCommit(t, fixture.canonical, "extract-a.txt", "a\n", "extraction part one")
	writeAndCommit(t, fixture.canonical, "extract-b.txt", "b\n", "extraction part two")
	head = writeAndCommit(t, fixture.canonical, "extract-c.txt", "c\n", "extraction part three")
	gitTest(t, fixture.canonical, "push", "origin", "codex/bookius-extraction")

	gitTest(t, fixture.canonical, "checkout", "main")
	gitTest(t, fixture.canonical, "merge", "--squash", "codex/bookius-extraction")
	gitTest(t, fixture.canonical, "commit", "-m", "bookius extraction (squash)")
	landingSHA = gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")

	// main moves on so the branch tree no longer equals the target tree,
	// exactly the shape that presents as unique rather than absorbed by tree
	// equality — see squashLandedFixture in branches_receipted_test.go.
	writeAndCommit(t, fixture.canonical, "later.txt", "later\n", "later work")
	gitTest(t, fixture.canonical, "push", "origin", "main")

	// The worktree and local branch are already gone; only the remote branch
	// survives. This mirrors the bug report exactly: wb worktree cleanup
	// --absorbed-by is unavailable because there is no worktree left.
	gitTest(t, fixture.canonical, "branch", "-D", "codex/bookius-extraction")
	return fixture, head, landingSHA
}

func absorbedByCleanupOptions(fixture *gitFixture, scope string, apply bool, absorbedBy string) BranchCleanupOptions {
	return BranchCleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Base: "main", Scope: scope,
		Apply: apply, AbsorbedBy: absorbedBy, OlderThan: 0,
	}
}

// TestBranchCleanupAbsorbedByRetiresContentProvenSquashAbsorbedRemoteBranch is
// the #182 acceptance scenario: a remote-only, content-identical
// squash-absorbed branch is retired with a recorded receipt via an explicit
// --absorbed-by commit, and dry-run and apply agree on the same evidence.
func TestBranchCleanupAbsorbedByRetiresContentProvenSquashAbsorbedRemoteBranch(t *testing.T) {
	fixture, _, landingSHA := squashAbsorbedNoWorktreeFixture(t)
	// A bare commit pointer never needs GitHub for the attested-absorption
	// proof itself; --scope remote's separate open-pull-request gate still
	// queries it, so the fixture answers with an empty pull-request list
	// rather than poisoning gh outright.
	installMergedPullRequestFixtures(t, nil, time.Time{})

	plan, err := BranchCleanup(context.Background(), absorbedByCleanupOptions(fixture, BranchScopeRemote, false, landingSHA))
	if err != nil {
		t.Fatal(err)
	}
	planned := resultFor(t, plan, "codex/bookius-extraction")
	if planned.Disposition != BranchReceipted {
		t.Fatalf("disposition = %q (evidence %q), want receipted", planned.Disposition, planned.Evidence)
	}
	if !planned.Eligible {
		t.Fatalf("attested branch not eligible: %q", planned.SkipReason)
	}
	if planned.LandingSHA != landingSHA {
		t.Fatalf("LandingSHA = %q, want the squash commit %q", planned.LandingSHA, landingSHA)
	}
	if !strings.Contains(planned.Evidence, "--absorbed-by") {
		t.Fatalf("evidence does not name the --absorbed-by proof: %q", planned.Evidence)
	}

	applied, err := BranchCleanup(context.Background(), absorbedByCleanupOptions(fixture, BranchScopeRemote, true, landingSHA))
	if err != nil {
		t.Fatal(err)
	}
	result := resultFor(t, applied, "codex/bookius-extraction")
	if !result.Applied || result.Outcome != "deleted" {
		t.Fatalf("outcome = %q applied=%v error=%q, want deleted", result.Outcome, result.Applied, result.Error)
	}
	if remoteBranchForTest(t, fixture.canonical, "codex/bookius-extraction") != "" {
		t.Fatal("remote branch survived a deleted outcome")
	}
}

// partialLandingFixture builds a branch with two commits where only the first
// landed — the "unknown path in the branch not present in the absorbing
// commit" shape #182 requires WB to refuse.
func partialLandingFixture(t *testing.T) (fixture *gitFixture, branchHead, landingSHA string) {
	t.Helper()
	fixture = newGitFixture(t)

	gitTest(t, fixture.canonical, "checkout", "-b", "feature/partial")
	landedCommit := writeAndCommit(t, fixture.canonical, "landed.txt", "landed\n", "part that landed")
	branchHead = writeAndCommit(t, fixture.canonical, "stranded.txt", "stranded\n", "part that did not land")
	gitTest(t, fixture.canonical, "push", "origin", "feature/partial")

	gitTest(t, fixture.canonical, "checkout", "main")
	gitTest(t, fixture.canonical, "cherry-pick", landedCommit)
	landingSHA = gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	gitTest(t, fixture.canonical, "push", "origin", "main")
	return fixture, branchHead, landingSHA
}

// TestBranchCleanupAbsorbedByRefusesResidualNotPresentInLandingCommit proves
// the fail-closed half of the acceptance criteria: a branch carrying content
// the named landing commit never received must never be deleted, and the
// refusal must name the failing proof rather than silently reclassifying the
// branch.
func TestBranchCleanupAbsorbedByRefusesResidualNotPresentInLandingCommit(t *testing.T) {
	fixture, _, landingSHA := partialLandingFixture(t)
	installPoisonedGitHubFixture(t)

	outcome, err := BranchCleanup(context.Background(), absorbedByCleanupOptions(fixture, BranchScopeLocal, true, landingSHA))
	if err != nil {
		t.Fatal(err)
	}
	result := resultFor(t, outcome, "feature/partial")
	if result.Eligible || result.Applied {
		t.Fatalf("branch with an unlanded residual became eligible: %#v", result)
	}
	if result.Disposition == BranchReceipted {
		t.Fatalf("residual branch was reclassified receipted: %#v", result)
	}
	if !strings.Contains(result.AbsorbedByRejection, "does not contain this branch's content") {
		t.Fatalf("rejection does not name the missing residual: %q", result.AbsorbedByRejection)
	}
	if !strings.Contains(result.Evidence, "--absorbed-by:") {
		t.Fatalf("evidence does not surface the failing --absorbed-by check: %q", result.Evidence)
	}
	if !gitRefExists(fixture.canonical, "refs/heads/feature/partial") {
		t.Fatal("branch with an unlanded residual was deleted")
	}
}

// TestBranchCleanupAbsorbedByRefusesRefDrift proves the target-drift half of
// the acceptance criteria: once the named landing commit no longer survives
// in the freshly fetched exact target (here, because the target was rewound
// past it), the pointer must refuse rather than fall back to any weaker
// evidence.
func TestBranchCleanupAbsorbedByRefusesRefDrift(t *testing.T) {
	fixture, _, landingSHA := squashAbsorbedNoWorktreeFixture(t)
	installPoisonedGitHubFixture(t)

	// Rewind the exact origin target so the landing commit itself never
	// reached it — a force-push that dropped the whole batch.
	gitTest(t, fixture.canonical, "push", "--force", "origin", landingSHA+"^:refs/heads/main")

	outcome, err := BranchCleanup(context.Background(), absorbedByCleanupOptions(fixture, BranchScopeRemote, true, landingSHA))
	if err != nil {
		t.Fatal(err)
	}
	result := resultFor(t, outcome, "codex/bookius-extraction")
	if result.Eligible || result.Applied {
		t.Fatalf("branch survived only by drifted target evidence: %#v", result)
	}
	if !strings.Contains(result.AbsorbedByRejection, "not contained in the exact fetched origin/main target") {
		t.Fatalf("rejection does not name the target drift: %q", result.AbsorbedByRejection)
	}
	if remoteBranchForTest(t, fixture.canonical, "codex/bookius-extraction") == "" {
		t.Fatal("remote branch was deleted despite target drift")
	}
}

// TestBranchCleanupAbsorbedByRefusesPointerThatIsNotTheExactEntryPoint proves
// the extra guard --absorbed-by needs beyond plain containment: naming a
// commit strictly downstream of the real landing point — one that already
// contained the work before it was even authored — must refuse, or the flag
// would degrade into a bare content assertion an operator could satisfy by
// naming the target tip.
func TestBranchCleanupAbsorbedByRefusesPointerThatIsNotTheExactEntryPoint(t *testing.T) {
	fixture, _, _ := squashAbsorbedNoWorktreeFixture(t)
	installPoisonedGitHubFixture(t)

	// squashAbsorbedNoWorktreeFixture already pushed one commit ("later work")
	// after the squash landed; its SHA already contains the work in its own
	// first parent, so naming it is not a landing receipt.
	notEntryPoint := gitTestOutput(t, fixture.canonical, "rev-parse", "origin/main")

	outcome, err := BranchCleanup(context.Background(), absorbedByCleanupOptions(fixture, BranchScopeRemote, true, notEntryPoint))
	if err != nil {
		t.Fatal(err)
	}
	result := resultFor(t, outcome, "codex/bookius-extraction")
	if result.Eligible || result.Applied {
		t.Fatalf("non-entry-point pointer was accepted: %#v", result)
	}
	if !strings.Contains(result.AbsorbedByRejection, "not where this work entered the target") {
		t.Fatalf("rejection does not name the entry-point failure: %q", result.AbsorbedByRejection)
	}
	if remoteBranchForTest(t, fixture.canonical, "codex/bookius-extraction") == "" {
		t.Fatal("remote branch was deleted despite a non-entry-point pointer")
	}
}

// TestBranchCleanupAbsorbedByUnrelatedPointerNeverRescuesAnUnrelatedBranch
// extends the #req:absorbed-is-report-only regression
// (TestBranchCleanupNeverDeletesAbsorbedUnderAnyFlagCombination) to the new
// flag: an --absorbed-by pointer that has nothing to do with a given branch
// must never act as a global bypass for it. The branch here landed and was
// then reverted — exactly the case the absorbed disposition exists to
// protect — and an unrelated, otherwise-valid landing commit for a different
// batch of work must not rescue it.
func TestBranchCleanupAbsorbedByUnrelatedPointerNeverRescuesAnUnrelatedBranch(t *testing.T) {
	fixture := newGitFixture(t)
	installPoisonedGitHubFixture(t)

	gitTest(t, fixture.canonical, "checkout", "-b", "feature/landed-then-reverted")
	sourceSHA := writeAndCommit(t, fixture.canonical, "reverted.txt", "v1\n", "work that will be reverted")
	gitTest(t, fixture.canonical, "checkout", "main")
	landedSHA := cherryPickWithDifferentMessage(t, fixture.canonical, sourceSHA, "landed: work that will be reverted")
	gitTest(t, fixture.canonical, "revert", "--no-edit", landedSHA)
	// A real, unrelated commit elsewhere in the same history: it resolves
	// fine and is contained in the target, but its content has nothing to do
	// with the branch under test.
	unrelatedSHA := writeAndCommit(t, fixture.canonical, "unrelated.txt", "unrelated\n", "unrelated later work")
	gitTest(t, fixture.canonical, "push", "origin", "main", "feature/landed-then-reverted")

	outcome, err := BranchCleanup(context.Background(), absorbedByCleanupOptions(fixture, BranchScopeLocal, true, unrelatedSHA))
	if err != nil {
		t.Fatal(err)
	}
	result := resultFor(t, outcome, "feature/landed-then-reverted")
	if result.Disposition != BranchAbsorbed {
		t.Fatalf("disposition = %q, want absorbed (patch-id evidence unchanged)", result.Disposition)
	}
	if result.Eligible || result.Applied {
		t.Fatalf("unrelated --absorbed-by pointer rescued an absorbed branch: %#v", result)
	}
	if result.AbsorbedByRejection == "" {
		t.Fatal("unrelated pointer produced no visible rejection")
	}
	if !gitRefExists(fixture.canonical, "refs/heads/feature/landed-then-reverted") {
		t.Fatal("absorbed branch was deleted under an unrelated --absorbed-by pointer")
	}
}
