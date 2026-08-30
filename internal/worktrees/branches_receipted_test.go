package worktrees

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// installReceiptFixture puts a deterministic fake gh on PATH that answers the
// commit-to-pull-request query for exactly one head SHA with one merged pull
// request, and an empty list for every other commit. It mirrors the payload
// GitHub's index returns: merged_at and merge_commit_sha are what the receipt
// path reads; branch names are deliberately irrelevant.
func installReceiptFixture(t *testing.T, head, landingSHA string) {
	t.Helper()
	binDir := t.TempDir()
	script := filepath.Join(binDir, "gh")
	content := "#!/bin/sh\nset -eu\n" +
		"if [ \"$1 $2\" != \"api --paginate\" ]; then echo \"unexpected gh command: $*\" >&2; exit 2; fi\n" +
		"case \"$3\" in\n" +
		"*\"$WB_TEST_RECEIPT_HEAD\"*) printf '%s\\n' \"$WB_TEST_RECEIPT_PULLS\";;\n" +
		"*) printf '[]\\n';;\n" +
		"esac\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := fmt.Sprintf(
		`[{"number":7,"html_url":"https://example.test/pull/7","state":"closed","merged_at":"2026-08-01T10:00:00Z","merge_commit_sha":%q,"head":{"ref":"whatever","sha":%q},"base":{"ref":"main","sha":""}}]`,
		landingSHA, head)
	t.Setenv("WB_TEST_RECEIPT_HEAD", head)
	t.Setenv("WB_TEST_RECEIPT_PULLS", payload)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// installPoisonedGitHubFixture makes every gh invocation fail loudly, so a
