package worktrees

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCreateWorktreeAtPlacementPublishesConfiguredCheckout(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		store    string
		shared   bool
		wantPath func(fixture *gitFixture, sharedRoot string) string
	}{
		{
			name:  "central default",
			store: "",
			wantPath: func(fixture *gitFixture, _ string) string {
				return filepath.Join(fixture.projectsRoot, ".worktrees", "placement-create", "acme", "app")
			},
		},
		{
			name:  "repository-local mode",
			store: "repository-local",
			wantPath: func(fixture *gitFixture, _ string) string {
				return filepath.Join(fixture.canonical, ".worktrees", "placement-create")
			},
		},
		{
			name: "explicit shared root", shared: true,
			wantPath: func(_ *gitFixture, sharedRoot string) string {
				resolved, err := resolveSharedWorktreesRoot(sharedRoot)
				if err != nil {
					panic(err)
				}
				return filepath.Join(resolved, "placement-create", "acme", "app")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGitFixture(t)
			sharedRoot := ""
			configHome := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", configHome)
			configPath := filepath.Join(configHome, "wb", "worktrees.yaml")
			switch {
			case test.shared:
				sharedRoot = filepath.Join(t.TempDir(), "shared-worktrees")
				mustWriteBranchConfig(t, configPath, "version: 1\nworktrees:\n  root: "+sharedRoot+"\n")
			case test.store != "":
				mustWriteBranchConfig(t, configPath, "version: 1\nworktrees:\n  store: "+test.store+"\n")
			}
			base := gitTestOutput(t, fixture.canonical, "rev-parse", "origin/main")
			placement, err := ResolveWorktreePlacement(context.Background(), fixture.projectsRoot, fixture.canonical, base)
			if err != nil {
				t.Fatal(err)
			}
			created, err := CreateWorktreeAtPlacement(context.Background(), fixture.projectsRoot, fixture.canonical, placement, "placement-create", "acme/app", "wb/placement-create", "main", base)
			if err != nil {
				t.Fatal(err)
			}
			defer created.Close()
			want := test.wantPath(fixture, sharedRoot)
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
