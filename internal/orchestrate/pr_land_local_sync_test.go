package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// addLandWorktree creates a linked git worktree of fixture.canonical checked
// out on branch, giving the local-sync tests a "local WB worktree" to act
// on. It carries its own git identity, matching every other repository the
// fixture creates: a CI runner has no global one to fall back on.
func addLandWorktree(t *testing.T, fixture *landFixture, branch string) string {
	t.Helper()
	dir := filepath.Join(fixture.root, "worktree-"+branch)
	runEngineGit(t, fixture.canonical, "worktree", "add", dir, branch)
	runEngineGit(t, dir, "config", "user.name", "WB Test")
	runEngineGit(t, dir, "config", "user.email", "wb@example.test")
	return dir
}

// TestLandFastForwardsACleanWorktreeAfterUpdateBranch is required test (a):
// a clean local worktree checked out on the PR branch ends at the updated
// head after `pr land` calls update-branch, and its upstream still resolves.
func TestLandFastForwardsACleanWorktreeAfterUpdateBranch(t *testing.T) {
	fixture := newLandFixture(t, "feature")
	worktree := addLandWorktree(t, fixture, "feature")
	advanceLandTarget(t, fixture)

	result, err := LandPullRequest(context.Background(), landOptions(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s, want success; reason = %s", result.Outcome, result.Reason)
	}
	if !fixtureHasMarker(fixture, "update-branch") {
		t.Fatal("candidate was not updated; the fast-forward path under test never ran")
	}
	wantHead := strings.TrimSpace(fixture.readState(t, "head"))
	gotHead := strings.TrimSpace(runEngineGit(t, worktree, "rev-parse", "HEAD"))
	if gotHead != wantHead {
		t.Fatalf("worktree HEAD = %s, want the updated head %s", gotHead, wantHead)
	}
	if !strings.Contains(result.LocalSync, "fast-forwarded worktree") {
		t.Fatalf("LocalSync = %q, want a fast-forward note", result.LocalSync)
	}
	if _, err := runGitAllowFail(worktree, "rev-parse", "--abbrev-ref", "feature@{upstream}"); err != nil {
		t.Fatalf("feature@{upstream} does not resolve after fast-forward: %v", err)
	}
}

// TestLandLeavesADirtyWorktreeUntouched is required test (b): a dirty
// worktree is left exactly as it was, the note says why, and the landing
// still succeeds.
func TestLandLeavesADirtyWorktreeUntouched(t *testing.T) {
	fixture := newLandFixture(t, "feature")
	worktree := addLandWorktree(t, fixture, "feature")
	dirtyFile := filepath.Join(worktree, "dirty.txt")
	writeEngineFile(t, dirtyFile, "local edit\n")
	advanceLandTarget(t, fixture)

	result, err := LandPullRequest(context.Background(), landOptions(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s, want success; reason = %s", result.Outcome, result.Reason)
	}
	if !strings.Contains(result.LocalSync, "uncommitted changes") {
		t.Fatalf("LocalSync = %q, want a note about uncommitted changes", result.LocalSync)
	}
	contents, err := os.ReadFile(dirtyFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "local edit\n" {
		t.Fatalf("dirty file was altered: %q", contents)
	}
	head := strings.TrimSpace(runEngineGit(t, worktree, "rev-parse", "HEAD"))
	if head != fixture.headSHA {
		t.Fatalf("worktree HEAD moved to %s, want it to stay at the pre-update head %s", head, fixture.headSHA)
	}
}

// TestLandLeavesADivergedWorktreeUntouched is required test (c): a worktree
// with a local commit the remote does not have is left untouched and noted.
func TestLandLeavesADivergedWorktreeUntouched(t *testing.T) {
	fixture := newLandFixture(t, "feature")
	worktree := addLandWorktree(t, fixture, "feature")
	writeEngineFile(t, filepath.Join(worktree, "extra.txt"), "local only\n")
	runEngineGit(t, worktree, "add", "-A")
	runEngineGit(t, worktree, "commit", "-m", "local-only commit")
	localHead := strings.TrimSpace(runEngineGit(t, worktree, "rev-parse", "HEAD"))
	advanceLandTarget(t, fixture)

	result, err := LandPullRequest(context.Background(), landOptions(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s, want success; reason = %s", result.Outcome, result.Reason)
	}
	if !strings.Contains(result.LocalSync, "diverged local commits") {
		t.Fatalf("LocalSync = %q, want a note about diverged local commits", result.LocalSync)
	}
	head := strings.TrimSpace(runEngineGit(t, worktree, "rev-parse", "HEAD"))
	if head != localHead {
		t.Fatalf("worktree HEAD moved to %s, want it to stay at the local-only commit %s", head, localHead)
	}
}

// TestLandDoesNotErrorWithNoWorktreeForTheBranch is required test (d): no
// worktree holds the branch, so there is no error and no note.
func TestLandDoesNotErrorWithNoWorktreeForTheBranch(t *testing.T) {
	fixture := newLandFixture(t, "feature")
	advanceLandTarget(t, fixture)

	result, err := LandPullRequest(context.Background(), landOptions(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s, want success; reason = %s", result.Outcome, result.Reason)
	}
	if result.LocalSync != "" {
		t.Fatalf("LocalSync = %q, want empty with no worktree for the branch", result.LocalSync)
	}
}

// TestFastForwardWorktreeToUpdatedHeadNotesAMismatchedFetch is required test
// (e): a fetched head that differs from the updated head is not merged and
// is noted. It exercises fastForwardWorktreeToUpdatedHead directly, with a
// deliberately wrong updatedHead, rather than through the full `pr land`
// fixture — the mismatch is trivial to construct this way and the assertion
// is exactly the same code path.
func TestFastForwardWorktreeToUpdatedHeadNotesAMismatchedFetch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	seed := filepath.Join(root, "seed")
	remote := filepath.Join(root, "remote.git")
	worktree := filepath.Join(root, "worktree")

	writeEngineFile(t, filepath.Join(seed, "file.txt"), "one\n")
	runEngineGit(t, seed, "init", "-b", "feature")
	runEngineGit(t, seed, "config", "user.name", "WB Test")
	runEngineGit(t, seed, "config", "user.email", "wb@example.test")
	runEngineGit(t, seed, "add", "-A")
	runEngineGit(t, seed, "commit", "-m", "initial")
	runEngineGit(t, root, "clone", "--bare", seed, remote)
	runEngineGit(t, root, "clone", remote, worktree)
	runEngineGit(t, worktree, "config", "user.name", "WB Test")
	runEngineGit(t, worktree, "config", "user.email", "wb@example.test")

	const bogusHead = "0000000000000000000000000000000000000000"
	note := fastForwardWorktreeToUpdatedHead(context.Background(), worktree, "feature", bogusHead)
	if !strings.Contains(note, "does not match updated head") {
		t.Fatalf("note = %q, want a fetched-head mismatch note", note)
	}
	head := strings.TrimSpace(runEngineGit(t, worktree, "rev-parse", "HEAD"))
	if head == bogusHead {
		t.Fatal("worktree HEAD was moved onto the bogus updated head")
	}
}

// runGitAllowFail is a thin runEngineGit variant that returns an error
// instead of failing the test, for assertions that want to check
// success/failure themselves (e.g. that a ref resolves at all).
func runGitAllowFail(directory string, args ...string) (string, error) {
	out, _, err := runCommand(context.Background(), 0, 0, directory, "git", args...)
	return out, err
}
