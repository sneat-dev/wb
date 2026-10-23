//go:build e2e

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

//nolint:paralleltest // newGitFixture changes process-wide Git environment for native replay.
func TestE2ERenameApplyRechecksAndClaimCutoverBeforePhysicalMove(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
	}{
		{name: "source becomes dirty", want: "worktree has local changes"},
		{name: "projection becomes malformed", want: "decode work-log projection"},
		{name: "private claim becomes malformed", want: "seal previous work log"},
		{name: "remote appears", want: "remote branch moved"},
		{name: "destination becomes occupied", want: "already exists"},
		{name: "authorized descriptor move succeeds"},
	} {
		//nolint:paralleltest // newGitFixture changes process-wide Git environment for every replay case.
		t.Run(tc.name, func(t *testing.T) {
			fixture := newGitFixture(t)
			oldTask := "recheck-old"
			newTask := "recheck-new"
			created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
				ProjectsRoot: fixture.projectsRoot, Operation: oldTask,
				WorkLog: WorkLogOptions{Model: "unknown"},
			})
			if err != nil {
				t.Fatal(err)
			}
			old := created[0]
			destination := filepath.Join(fixture.canonical, ".worktrees", newTask)
			hookReached, moveAuthorized := false, 0
			outcome, renameErr := Rename(context.Background(), RenameOptions{
				ProjectsRoot: fixture.projectsRoot, OldTask: oldTask, NewTask: newTask,
				Apply: true, DeleteRemote: true, WorkLog: WorkLogOptions{Model: "unknown"},
				afterPreApplyReservation: func() error {
					hookReached = true
					switch tc.name {
					case "source becomes dirty":
						return os.WriteFile(filepath.Join(old.WorktreeDir, "unknown.txt"), []byte("new data"), 0o600)
					case "projection becomes malformed":
						return os.WriteFile(filepath.Join(old.WorktreeDir, workLogProjectionDirectory, workLogProjectionName), []byte("{"), 0o600)
					case "private claim becomes malformed":
						_, _, path, claimErr := activeWorkLogClaim(filepath.Join(fixture.projectsRoot, ".wb"), old.WorktreeDir)
						if claimErr != nil {
							return claimErr
						}
						return os.WriteFile(path, []byte("{"), 0o600)
					case "remote appears":
						gitTest(t, old.WorktreeDir, "push", "origin", old.Branch)
					case "destination becomes occupied":
						return os.Mkdir(destination, 0o700)
					}
					return nil
				},
				afterWorktreeMoveAuthorization: func(string) { moveAuthorized++ },
			})
			if !hookReached {
				t.Fatalf("reservation hook was not reached: outcome=%#v err=%v", outcome, renameErr)
			}
			if tc.want == "" {
				if renameErr != nil || moveAuthorized != 1 || len(outcome.Results) != 1 || !outcome.Results[0].Applied {
					t.Fatalf("authorized move result=%#v authorized=%d err=%v", outcome.Results, moveAuthorized, renameErr)
				}
				if _, err := os.Stat(destination); err != nil {
					t.Fatalf("destination not published: %v", err)
				}
				return
			}
			if renameErr == nil || !strings.Contains(renameErr.Error(), tc.want) {
				t.Fatalf("rename error=%v, want %q; outcome=%#v", renameErr, tc.want, outcome.Results)
			}
			if moveAuthorized != 0 {
				t.Fatalf("source moved after %q refusal", tc.name)
			}
			if _, err := os.Stat(old.WorktreeDir); err != nil {
				t.Fatalf("source lost after refusal: %v", err)
			}
			if len(outcome.Results) != 1 || outcome.Results[0].Applied {
				t.Fatalf("failed rename report=%#v", outcome.Results)
			}
		})
	}
}
