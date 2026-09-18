package gitops

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func lgCovLandOptions() LandOptions {
	return LandOptions{
		DefaultBranch: "main",
		CommitMessage: "land the change",
		PRBranch:      "wb/land",
		PRTitle:       "land the change",
		PRBody:        "body",
	}
}

// A clean local copy lets Land commit in a detached worktree and push the
// commit straight onto the default branch, leaving the caller's checkout alone.
func TestLgCovLandPushesDirectlyWhenLocalIsClean(t *testing.T) {
	origin, clone := lgCovSeededClone(t)
	beforeHead := gitIn(t, clone, "rev-parse", "HEAD")

	var worktree string
	outcome, err := Land(clone, lgCovLandOptions(), func(wt string) (bool, string, error) {
		worktree = wt
		lgCovWriteFile(t, wt, "landed.txt", "landed\n")
		return true, "wrote landed.txt", nil
	})
	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	if !outcome.Changed || outcome.PRURL != "" {
		t.Fatalf("Outcome = %+v, want Changed with no PR", outcome)
	}
	if outcome.Detail != "pushed to main" {
		t.Fatalf("Detail = %q, want %q", outcome.Detail, "pushed to main")
	}
	if got := gitIn(t, origin, "show", "main:landed.txt"); got != "landed" {
		t.Fatalf("origin/main:landed.txt = %q, want the mutation's content", got)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("temporary worktree %s still present (stat err %v), want it removed", worktree, err)
	}
	if after := gitIn(t, clone, "rev-parse", "HEAD"); after != beforeHead {
		t.Fatalf("Land moved the caller's HEAD from %s to %s", beforeHead, after)
	}
}

// A mutation that changes nothing must not create a commit or touch the remote.
func TestLgCovLandReportsNothingToDoWithoutCommitting(t *testing.T) {
	origin, clone := lgCovSeededClone(t)
	beforeMain := gitIn(t, origin, "rev-parse", "main")

	outcome, err := Land(clone, lgCovLandOptions(), func(string) (bool, string, error) {
		return false, "nothing to change", nil
	})
	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	if outcome.Changed || outcome.PRURL != "" || outcome.Detail != "nothing to change" {
		t.Fatalf("Outcome = %+v, want the mutator's no-op detail and no push", outcome)
	}
	if after := gitIn(t, origin, "rev-parse", "main"); after != beforeMain {
		t.Fatalf("origin/main moved from %s to %s despite a no-op mutation", beforeMain, after)
	}
}

