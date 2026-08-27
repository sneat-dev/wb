package worktrees

import (
	"context"
	"strings"
	"testing"
)

func TestCheckpointRemoteRefRejectsUnsafeOrEmptyTask(t *testing.T) {
	for _, task := range []string{"", "  ", "../escape", "a/b", "task with spaces"} {
		if _, err := CheckpointRemoteRef(task); err == nil {
			t.Errorf("task %q was accepted as a safe ref segment", task)
		}
	}
	ref, err := CheckpointRemoteRef("hook-tiers-remote-checkpoints")
	if err != nil {
		t.Fatal(err)
	}
	if ref != "refs/wb/checkpoints/hook-tiers-remote-checkpoints" {
		t.Fatalf("ref = %q", ref)
	}
}

// TestPushRemoteCheckpointNeverTouchesRefsHeads proves the force in the
// pushed refspec is scoped to exactly the one destination checkpoint ref: the
// branch this repository is actually on advances independently of the
// checkpoint push, and the checkpoint never appears under refs/heads on the
// remote.
func TestPushRemoteCheckpointNeverTouchesRefsHeads(t *testing.T) {
	fixture, worktree, _ := newSessionCheckpointFixture(t, "checkpoint-push")
	branch := gitTestOutput(t, worktree, "branch", "--show-current")
	branchTipBefore := remoteBranchTipFrom(t, worktree, fixture.remote, branch)
	head := gitTestOutput(t, worktree, "rev-parse", "HEAD")

	result, err := PushRemoteCheckpoint(context.Background(), PushRemoteCheckpointOptions{
		Root: worktree, Task: "checkpoint-push", HeadSHA: head,
	})
	if err != nil {
		t.Fatalf("push remote checkpoint: %v", err)
	}
	if result.Ref != "refs/wb/checkpoints/checkpoint-push" || result.SHA != head || !result.Pushed {
		t.Fatalf("result = %#v", result)
	}
	if result.Notice != NotALandingReceiptNotice {
		t.Fatalf("notice = %q, want the fixed not-a-landing-receipt disclaimer", result.Notice)
	}

	// The branch's own remote tip must be untouched by the checkpoint push.
	if got := remoteBranchTipFrom(t, worktree, fixture.remote, branch); got != branchTipBefore {
		t.Fatalf("branch %s remote tip changed from %s to %s", branch, branchTipBefore, got)
	}
	// The checkpoint ref must exist at the remote, at exactly HEAD.
	checkpointTip := strings.Fields(gitTestOutput(t, worktree, "ls-remote", "--", fixture.remote, "refs/wb/checkpoints/checkpoint-push"))
	if len(checkpointTip) != 2 || checkpointTip[0] != head {
		t.Fatalf("remote checkpoint ref = %#v, want tip %s", checkpointTip, head)
	}
	// It must never appear as a branch.
	branches := gitTestOutput(t, worktree, "ls-remote", "--heads", "--", fixture.remote)
	if strings.Contains(branches, "checkpoints") {
		t.Fatalf("checkpoint ref leaked into refs/heads: %s", branches)
	}
}

// TestPushRemoteCheckpointIsForceUpdatable proves a checkpoint ref may move
// non-fast-forward (an agent rewinding or amending its own in-progress work),
// which a branch push without --force would reject.
func TestPushRemoteCheckpointIsForceUpdatable(t *testing.T) {
	_, worktree, _ := newSessionCheckpointFixture(t, "checkpoint-rewrite")
	firstHead := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	if _, err := PushRemoteCheckpoint(context.Background(), PushRemoteCheckpointOptions{
		Root: worktree, Task: "checkpoint-rewrite", HeadSHA: firstHead,
	}); err != nil {
		t.Fatalf("first checkpoint push: %v", err)
	}

	// Rewrite history locally (amend), producing a commit that is NOT a
	// descendant of the first checkpoint -- a plain push would be rejected.
	gitTest(t, worktree, "commit", "--amend", "-m", "amended checkpoint commit")
	secondHead := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	if secondHead == firstHead {
		t.Fatal("amend did not produce a new commit")
	}
	result, err := PushRemoteCheckpoint(context.Background(), PushRemoteCheckpointOptions{
		Root: worktree, Task: "checkpoint-rewrite", HeadSHA: secondHead,
	})
	if err != nil {
		t.Fatalf("force-updating checkpoint push: %v", err)
	}
	if result.SHA != secondHead {
		t.Fatalf("checkpoint SHA = %q, want the rewritten %q", result.SHA, secondHead)
	}
}

func TestPushRemoteCheckpointRejectsAnUnresolvedCommit(t *testing.T) {
	_, worktree, _ := newSessionCheckpointFixture(t, "checkpoint-bad-sha")
	if _, err := PushRemoteCheckpoint(context.Background(), PushRemoteCheckpointOptions{
		Root: worktree, Task: "checkpoint-bad-sha", HeadSHA: "not-a-sha",
	}); err == nil {
		t.Fatal("expected an error for a non-object-id HeadSHA")
	}
}

