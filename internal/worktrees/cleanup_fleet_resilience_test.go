package worktrees

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// An explicit batch is a different question from one named task. One task that cannot
// be corroborated at preflight says nothing about the tasks already proven
// eligible beside it, and each of those cost a `git fetch` to prove. This is the
// regression for a live named cleanup batch that verified ninety-nine
// eligible tasks, applied ten, and then threw the remaining eighty-nine away
// because a single worktree could not be corroborated.
//
// CleanupOutcome already documents the intended contract — a bad candidate
// "blocks eligibility only for its own coordinated task ... Every other task in
// the run proceeds normally" — and the task loop contradicted it.
//
// The original sweep was broken by a live branch its claim did not name; that
// specific refusal is gone (see
// TestCleanupRetiresATaskWhoseBranchWasRenamedAfterItsClaim), so the sibling
// here is broken the way a worktree can still genuinely fail preflight: its
// projection names a claim that does not exist.
func TestCleanupNamedTasksKeepSweepingWhenOneTaskCannotBeCorroborated(t *testing.T) {
	fixture := newGitFixture(t)
	healthy, healthyHead, mergedAt := prepareMergedTaskInFixture(t, fixture, "cleanup-healthy")
	broken, brokenHead, _ := prepareMergedTaskInFixture(t, fixture, "cleanup-broken")
	installMergedPullRequestFixtures(t, []string{healthyHead, brokenHead}, mergedAt)

	breakWorkLogCorroboration(t, broken.WorktreeDir)

	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Tasks:        []string{"cleanup-healthy", "cleanup-broken"},
		Apply:        true,
		Now:          func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatalf("one uncorroborated task aborted the whole sweep: %v", err)
	}
	if !cleanupApplied(outcome, "cleanup-healthy") {
		t.Fatalf("an uncorroborated sibling stopped a healthy task being cleaned: %#v", outcome.Results)
	}
	if cleanupApplied(outcome, "cleanup-broken") {
		t.Fatal("an uncorroborated task must never be applied")
	}
	if !cleanupReported(outcome, "cleanup-broken") {
		t.Fatalf("an uncorroborated task must be reported, not silently skipped: %#v", outcome.Diagnostics)
	}
	if _, statErr := os.Stat(healthy.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("the healthy task's worktree survived a sweep that reported it applied: %v", statErr)
	}
}

// A named task remains the operator's exact subject, so its failure is still
// the answer to what they asked and must still end the command.
func TestCleanupNamedTaskStillFailsWhenItCannotBeCorroborated(t *testing.T) {
	fixture := newGitFixture(t)
	broken, brokenHead, mergedAt := prepareMergedTaskInFixture(t, fixture, "cleanup-broken")
	installMergedPullRequestFixtures(t, []string{brokenHead}, mergedAt)
	breakWorkLogCorroboration(t, broken.WorktreeDir)

	_, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "cleanup-broken",
		Apply:        true,
		DeleteRemote: true,
		Now:          func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err == nil {
		t.Fatal("a named task that cannot be corroborated must fail the command")
	}
}

func TestCleanupNamedTaskDryRunRejectsUncorroboratedClaim(t *testing.T) {
	fixture := newGitFixture(t)
	broken, brokenHead, mergedAt := prepareMergedTaskInFixture(t, fixture, "cleanup-broken")
	installMergedPullRequestFixtures(t, []string{brokenHead}, mergedAt)
	breakWorkLogCorroboration(t, broken.WorktreeDir)

	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "cleanup-broken",
		Now:          func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatalf("plan cleanup for an uncorroborated named task: %v", err)
	}
	if len(outcome.Results) != 1 {
		t.Fatalf("cleanup plan results = %d, want 1: %#v", len(outcome.Results), outcome.Results)
	}
	result := outcome.Results[0]
	if result.Eligible {
		t.Fatalf("dry-run cleanup marked an uncorroborated claim eligible: %#v", result)
	}
	// The reason must name the repository the preflight was run for and the
	// claim WB could not read, so an operator can find the record rather than
	// re-derive which of a batch's worktrees is broken.
	for _, want := range []string{"preflight Work Log for acme/app", "claim"} {
		if !strings.Contains(result.Reason, want) {
			t.Fatalf("dry-run cleanup reason = %q, want it to contain %q", result.Reason, want)
		}
	}
	if _, statErr := os.Stat(broken.WorktreeDir); statErr != nil {
		t.Fatalf("a dry run touched the worktree it refused: %v", statErr)
	}
}

