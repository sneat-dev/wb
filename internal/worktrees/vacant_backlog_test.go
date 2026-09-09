package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// strandCleanupBacklogRecord interrupts a cleanup after Git removed the
// checkout and before the exact local branch deletion, which is what leaves a
// durable record behind.
func strandCleanupBacklogRecord(t *testing.T, fixture *gitFixture, task string, mergedAt time.Time) CleanupResult {
	t.Helper()
	injected := errors.New("injected crash after worktree removal")
	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: task,
		Apply: true, DeleteRemote: true, OlderThan: 0,
		Now:                         func() time.Time { return mergedAt.Add(time.Hour) },
		afterCleanupWorktreeRemoval: func(string) error { return injected },
	})
	if !errors.Is(err, injected) {
		t.Fatalf("cleanup interruption = %v, want %v", err, injected)
	}
	if len(outcome.Results) != 1 || outcome.Results[0].BacklogID == "" {
		t.Fatalf("interrupted cleanup left no backlog record: %#v", outcome.Results)
	}
	return outcome.Results[0]
}

// TestCleanupCompletesBacklogRecordWhoseTaskNamespaceIsGone is the regression
// for the state the founder's own manual repair produced: after WB failed to
// finish a removal, the task directory and the local branch were deleted by
// hand, and the private record was left with nothing to act on and no
// namespace to lock. Cleanup could never resume it again, and because the
// resume runs before task selection, that one stale record failed every
// subsequent `--all-merged --apply` sweep.
func TestCleanupCompletesBacklogRecordWhoseTaskNamespaceIsGone(t *testing.T) {
	fixture, created, head, mergedAt := prepareMergedTask(t, "cleanup-vacant-namespace")
	installMergedPullRequestFixture(t, head, mergedAt)
	stranded := strandCleanupBacklogRecord(t, fixture, "cleanup-vacant-namespace", mergedAt)

	// The manual repair removes WB's logical coordination namespace, not the
	// placement-specific checkout parent. Repo-local placement keeps that lock
	// under WB_HOME while the checkout lives below the canonical repository.
	taskNamespace := filepath.Join(fixture.home, "worktrees", "cleanup-vacant-namespace")
	if err := os.RemoveAll(taskNamespace); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "branch", "-D", created.Branch)
	if _, statErr := os.Stat(taskNamespace); !os.IsNotExist(statErr) {
		t.Fatalf("fixture did not remove the task namespace: %v", statErr)
	}
	// The record's own subjects are now all absent, which is the only state
	// that authorizes closing it without the lock its namespace would carry.

	resumed, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-vacant-namespace",
		Apply: true, DeleteRemote: true, OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(2 * time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.Results) != 1 || !resumed.Results[0].Applied {
		t.Fatalf("resumed cleanup = %#v", resumed.Results)
	}
	var record lifecycleBacklogRecord
	content, readErr := os.ReadFile(filepath.Join(lifecycleBacklogDirectory(fixture.home), stranded.BacklogID+".json"))
	if readErr != nil || json.Unmarshal(content, &record) != nil || record.Stage != lifecycleStageComplete {
		t.Fatalf("record = %#v read=%v, want stage complete", record, readErr)
	}
}

// TestCleanupRefusesVacantBacklogRecordWhoseBranchSurvives keeps the narrow
// boundary of that completion: it is allowed because nothing is left to delete.
// A branch the record is still responsible for retiring is work, and work needs
// the task lock WB can no longer take.
func TestCleanupRefusesVacantBacklogRecordWhoseBranchSurvives(t *testing.T) {
	fixture, created, head, mergedAt := prepareMergedTask(t, "cleanup-vacant-branch-alive")
	installMergedPullRequestFixture(t, head, mergedAt)
	stranded := strandCleanupBacklogRecord(t, fixture, "cleanup-vacant-branch-alive", mergedAt)

	// The logical namespace is removed by hand; the branch deliberately remains.
	taskNamespace := filepath.Join(fixture.home, "worktrees", "cleanup-vacant-branch-alive")
	if err := os.RemoveAll(taskNamespace); err != nil {
		t.Fatal(err)
	}

	_, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-vacant-branch-alive",
		Apply: true, DeleteRemote: true, OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(2 * time.Hour) },
	})
	if err == nil {
		t.Fatal("cleanup completed a record that still owed a branch deletion")
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, created.Branch); branchErr != nil || !exists {
		t.Fatalf("branch exists=%t err=%v, want it untouched", exists, branchErr)
	}
	var record lifecycleBacklogRecord
	content, readErr := os.ReadFile(filepath.Join(lifecycleBacklogDirectory(fixture.home), stranded.BacklogID+".json"))
	if readErr != nil || json.Unmarshal(content, &record) != nil || record.Stage == lifecycleStageComplete {
		t.Fatalf("record = %#v read=%v, want incomplete backlog", record, readErr)
	}
}
