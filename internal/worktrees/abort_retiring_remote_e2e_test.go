//go:build e2e

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func strandedDiscardedAbort(t *testing.T, fixture *gitFixture, task string) lifecycleBacklogRecord {
	t.Helper()
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: task, WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := created[0]
	if err := os.WriteFile(filepath.Join(result.WorktreeDir, "work.txt"), []byte("retained work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, result.WorktreeDir, "add", "work.txt")
	gitTest(t, result.WorktreeDir, "commit", "-m", "retained work")
	head := gitTestOutput(t, result.WorktreeDir, "rev-parse", "HEAD")
	gitTest(t, result.WorktreeDir, "push", "-u", "origin", result.Branch)
	if err := sealDiscardedWorkLogAfterAbsorbedByProof(fixture.home, result.WorktreeDir, head, nil); err != nil {
		t.Fatal(err)
	}
	record := newLifecycleBacklogRecord(fixture.projectsRoot, ListResult{
		Task: task, Repository: "acme/app", CanonicalDir: fixture.canonical,
		WorktreesRoot: filepath.Join(fixture.canonical, ".worktrees"), WorktreeDir: result.WorktreeDir,
		Branch: result.Branch, Base: "main", HeadSHA: head, RemoteHeadSHA: head, Local: true,
	}, string(AbortDiscarded))
	if err := persistLifecycleBacklog(fixture.home, &record, lifecycleStageRetiringRemote); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "worktree", "remove", "--force", result.WorktreeDir)
	return record
}

//nolint:paralleltest // native Git fixtures configure process-wide Git and WB environment.
func TestE2EAbortDiscardedResumesRetiringRemoteUnderExactLease(t *testing.T) {
	fixture := newGitFixture(t)
	record := strandedDiscardedAbort(t, fixture, "abort-retiring-remote")
	results, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: record.Task,
		Disposition: AbortDiscarded, DeleteRemote: true, Apply: true,
	})
	if err != nil || len(results) != 1 || !results[0].Applied || !results[0].BranchDeleted {
		t.Fatalf("resume exact discarded backlog = %+v, %v", results, err)
	}
	if head := remoteHeadForTest(t, fixture.canonical, record.Branch); head != "" {
		t.Fatalf("recorded remote survived at %s", head)
	}
	if exists, err := localBranchExists(context.Background(), fixture.canonical, record.Branch); err != nil || exists {
		t.Fatalf("recorded local branch exists=%t err=%v", exists, err)
	}
}

//nolint:paralleltest // native Git fixtures configure process-wide Git and WB environment.
func TestE2EAbortDiscardedRefusesAdvancedRetiringRemote(t *testing.T) {
	fixture := newGitFixture(t)
	record := strandedDiscardedAbort(t, fixture, "abort-retiring-advanced")
	gitTest(t, fixture.canonical, "fetch", "origin", record.Branch)
	gitTest(t, fixture.canonical, "branch", "-f", "advanced-abort-probe", "FETCH_HEAD")
	worktree := filepath.Join(t.TempDir(), "advance")
	gitTest(t, fixture.canonical, "worktree", "add", worktree, "advanced-abort-probe")
	if err := os.WriteFile(filepath.Join(worktree, "later.txt"), []byte("later\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "add", "later.txt")
	gitTest(t, worktree, "commit", "-m", "later work")
	gitTest(t, worktree, "push", "origin", "HEAD:"+record.Branch)
	advanced := remoteHeadForTest(t, fixture.canonical, record.Branch)
	results, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: record.Task,
		Disposition: AbortDiscarded, DeleteRemote: true, Apply: true,
	})
	if err == nil || !strings.Contains(err.Error(), "advanced from") || len(results) != 1 || results[0].Applied {
		t.Fatalf("advanced remote accepted: %+v, %v", results, err)
	}
	if head := remoteHeadForTest(t, fixture.canonical, record.Branch); head != advanced {
		t.Fatalf("advanced remote changed from %s to %s", advanced, head)
	}
}
