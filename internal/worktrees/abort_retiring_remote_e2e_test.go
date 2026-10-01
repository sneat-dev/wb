//go:build e2e

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

//nolint:paralleltest // native Git fixtures configure process-wide Git and WB environment.
func TestE2EAbortDiscardedResumesRetiringRemoteUnderExactLease(t *testing.T) {
	fixture := newGitFixture(t)
	record := strandAtRetiringRemoteDisposition(t, fixture, "abort-retiring-remote", string(AbortDiscarded))
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
	record := strandAtRetiringRemoteDisposition(t, fixture, "abort-retiring-advanced", string(AbortDiscarded))
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
