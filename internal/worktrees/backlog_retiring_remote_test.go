package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// strandAtRetiringRemote reproduces the state two real records sat in for ten
// and five days: the worktree and its registration are gone, the record is
// sealed at retiring_remote, and origin still holds the branch the interrupted
// run had already authorized itself to delete.
func strandAtRetiringRemote(t *testing.T, fixture *gitFixture, task string) lifecycleBacklogRecord {
	t.Helper()
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    task, WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := created[0]
	if err := os.WriteFile(filepath.Join(result.WorktreeDir, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, result.WorktreeDir, "add", "f.txt")
	gitTest(t, result.WorktreeDir, "commit", "-m", "work")
	head := gitTestOutput(t, result.WorktreeDir, "rev-parse", "HEAD")
	gitTest(t, result.WorktreeDir, "push", "-u", "origin", result.Branch)

	record := newLifecycleBacklogRecord(fixture.projectsRoot, ListResult{
		Task: task, Repository: "acme/app",
		CanonicalDir: fixture.canonical, WorktreesRoot: filepath.Join(fixture.home, "worktrees"),
		WorktreeDir: result.WorktreeDir, Branch: result.Branch, Base: "main",
		HeadSHA: head, RemoteHeadSHA: head,
	}, "removed")
	if err := persistLifecycleBacklog(fixture.home, &record, lifecycleStageRetiringRemote); err != nil {
		t.Fatal(err)
	}
	// Unregister and remove the checkout, which is what the interrupted run had
	// already done before it reached the remote step.
	gitTest(t, fixture.canonical, "worktree", "remove", "--force", result.WorktreeDir)
	return record
}

// A record sealed at retiring_remote must be discoverable. Before this it was
// dropped by the loader's stage switch, so `wb worktree cleanup <task>`
// answered "was not found" and the record could never be finished by any route.
func TestRetiringRemoteBacklogIsResumable(t *testing.T) {
	fixture := newGitFixture(t)
	record := strandAtRetiringRemote(t, fixture, "stranded-retiring-remote")

	found, err := loadResumableLifecycleBacklog(
		context.Background(), fixture.home, fixture.projectsRoot,
		[]string{filepath.Join(fixture.home, "worktrees")}, taskSelectionSet([]string{record.Task}), "", "removed",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Task != record.Task {
		t.Fatalf("a retiring_remote record must be discoverable, got %#v", found)
	}
}

// Resuming it finishes the remote retirement its own run authorized, under a
// lease against the SHA that run observed.
func TestResumeRetiringRemoteRetiresTheBranchUnderLease(t *testing.T) {
	fixture := newGitFixture(t)
	record := strandAtRetiringRemote(t, fixture, "finish-retiring-remote")
	if head := remoteHeadForTest(t, fixture.canonical, record.Branch); head == "" {
		t.Fatal("fixture did not publish the branch")
	}

	if err := resumeLifecycleBacklog(context.Background(), fixture.home, &record, true); err != nil {
		t.Fatalf("resume refused a record it authorized itself to finish: %v", err)
	}
	if head := remoteHeadForTest(t, fixture.canonical, record.Branch); head != "" {
		t.Fatalf("origin/%s survived the resume at %s", record.Branch, head)
	}
	if record.Stage != lifecycleStageComplete {
		t.Fatalf("record stage = %q, want complete", record.Stage)
	}
}

// The lease is the safety argument: work that landed after the interruption
// must never be discarded.
func TestResumeRetiringRemoteRefusesABranchThatAdvanced(t *testing.T) {
	fixture := newGitFixture(t)
	record := strandAtRetiringRemote(t, fixture, "advanced-retiring-remote")

	// Someone pushed to the branch after the interrupted run observed it.
	gitTest(t, fixture.canonical, "fetch", "origin", record.Branch)
	gitTest(t, fixture.canonical, "branch", "-f", "advance-probe", "FETCH_HEAD")
	worktree := filepath.Join(t.TempDir(), "advance")
	gitTest(t, fixture.canonical, "worktree", "add", worktree, "advance-probe")
	if err := os.WriteFile(filepath.Join(worktree, "later.txt"), []byte("later\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "add", "later.txt")
	gitTest(t, worktree, "commit", "-m", "landed after the interruption")
	gitTest(t, worktree, "push", "origin", "HEAD:"+record.Branch)

	err := resumeLifecycleBacklog(context.Background(), fixture.home, &record, true)
	if err == nil {
		t.Fatal("resume discarded a branch that advanced after the interruption")
	}
	if !strings.Contains(err.Error(), "advanced from") {
		t.Fatalf("refusal must name the advance, got %v", err)
	}
	if head := remoteHeadForTest(t, fixture.canonical, record.Branch); head == "" {
		t.Fatal("the advanced branch was deleted anyway")
	}
}

// Without --remote the resume must not delete anything on origin.
func TestResumeRetiringRemoteRequiresRemoteAuthority(t *testing.T) {
	fixture := newGitFixture(t)
	record := strandAtRetiringRemote(t, fixture, "no-remote-authority")
	err := resumeLifecycleBacklog(context.Background(), fixture.home, &record, false)
	if err == nil {
		t.Fatal("resume retired a remote branch without --remote")
	}
	if head := remoteHeadForTest(t, fixture.canonical, record.Branch); head == "" {
		t.Fatal("the branch was deleted without remote authority")
	}
	_ = time.Now
}

func remoteHeadForTest(t *testing.T, canonical, branch string) string {
	t.Helper()
	head, err := remoteBranchHead(context.Background(), canonical, branch)
	if err != nil {
		t.Fatal(err)
	}
	return head
}
