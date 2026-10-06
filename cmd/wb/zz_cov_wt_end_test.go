package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/streams"
	"github.com/spf13/cobra"
)

func TestCwWtGitStashCaptureAndNotesAndRetirer(t *testing.T) {
	root := t.TempDir()
	engine, err := worktreeEndEngine(root, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	// claimReleaser always reports the path it took.

	if got := engine.Claims.Release(root, "absent-task"); got == "" {
		t.Fatal("claimReleaser returned an empty outcome")
	}
}

func TestCwWtWorktreeEndInProcess(t *testing.T) {
	projects, _, _ := initGCFixture(t)
	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeEndCmd(&invocation{projectsRoot: projects}) }, "gc-cli")
	if err != nil {
		t.Fatalf("worktree end dry run: %v", err)
	}
	if !strings.Contains(stdout, "would end task gc-cli") || !strings.Contains(stdout, "nothing was changed") {
		t.Fatalf("worktree end stdout = %q", stdout)
	}

	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeEndCmd(&invocation{projectsRoot: projects}) }, "gc-cli", "--format", "json")
	if err != nil {
		t.Fatalf("worktree end json: %v", err)
	}
	if !strings.Contains(stdout, "\"task\"") {
		t.Fatalf("worktree end json stdout = %q", stdout)
	}

	// A task that does not exist is reported as an errfindings-free error.
	_, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeEndCmd(&invocation{projectsRoot: projects}) }, "absent-task")
	if err == nil || !strings.Contains(err.Error(), "has no worktrees") {
		t.Fatalf("worktree end of an absent task = %v", err)
	}
}

func TestCwWtWorktreeEndRefusesLiveLink(t *testing.T) {
	projects, _, worktree := initGCFixture(t)
	// A go.work with use entries is the guard that stops end (exit 2).
	if err := os.WriteFile(filepath.Join(worktree, streams.GoWorkFile), []byte("go 1.24\n\nuse ./local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeEndCmd(&invocation{projectsRoot: projects}) }, "gc-cli", "--apply")
	if code := exitCodeOf(t, err); code != exitUsage {
		t.Fatalf("worktree end with a live link exit = %d (%v)\n%s", code, err, stdout)
	}
	if err == nil || !strings.Contains(err.Error(), "go.work carries use entries") {
		t.Fatalf("live-link refusal = %v", err)
	}
}
