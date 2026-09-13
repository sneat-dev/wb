package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/worktrees"
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

func TestStreamWorktreesPassesExactSquashReceiptToCleanup(t *testing.T) {
	previous := streamWorktreeCleanup
	t.Cleanup(func() { streamWorktreeCleanup = previous })
	var got worktrees.CleanupOptions
	streamWorktreeCleanup = func(_ context.Context, options worktrees.CleanupOptions) (worktrees.CleanupOutcome, error) {
		got = options
		return worktrees.CleanupOutcome{Results: []worktrees.CleanupResult{{
			ListResult: worktrees.ListResult{Repository: "acme/app"}, Applied: true,
		}}}, nil
	}
	receipt := &streams.SquashAbsorptionReceipt{
		Target: "main", SourceBranch: "stream/incidentius", SourceSHA: "41cd41cd41cd41cd41cd41cd41cd41cd41cd41cd",
		CandidateSHA: "8def8def8def8def8def8def8def8def8def8def", LandingSHA: "8d0e8d0e8d0e8d0e8d0e8d0e8d0e8d0e8d0e8d0e",
	}
	worktree := "/worktrees/incidentius/acme/app"
	if err := (&streamWorktrees{projectsRoot: t.TempDir()}).Remove(context.Background(), "incidentius", "acme/app", worktree, receipt); err != nil {
		t.Fatal(err)
	}
	if len(got.MergeReceiptProofs) != 1 {
		t.Fatalf("cleanup proofs = %#v, want one", got.MergeReceiptProofs)
	}
	proof := got.MergeReceiptProofs[0]
	if proof.Repository != "acme/app" || proof.Target != receipt.Target || proof.SourceTask != "incidentius" ||
		proof.SourceWorktree != worktree || proof.SourceBranch != receipt.SourceBranch || proof.SourceSHA != receipt.SourceSHA ||
		proof.CandidateSHA != receipt.CandidateSHA || proof.LandingSHA != receipt.LandingSHA {
		t.Fatalf("cleanup proof = %#v, want exact receipt mapping", proof)
	}
}
