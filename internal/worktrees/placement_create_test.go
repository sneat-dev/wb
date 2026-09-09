package worktrees

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCreateWorktreeAtPlacementPublishesConfiguredCheckout(t *testing.T) {
	for _, test := range []struct {
		name   string
		shared bool
	}{
		{name: "repository-local default"},
		{name: "explicit shared root", shared: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGitFixture(t)
			sharedRoot := ""
			if test.shared {
				configHome := t.TempDir()
				sharedRoot = filepath.Join(t.TempDir(), "shared-worktrees")
				t.Setenv("XDG_CONFIG_HOME", configHome)
				mustWriteBranchConfig(t, filepath.Join(configHome, "wb", "worktrees.yaml"), "version: 1\nworktrees:\n  root: "+sharedRoot+"\n")
			}
			base := gitTestOutput(t, fixture.canonical, "rev-parse", "origin/main")
			placement, err := ResolveWorktreePlacement(context.Background(), fixture.canonical, base)
			if err != nil {
				t.Fatal(err)
			}
			created, err := CreateWorktreeAtPlacement(context.Background(), fixture.canonical, placement, "placement-create", "acme/app", "wb/placement-create", "main", base)
			if err != nil {
				t.Fatal(err)
			}
			defer created.Close()
			want := filepath.Join(fixture.canonical, ".worktrees", "placement-create")
			if test.shared {
				resolved, resolveErr := resolveSharedWorktreesRoot(sharedRoot)
				if resolveErr != nil {
					t.Fatal(resolveErr)
				}
				want = filepath.Join(resolved, "placement-create", "acme", "app")
			}
			if created.Path != want {
				t.Fatalf("created worktree = %q, want %q", created.Path, want)
			}
			if got := gitTestOutput(t, created.Path, "branch", "--show-current"); got != "wb/placement-create" {
				t.Fatalf("worktree branch = %q", got)
			}
			if status := gitTestOutput(t, fixture.canonical, "status", "--porcelain"); status != "" {
				t.Fatalf("canonical status = %q", status)
			}
		})
	}
}
