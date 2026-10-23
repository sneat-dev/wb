//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

//nolint:paralleltest // The final native case uses newGitFixture, which changes process-wide Git environment.
func TestE2ERenameRollbackStopsAtUnprovedRefAndMoveBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name          string
		oldDeleted    bool
		remoteDeleted bool
		moved         bool
		newPathExists bool
		branch        string
		branchErr     error
		want          string
	}{
		{name: "missing canonical for old ref", oldDeleted: true},
		{name: "remote query fails", remoteDeleted: true, want: "inspect remote before recycle rollback"},
		{name: "moved checkout branch query fails", moved: true, branchErr: errors.New("injected branch query failure"), want: "injected branch query failure"},
		{name: "moved checkout cannot restore branch", moved: true, newPathExists: true, branch: "other", want: "restore old branch checkout"},
		{name: "moved checkout cannot reverse physical move", moved: true, newPathExists: true, branch: "old", want: "move failed recycle back to source"},
		{name: "new branch query fails", want: ""},
	} {
		//nolint:paralleltest // native rollback cases share process-wide Git environment with the fixture case.
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			oldPath := filepath.Join(root, "old")
			if err := os.Mkdir(oldPath, 0o700); err != nil {
				t.Fatal(err)
			}
			newPath := filepath.Join(root, "new")
			if tc.newPathExists {
				if err := os.Mkdir(newPath, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			plan := &renamePlan{
				entry: ListResult{Repository: "acme/app", CanonicalDir: filepath.Join(root, "missing-canonical"), WorktreeDir: oldPath,
					WorktreesRoot: root, Branch: "old", HeadSHA: "0123456789abcdef0123456789abcdef01234567"},
				result:           RenameResult{NewWorktreeDir: newPath, NewBranch: "new"},
				oldBranchDeleted: tc.oldDeleted, remoteDeleted: tc.remoteDeleted, moved: tc.moved,
			}
			ctx := context.Background()
			if tc.moved {
				fake := runnertest.New(t)
				fake.ExpectArgv([]string{"git", "-C", newPath, "branch", "--show-current"},
					runner.Result{CombinedOutput: tc.branch + "\n"}, tc.branchErr)
				ctx = withGitRunner(ctx, fake)
			}
			err := rollbackRenamePlan(ctx, filepath.Join(root, "home"), plan)
			if err == nil || (tc.want != "" && !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("rollback error=%v, want %q", err, tc.want)
			}
			if _, statErr := os.Stat(oldPath); statErr != nil {
				t.Fatalf("source changed after refusal: %v", statErr)
			}
		})
	}

	//nolint:paralleltest // newGitFixture changes process-wide Git environment for native replay.
	t.Run("old branch update-ref rejects invalid recorded SHA", func(t *testing.T) {
		fixture := newGitFixture(t)
		plan := &renamePlan{
			entry:            ListResult{Repository: "acme/app", CanonicalDir: fixture.canonical, WorktreeDir: t.TempDir(), Branch: "old", HeadSHA: "invalid-sha"},
			oldBranchDeleted: true,
		}
		if err := rollbackRenamePlan(context.Background(), filepath.Join(fixture.projectsRoot, ".wb"), plan); err == nil || !strings.Contains(err.Error(), "restore old branch") {
			t.Fatalf("invalid recorded SHA rollback error=%v", err)
		}
	})
}
