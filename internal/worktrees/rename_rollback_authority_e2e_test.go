//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

//nolint:paralleltest // newGitFixture changes process-wide Git environment for native replay.
func TestE2ERenameRollbackRefusesUnreadableFreshProjectionBeforeCompensation(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "projection-old",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	old := created[0]
	destination := filepath.Join(fixture.canonical, ".worktrees", "projection-new")
	bindReached := false
	outcome, renameErr := Rename(context.Background(), RenameOptions{
		ProjectsRoot: fixture.projectsRoot, OldTask: "projection-old", NewTask: "projection-new",
		Apply: true, DeleteRemote: true, WorkLog: WorkLogOptions{Model: "unknown"},
		beforeRenameBind: func(string) error {
			bindReached = true
			projectionDir := filepath.Join(destination, workLogProjectionDirectory)
			if err := os.MkdirAll(projectionDir, 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(projectionDir, workLogProjectionName), []byte("{"), 0o600); err != nil {
				return err
			}
			return errors.New("trigger rollback")
		},
	})
	if !bindReached {
		t.Fatalf("rename did not reach bind: result=%#v err=%v", outcome.Results, renameErr)
	}
	if _, err := os.Lstat(destination); err != nil {
		t.Fatalf("rollback mutated the destination after unreadable evidence: %v; result=%#v err=%v", err, outcome.Results, renameErr)
	}
	if gitRefExists(fixture.canonical, "refs/heads/"+old.Branch) {
		t.Fatal("rollback restored the old branch despite unreadable fresh claim evidence")
	}
	if renameErr == nil || !strings.Contains(renameErr.Error(), "decode work-log projection") {
		t.Fatalf("rollback result=%#v err=%v, want projection read refusal", outcome.Results, renameErr)
	}
}

//nolint:paralleltest // newGitFixture changes process-wide Git environment for native replay.
func TestE2ERenameRollbackPublishesRecordedSHAWhenLocalRefAdvances(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "remote-restore-old",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	old := created[0]
	oldHead := gitTestOutput(t, old.WorktreeDir, "rev-parse", "HEAD")
	gitTest(t, old.WorktreeDir, "push", "origin", old.Branch)
	advancedHead := gitTestOutput(t, fixture.canonical, "commit-tree", oldHead+"^{tree}", "-p", oldHead, "-m", "concurrent local advance")
	interleaved := false
	ctx := withCanonicalGitInterceptor(context.Background(), func(_ context.Context, args []string, run func() ([]byte, error)) ([]byte, error) {
		output, err := run()
		if err == nil && len(args) == 4 && args[0] == "update-ref" && args[1] == "refs/heads/"+old.Branch && args[2] == oldHead && args[3] == "" {
			gitTest(t, fixture.canonical, "update-ref", "refs/heads/"+old.Branch, advancedHead, oldHead)
			interleaved = true
		}
		return output, err
	})
	_, renameErr := Rename(ctx, RenameOptions{
		ProjectsRoot: fixture.projectsRoot, OldTask: "remote-restore-old", NewTask: "remote-restore-new",
		Apply: true, DeleteRemote: true, WorkLog: WorkLogOptions{Model: "unknown"},
		beforeRenameBind: func(string) error { return errors.New("trigger rollback") },
	})
	if !interleaved {
		t.Fatalf("rollback never restored the local branch; err=%v", renameErr)
	}
	if head := remoteHeadForTest(t, fixture.canonical, old.Branch); head != oldHead {
		t.Fatalf("rollback published mutable local ref %s instead of recorded %s; err=%v", head, oldHead, renameErr)
	}
	if renameErr == nil || !strings.Contains(renameErr.Error(), "trigger rollback") {
		t.Fatalf("rename error = %v, want injected failure", renameErr)
	}
}