// TestCleanupRetiresATaskWhoseBranchWasRenamedAfterItsClaim is the other half of
// the contract the three tests above used to encode. WB used to refuse a
// worktree whose live branch no longer matched the name in its Work Log claim,
// with a message that admitted the contradiction in its own text — it said
// landing evidence is commit-based and then refused on a name. On this fleet
// that stranded tasks whose work had already landed and whose branch was gone
// from origin, and asked the operator to `git branch -m` a name back purely as
// ceremony.
//
// The commit checks are the whole proof and they still all run: the live head
// must be exactly the terminal commit and must descend from the claimed base.
// The rename survives as a note on the receipt, because the branch WB deletes
// is the live one and the operator is owed both names.
//
// Implements: dependency-streams#req:landing-evidence-is-commit-based-not-name-based.
func TestCleanupRetiresATaskWhoseBranchWasRenamedAfterItsClaim(t *testing.T) {
	fixture := newGitFixture(t)
	renamed, head, mergedAt := prepareMergedTaskInFixture(t, fixture, "cleanup-renamed")
	installMergedPullRequestFixtures(t, []string{head}, mergedAt)
	claimBranch := renamed.Branch
	gitTest(t, renamed.WorktreeDir, "branch", "-m", "renovate/renamed-after-the-claim")

	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "cleanup-renamed",
		Apply:        true,
		Now:          func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatalf("a renamed live branch refused a landed task: %v", err)
	}
	if !cleanupApplied(outcome, "cleanup-renamed") {
		t.Fatalf("a renamed live branch stranded a landed task: %#v", outcome.Results)
	}
	if _, statErr := os.Stat(renamed.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("the retired worktree survived: %v", statErr)
	}
	var notes []string
	for _, result := range outcome.Results {
		if result.Task == "cleanup-renamed" {
			notes = append(notes, result.Notes...)
		}
	}
	joined := strings.Join(notes, "\n")
	for _, want := range []string{"renovate/renamed-after-the-claim", claimBranch} {
		if !strings.Contains(joined, want) {
			t.Fatalf("receipt notes = %q, want them to name %q", joined, want)
		}
	}
}

// breakWorkLogCorroboration points a worktree's Work Log projection at a claim
// that does not exist. It is the surviving shape of "this worktree cannot be
// corroborated": every identity in the chain is well-formed, and the private
// record the projection promises is simply not there.
func breakWorkLogCorroboration(t *testing.T, worktree string) {
	t.Helper()
	projection, err := readWorkLogProjection(worktree)
	if err != nil {
		t.Fatal(err)
	}
	// Stay a well-formed claim ID (validClaimID wants 64 hex characters) so the
	// projection itself is valid and the failure is the missing private record.
	last := projection.ClaimID[len(projection.ClaimID)-1]
	replacement := byte('a')
	if last == replacement {
		replacement = 'b'
	}
	projection.ClaimID = projection.ClaimID[:len(projection.ClaimID)-1] + string(replacement)
	contents, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(worktree, workLogProjectionDirectory, workLogProjectionName)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func cleanupApplied(outcome CleanupOutcome, task string) bool {
	for _, result := range outcome.Results {
		if result.Task == task && result.Applied {
			return true
		}
	}
	return false
}

func cleanupReported(outcome CleanupOutcome, task string) bool {
	for _, diagnostic := range outcome.Diagnostics {
		if diagnostic.Task == task {
			return true
		}
	}
	for _, result := range outcome.Results {
		if result.Task == task && strings.TrimSpace(result.Reason) != "" {
			return true
		}
	}
	return false
}