func TestLgCovLandPropagatesMutatorError(t *testing.T) {
	_, clone := lgCovSeededClone(t)
	sentinel := errors.New("mutator exploded")

	outcome, err := Land(clone, lgCovLandOptions(), func(string) (bool, string, error) {
		return false, "", sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Land error = %v, want it to wrap %v", err, sentinel)
	}
	if outcome != (Outcome{}) {
		t.Fatalf("Outcome = %+v, want the zero value when the mutator fails", outcome)
	}
}

func TestLgCovLandFailsWhenFetchFails(t *testing.T) {
	lgCovGitIdentity(t)
	called := false
	_, err := Land(t.TempDir(), lgCovLandOptions(), func(string) (bool, string, error) {
		called = true
		return true, "x", nil
	})
	if err == nil {
		t.Fatal("Land outside a git repository should fail at fetch")
	}
	if called {
		t.Fatal("the mutator ran even though Land could not fetch")
	}
}

// A clone whose origin publishes no branches has no origin/<default> to detach
// a worktree at; Land must report that rather than run the mutator on a
// missing tree.
func TestLgCovLandFailsWhenDefaultBranchRefIsMissing(t *testing.T) {
	lgCovGitIdentity(t)
	origin := t.TempDir()
	gitIn(t, origin, "init", "-q", "--bare", "-b", "main")
	clone := filepath.Join(t.TempDir(), "clone")
	gitIn(t, t.TempDir(), "clone", "-q", origin, clone)

	called := false
	_, err := Land(clone, lgCovLandOptions(), func(string) (bool, string, error) {
		called = true
		return true, "x", nil
	})
	if err == nil {
		t.Fatal("Land should fail when origin/main does not exist")
	}
	if called {
		t.Fatal("the mutator ran even though the worktree could not be created")
	}
}

// Uncommitted work in the caller's clone must never be joined by a push to the
// default branch: Land opens a PR instead, on a real branch, with the mutation.
func TestLgCovLandOpensPullRequestWhenLocalCopyIsDirty(t *testing.T) {
	origin, clone := lgCovSeededClone(t)
	lgCovWriteFile(t, clone, "wip.txt", "uncommitted work\n")
	beforeMain := gitIn(t, origin, "rev-parse", "main")

	ghLog := lgCovFakeGh(t, "https://example.test/pr/42")

	outcome, err := Land(clone, lgCovLandOptions(), func(wt string) (bool, string, error) {
		lgCovWriteFile(t, wt, "landed.txt", "landed\n")
		return true, "wrote landed.txt", nil
	})
	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	if outcome.PRURL != "https://example.test/pr/42" {
		t.Fatalf("PRURL = %q, want the URL gh printed", outcome.PRURL)
	}
	if !strings.Contains(outcome.Detail, "local has uncommitted changes") {
		t.Fatalf("Detail = %q, want it to explain the dirty-local fallback", outcome.Detail)
	}
	if got := gitIn(t, origin, "show", "wb/land:landed.txt"); got != "landed" {
		t.Fatalf("PR branch content = %q, want the mutation on wb/land", got)
	}
	if after := gitIn(t, origin, "rev-parse", "main"); after != beforeMain {
		t.Fatalf("origin/main moved to %s despite the dirty-local fallback", after)
	}
	if _, err := os.Stat(filepath.Join(clone, "wip.txt")); err != nil {
		t.Fatalf("the caller's uncommitted file was disturbed: %v", err)
	}

	invocations := lgCovInvocations(t, ghLog)
	if len(invocations) != 2 {
		t.Fatalf("gh invocations = %v, want a create and an auto-merge", invocations)
	}
	if !strings.Contains(invocations[0], "pr create") || !strings.Contains(invocations[0], "--base main") || !strings.Contains(invocations[0], "--head wb/land") {
		t.Fatalf("gh create invocation = %q, want base main and head wb/land", invocations[0])
	}
	if !strings.Contains(invocations[1], "pr merge") || !strings.Contains(invocations[1], "--auto") || !strings.Contains(invocations[1], "--squash") {
		t.Fatalf("gh merge invocation = %q, want an auto squash merge", invocations[1])
	}
}

// A rejected direct push to a protected default branch falls back to the PR
// path with the mutation on its own branch.
func TestLgCovLandFallsBackToPullRequestWhenDefaultBranchIsProtected(t *testing.T) {
	origin, clone := lgCovSeededClone(t)
	lgCovRejectPushesTo(t, origin, "refs/heads/main")
	beforeMain := gitIn(t, origin, "rev-parse", "main")

	ghLog := lgCovFakeGh(t, "https://example.test/pr/7")

	outcome, err := Land(clone, lgCovLandOptions(), func(wt string) (bool, string, error) {
		lgCovWriteFile(t, wt, "landed.txt", "landed\n")
		return true, "wrote landed.txt", nil
	})
	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	if outcome.PRURL != "https://example.test/pr/7" {
		t.Fatalf("PRURL = %q, want the URL gh printed", outcome.PRURL)
	}
	if !strings.Contains(outcome.Detail, "protected branch") {
		t.Fatalf("Detail = %q, want it to explain the protected-branch fallback", outcome.Detail)
	}
	if got := gitIn(t, origin, "show", "wb/land:landed.txt"); got != "landed" {
		t.Fatalf("PR branch content = %q, want the mutation on wb/land", got)
	}
	if after := gitIn(t, origin, "rev-parse", "main"); after != beforeMain {
		t.Fatalf("origin/main moved to %s despite the protected branch", after)
	}
	if invocations := lgCovInvocations(t, ghLog); len(invocations) != 2 {
		t.Fatalf("gh invocations = %v, want a create and an auto-merge", invocations)
	}
}

// A temporary worktree directory that cannot be created is reported before the
// mutator ever sees a worktree path.
func TestLgCovLandReportsWorktreeTempFailure(t *testing.T) {
	_, clone := lgCovSeededClone(t)
	blocker := lgCovWriteFile(t, t.TempDir(), "blocker", "not a directory\n")
	t.Setenv("TMPDIR", filepath.Join(blocker, "tmp"))

	called := false
	_, err := Land(clone, lgCovLandOptions(), func(string) (bool, string, error) {
		called = true
		return true, "x", nil
	})
	if err == nil {
		t.Fatal("Land should fail when its temporary worktree directory cannot be created")
	}
	if called {
		t.Fatal("the mutator ran even though no temporary worktree could be created")
	}
}

// A failure to stage the mutation must be reported instead of committing a
// partial or empty change.
func TestLgCovLandReportsStagingFailure(t *testing.T) {
	_, clone := lgCovSeededClone(t)
	lgCovFakeGitFailingSubcommand(t, "add", "fake: staging failed")

	_, err := Land(clone, lgCovLandOptions(), func(wt string) (bool, string, error) {
		lgCovWriteFile(t, wt, "landed.txt", "landed\n")
		return true, "wrote landed.txt", nil
	})
	if err == nil {
		t.Fatal("Land should error when git add fails in the worktree")
	}
	if !strings.Contains(err.Error(), "fake: staging failed") {
		t.Fatalf("error = %q, want the staging failure from git", err.Error())
	}
}

// A failure to commit must not be mistaken for a landed change, and nothing may
// reach the remote.
func TestLgCovLandReportsCommitFailure(t *testing.T) {
	origin, clone := lgCovSeededClone(t)
	beforeMain := gitIn(t, origin, "rev-parse", "main")
	lgCovFakeGitFailingSubcommand(t, "commit", "fake: commit failed")

	_, err := Land(clone, lgCovLandOptions(), func(wt string) (bool, string, error) {
		lgCovWriteFile(t, wt, "landed.txt", "landed\n")
		return true, "wrote landed.txt", nil
	})
	if err == nil {
		t.Fatal("Land should error when git commit fails in the worktree")
	}
	if !strings.Contains(err.Error(), "fake: commit failed") {
		t.Fatalf("error = %q, want the commit failure from git", err.Error())
	}
	if after := gitIn(t, origin, "rev-parse", "main"); after != beforeMain {
		t.Fatalf("origin/main moved to %s despite a failed commit", after)
	}
}

// openPR creates a real local branch before pushing, pushes it to origin, and
// returns the last line gh printed as the PR URL.
func TestLgCovOpenPRPushesBranchAndCreatesPR(t *testing.T) {
	origin, clone := lgCovSeededClone(t)
	lgCovWriteFile(t, clone, "change.txt", "change\n")
	gitIn(t, clone, "add", "-A")
	gitIn(t, clone, "commit", "-q", "-m", "change")
	head := gitIn(t, clone, "rev-parse", "HEAD")

	ghLog := lgCovFakeGh(t, "https://example.test/pr/9")

	outcome, err := openPR(clone, lgCovLandOptions(), "protected branch")
	if err != nil {
		t.Fatalf("openPR: %v", err)
	}
	wantDetail := "PR https://example.test/pr/9 (protected branch)"
	if outcome.PRURL != "https://example.test/pr/9" || outcome.Detail != wantDetail || !outcome.Changed {
		t.Fatalf("Outcome = %+v, want PRURL %q and Detail %q", outcome, "https://example.test/pr/9", wantDetail)
	}
	if got := gitIn(t, origin, "rev-parse", "wb/land"); got != head {
		t.Fatalf("origin wb/land = %s, want the pushed HEAD %s", got, head)
	}

	invocations := lgCovInvocations(t, ghLog)
	if len(invocations) != 2 {
		t.Fatalf("gh invocations = %v, want a create and an auto-merge", invocations)
	}
	if !strings.Contains(invocations[1], "pr merge") || !strings.Contains(invocations[1], "https://example.test/pr/9") {
		t.Fatalf("merge invocation = %q, want it to auto-merge the created PR", invocations[1])
	}
}

// An invalid PR branch name fails at checkout, before anything is pushed.
func TestLgCovOpenPRReportsBranchCreationFailure(t *testing.T) {
	_, clone := lgCovSeededClone(t)
	lgCovWriteFile(t, clone, "change.txt", "change\n")
	gitIn(t, clone, "add", "-A")
	gitIn(t, clone, "commit", "-q", "-m", "change")

	opt := lgCovLandOptions()
	opt.PRBranch = "bad..branch"
	_, err := openPR(clone, opt, "protected branch")
	if err == nil {
		t.Fatal("openPR with an invalid branch name should error")
	}
	if !strings.Contains(err.Error(), "create branch bad..branch failed") {
		t.Fatalf("error = %q, want it to name the branch it could not create", err.Error())
	}
}

// A rejected branch push is reported before any PR is attempted.
func TestLgCovOpenPRReportsPushFailure(t *testing.T) {
	origin, clone := lgCovSeededClone(t)
	lgCovRejectPushesTo(t, origin, "refs/heads/wb/land")
	lgCovWriteFile(t, clone, "change.txt", "change\n")
	gitIn(t, clone, "add", "-A")
	gitIn(t, clone, "commit", "-q", "-m", "change")

	ghLog := lgCovFakeGh(t, "https://example.test/pr/11")

	_, err := openPR(clone, lgCovLandOptions(), "protected branch")
	if err == nil {
		t.Fatal("openPR should error when the branch push is rejected")
	}
	if !strings.Contains(err.Error(), "push to wb/land failed") {
		t.Fatalf("error = %q, want it to name the rejected push", err.Error())
	}
	if invocations := lgCovInvocations(t, ghLog); len(invocations) != 0 {
		t.Fatalf("gh ran %v despite the push failure, want no calls", invocations)
	}
}

// The branch is published first, so a gh failure still leaves the work pushed
// and reports the PR creation failure.
func TestLgCovOpenPRReportsPRCreationFailure(t *testing.T) {
	origin, clone := lgCovSeededClone(t)
	lgCovWriteFile(t, clone, "change.txt", "change\n")
	gitIn(t, clone, "add", "-A")
	gitIn(t, clone, "commit", "-q", "-m", "change")
	head := gitIn(t, clone, "rev-parse", "HEAD")

	ghLog := lgCovFakeGhFailingCreate(t)

	_, err := openPR(clone, lgCovLandOptions(), "protected branch")
	if err == nil {
		t.Fatal("openPR should error when gh pr create fails")
	}
	if !strings.Contains(err.Error(), "PR creation failed") {
		t.Fatalf("error = %q, want it to name the PR creation failure", err.Error())
	}
	if got := gitIn(t, origin, "rev-parse", "wb/land"); got != head {
		t.Fatalf("origin wb/land = %s, want the branch pushed before gh ran (%s)", got, head)
	}
	if invocations := lgCovInvocations(t, ghLog); len(invocations) != 1 || !strings.Contains(invocations[0], "pr create") {
		t.Fatalf("gh invocations = %v, want exactly one failed pr create", invocations)
	}
}
