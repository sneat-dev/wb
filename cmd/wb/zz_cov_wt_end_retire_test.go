package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestCwWtCleanupRetirerRetiresCleanWorktree(t *testing.T) {
	projects, _, worktree := initGCFixture(t)
	engine, factoryErr := worktreeEndEngine(projects, io.Discard)
	if factoryErr != nil {
		t.Fatal(factoryErr)
	}
	// A clean checkout integrated into origin/main is what cleanup can retire.
	if err := os.Remove(filepath.Join(worktree, "wip.txt")); err != nil {
		t.Fatal(err)
	}
	if err := engine.Retirer.Retire(context.Background(), projects, "gc-cli", "acme/app", worktree); err != nil {
		t.Fatalf("cleanupRetirer.Retire: %v", err)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("cleanupRetirer left the checkout behind: %v", err)
	}
}

func TestCwWtCleanupRetirerReportsUnretiredCandidate(t *testing.T) {
	projects, _, worktree := initGCFixture(t)
	engine, factoryErr := worktreeEndEngine(projects, io.Discard)
	if factoryErr != nil {
		t.Fatal(factoryErr)
	}
	// The gc fixture leaves the worktree dirty, so cleanup refuses it and the
	// retirer must say why rather than report success.
	err := engine.Retirer.Retire(context.Background(), projects, "gc-cli", "acme/app", worktree)
	if err == nil {
		t.Fatal("cleanupRetirer.Retire on a dirty checkout returned nil")
	}
	if !strings.Contains(err.Error(), "cleanup did not retire") && !strings.Contains(err.Error(), "no candidate") {
		t.Fatalf("cleanupRetirer error = %v", err)
	}
}

func TestCwWtWorktreeEndDryRunIsNotAFinding(t *testing.T) {
	projects, _, _ := initGCFixture(t)
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeEndCmd(&invocation{projectsRoot: projects}) }, "gc-cli"); err != nil {
		t.Fatalf("worktree end dry run: %v", err)
	}
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeEndCmd(&invocation{projectsRoot: projects}) }, "gc-cli", "--format", "json"); err != nil {
		t.Fatalf("worktree end dry run json: %v", err)
	}
}
