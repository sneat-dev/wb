package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// createLandedTaskWorktree gives the fixture's pull-request branch the WB
// worktree and claim that a landing's cleanup retires.
func createLandedTaskWorktree(t *testing.T, fixture *landFixture, task, branch string) string {
	t.Helper()
	created, err := worktrees.Create(context.Background(), []string{"acme/app"}, worktrees.CreateOptions{
		ProjectsRoot: fixture.projects, Operation: task, Branch: branch, BranchChosen: true, Resume: true,
		WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return created[0].WorktreeDir
}

func remoteBranchExists(t *testing.T, fixture *landFixture, branch string) bool {
	t.Helper()
	return strings.TrimSpace(runEngineGit(t, fixture.remote, "branch", "--list", branch)) != ""
}

// A landing's post-merge cleanup (delete the merged remote branch, retire the
// worktree, release the claim) never depends on which branch the canonical clone
// has checked out, clean or dirty (sneat-dev/wb#824).
//
//nolint:paralleltest // newLandFixture installs a process-wide fake gh and Git environment.
func TestLandCleansUpWhileTheCanonicalCloneIsOnAnotherBranch(t *testing.T) {
	for _, test := range []struct {
		name  string
		dirty bool
	}{{"clean", false}, {"dirty", true}} {
		//nolint:paralleltest // Child fixtures each change process-wide PATH and Git environment.
		t.Run(test.name, func(t *testing.T) {
			fixture := newLandFixture(t, "bump/other-branch", "go.sum")
			worktree := createLandedTaskWorktree(t, fixture, "other-branch-task", "bump/other-branch")

			// Another agent's branch, left checked out in the shared clone.
			runEngineGit(t, fixture.canonical, "checkout", "-b", "feat/coverage-100")
			if test.dirty {
				writeEngineFile(t, filepath.Join(fixture.canonical, "wip.txt"), "another agent's uncommitted work\n")
			}

			options := landOptions(fixture)
			options.Keep = false
			result, err := LandPullRequest(context.Background(), options)
			if err != nil {
				t.Fatal(err)
			}
			if result.Outcome != LandSuccess {
				t.Fatalf("outcome=%s reason=%s receipt=%+v", result.Outcome, result.Reason, result)
			}
			if !result.BranchDeleted || remoteBranchExists(t, fixture, "bump/other-branch") {
				t.Fatalf("the merged remote branch was not deleted: %+v", result)
			}
			if _, statErr := os.Stat(worktree); !os.IsNotExist(statErr) {
				t.Fatalf("the worktree was not retired: %v", statErr)
			}
			if len(result.CleanedTasks) != 1 || result.CleanedTasks[0] != "other-branch-task" {
				t.Fatalf("the task was not retired (claim released) through cleanup: %+v", result.CleanedTasks)
			}
			if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "branch", "--show-current")); got != "feat/coverage-100" {
				t.Fatalf("landing moved the canonical clone off its branch: %q", got)
			}
			if result.CanonicalSync != "not_checked_out" {
				t.Fatalf("canonical sync = %q, want not_checked_out for a clone on another branch", result.CanonicalSync)
			}
			if test.dirty {
				if content, readErr := os.ReadFile(filepath.Join(fixture.canonical, "wip.txt")); readErr != nil || !strings.Contains(string(content), "uncommitted work") {
					t.Fatalf("landing disturbed the canonical clone's uncommitted work: %q, %v", content, readErr)
				}
			}
		})
	}
}

