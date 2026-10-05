//go:build e2e

package orchestrate

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

//nolint:paralleltest // Native inventory reads legacy HOME and configured XDG roots; this TOP owns both via t.Setenv.
func TestE2EPRCreateWorktreeResolutionNativeInventory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	ctx := context.Background()
	//nolint:paralleltest // Parent's process-wide private inventory environment must remain held through native List.
	t.Run("fatal directory inventory", func(t *testing.T) {
		root := t.TempDir()
		resolution, err := wbhome.Resolve(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(resolution.Write.WorktreesRoot, []byte("not a directory"), 0600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(resolution.Write.WorktreesRoot)
		if err != nil || info.IsDir() {
			t.Fatalf("physical inventory refusal precondition info=%v err=%v", info, err)
		}
		got, err := ResolvePullRequestCreateWorktree(ctx, root, "fatal-task")
		var pathErr *os.PathError
		if got != "" || !errors.As(err, &pathErr) || !strings.HasPrefix(err.Error(), `resolve task "fatal-task": read worktree tasks under `) || !strings.Contains(err.Error(), resolution.Write.WorktreesRoot) {
			t.Fatalf("got=%q err=%v", got, err)
		}
	})
	//nolint:paralleltest // Parent owns HOME/XDG while real Create, Guard, WorkLog and List establish inventory authority.
	t.Run("ambiguous managed task", func(t *testing.T) {
		fixture := newExplicitRootEngineFixture(t)
		other := filepath.Join(fixture.githubDir, "acme", "other")
		runEngineGit(t, fixture.githubDir, "clone", "--no-hardlinks", fixture.repository.CloneURL, other)
		runEngineGit(t, other, "config", "user.name", "WB Test")
		runEngineGit(t, other, "config", "user.email", "wb@example.test")
		const task = "pr-resolution-ambiguous"
		created, err := worktrees.Create(ctx, []string{"acme/app", "acme/other"}, worktrees.CreateOptions{ProjectsRoot: fixture.githubDir, Operation: task, Branch: "feature/pr-resolution", BranchChosen: true, Base: "main", WorkLog: worktrees.WorkLogOptions{Model: "unknown"}})
		if err != nil || len(created) != 2 {
			t.Fatalf("actual two-repository Create=%+v err=%v", created, err)
		}
		type evidence struct {
			path, head string
			bytes      []byte
		}
		records := []evidence{}
		for _, entry := range created {
			guard, err := worktrees.Guard(ctx, entry.WorktreeDir, worktrees.GuardOptions{ProjectsRoot: fixture.githubDir})
			if err != nil || guard.Kind != "linked" {
				t.Fatalf("actual Guard=%+v err=%v", guard, err)
			}
			view, err := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: entry.WorktreeDir})
			if err != nil || view.Manifest == nil || view.Claim == nil || view.Manifest.Repository != entry.Repository || view.Claim.Repository != entry.Repository || view.Claim.Lifecycle != "active" || view.Claim.ClaimPath == "" {
				t.Fatalf("actual custody view=%+v err=%v", view, err)
			}
			for _, path := range []string{filepath.Join(entry.WorktreeDir, ".wb", "local", "manifest.yaml"), view.Claim.ClaimPath} {
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				records = append(records, evidence{path: path, bytes: raw})
			}
			records = append(records, evidence{path: entry.WorktreeDir, head: runEngineGit(t, entry.WorktreeDir, "rev-parse", "HEAD")})
			resolved, err := ResolvePullRequestCreateWorktree(ctx, fixture.githubDir, entry.WorktreeDir)
			if err != nil || resolved != entry.WorktreeDir {
				t.Fatalf("native public path got=%q err=%v", resolved, err)
			}
		}
		entries, err := worktrees.List(ctx, worktrees.ListOptions{ProjectsRoot: fixture.githubDir, Task: task})
		if err != nil || len(entries) != 2 {
			t.Fatalf("actual inventory=%+v err=%v", entries, err)
		}
		repositories := []string{entries[0].Repository, entries[1].Repository}
		if !reflect.DeepEqual(repositories, []string{"acme/app", "acme/other"}) {
			t.Fatalf("actual inventory ordering=%q", repositories)
		}
		names := []string{}
		for _, entry := range entries {
			names = append(names, entry.Repository+" "+entry.WorktreeDir)
		}
		got, err := ResolvePullRequestCreateWorktree(ctx, fixture.githubDir, task)
		expected := `task "` + task + `" has more than one worktree (` + strings.Join(names, "; ") + `); pass a path instead`
		if got != "" || err == nil || err.Error() != expected {
			t.Fatalf("got=%q err=%v want=%q", got, err, expected)
		}
		missing := "pr-resolution-missing"
		got, err = ResolvePullRequestCreateWorktree(ctx, fixture.githubDir, missing)
		if got != "" || err == nil || err.Error() != `no worktree or task "`+missing+`" found` {
			t.Fatalf("missing got=%q err=%v", got, err)
		}
		for _, record := range records {
			if record.head != "" {
				if got := runEngineGit(t, record.path, "rev-parse", "HEAD"); got != record.head {
					t.Fatalf("HEAD changed for %s: %q -> %q", record.path, record.head, got)
				}
			} else {
				after, err := os.ReadFile(record.path)
				if err != nil || !bytes.Equal(after, record.bytes) {
					t.Fatalf("authority record changed %s: %v", record.path, err)
				}
			}
		}
	})
}
