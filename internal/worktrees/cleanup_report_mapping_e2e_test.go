//go:build e2e

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

//nolint:paralleltest // newGitFixture changes process-wide Git environment for this native journey.
func TestE2ECleanupPreservedBacklogReportsOnlyActualDeletion(t *testing.T) {
	fixture := newGitFixture(t)
	const branch = "preserved-cleanup-report"
	const task = "preserved-report"
	gitTest(t, fixture.canonical, "branch", branch, "main")
	gitTest(t, fixture.canonical, "push", "origin", branch)
	head := gitTestOutput(t, fixture.canonical, "rev-parse", branch)
	record := newLifecycleBacklogRecord(fixture.projectsRoot, ListResult{
		Task: task, Repository: "acme/app", CanonicalDir: fixture.canonical,
		WorktreesRoot: filepath.Join(fixture.canonical, ".worktrees"),
		WorktreeDir:   filepath.Join(fixture.canonical, ".worktrees", task),
		Branch:        branch, Base: "main", HeadSHA: head, RemoteHeadSHA: head, Local: true,
	}, "removed")
	record.RecoveryKind = "create_work_log_failed"
	record.PreserveLocalBranch = true
	if err := persistLifecycleBacklog(fixture.home, &record, lifecycleStageRemovingWorktree); err != nil {
		t.Fatal(err)
	}
	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: task, Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 || !outcome.Results[0].Applied {
		t.Fatalf("cleanup result = %#v", outcome.Results)
	}
	if exists, err := localBranchExists(context.Background(), fixture.canonical, branch); err != nil || !exists {
		t.Fatalf("preserved local branch exists=%t err=%v", exists, err)
	}
	if got := remoteHeadForTest(t, fixture.canonical, branch); got != head {
		t.Fatalf("preserved remote head = %q, want %q", got, head)
	}
	if outcome.Results[0].BranchDeleted || outcome.Results[0].RemoteDeleted {
		t.Fatalf("preserved refs were reported deleted: %#v", outcome.Results[0])
	}

	// A detached review checkout never owned a branch ref to retire.
	const detachedTask = "detached-report"
	detached := newLifecycleBacklogRecord(fixture.projectsRoot, ListResult{
		Task: detachedTask, Repository: "acme/app", CanonicalDir: fixture.canonical,
		WorktreesRoot: filepath.Join(fixture.canonical, ".worktrees"),
		WorktreeDir:   filepath.Join(fixture.canonical, ".worktrees", detachedTask),
		Base:          "main", HeadSHA: gitTestOutput(t, fixture.canonical, "rev-parse", "main"),
		Detached: true, Local: true,
	}, "removed")
	if err := persistLifecycleBacklog(fixture.home, &detached, lifecycleStageRemovingWorktree); err != nil {
		t.Fatal(err)
	}
	detachedOutcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: detachedTask, Apply: true,
	})
	if err != nil || len(detachedOutcome.Results) != 1 || !detachedOutcome.Results[0].Applied {
		t.Fatalf("detached cleanup result=%#v err=%v", detachedOutcome.Results, err)
	}
	if detachedOutcome.Results[0].BranchDeleted || detachedOutcome.Results[0].RemoteDeleted {
		t.Fatalf("detached checkout reported ref deletion: %#v", detachedOutcome.Results[0])
	}

	// A locally deleted branch with no observed remote still records the
	// cumulative local retirement, without inventing a remote deletion.
	const localTask = "local-only-report"
	const localBranch = "local-only-cleanup-report"
	gitTest(t, fixture.canonical, "branch", localBranch, "main")
	localHead := gitTestOutput(t, fixture.canonical, "rev-parse", localBranch)
	local := newLifecycleBacklogRecord(fixture.projectsRoot, ListResult{
		Task: localTask, Repository: "acme/app", CanonicalDir: fixture.canonical,
		WorktreesRoot: filepath.Join(fixture.canonical, ".worktrees"),
		WorktreeDir:   filepath.Join(fixture.canonical, ".worktrees", localTask),
		Branch:        localBranch, Base: "main", HeadSHA: localHead, Local: true,
	}, "removed")
	if err := persistLifecycleBacklog(fixture.home, &local, lifecycleStageRemovingLocalBranch); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "branch", "-D", localBranch)
	localOutcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: localTask, Apply: true,
	})
	if err != nil || len(localOutcome.Results) != 1 || !localOutcome.Results[0].Applied {
		t.Fatalf("local-only cleanup result=%#v err=%v", localOutcome.Results, err)
	}
	if !localOutcome.Results[0].BranchDeleted || localOutcome.Results[0].RemoteDeleted {
		t.Fatalf("local-only retirement report = %#v", localOutcome.Results[0])
	}

	// A crashed removal may unregister the worktree while leaving path residue.
	// Resume must finish that exact tree through its held directory before refs.
	residue := strandAtRetiringRemote(t, fixture, "residual-cleanup")
	if err := os.MkdirAll(residue.WorktreeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(residue.WorktreeDir, "left-behind.txt"), []byte("residue"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := resumeLifecycleBacklog(context.Background(), fixture.home, &residue, true); err != nil {
		t.Fatalf("resume unregistered residue: %v", err)
	}
	if _, err := os.Lstat(residue.WorktreeDir); !os.IsNotExist(err) {
		t.Fatalf("residual checkout still exists: %v", err)
	}
	if exists, err := localBranchExists(context.Background(), fixture.canonical, residue.Branch); err != nil || exists {
		t.Fatalf("residual local branch exists=%t err=%v", exists, err)
	}
	if head := remoteHeadForTest(t, fixture.canonical, residue.Branch); head != "" {
		t.Fatalf("residual remote branch survived at %s", head)
	}
	if residue.Stage != lifecycleStageComplete {
		t.Fatalf("residual backlog stage=%s", residue.Stage)
	}
}
