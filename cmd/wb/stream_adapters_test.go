package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStreamWorktreesPlansLocalAndConfiguredSharedPaths(t *testing.T) {
	projectsRoot := t.TempDir()
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	adapter := streamWorktrees{projectsRoot: projectsRoot}

	local, err := adapter.PlannedWorktree("stream-paths", "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	wantLocal := filepath.Join(projectsRoot, "acme", "app", ".worktrees", "stream-paths")
	if local != wantLocal {
		t.Fatalf("local planned path = %q, want %q", local, wantLocal)
	}

	sharedRoot := filepath.Join(t.TempDir(), "shared-worktrees")
	configPath := filepath.Join(configHome, "wb", "worktrees.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("version: 1\nworktrees:\n  root: "+sharedRoot+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	shared, err := adapter.PlannedWorktree("stream-paths", "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	sharedParent, err := filepath.EvalSymlinks(filepath.Dir(sharedRoot))
	if err != nil {
		t.Fatal(err)
	}
	wantShared := filepath.Join(sharedParent, filepath.Base(sharedRoot), "stream-paths", "acme", "app")
	if shared != wantShared {
		t.Fatalf("shared planned path = %q, want %q", shared, wantShared)
	}
}

func TestStreamWorktreesRefusesAnInvalidConfiguredRoot(t *testing.T) {
	projectsRoot := t.TempDir()
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configPath := filepath.Join(configHome, "wb", "worktrees.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("version: 1\nworktrees:\n  root: relative/root\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&streamWorktrees{projectsRoot: projectsRoot}).PlannedWorktree("stream-paths", "acme/app"); err == nil {
		t.Fatal("invalid user worktree root planned a stream checkout")
	}
}
