package worktrees

import (
	"context"
	"testing"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionpark"
)

func preparePushedParkedWorktree(t *testing.T, fixture *gitFixture, worktree string) string {
	t.Helper()
	useIdentityRemote(t, fixture, worktree)
	branch := gitTestOutput(t, worktree, "branch", "--show-current")
	gitTest(t, worktree, "push", "origin", branch)
	return branch
}

func captureParkedWorktreeMember(t *testing.T, fixture *gitFixture, worktree string, source session.Record, branch string) (GuardResult, sessionpark.Worktree) {
	t.Helper()
	guard, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot, Admission: AdmissionEnforce})
	if err != nil {
		t.Fatal(err)
	}
	member, err := CaptureParkedSessionWorktree(context.Background(), fixture.projectsRoot, ListResult{
		Repository: "acme/app", CanonicalDir: guard.CanonicalDir, WorktreeDir: worktree,
		WorktreesRoot: guard.WorktreesRoot, Branch: branch,
	}, source)
	if err != nil {
		t.Fatal(err)
	}
	return guard, member
}
