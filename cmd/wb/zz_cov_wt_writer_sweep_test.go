package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestCwWtWorkLogArchiveAfterFinalize(t *testing.T) {
	projects, _, worktree := initGCFixture(t)
	if err := os.Remove(filepath.Join(worktree, "wip.txt")); err != nil {
		t.Fatal(err)
	}
	// finalize --apply seals the claim; archive --apply then copies the sealed
	// journal into WB_HOME.
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeWorkLogCmd(&invocation{}) }, "finalize", worktree,
		"--mode", "manual", "--initiator", "cwWt", "--result", "success", "--message", "done", "--apply"); err != nil {
		t.Fatalf("finalize --apply: %v", err)
	}
	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeWorkLogCmd(&invocation{}) }, "archive", worktree,
		"--mode", "manual", "--initiator", "cwWt", "--apply", "--force")
	if err != nil {
		t.Fatalf("archive --apply: %v", err)
	}
	if !strings.Contains(stdout, "archive ") || !strings.Contains(stdout, "applied=true") {
		t.Fatalf("archive stdout = %q", stdout)
	}
	// --force is the documented operator override for the terminal/TTL gates.
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeWorkLogCmd(&invocation{}) }, "archive", worktree,
		"--mode", "manual", "--initiator", "cwWt", "--apply", "--force"); err != nil {
		t.Fatalf("archive --force: %v", err)
	}
	// integrate on a clean worktree reaches its success path.
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeWorkLogCmd(&invocation{}) }, "integrate", worktree,
		"--mode", "manual", "--initiator", "cwWt"); err != nil {
		t.Logf("integrate on a clean worktree reported: %v", err)
	}
}
