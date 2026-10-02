package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/testenv"
)

func TestParkedLocalCustodyResolvedWorktreeDirsHandlesNilAndCopiesIdentityMap(t *testing.T) {
	t.Parallel()
	var absent *ParkedLocalCustody
	if got := absent.ResolvedWorktreeDirs(); got != nil {
		t.Fatalf("nil custody dirs = %#v, want nil", got)
	}
	custody := &ParkedLocalCustody{members: []parkedLocalMember{
		{member: sessionpark.Worktree{WorktreeDir: "/recorded/one"}, resolvedWorktreeDir: "/current/one"},
		{member: sessionpark.Worktree{WorktreeDir: "/recorded/two"}, resolvedWorktreeDir: "/current/two"},
	}}
	dirs := custody.ResolvedWorktreeDirs()
	if len(dirs) != 2 || dirs["/recorded/one"] != "/current/one" || dirs["/recorded/two"] != "/current/two" {
		t.Fatalf("resolved dirs = %#v", dirs)
	}
	dirs["/recorded/one"] = "changed by caller"
	if got := custody.ResolvedWorktreeDirs()["/recorded/one"]; got != "/current/one" {
		t.Fatalf("caller changed retained custody mapping to %q", got)
	}
}

func TestCaptureParkedSessionWorktreeRejectsCredentialRemoteWithoutDisclosureOrMutation(t *testing.T) {
	fixture, worktree, source := newSessionCheckpointFixture(t, "park-capture-credential-remote")
	const secret = "parked-source-secret"
	gitTest(t, worktree, "remote", "set-url", "origin", "https://user:"+secret+"@example.invalid/acme/app.git")
	branch := gitTestOutput(t, worktree, "branch", "--show-current")
	headBefore := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	statusBefore := gitTestOutput(t, worktree, "status", "--porcelain=v1")
	eventsBefore, err := readLocalEvents(worktree)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot, Admission: AdmissionEnforce})
	if err != nil {
		t.Fatal(err)
	}
	_, err = CaptureParkedSessionWorktree(context.Background(), fixture.projectsRoot, ListResult{
		Repository: "acme/app", CanonicalDir: guard.CanonicalDir, WorktreeDir: worktree,
		WorktreesRoot: guard.WorktreesRoot, Branch: branch,
	}, source)
	if err == nil || !strings.Contains(err.Error(), "origin fetch remote is unsafe") {
		t.Fatalf("credential-bearing source remote error = %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("credential-bearing source remote was disclosed: %v", err)
	}
	if got := gitTestOutput(t, worktree, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("park refusal changed HEAD from %s to %s", headBefore, got)
	}
	if got := gitTestOutput(t, worktree, "status", "--porcelain=v1"); got != statusBefore {
		t.Fatalf("park refusal changed status from %q to %q", statusBefore, got)
	}
	eventsAfter, eventErr := readLocalEvents(worktree)
	if eventErr != nil {
		t.Fatal(eventErr)
	}
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("park refusal changed Work Log events from %d to %d", len(eventsBefore), len(eventsAfter))
	}
}

func useIdentityRemote(t *testing.T, fixture *gitFixture, worktree string) {
	t.Helper()
	remote := filepath.Join(filepath.Dir(fixture.remote), "acme", "app.git")
	if err := os.MkdirAll(filepath.Dir(remote), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, filepath.Dir(fixture.remote), "clone", "--bare", fixture.remote, remote)
	testenv.ConfigureGitAutoMaintenanceOff(t, remote)
	gitTest(t, worktree, "remote", "set-url", "origin", remote)
}
