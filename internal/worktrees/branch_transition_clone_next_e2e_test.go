//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

//nolint:paralleltest // newGitFixture configures process-wide Git and WB environment.
func TestE2EBranchTransitionCloneRollbackAndRepairRefusals(t *testing.T) {
	for _, name := range []string{"repair-refusal-restores", "occupied-rollback", "rollback-repair-refusal"} {
		//nolint:paralleltest // Each case calls newGitFixture, which changes process-wide Git/WB environment.
		t.Run(name, func(t *testing.T) {
			fixture := newGitFixture(t)
			source := fixture.canonical
			destination := filepath.Join(t.TempDir(), "host", "acme", "app")
			linked := filepath.Join(t.TempDir(), "linked")
			gitTest(t, source, "worktree", "add", "-b", "feature/transition", linked)
			before := gitTestOutput(t, source, "rev-parse", "HEAD")
			calls := []string{}
			held := linked + "-held"
			query := func(ctx context.Context, repo string, args ...string) (string, error) {
				calls = append(calls, repo)
				if len(calls) == 1 {
					if err := os.Rename(linked, held); err != nil {
						t.Fatal(err)
					}
					if name == "occupied-rollback" {
						if err := os.Mkdir(source, 0700); err != nil {
							t.Fatal(err)
						}
					}
				} else if name == "repair-refusal-restores" {
					if err := os.Rename(held, linked); err != nil {
						t.Fatal(err)
					}
				}
				return git(ctx, repo, args...)
			}
			_, err := applyCloneMoveWithQuery(context.Background(), source, destination, query)
			if err == nil || !strings.Contains(err.Error(), "repair worktree registration") {
				t.Fatalf("native first repair refusal = %v", err)
			}
			switch name {
			case "occupied-rollback":
				if !strings.Contains(err.Error(), "restore clone") || len(calls) != 1 {
					t.Fatalf("occupied restore error/order = %v, %v", err, calls)
				}
				if got := gitTestOutput(t, destination, "rev-parse", "HEAD"); got != before {
					t.Fatalf("stranded clone changed HEAD: %q", got)
				}
			case "rollback-repair-refusal":
				if !strings.Contains(err.Error(), "after rollback") || !reflect.DeepEqual(calls, []string{destination, source}) {
					t.Fatalf("native rollback repair error/order = %v, %v", err, calls)
				}
			case "repair-refusal-restores":
				if strings.Contains(err.Error(), "after rollback") || !reflect.DeepEqual(calls, []string{destination, source}) {
					t.Fatalf("restore sequence = %v, %v", err, calls)
				}
				if got := gitTestOutput(t, source, "rev-parse", "HEAD"); got != before {
					t.Fatalf("restored clone changed HEAD: %q", got)
				}
			}
			if name != "occupied-rollback" {
				if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("destination survived rollback: %v", err)
				}
			}
		})
	}
}

//nolint:paralleltest // newGitFixture configures process-wide Git and WB environment.
func TestE2EBranchTransitionReconcileRepairRefusals(t *testing.T) {
	for _, after := range []bool{false, true} {
		name := "repair-command"
		if after {
			name = "post-repair-verification"
		}
		//nolint:paralleltest // Each case calls newGitFixture, which changes process-wide Git/WB environment.
		t.Run(name, func(t *testing.T) {
			fixture := newGitFixture(t)
			source := fixture.canonical
			linked := filepath.Join(source, ".worktrees", "nested")
			gitTest(t, source, "worktree", "add", "-b", "feature/nested", linked)
			destination := filepath.Join(t.TempDir(), "moved")
			if err := os.Rename(source, destination); err != nil {
				t.Fatal(err)
			}
			movedLinked := filepath.Join(destination, ".worktrees", "nested")
			hit := false
			query := func(ctx context.Context, repo string, args ...string) (string, error) {
				hit = true
				damage := func() {
					if err := os.WriteFile(filepath.Join(movedLinked, ".git"), []byte("gitdir: /missing/transition-admin\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if !after {
					// The actual repair path no longer exists; Git must refuse it.
					if err := os.Rename(movedLinked, movedLinked+"-held"); err != nil {
						t.Fatal(err)
					}
				}
				out, err := git(ctx, repo, args...)
				if after && err == nil {
					damage()
				}
				return out, err
			}
			status, _, err := reconcileClonePlacementWithQuery(context.Background(), destination, source, true, query)
			want := "repair worktree registration"
			if after {
				want = "stranded"
			}
			if !hit || status != "" || err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("native reconciliation boundary = %q, %v, hit=%v", status, err, hit)
			}
		})
	}
}
