package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func createCheckpointFixtureWorktree(t *testing.T) (*gitFixture, string) {
	t.Helper()
	fixture := newGitFixture(t)
	promptPath := writeWorkLogPromptFile(t, "build the checkpoint engine\n")
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "checkpoint-fixture",
		WorkLog: WorkLogOptions{
			RunID: "checkpoint-run", Model: "unknown",
			OriginalPrompt: promptPath, RequireOriginalPrompt: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return fixture, created[0].WorktreeDir
}

// A checkpoint must succeed on code that would fail every configured
// verification hook, and must capture untracked files alongside tracked
// changes — this is the entire point of the feature.
func TestCheckpointCapturesBrokenAndUntrackedWork(t *testing.T) {
	_, worktree := createCheckpointFixtureWorktree(t)

	if err := os.WriteFile(filepath.Join(worktree, "main.go"), []byte("package main\n\nfunc broken( {\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "untracked.txt"), []byte("never committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Checkpoint(context.Background(), CheckpointOptions{Worktree: worktree, Push: false})
	if err != nil {
		t.Fatalf("Checkpoint on broken/untracked work: %v", err)
	}
	if !result.Created || !result.Dirty || result.Ref == "" || result.Commit == "" {
		t.Fatalf("checkpoint result = %#v", result)
	}
	if !strings.HasPrefix(result.Ref, checkpointRefPrefix) {
		t.Fatalf("checkpoint ref %q outside dedicated namespace", result.Ref)
	}

	files := gitTestOutput(t, worktree, "show", "--name-only", "--format=", result.Commit)
	if !strings.Contains(files, "main.go") || !strings.Contains(files, "untracked.txt") {
		t.Fatalf("checkpoint commit missing files:\n%s", files)
	}

	// The checkpoint commit must never be reachable as, or from, a branch.
	branches := gitTestOutput(t, worktree, "branch", "--all", "--contains", result.Commit)
	if strings.TrimSpace(branches) != "" {
		t.Fatalf("checkpoint commit reachable from a branch: %s", branches)
	}
}

// A checkpoint must never touch the caller's real index or working tree —
// only a private scratch index.
func TestCheckpointDoesNotMutateRealIndexOrWorkingTree(t *testing.T) {
	_, worktree := createCheckpointFixtureWorktree(t)

	if err := os.WriteFile(filepath.Join(worktree, "staged.txt"), []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(worktree, "unstaged.txt"), []byte("unstaged\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	before := gitTestOutput(t, worktree, "status", "--porcelain")

	if _, err := Checkpoint(context.Background(), CheckpointOptions{Worktree: worktree, Push: false}); err != nil {
		t.Fatal(err)
	}

	after := gitTestOutput(t, worktree, "status", "--porcelain")
	if before != after {
		t.Fatalf("checkpoint changed working tree/index state:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	staged := gitTestOutput(t, worktree, "diff", "--cached", "--name-only")
	if strings.TrimSpace(staged) != "staged.txt" {
		t.Fatalf("checkpoint disturbed staged set: %q", staged)
	}
}

// Calling checkpoint again with nothing new must be a true no-op: no new
// object or ref, so the command stays cheap to call between every step.
func TestCheckpointIsIdempotentNoOp(t *testing.T) {
	_, worktree := createCheckpointFixtureWorktree(t)

	if err := os.WriteFile(filepath.Join(worktree, "work.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := Checkpoint(context.Background(), CheckpointOptions{Worktree: worktree, Push: false})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created {
		t.Fatalf("first checkpoint should be created: %#v", first)
	}

	second, err := Checkpoint(context.Background(), CheckpointOptions{Worktree: worktree, Push: false})
	if err != nil {
		t.Fatal(err)
	}
	if second.Created {
		t.Fatalf("unchanged checkpoint should reuse the prior ref: %#v", second)
	}
	if second.Ref != first.Ref || second.Commit != first.Commit {
		t.Fatalf("no-op checkpoint changed identity: first=%#v second=%#v", first, second)
	}

	refs := gitTestOutput(t, worktree, "for-each-ref", checkpointRefPrefix)
	if strings.Count(strings.TrimSpace(refs), "\n")+1 != 1 {
		t.Fatalf("no-op checkpoint grew the ref namespace:\n%s", refs)
	}

	// A real change after a no-op must produce a new checkpoint.
	if err := os.WriteFile(filepath.Join(worktree, "work.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	third, err := Checkpoint(context.Background(), CheckpointOptions{Worktree: worktree, Push: false})
	if err != nil {
		t.Fatal(err)
	}
	if !third.Created || third.Ref == first.Ref {
		t.Fatalf("changed worktree should produce a new checkpoint: %#v", third)
	}
}

// A clean worktree (nothing dirty) still gets an initial checkpoint so real,
// already-committed HEAD content can be pushed off-machine — the exact
// scenario where a crashed machine held the only copy of committed work.
func TestCheckpointOnCleanWorktreeStillCheckpointsHead(t *testing.T) {
	_, worktree := createCheckpointFixtureWorktree(t)

	result, err := Checkpoint(context.Background(), CheckpointOptions{Worktree: worktree, Push: false})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.Dirty {
		t.Fatalf("clean-worktree checkpoint = %#v", result)
	}
	tree := gitTestOutput(t, worktree, "rev-parse", result.Commit+"^{tree}")
	headTree := gitTestOutput(t, worktree, "rev-parse", "HEAD^{tree}")
	if tree != headTree {
		t.Fatalf("clean checkpoint tree %s != HEAD tree %s", tree, headTree)
	}
}

// Recovery: list must enumerate checkpoints newest first with recorded
// metadata, and restore must create an inspectable branch without touching
// the caller's current branch, working tree, or index.
func TestCheckpointListAndRestore(t *testing.T) {
	_, worktree := createCheckpointFixtureWorktree(t)
	startBranch := gitTestOutput(t, worktree, "branch", "--show-current")
	startHead := gitTestOutput(t, worktree, "rev-parse", "HEAD")

	if err := os.WriteFile(filepath.Join(worktree, "a.txt"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := Checkpoint(context.Background(), CheckpointOptions{Worktree: worktree, Message: "first pass", Push: false})
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(worktree, "a.txt"), []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := Checkpoint(context.Background(), CheckpointOptions{Worktree: worktree, Message: "second pass", Push: false})
	if err != nil {
		t.Fatal(err)
	}
	if first.Ref == second.Ref {
		t.Fatalf("distinct checkpoints must get distinct refs")
	}

	refs, err := CheckpointList(context.Background(), CheckpointListOptions{Worktree: worktree})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("expected 2 checkpoints, got %d: %#v", len(refs), refs)
	}
	if refs[0].Ref != second.Ref || refs[1].Ref != first.Ref {
		t.Fatalf("list must be newest first: %#v", refs)
	}
	if refs[0].Message != "second pass" || refs[1].Message != "first pass" {
		t.Fatalf("list must recover the checkpoint message: %#v", refs)
	}

	dryRun, err := CheckpointRestore(context.Background(), CheckpointRestoreOptions{Worktree: worktree, Ref: first.Ref})
	if err != nil {
		t.Fatal(err)
	}
	if dryRun.Applied || dryRun.DiffStat == "" {
		t.Fatalf("dry-run restore = %#v", dryRun)
	}

	applied, err := CheckpointRestore(context.Background(), CheckpointRestoreOptions{
		Worktree: worktree, Ref: first.Ref, Branch: "recovered/first", Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || applied.Branch != "recovered/first" {
		t.Fatalf("applied restore = %#v", applied)
	}

	branchHead := gitTestOutput(t, worktree, "rev-parse", "recovered/first")
	if branchHead != first.Commit {
		t.Fatalf("recovery branch head %s != checkpoint commit %s", branchHead, first.Commit)
	}
	if gitTestOutput(t, worktree, "branch", "--show-current") != strings.TrimSpace(startBranch) {
		t.Fatalf("restore must not switch the current branch")
	}
	if gitTestOutput(t, worktree, "rev-parse", "HEAD") != strings.TrimSpace(startHead) {
		t.Fatalf("restore must not move the current branch's HEAD")
	}

	if _, err := CheckpointRestore(context.Background(), CheckpointRestoreOptions{
		Worktree: worktree, Ref: second.Ref, Branch: "recovered/first", Apply: true,
	}); err == nil {
		t.Fatal("restore must refuse to overwrite an existing branch without --force")
	}
}

// Getting a checkpoint off the machine: push is best-effort and must not
// fail the command, but when it succeeds the object is on the remote even
// though refs/wb/checkpoints/* was never a branch.
func TestCheckpointPushIsBestEffortAndOffMachine(t *testing.T) {
	fixture, worktree := createCheckpointFixtureWorktree(t)

	if err := os.WriteFile(filepath.Join(worktree, "b.txt"), []byte("push me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Checkpoint(context.Background(), CheckpointOptions{Worktree: worktree, Push: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Pushed || result.PushError != "" {
		t.Fatalf("expected successful push: %#v", result)
	}
	remoteRefs := gitTestOutput(t, worktree, "ls-remote", "--refs", fixture.remote, checkpointRefPrefix+"*")
	if !strings.Contains(remoteRefs, result.Commit) {
		t.Fatalf("checkpoint commit not found on remote:\n%s", remoteRefs)
	}

	// A checkpoint push must succeed even when a pre-push hook would refuse
	// any other push outright. Hooks for a linked worktree live in the
	// shared common Git directory, not the per-worktree one.
	commonDir := gitTestOutput(t, worktree, "rev-parse", "--git-common-dir")
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(worktree, commonDir)
	}
	hookPath := filepath.Join(commonDir, "hooks", "pre-push")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "c.txt"), []byte("still pushable\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := Checkpoint(context.Background(), CheckpointOptions{Worktree: worktree, Push: true})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Pushed {
		t.Fatalf("checkpoint push must bypass a refusing pre-push hook: %#v", second)
	}

	// Offline/no-op push failure must not fail the overall checkpoint.
	noRemote, err := Checkpoint(context.Background(), CheckpointOptions{
		Worktree: worktree, Push: true, Remote: "no-such-remote",
	})
	if err != nil {
		t.Fatalf("push failure must not fail checkpoint: %v", err)
	}
	if noRemote.Pushed || noRemote.PushError == "" {
		t.Fatalf("expected reported, non-fatal push failure: %#v", noRemote)
	}
}

// The exact shape of the incident this feature exists to prevent: an
// upstream branch that is gone while HEAD still carries commits nobody else
// has a copy of.
func TestCheckpointReportsGoneUpstream(t *testing.T) {
	_, worktree := createCheckpointFixtureWorktree(t)
	branch := strings.TrimSpace(gitTestOutput(t, worktree, "branch", "--show-current"))

	// wb worktree create does not itself push the new branch, so establish
	// upstream tracking the way a real agent does on its first push (e.g. to
	// open a PR) before simulating that remote branch later being deleted.
	gitTest(t, worktree, "push", "-u", "origin", branch)

	clean, err := Checkpoint(context.Background(), CheckpointOptions{Worktree: worktree, Push: false})
	if err != nil {
		t.Fatal(err)
	}
	if clean.UpstreamGone {
		t.Fatalf("freshly created worktree should have a live upstream: %#v", clean)
	}

	if err := os.WriteFile(filepath.Join(worktree, "orphaned.txt"), []byte("only copy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "add", "orphaned.txt")
	gitTest(t, worktree, "commit", "-m", "only on this machine")

	// Simulate the remote branch having been deleted and pruned.
	gitTest(t, worktree, "update-ref", "-d", "refs/remotes/origin/"+branch)

	afterPrune, err := Checkpoint(context.Background(), CheckpointOptions{Worktree: worktree, Push: false})
	if err != nil {
		t.Fatal(err)
	}
	if !afterPrune.UpstreamGone {
		t.Fatalf("expected upstream_gone after remote-tracking ref removal: %#v", afterPrune)
	}
	if afterPrune.UnpushedCommits < 1 {
		t.Fatalf("expected at least the orphaned commit to be counted: %#v", afterPrune)
	}
}
