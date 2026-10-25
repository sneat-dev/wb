package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func writeGuardRemoteConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Only a configured git remote store yields a clone path to exempt; anything
// else — no config, a hub store, an unresolvable path — exempts nothing.
func TestRemoteStoreClonePathResolvesOnlyAConfiguredGitStore(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	git := writeGuardRemoteConfig(t, "remote:\n  repo: team/state\n  machine: box\n")
	physicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := remoteStoreClonePath(git, root), filepath.Join(physicalRoot, ".wb", "remote-store", "team", "state"); got != want {
		t.Fatalf("git store clone path = %q, want %q", got, want)
	}
	if got := remoteStoreClonePath(filepath.Join(t.TempDir(), "missing.yaml"), root); got != "" {
		t.Fatalf("unconfigured clone path = %q, want empty", got)
	}
	hub := writeGuardRemoteConfig(t, "remote:\n  provider: hub\n  url: https://hub.example.test\n  machine: box\n")
	if got := remoteStoreClonePath(hub, root); got != "" {
		t.Fatalf("hub clone path = %q, want empty", got)
	}
	if got := remoteStoreClonePath(git, ""); got != "" {
		t.Fatalf("clone path without a projects root = %q, want empty", got)
	}
}

// The guard the hooks call receives the remote-store clone path, so the WB
// managed hooks in that clone stop refusing WB's own commits (wb#899).
func TestWorktreeGuardIsToldWhichCloneIsTheRemoteStore(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	config := writeGuardRemoteConfig(t, "remote:\n  repo: team/state\n  machine: box\n")
	var seen worktrees.GuardOptions
	guard := guardExemptingRemoteStore(func() string { return config }, func(_ context.Context, _ string, options worktrees.GuardOptions) (worktrees.GuardResult, error) {
		seen = options
		return worktrees.GuardResult{}, nil
	})
	if _, err := guard(context.Background(), ".", worktrees.GuardOptions{ProjectsRoot: root}); err != nil {
		t.Fatal(err)
	}
	if want := remoteStoreClonePath(config, root); seen.RemoteStoreClone != want || want == "" {
		t.Fatalf("RemoteStoreClone = %q, want %q", seen.RemoteStoreClone, want)
	}
	if seen.ProjectsRoot != root {
		t.Fatalf("ProjectsRoot = %q, want it passed through", seen.ProjectsRoot)
	}
}

// The exemption follows the store: the legacy canonical-path clone while a
// machine has not migrated (its fleet hooks still call the guard), and the
// clone inside .wb once it has (sneat-dev/wb#901).
func TestRemoteStoreGuardExemptionFollowsTheMigratedClone(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := writeGuardRemoteConfig(t, "remote:\n  repo: team/state\n  machine: box\n")
	legacy := filepath.Join(root, "team", "state")
	store := filepath.Join(root, ".wb", "remote-store", "team", "state")
	for _, clone := range []string{legacy, store} {
		if err := os.MkdirAll(filepath.Join(clone, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if got := remoteStoreClonePath(config, root); got != clone {
			t.Fatalf("exempted clone = %q, want %q", got, clone)
		}
	}
}
