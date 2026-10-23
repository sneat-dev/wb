package main

import (
	"bytes"
	"github.com/sneat-dev/wb/internal/agentrun"
	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/checkoutmarker"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runAgentRootCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errout bytes.Buffer
	code := run(args, &out, &errout)
	return code, out.String(), errout.String()
}
func TestAgentCommandFamilyIsPublicAndDiscoverable(t *testing.T) {
	root := newRootCmd()
	for _, path := range [][]string{
		{"agent"}, {"agent", "dispatch"}, {"agent", "status"}, {"agent", "await"},
		{"agent", "list"}, {"agent", "logs"}, {"agent", "stop"},
	} {
		found, _, err := root.Find(path)
		if err != nil || found == nil {
			t.Fatalf("wb %s is not registered: %v", strings.Join(path, " "), err)
		}
		if found.CommandPath() != "wb "+strings.Join(path, " ") {
			t.Fatalf("wb %s resolved to %q", strings.Join(path, " "), found.CommandPath())
		}
	}

	// The machine-readable catalog is how a cold agent finds a command, so the
	// agent family must be reachable through it.
	code, stdout, stderr := runAgentRootCLI(t, "commands", "--search", "offload worker", "--format", "json")
	if code != exitOK {
		t.Fatalf("commands exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "wb agent dispatch") {
		t.Fatalf("the catalog does not surface wb agent dispatch:\n%s", stdout)
	}
}
func TestAgentDispatchDepsWiresBeforeAndAfterCreateHooks(t *testing.T) {
	root := t.TempDir()
	physical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(physical, ".wb")
	var stderr bytes.Buffer
	_, deps, err := newAgentService().PrepareDispatch(agentrun.DispatchRequest{ProjectsRoot: root, Stderr: &stderr, Request: agents.DispatchRequest{Base: "main"}})
	if err != nil {
		t.Fatalf("agentDispatchDeps: %v", err)
	}
	if err := worktreeCreateDependencies(testInvocation(t, root)).BeforeCreate(root, []string{"acme/app"}); err == nil {
		t.Fatal("worktree create adapter must preserve missing-canonical refusal")
	}
	if deps.Home != home {
		t.Fatalf("deps.Home = %q, want %q", deps.Home, home)
	}

	// BeforeCreate refreshes managed hooks for the repository about to be
	// created. A repository with no canonical clone yet is a real, exact
	// refusal - proving the closure reaches the real hook refresh rather than
	// a stub that always succeeds.
	if err := deps.BeforeCreate([]string{"acme/app"}); err == nil {
		t.Fatal("BeforeCreate against a nonexistent canonical clone must fail")
	}

	// AfterCreate writes the checkout marker beside the created worktree.
	worktreeDir := filepath.Join(root, "acme", "app")
	initTestRepository(t, worktreeDir)
	deps.AfterCreate([]string{"acme/app"}, []worktrees.CreateResult{{Repository: "acme/app", WorktreeDir: worktreeDir}})
	if _, statErr := os.Stat(filepath.Join(worktreeDir, checkoutmarker.FileName)); statErr != nil {
		t.Fatalf("AfterCreate did not write %s: %v\nstderr: %s", checkoutmarker.FileName, statErr, stderr.String())
	}
	// The actual worktree-create forwarding caller uses the same neutral operation.
	marker := filepath.Join(worktreeDir, checkoutmarker.FileName)
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	command := &cobra.Command{}
	command.SetErr(&stderr)
	markCreatedCheckouts(testInvocation(t, root), command, "main", []worktrees.CreateResult{{Repository: "acme/app", WorktreeDir: worktreeDir}})
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("worktree create adapter did not restore its marker", err)
	}

}