// When the merge succeeded and the cleanup that follows fails, the landing says
// so with its own exit status and the exact command that finishes it, and that
// command works.
//
//nolint:paralleltest // newLandFixture installs a process-wide fake gh and Git environment.
func TestLandWhoseCleanupFailedReportsLandedIncompleteAndResumes(t *testing.T) {
	fixture := newLandFixture(t, "bump/resume", "go.sum")
	worktree := createLandedTaskWorktree(t, fixture, "resume-task", "bump/resume")

	// A pre-push hook that refuses only remote-ref deletions: the deletion
	// that retires the merged branch is the one that fails.
	hooks := filepath.Join(fixture.canonical, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(hooks, "pre-push")
	script := "#!/bin/sh\nwhile read local_ref local_sha remote_ref remote_sha; do\n" +
		"  if [ \"$local_ref\" = \"(delete)\" ]; then echo 'deletion refused by hook' >&2; exit 1; fi\ndone\n"
	if err := testenv.WriteExecutableFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	options := landOptions(fixture)
	options.Keep = false
	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatalf("a landed-but-incomplete result is a receipt, not an error: %v", err)
	}
	if result.Outcome != LandLandedIncomplete || result.RefusalCode != LandRefusalBranchRetirement {
		t.Fatalf("result = %+v, want landed-incomplete / %s", result, LandRefusalBranchRetirement)
	}
	if result.ExitCode() != ExitLandedIncomplete {
		t.Fatalf("exit code = %d, want %d", result.ExitCode(), ExitLandedIncomplete)
	}
	if !strings.Contains(result.Reason, "deletion refused by hook") || !strings.Contains(result.Reason, "landed") {
		t.Fatalf("reason does not say what landed and what failed: %s", result.Reason)
	}
	if result.ResumeCommand != "wb pr land acme/app#7" {
		t.Fatalf("resume command = %q", result.ResumeCommand)
	}
	if result.MergeSHA == "" || !remoteBranchExists(t, fixture, "bump/resume") {
		t.Fatalf("the fixture must leave the merged branch behind: %+v", result)
	}

	// The cause is fixed; the printed command finishes the job.
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	resumeOptions := landOptions(fixture)
	resumeOptions.Keep = false
	resumed, err := LandPullRequest(context.Background(), resumeOptions)
	if err != nil || resumed.Outcome != LandSuccess {
		t.Fatalf("resume = (%+v, %v), want success", resumed, err)
	}
	if !resumed.BranchDeleted || remoteBranchExists(t, fixture, "bump/resume") {
		t.Fatalf("resume did not delete the merged branch: %+v", resumed)
	}
	if _, statErr := os.Stat(worktree); !os.IsNotExist(statErr) {
		t.Fatalf("resume did not retire the worktree: %v", statErr)
	}
}

// pr create --land maps a landed-incomplete landing onto its own outcome and
// exit status rather than "findings".
//
//nolint:paralleltest // newCreateFixture installs a process-wide fake gh and Git environment.
func TestCreateLandMapsALandedIncompleteLandingToItsOwnOutcome(t *testing.T) {
	fixture := newCreateFixture(t)
	fixture.writeState(t, "files", `[{"filename":"go.sum","status":"modified","patch":"@@ -1,1 +1,1 @@\n-old h1:x=\n+new h1:y=\n"}]`)
	worktree := fixture.createWorktree(t, "create-incomplete-task", "feature/create-incomplete", "main", "go.sum")
	hooks := filepath.Join(fixture.canonical, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nwhile read local_ref local_sha remote_ref remote_sha; do\n" +
		"  if [ \"$local_ref\" = \"(delete)\" ]; then echo 'deletion refused by hook' >&2; exit 1; fi\ndone\n"
	if err := testenv.WriteExecutableFile(filepath.Join(hooks, "pre-push"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects, Land: true,
		LinkPreflight: func(string) error { return nil },
		LandOptions: &PullRequestLandOptions{
			ProjectsRoot: fixture.projects, Keep: true, CheckPollInterval: time.Millisecond, Slice: 10 * time.Second,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CreateLandedIncomplete || result.RefusalCode != LandRefusalBranchRetirement || result.ExitCode() != ExitLandedIncomplete {
		t.Fatalf("outcome=%s code=%s exit=%d reason=%s", result.Outcome, result.RefusalCode, result.ExitCode(), result.Reason)
	}
	if result.LandResult == nil || result.LandResult.ResumeCommand != "wb pr land acme/app#9 --keep" {
		t.Fatalf("land result = %+v", result.LandResult)
	}
}