// TestFetchRemoteCheckpointRetrievesIntoALocalRefNeverABranch is the
// cross-machine retrieval side: a checkpoint pushed from one worktree is
// fetched into a second, independent clone of the same remote, landing under
// the identical refs/wb/checkpoints/<task> local ref rather than any branch.
func TestFetchRemoteCheckpointRetrievesIntoALocalRefNeverABranch(t *testing.T) {
	fixture, worktree, _ := newSessionCheckpointFixture(t, "checkpoint-fetch")
	head := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	if _, err := PushRemoteCheckpoint(context.Background(), PushRemoteCheckpointOptions{
		Root: worktree, Task: "checkpoint-fetch", HeadSHA: head,
	}); err != nil {
		t.Fatalf("push remote checkpoint: %v", err)
	}

	// A second, independent clone stands in for "another machine".
	secondClone := t.TempDir() + "/second-clone"
	gitTest(t, worktree, "clone", fixture.remote, secondClone)
	configureGitUser(t, secondClone)

	result, err := FetchRemoteCheckpoint(context.Background(), FetchRemoteCheckpointOptions{
		Root: secondClone, Task: "checkpoint-fetch",
	})
	if err != nil {
		t.Fatalf("fetch remote checkpoint: %v", err)
	}
	if result.SHA != head || result.Ref != "refs/wb/checkpoints/checkpoint-fetch" || result.LocalRef != result.Ref {
		t.Fatalf("result = %#v, want SHA %s", result, head)
	}
	if result.Notice != NotALandingReceiptNotice {
		t.Fatalf("notice = %q", result.Notice)
	}
	if got := gitTestOutput(t, secondClone, "rev-parse", "--verify", "refs/wb/checkpoints/checkpoint-fetch^{commit}"); got != head {
		t.Fatalf("local checkpoint ref = %q, want %q", got, head)
	}
	if branches := gitTestOutput(t, secondClone, "branch", "--list"); strings.Contains(branches, "checkpoint") {
		t.Fatalf("fetched checkpoint appeared as a local branch: %q", branches)
	}
}

func TestFetchRemoteCheckpointFailsCleanlyWhenNoSuchCheckpointExists(t *testing.T) {
	_, worktree, _ := newSessionCheckpointFixture(t, "checkpoint-missing")
	if _, err := FetchRemoteCheckpoint(context.Background(), FetchRemoteCheckpointOptions{
		Root: worktree, Task: "never-checkpointed",
	}); err == nil {
		t.Fatal("expected an error fetching a checkpoint ref that was never pushed")
	}
}

// TestLogCheckpointPushesRemoteCheckpointAndNeverClaimsLanding is the direct
// assertion the founder asked for: nothing about the checkpoint verb's
// output may report, log, or imply that the checkpointed task is merged,
// landed, or done.
func TestLogCheckpointPushesRemoteCheckpointAndNeverClaimsLanding(t *testing.T) {
	fixture, worktree, _ := newSessionCheckpointFixture(t, "checkpoint-log-verb")
	result, err := LogCheckpoint(context.Background(), LogCheckpointOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Message: "mid-task progress",
	})
	if err != nil {
		t.Fatalf("log checkpoint: %v", err)
	}
	if result.RemoteCheckpoint == nil {
		t.Fatal("expected a remote checkpoint result")
	}
	if result.RemoteCheckpoint.Ref != "refs/wb/checkpoints/checkpoint-log-verb" {
		t.Fatalf("checkpoint ref = %q", result.RemoteCheckpoint.Ref)
	}
	if result.RemoteCheckpoint.Notice != NotALandingReceiptNotice {
		t.Fatalf("checkpoint notice = %q", result.RemoteCheckpoint.Notice)
	}
	joinedNotes := strings.Join(result.Notes, "\n")
	if !strings.Contains(joinedNotes, "NOT a landing receipt") {
		t.Fatalf("notes never state the not-a-landing-receipt disclaimer: %v", result.Notes)
	}
	if strings.Count(joinedNotes, "NOT a landing receipt") == 0 {
		t.Fatalf("expected the not-a-landing-receipt disclaimer to appear: %v", result.Notes)
	}
	for _, forbidden := range []string{"shipped", "task complete", "checkpoint complete", "successfully landed"} {
		if strings.Contains(strings.ToLower(joinedNotes), forbidden) {
			t.Fatalf("checkpoint notes must never claim landing status; found %q in %v", forbidden, result.Notes)
		}
	}
	remoteTip := strings.Fields(gitTestOutput(t, worktree, "ls-remote", "--", fixture.remote, result.RemoteCheckpoint.Ref))
	if len(remoteTip) != 2 || remoteTip[0] != result.RemoteCheckpoint.SHA {
		t.Fatalf("remote checkpoint tip = %#v, want %s", remoteTip, result.RemoteCheckpoint.SHA)
	}
}

func TestLogCheckpointSkipRemoteOptsOutOfThePush(t *testing.T) {
	fixture, worktree, _ := newSessionCheckpointFixture(t, "checkpoint-skip-remote")
	result, err := LogCheckpoint(context.Background(), LogCheckpointOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, SkipRemote: true,
	})
	if err != nil {
		t.Fatalf("log checkpoint: %v", err)
	}
	if result.RemoteCheckpoint != nil {
		t.Fatalf("expected no remote checkpoint when SkipRemote is set, got %#v", result.RemoteCheckpoint)
	}
	remoteRefs := gitTestOutput(t, worktree, "ls-remote", "--", fixture.remote)
	if strings.Contains(remoteRefs, "checkpoints") {
		t.Fatalf("a checkpoint ref was pushed despite SkipRemote: %s", remoteRefs)
	}
}