// test can prove either that no query happened or that a failure fails closed.
func installPoisonedGitHubFixture(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	script := filepath.Join(binDir, "gh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'poisoned gh was invoked' >&2\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// squashLandedFixture builds the shape that motivated receipts: a two-commit
// branch squash-merged into main, with main then moving on. No individual
// patch-id survives squashing, so patch evidence reports the branch unique
// even though every byte of it is in the target.
func squashLandedFixture(t *testing.T) (fixture *gitFixture, head, landingSHA string) {
	t.Helper()
	fixture = newGitFixture(t)

	gitTest(t, fixture.canonical, "checkout", "-b", "feature/landed")
	writeAndCommit(t, fixture.canonical, "landed-a.txt", "a\n", "landed work part one")
	head = writeAndCommit(t, fixture.canonical, "landed-b.txt", "b\n", "landed work part two")

	gitTest(t, fixture.canonical, "checkout", "main")
	gitTest(t, fixture.canonical, "merge", "--squash", "feature/landed")
	gitTest(t, fixture.canonical, "commit", "-m", "landed work (#7)")
	landingSHA = gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")

	// Main moves on so the branch tree no longer equals the target tree:
	// without this the branch would present as absorbed by tree equality
	// rather than as the unique-shaped case squashing actually produces.
	writeAndCommit(t, fixture.canonical, "later.txt", "later\n", "later work")
	gitTest(t, fixture.canonical, "push", "origin", "main")
	return fixture, head, landingSHA
}

func cleanupOptions(fixture *gitFixture, apply, receipts bool) BranchCleanupOptions {
	return BranchCleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Base: "main", Scope: "local",
		Apply: apply, Receipts: receipts, OlderThan: 0,
	}
}

func resultFor(t *testing.T, outcome BranchCleanupOutcome, branch string) BranchCleanupResult {
	t.Helper()
	for _, result := range outcome.Results {
		if result.Branch == branch {
			return result
		}
	}
	t.Fatalf("branch %s missing from results: %#v", branch, outcome.Results)
	return BranchCleanupResult{}
}

// Without --receipts nothing may query GitHub, and the branch keeps its
// patch-evidence disposition. The poisoned gh proves the absence of a query:
// any call would surface in the evidence or fail the run.
func TestBranchCleanupWithoutReceiptsNeverQueriesGitHub(t *testing.T) {
	fixture, _, _ := squashLandedFixture(t)
	installPoisonedGitHubFixture(t)

	outcome, err := BranchCleanup(context.Background(), cleanupOptions(fixture, false, false))
	if err != nil {
		t.Fatal(err)
	}
	result := resultFor(t, outcome, "feature/landed")
	if result.Disposition != BranchUnique {
		t.Fatalf("disposition = %q, want unique from patch evidence", result.Disposition)
	}
	if result.Eligible {
		t.Fatal("branch became eligible without --receipts")
	}
	if strings.Contains(result.Evidence, "receipt") {
		t.Fatalf("evidence mentions receipts without the flag: %q", result.Evidence)
	}
}

// The core of the feature: a squash-landed branch is proved receipted,
// becomes eligible, and --apply deletes its local ref.
func TestBranchCleanupReceiptedSquashLandingIsDeleted(t *testing.T) {
	fixture, head, landingSHA := squashLandedFixture(t)
	installReceiptFixture(t, head, landingSHA)

	plan, err := BranchCleanup(context.Background(), cleanupOptions(fixture, false, true))
	if err != nil {
		t.Fatal(err)
	}
	planned := resultFor(t, plan, "feature/landed")
	if planned.Disposition != BranchReceipted {
		t.Fatalf("disposition = %q (evidence %q), want receipted", planned.Disposition, planned.Evidence)
	}
	if !planned.Eligible {
		t.Fatalf("receipted branch not eligible: %q", planned.SkipReason)
	}
	if planned.LandingSHA != landingSHA {
		t.Fatalf("LandingSHA = %q, want the squash commit %q", planned.LandingSHA, landingSHA)
	}
	if !strings.Contains(planned.Reason, "#7") {
		t.Fatalf("reason does not name the pull request: %q", planned.Reason)
	}

	applied, err := BranchCleanup(context.Background(), cleanupOptions(fixture, true, true))
	if err != nil {
		t.Fatal(err)
	}
	result := resultFor(t, applied, "feature/landed")
	if !result.Applied || result.Outcome != "deleted" {
		t.Fatalf("outcome = %q applied=%v error=%q, want deleted", result.Outcome, result.Applied, result.Error)
	}
	if _, err := gitTestRun(fixture.canonical, "rev-parse", "--verify", "refs/heads/feature/landed"); err == nil {
		t.Fatal("local ref survived a deleted outcome")
	}
}

// Work that landed and was then reverted is the case absorbed-is-report-only
// exists for. The receipt's second proof — against the target, not just the
// landing commit — must refuse it, and the failure must name itself.
func TestBranchCleanupReceiptRefusesRevertedLanding(t *testing.T) {
	fixture, head, landingSHA := squashLandedFixture(t)
	gitTest(t, fixture.canonical, "revert", "--no-edit", landingSHA)
	gitTest(t, fixture.canonical, "push", "origin", "main")
	installReceiptFixture(t, head, landingSHA)

	outcome, err := BranchCleanup(context.Background(), cleanupOptions(fixture, true, true))
	if err != nil {
		t.Fatal(err)
	}
	result := resultFor(t, outcome, "feature/landed")
	if result.Disposition == BranchReceipted || result.Eligible || result.Applied {
		t.Fatalf("reverted landing was still deletable: %#v", result)
	}
	// A revert and later edits are indistinguishable by tree arithmetic, so
	// the refusal must say so and name the landing commit where the content
	// remains recoverable.
	if !strings.Contains(result.Evidence, "receipt:") || !strings.Contains(result.Evidence, "diverged") {
		t.Fatalf("evidence does not name the failing proof: %q", result.Evidence)
	}
	if !strings.Contains(result.Evidence, "recoverable at landing") {
		t.Fatalf("evidence does not point at the recoverable landing commit: %q", result.Evidence)
	}
	if _, err := gitTestRun(fixture.canonical, "rev-parse", "--verify", "refs/heads/feature/landed"); err != nil {
		t.Fatal("branch holding reverted work was deleted")
	}
}

// A receipt WB cannot obtain must fail closed with the failing check named —
// a branch never becomes eligible because a check could not be run.
func TestBranchCleanupReceiptQueryFailureFailsClosed(t *testing.T) {
	fixture, _, _ := squashLandedFixture(t)
	installPoisonedGitHubFixture(t)

	outcome, err := BranchCleanup(context.Background(), cleanupOptions(fixture, true, true))
	if err != nil {
		t.Fatal(err)
	}
	result := resultFor(t, outcome, "feature/landed")
	if result.Eligible || result.Applied {
		t.Fatalf("branch became eligible on a failed query: %#v", result)
	}
	if !strings.Contains(result.Evidence, "receipt: pull-request query failed") {
		t.Fatalf("evidence does not name the query failure: %q", result.Evidence)
	}
	if _, err := gitTestRun(fixture.canonical, "rev-parse", "--verify", "refs/heads/feature/landed"); err != nil {
		t.Fatal("branch was deleted despite unavailable receipt evidence")
	}
}

// Work reverted between plan and apply must refuse its own deletion at the
// apply-time recheck, exactly as a moved branch does.
func TestBranchCleanupReceiptRecheckRefusesRevertBetweenPlanAndApply(t *testing.T) {
	fixture, head, landingSHA := squashLandedFixture(t)
	installReceiptFixture(t, head, landingSHA)
	ctx := context.Background()

	options, err := normalizeBranchCleanupOptions(cleanupOptions(fixture, true, true))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	sweep := branchSweepOptions{
		ProjectsRoot: options.ProjectsRoot, Base: options.Base, Scope: options.Scope,
		OlderThan: options.OlderThan, Receipts: true, Now: now,
	}
	entries, _, paths, err := classifyFleetBranchesWithPaths(ctx, sweep)
	if err != nil {
		t.Fatal(err)
	}
	results := planBranchCleanup(entries, sweep)
	planned := false
	for _, result := range results {
		if result.Branch == "feature/landed" && result.Eligible {
			planned = true
		}
	}
	if !planned {
		t.Fatalf("fixture branch was not planned: %#v", results)
	}

	// The revert lands on origin between plan and apply.
	gitTest(t, fixture.canonical, "revert", "--no-edit", landingSHA)
	gitTest(t, fixture.canonical, "push", "origin", "main")

	applyBranchCleanup(ctx, results, paths, options, now)

	for _, result := range results {
		if result.Branch != "feature/landed" {
			continue
		}
		if result.Applied || result.Outcome != "failed" {
			t.Fatalf("recheck did not refuse the reverted landing: %#v", result)
		}
		if !strings.Contains(result.Error, "receipt") && !strings.Contains(result.Error, "landing") {
			t.Fatalf("recheck error does not explain itself: %q", result.Error)
		}
	}
	if _, err := gitTestRun(fixture.canonical, "rev-parse", "--verify", "refs/heads/feature/landed"); err != nil {
		t.Fatal("branch was deleted although its landing was reverted between plan and apply")
	}
}

// delegate-wb-owned-branches still holds: a checked-out branch is in-use and
// untouchable even with a valid receipt in hand.
func TestBranchCleanupInUseOutranksAReceipt(t *testing.T) {
	fixture, head, landingSHA := squashLandedFixture(t)
	installReceiptFixture(t, head, landingSHA)
	worktreeDir := filepath.Join(t.TempDir(), "landed-worktree")
	gitTest(t, fixture.canonical, "worktree", "add", worktreeDir, "feature/landed")

	outcome, err := BranchCleanup(context.Background(), cleanupOptions(fixture, true, true))
	if err != nil {
		t.Fatal(err)
	}
	result := resultFor(t, outcome, "feature/landed")
	if result.Disposition != BranchInUse || result.Eligible || result.Applied {
		t.Fatalf("in-use branch was not protected from receipts: %#v", result)
	}
}
