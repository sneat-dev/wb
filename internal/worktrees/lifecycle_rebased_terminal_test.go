package worktrees

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// prepareFinalizedThenRebasedTask reproduces S63's exact stuck shape (the VM
// example: datatug-cli's phase1-core-module-dependency worktree, PR #197,
// merge commit 940902b): `wb worktree log finalize --apply` seals the claim
// as landed at head1, and only THEN does the branch get rebased onto a
// moved target (force-pushed with lease) before it lands on main as a merge
// commit. Unlike prepareFinalizedThenAdvancedTask (S41, which only adds a
// follow-up commit ON TOP of head1), a rebase discards head1 from head2's
// own parent chain entirely: head2 is a sibling of head1 in Git's object
// graph, not a descendant of it, even though its patch content is
// byte-identical. isAncestor(head1, head2) is false by construction here —
// that is the whole point, and the fixture asserts it — so this can only be
// authorized by patch-id equivalence (S63 finding a), never by plain
// ancestry (S41/PR #467's own check).
func prepareFinalizedThenRebasedTask(t *testing.T, task string) (fixture *gitFixture, result CreateResult, head1, head2, mergeSHA string, mergedAt time.Time) {
	t.Helper()
	fixture = newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    task, WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result = created[0]
	if err := os.WriteFile(filepath.Join(result.WorktreeDir, "feature.txt"), []byte(task+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, result.WorktreeDir, "add", "feature.txt")
	gitTest(t, result.WorktreeDir, "commit", "-m", "feature")
	head1 = gitTestOutput(t, result.WorktreeDir, "rev-parse", "HEAD")
	gitTest(t, result.WorktreeDir, "push", "-u", "origin", result.Branch)

	if _, err := LogFinalize(context.Background(), LogFinalizeOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: result.WorktreeDir,
		Result: "success", Message: "landed before the rebase", Apply: true,
	}); err != nil {
		t.Fatal(err)
	}

	// Advance main with an unrelated commit, then rebase the finalized
	// branch onto it: same patch content, brand new commit, no longer a
	// descendant of head1 at all.
	if err := os.WriteFile(filepath.Join(fixture.canonical, "unrelated.txt"), []byte("someone else's PR\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "add", "unrelated.txt")
	gitTest(t, fixture.canonical, "commit", "-m", "unrelated concurrent change")
	gitTest(t, fixture.canonical, "push", "origin", "main")

	gitTest(t, result.WorktreeDir, "fetch", "origin", "main")
	gitTest(t, result.WorktreeDir, "rebase", "origin/main")
	head2 = gitTestOutput(t, result.WorktreeDir, "rev-parse", "HEAD")
	gitTest(t, result.WorktreeDir, "push", "--force-with-lease", "origin", result.Branch)

	if descended, err := isAncestor(context.Background(), result.WorktreeDir, head1, head2); err != nil {
		t.Fatal(err)
	} else if descended {
		t.Fatal("fixture must actually rebase: head2 must not be a Git descendant of head1 (or the reproduction proves nothing new)")
	}

	gitTest(t, fixture.canonical, "fetch", "origin")
	gitTest(t, fixture.canonical, "merge", "--no-ff", "origin/"+result.Branch, "-m", "merge rebased feature")
	mergeSHA = gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	gitTest(t, fixture.canonical, "push", "origin", "main")

	return fixture, result, head1, head2, mergeSHA, time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
}

// readWorkLogTerminalForTest and readWorkLogCleanupRecordForTest read the
// two private, append-only Work Log records this file's tests must prove
// about: the exact-match finalize terminal (must never be rewritten) and the
// additive cleanup record (must be appended, pointing at the new head).
func readWorkLogTerminalForTest(t *testing.T, home string, projection workLogProjection) workLogTerminalRecord {
	t.Helper()
	path := filepath.Join(home, "worklogs", projection.EffortID, "runs", projection.RunID, "terminals", projection.ClaimID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read terminal %s: %v", path, err)
	}
	var terminal workLogTerminalRecord
	if err := json.Unmarshal(data, &terminal); err != nil {
		t.Fatalf("decode terminal %s: %v", path, err)
	}
	return terminal
}

func readWorkLogCleanupRecordForTest(t *testing.T, home string, projection workLogProjection) workLogCleanupRecord {
	t.Helper()
	path := filepath.Join(home, "worklogs", projection.EffortID, "runs", projection.RunID, "cleanups", projection.ClaimID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read additive cleanup record %s: %v", path, err)
	}
	var record workLogCleanupRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("decode additive cleanup record %s: %v", path, err)
	}
	return record
}

// TestCleanupAcceptsRebasedTerminalAfterFinalizeByPatchID is S63's cleanup
// reproduction: `wb worktree cleanup <task> --apply --remote` on a worktree
// finalized before a rebase used to refuse with "immutable terminal does not
// authorize cleanup of the finalized claim" (S41/PR #467's
// acceptAdvancedCleanupTerminal only proves plain Git descendance, which a
// rebase breaks). It must now succeed via patch-id equivalence, and must
// never rewrite the original finalize terminal.
func TestCleanupAcceptsRebasedTerminalAfterFinalizeByPatchID(t *testing.T) {
	const task = "cleanup-rebased-terminal"
	fixture, result, head1, head2, mergeSHA, mergedAt := prepareFinalizedThenRebasedTask(t, task)
	installMergedPullRequestFixtureWithMerge(t, head2, mergeSHA, mergedAt)

	projection, err := readWorkLogProjection(result.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: task, Apply: true, DeleteRemote: true,
		OlderThan: 0, Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 || !outcome.Results[0].Applied {
		t.Fatalf("rebased-terminal cleanup outcome = %#v", outcome.Results)
	}
	if _, statErr := os.Stat(result.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("rebased-terminal worktree remains after cleanup: %v", statErr)
	}

	terminal := readWorkLogTerminalForTest(t, fixture.home, projection)
	if terminal.FinalCommit != head1 || terminal.Disposition != "landed" {
		t.Fatalf("cleanup rewrote or misread the finalize terminal: %#v, want FinalCommit=%s Disposition=landed", terminal, head1)
	}
	record := readWorkLogCleanupRecordForTest(t, fixture.home, projection)
	if record.TerminalFinalCommit != head1 || record.FinalCommit != head2 {
		t.Fatalf("additive cleanup record = %#v, want TerminalFinalCommit=%s FinalCommit=%s", record, head1, head2)
	}
}

// TestAbortDiscardedAcceptsRebasedTerminalAfterFinalize is S63's abort
// reproduction: `wb worktree abort <task> --disposition discarded
// --absorbed-by <merge-sha> --apply --remote` on a worktree finalized before
// a rebase used to pass the absorbed-by proof
// (verifyAttestedMergeCommitPullRequest, PR #467) and then fail at the seal
// step with "immutable terminal conflicts with requested transition",
// because sealWorkLogForRecycleWithEvidence tried to reseal the already-
// "landed" terminal as "discarded" at a different FinalCommit. It must now
// append the same additive record acceptAdvancedCleanupTerminal writes for
// cleanup, never attempt to rewrite the terminal.
func TestAbortDiscardedAcceptsRebasedTerminalAfterFinalize(t *testing.T) {
	const task = "abort-rebased-terminal"
	fixture, result, head1, head2, mergeSHA, mergedAt := prepareFinalizedThenRebasedTask(t, task)
	gitTest(t, fixture.remote, "update-ref", "refs/pull/77/head", head2)
	installAbsorbingPullRequestFixture(t, head2, mergeSHA, mergedAt)

	projection, err := readWorkLogProjection(result.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}

	results, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: task,
		Disposition: AbortDiscarded, AbsorbedBy: "77", DeleteRemote: true, Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Applied || !results[0].WorktreeGone || !results[0].BranchDeleted {
		t.Fatalf("rebased-terminal abort = %#v", results)
	}

	terminal := readWorkLogTerminalForTest(t, fixture.home, projection)
	if terminal.FinalCommit != head1 || terminal.Disposition != "landed" {
		t.Fatalf("abort rewrote or misread the finalize terminal: %#v, want FinalCommit=%s Disposition=landed", terminal, head1)
	}
	record := readWorkLogCleanupRecordForTest(t, fixture.home, projection)
	if record.TerminalFinalCommit != head1 || record.FinalCommit != head2 {
		t.Fatalf("additive cleanup record = %#v, want TerminalFinalCommit=%s FinalCommit=%s", record, head1, head2)
	}
}

// TestAbsorbedByAcceptsPullRequestURL proves S63's --absorbed-by parsing
// fix: a full PR URL (what an operator copies out of GitHub, and what the
// S63 brief's fact 8 reports failing with "does not resolve to a commit")
// resolves the same pull request a bare number does.
func TestAbsorbedByAcceptsPullRequestURL(t *testing.T) {
	fixture := newGitFixture(t)
	result, head, mergedAt := prepareMergedTaskInFixture(t, fixture, "absorbed-by-url")
	mergeSHA := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	installAbsorbingPullRequestFixture(t, head, mergeSHA, mergedAt)

	shaByNumber, prByNumber, rejectionByNumber, err := resolveAbsorbedBy(context.Background(), result.WorktreeDir, fixture.canonical, "acme/app", "main", "77")
	if err != nil {
		t.Fatal(err)
	}
	shaByURL, prByURL, rejectionByURL, err := resolveAbsorbedBy(context.Background(), result.WorktreeDir, fixture.canonical, "acme/app", "main", "https://github.com/acme/app/pull/77")
	if err != nil {
		t.Fatal(err)
	}
	if rejectionByNumber != "" {
		t.Fatalf("resolveAbsorbedBy(77) rejection = %q, want none", rejectionByNumber)
	}
	if rejectionByURL != "" {
		t.Fatalf("resolveAbsorbedBy(URL) rejection = %q, want none", rejectionByURL)
	}
	if shaByNumber != shaByURL || shaByNumber != mergeSHA {
		t.Fatalf("landing SHA by number = %s, by URL = %s, want both = %s", shaByNumber, shaByURL, mergeSHA)
	}
	if prByNumber == nil || prByURL == nil || prByNumber.Number != prByURL.Number || prByNumber.Number != 77 {
		t.Fatalf("pull request by number = %#v, by URL = %#v, want both Number=77", prByNumber, prByURL)
	}
}
