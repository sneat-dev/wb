//go:build e2e

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestE2ELifecycleNextLocationRefusesGitRootMismatch(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	canonical := newLegacyClone(t, projects, "acme", "app")
	path := filepath.Join(canonical, "nested")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	inspection := lifecycleInspection{ctx: context.Background(), projectsRoot: projects, worktree: path, base: "main", task: "task"}
	if err := inspection.locate(); err == nil || !strings.Contains(err.Error(), "has Git root "+canonical) {
		t.Fatalf("nested Git root admitted: %+v %v", inspection, err)
	}
	if entries, err := os.ReadDir(path); err != nil || len(entries) != 0 {
		t.Fatalf("inspection published metadata: %v %v", entries, err)
	}
}

func TestE2ELifecycleNextLocationRechecksNativeCommonDirectory(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"missing_gitdir", "foreign_common"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			projects := t.TempDir()
			canonical := newLegacyClone(t, projects, "acme", "app")
			foreign := newLegacyClone(t, projects, "other", "app")
			root := filepath.Join(canonical, ".worktrees")
			path := filepath.Join(root, "task")
			gitTest(t, canonical, "worktree", "add", "-b", "lifecycle-location", path, "main")
			pointer := filepath.Join(path, ".git")
			target := filepath.Join(foreign, ".git")
			if boundary == "missing_gitdir" {
				target = filepath.Join(t.TempDir(), "missing-gitdir")
			}
			successor := []byte("gitdir: " + target + "\n")
			observed := false
			observer := &lifecycleNextNativeGitObservation{Runner: runner.New(), after: func(_ string, args []string, _ runner.Result, err error) {
				if !observed && err == nil && reflect.DeepEqual(args, []string{"-C", path, "rev-parse", "--path-format=absolute", "--git-common-dir"}) {
					observed = true
					if err := os.WriteFile(pointer, successor, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}}
			inspection := lifecycleInspection{ctx: withGitRunner(context.Background(), observer), projectsRoot: projects, worktree: path, base: "main", task: "task", layout: wbhome.Layout{WorktreesRoot: root, Local: true}}
			err := inspection.locate()
			if !observed || err == nil {
				t.Fatalf("native common-directory drift admitted: %+v %v observed=%v", inspection, err, observed)
			}
			if boundary == "foreign_common" && !strings.Contains(err.Error(), "belongs to "+target+", not "+canonical) {
				t.Fatalf("foreign common refusal=%v", err)
			}
			if boundary == "missing_gitdir" && strings.Contains(err.Error(), "derive linked worktree identity") {
				t.Fatalf("fixture failed before final native recheck: %v", err)
			}
			if data, err := os.ReadFile(pointer); err != nil || string(data) != string(successor) {
				t.Fatalf("refusal rewrote attacker-owned pointer: %q %v", data, err)
			}
			if inspection.result.WorktreeDir != "" {
				t.Fatalf("failed location published successful state: %+v", inspection.result)
			}
		})
	}
}

func TestE2ELifecycleNextLocationRetainsNativeNonRepositoryRefusal(t *testing.T) {
	t.Parallel()
	path := t.TempDir()
	_, control := git(context.Background(), path, "rev-parse", "--show-toplevel")
	if control == nil {
		t.Fatal("plain native directory unexpectedly admitted as Git root")
	}
	inspection := lifecycleInspection{ctx: context.Background(), worktree: path, base: "main", task: "task"}
	err := inspection.locate()
	if err == nil || !strings.Contains(err.Error(), "inspect "+path) || !strings.Contains(err.Error(), control.Error()) || inspection.result.WorktreeDir != "" {
		t.Fatalf("native nonrepository refusal=%+v %v control=%v", inspection.result, err, control)
	}
	if entries, err := os.ReadDir(path); err != nil || len(entries) != 0 {
		t.Fatalf("refusal published repository metadata: %v %v", entries, err)
	}
}
