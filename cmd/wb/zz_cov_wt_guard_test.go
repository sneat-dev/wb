package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCwWtWorktreeGuardPublishedCanonicalClone(t *testing.T) {
	seed := t.TempDir()
	projects := t.TempDir()
	clone := filepath.Join(projects, "acme", "app")
	cwCovCloneWithOrigin(t, seed, "app", clone)
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))

	stdout, stderr, err := cwCovExec(t, projects, newWorktreeGuardCmd, clone, "--published")
	if err != nil {
		t.Fatalf("guard --published on a published canonical clone: %v (stderr=%s)", err, stderr)
	}
	if !strings.Contains(stdout, "ok: canonical checkout ") {
		t.Fatalf("guard --published stdout = %q", stdout)
	}
	if !strings.Contains(stdout, "(fresh against origin/main") {
		t.Fatalf("guard --published did not report freshness: %q", stdout)
	}
	if _, _, err := cwCovExec(t, projects, newWorktreeGuardCmd, clone, "--published", "--quiet"); err != nil {
		t.Fatalf("guard --published --quiet: %v", err)
	}

	// A detached HEAD cannot be published anywhere; the command must still
	// complete and report the finding (or an explicit refusal).
	// A detached HEAD is refused before publication can even be judged: a
	// commit there is reachable from no branch.
	runGit(t, clone, "checkout", "--detach", "HEAD")
	if _, _, err := cwCovExec(t, projects, newWorktreeGuardCmd, clone, "--published"); err == nil {
		t.Fatal("guard --published on a detached HEAD must be refused")
	}
}

func TestCwWtWorktreeGuardAdmissionWarningOnUnrecordedWorktree(t *testing.T) {
	seed := t.TempDir()
	projects := t.TempDir()
	clone := filepath.Join(projects, "acme", "app")
	cwCovCloneWithOrigin(t, seed, "app", clone)
	home := filepath.Join(t.TempDir(), "wb-home")
	t.Setenv("WB_HOME", home)

	// A checkout placed at the resolver-recognized managed location with no
	// recorded instruction is exactly what --admission enforce refuses.
	managed := filepath.Join(home, "worktrees", "cw-wt-task", "acme", "app")
	if err := os.MkdirAll(filepath.Dir(managed), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, clone, "worktree", "add", "-b", "cw-wt-task", managed)

	// Without a manifest the checkout is refused outright, naming the remedy.
	_, _, err := cwCovExec(t, projects, newWorktreeGuardCmd, managed, "--admission", "enforce")
	if err == nil || !strings.Contains(err.Error(), "no WB manifest") {
		t.Fatalf("guard admission without a manifest = %v", err)
	}

	// Backfill gives it a manifest but never fabricates a prompt, which is
	// exactly the state --admission warn exists to surface.
	if _, _, err := cwCovExec(t, projects, newWorktreeBackfillCmd, "--apply"); err != nil {
		t.Fatalf("backfill before admission: %v", err)
	}
	// enforce refuses the same checkout outright.
	_, _, err = cwCovExec(t, projects, newWorktreeGuardCmd, managed, "--admission", "enforce")
	if err == nil {
		t.Fatal("guard --admission enforce after backfill must refuse an unrecorded effort")
	}

	// warn reports the same fact as a warning without refusing.
	_, stderr, err := cwCovExec(t, projects, newWorktreeGuardCmd, managed, "--admission", "warn")
	if err != nil {
		t.Fatalf("guard --admission warn after backfill: %v (stderr=%s)", err, stderr)
	}
	if !strings.Contains(stderr, "warning:") {
		t.Fatalf("guard admission stderr = %q", stderr)
	}

	// --quiet still prints the admission warning: warn mode exists to be seen.
	stdout, stderr, err := cwCovExec(t, projects, newWorktreeGuardCmd, managed, "--admission", "warn", "--quiet")
	if err != nil {
		t.Fatalf("guard --admission warn --quiet: %v", err)
	}
	if stdout != "" {
		t.Fatalf("guard --quiet stdout = %q", stdout)
	}
	if !strings.Contains(stderr, "warning:") {
		t.Fatalf("guard --quiet stderr = %q", stderr)
	}
}

func TestCwWtWorktreeGuardRefusesMisplacedWorktree(t *testing.T) {
	seed := t.TempDir()
	projects := t.TempDir()
	clone := filepath.Join(projects, "acme", "app")
	cwCovCloneWithOrigin(t, seed, "app", clone)
	external := filepath.Join(t.TempDir(), "external-checkout")
	runGit(t, clone, "worktree", "add", "-b", "external-branch", external)
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))

	// A linked checkout outside every recognized hierarchy is refused with a
	// remedy naming the command that creates a valid one.
	_, _, err := cwCovExec(t, projects, newWorktreeGuardCmd, external)
	if err == nil || !strings.Contains(err.Error(), "must be below a resolver-recognized") {
		t.Fatalf("guard on a misplaced worktree = %v", err)
	}
}

func TestCwWtWorktreeInfoOnMissingPath(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))
	if _, _, err := cwCovExec(t, projects, newWorktreeInfoCmd, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("worktree info on a missing path must fail")
	}
}

func TestCwWtWorktreeLogBackendErrors(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))
	missing := filepath.Join(t.TempDir(), "missing")

	// Each log verb propagates the backend's refusal for an unknown worktree.
	verbs := [][]string{
		{"init", missing, "--mode", "manual", "--initiator", "cwWt", "--prompt", "x"},
		{"steer", missing, "--mode", "manual", "--initiator", "cwWt", "--prompt", "x"},
		{"refresh", missing, "--mode", "manual", "--initiator", "cwWt"},
		{"integrate", missing, "--mode", "manual", "--initiator", "cwWt"},
		{"handoff", missing, "--mode", "manual", "--initiator", "cwWt", "--summary", "s", "--successor", "s"},
		{"recover", missing, "--mode", "manual", "--initiator", "cwWt"},
		{"sync", missing, "--mode", "manual", "--initiator", "cwWt"},
		{"archive", missing, "--mode", "manual", "--initiator", "cwWt"},
	}
	for _, args := range verbs {
		if _, _, err := cwCovExec(t, projects, newWorktreeWorkLogCmd, args...); err == nil {
			t.Errorf("log %s on a missing worktree returned nil", args[0])
		}
	}

	// checkpoint without --skip-remote still refuses an unknown worktree.
	if _, _, err := cwCovExec(t, projects, newWorktreeWorkLogCmd, "checkpoint", missing, "--mode", "manual", "--initiator", "cwWt"); err == nil {
		t.Error("log checkpoint on a missing worktree returned nil")
	}

	// set propagates the backend refusal too.
	if _, _, err := cwCovExec(t, projects, newWorktreeSetCmd, missing, "--prompt", "x"); err == nil {
		t.Error("worktree set on a missing worktree returned nil")
	}

	// An unreadable projects root is reported by the read-only log dump.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cwCovExec(t, blocker, newWorktreeWorkLogCmd, "show", missing); err == nil {
		t.Error("log show against an unreadable root returned nil")
	}
}
