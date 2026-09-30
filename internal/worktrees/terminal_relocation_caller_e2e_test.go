package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

//nolint:paralleltest // newGitFixture sets process environment while creating its isolated repository
func TestE2ETerminalRelocationRejectsUnreadableTerminal(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "terminal-relocation-corrupt",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	if _, err := LogFinalize(context.Background(), LogFinalizeOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Result: "success", Apply: true,
	}); err != nil {
		t.Fatal(err)
	}
	claim := readOrphanTestClaim(t, created[0].WorkLogPath)
	terminalPath := filepath.Join(filepath.Dir(filepath.Dir(created[0].WorkLogPath)), "terminals", claim.ClaimID+".json")
	if err := os.WriteFile(terminalPath, []byte("{broken terminal"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, terminal, err := claimForRelocation(fixture.home, worktree)
	if err == nil || terminal != nil || !strings.Contains(err.Error(), "invalid character") {
		t.Fatalf("corrupt terminal accepted: terminal=%#v err=%v", terminal, err)
	}
}
