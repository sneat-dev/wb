package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Finishing a named task must not leave its source branch on origin as
// invisible backlog — but on 2026-09-02 that rule was enforced from the flag
// shape alone, before WB looked at anything. A task whose remote branch had
// already been deleted by the merge that landed it still refused to clean,
// and every lane paid the same two commands to work around a branch that did
// not exist. These tests pin the rule to the evidence instead.

func TestNamedCleanupRefusesWhileTheOriginBranchStillExists(t *testing.T) {
	fixture, created, head, mergedAt := prepareMergedTask(t, "remote-still-present")
	installMergedPullRequestFixture(t, head, mergedAt)

	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "remote-still-present",
		Apply: true, RequireRemoteRetirement: true, OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 {
		t.Fatalf("results = %#v", outcome.Results)
	}
	result := outcome.Results[0]
	if result.Eligible || result.Applied {
		t.Fatalf("cleanup applied while origin/%s still exists: %#v", created.Branch, result)
	}
	if !strings.Contains(result.Reason, "origin/"+created.Branch) || !strings.Contains(result.Reason, "--remote") {
		t.Fatalf("reason = %q, want it to name the surviving branch and the flag that retires it", result.Reason)
	}
	if remoteHead, remoteErr := remoteBranchHead(context.Background(), fixture.canonical, created.Branch); remoteErr != nil || remoteHead == "" {
		t.Fatalf("refused cleanup must leave the remote branch intact: head=%q err=%v", remoteHead, remoteErr)
	}
}

func TestNamedCleanupProceedsWhenTheOriginBranchIsAlreadyGone(t *testing.T) {
	fixture, created, head, mergedAt := prepareMergedTask(t, "remote-already-gone")
	installMergedPullRequestFixture(t, head, mergedAt)

	// The exact 2026-09-02 state: the branch that carried the work is no
	// longer on origin, so there is nothing left for --remote to retire.
	gitTest(t, fixture.canonical, "push", "origin", "--delete", created.Branch)
	if remoteHead, remoteErr := remoteBranchHead(context.Background(), fixture.canonical, created.Branch); remoteErr != nil || remoteHead != "" {
		t.Fatalf("fixture head=%q err=%v, want the remote branch already deleted", remoteHead, remoteErr)
	}

	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "remote-already-gone",
		Apply: true, RequireRemoteRetirement: true, OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 {
		t.Fatalf("results = %#v", outcome.Results)
	}
	result := outcome.Results[0]
	if !result.Eligible || !result.Applied || !result.WorktreeGone || !result.BranchDeleted {
		t.Fatalf("cleanup did not finish a task whose remote branch was already gone: %#v (reason %q)", result, result.Reason)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, created.Branch); branchErr != nil || exists {
		t.Fatalf("local branch exists=%t err=%v", exists, branchErr)
	}
}

// Safety is decided before policy, so a candidate that is unsafe for an
// ordinary reason must still report that reason rather than the
// remote-retirement guidance, which would send an operator to the wrong fix.
func TestNamedCleanupReportsSafetyBeforeRemoteRetirement(t *testing.T) {
	fixture, created, head, mergedAt := prepareMergedTask(t, "unsafe-and-remote-present")
	installMergedPullRequestFixture(t, head, mergedAt)
	if err := os.WriteFile(filepath.Join(created.WorktreeDir, "dirty.txt"), []byte("uncommitted\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "unsafe-and-remote-present",
		Apply: true, RequireRemoteRetirement: true, OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 || outcome.Results[0].Eligible {
		t.Fatalf("results = %#v", outcome.Results)
	}
	if reason := outcome.Results[0].Reason; !strings.Contains(reason, "local changes") {
		t.Fatalf("reason = %q, want the dirty-worktree safety reason rather than remote-retirement guidance for %s", reason, created.Branch)
	}
}
