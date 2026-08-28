package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/canonicalrescue"
)

// dirtyCanonicalClone reproduces the shape that nearly lost a finished lesson:
// a modified tracked file plus an untracked document that exists nowhere else.
func dirtyCanonicalClone(t *testing.T, checkouts checkoutFixture) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(checkouts.Canonical, "README.md"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lesson := filepath.Join(checkouts.Canonical, "spec", "lessons", "unlanded", "README.md")
	if err := os.MkdirAll(filepath.Dir(lesson), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lesson, []byte("a finished lesson\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestWorktreeRescueReportsWithoutChangingAnything pins the default. A rescue
// that acted by default would be a discard waiting to happen.
func TestWorktreeRescueReportsWithoutChangingAnything(t *testing.T) {
	checkouts := newCheckoutFixture(t)
	dirtyCanonicalClone(t, checkouts)
	before := runCommand(t, checkouts.Canonical, "git", "status", "--porcelain=v1")

	code, stdout, _ := runCheckoutCommand(t, "--projects-root", checkouts.ProjectsRoot, "worktree", "rescue", checkouts.Canonical)
	if code != exitFindings {
		t.Fatalf("a dirty clone reported exit %d, want %d", code, exitFindings)
	}
	if !strings.Contains(stdout, "Nothing has been changed") {
		t.Fatalf("the report does not say it changed nothing:\n%s", stdout)
	}
	if !strings.Contains(stdout, "wb worktree rescue") || !strings.Contains(stdout, "--apply") {
		t.Fatalf("the report does not name the remedy:\n%s", stdout)
	}
	if after := runCommand(t, checkouts.Canonical, "git", "status", "--porcelain=v1"); after != before {
		t.Fatalf("a report changed the clone:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if branches := runCommand(t, checkouts.Canonical, "git", "branch", "--list", "rescue/*"); branches != "" {
		t.Fatalf("a report created a branch: %s", branches)
	}
}

// TestWorktreeRescueApplyPreservesAndLeavesTheCloneDirty checks that the
// preserving step and the discarding step really are separate.
func TestWorktreeRescueApplyPreservesAndLeavesTheCloneDirty(t *testing.T) {
	checkouts := newCheckoutFixture(t)
	dirtyCanonicalClone(t, checkouts)
	code, stdout, stderr := runCheckoutCommand(t,
		"--projects-root", checkouts.ProjectsRoot, "worktree", "rescue", checkouts.Canonical,
		"--apply", "--branch", "rescue/one")
	if code != exitOK {
		t.Fatalf("apply exited %d: %s%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "captured onto rescue/one") {
		t.Fatalf("apply did not report the branch:\n%s", stdout)
	}
	if !strings.Contains(stdout, "still dirty on purpose") {
		t.Fatalf("apply did not say the clone was left alone:\n%s", stdout)
	}
	listing := runCommand(t, checkouts.Canonical, "git", "ls-tree", "-r", "--name-only", "rescue/one")
	if !strings.Contains(listing, "spec/lessons/unlanded/README.md") {
		t.Fatalf("the untracked lesson is not on the rescue branch:\n%s", listing)
	}
	if status := runCommand(t, checkouts.Canonical, "git", "status", "--porcelain=v1"); status == "" {
		t.Fatal("apply cleaned the clone; preserving and discarding must stay separate")
	}
}

// TestWorktreeRescueRestoreRequiresApply keeps the destructive flag from ever
// running on its own.
func TestWorktreeRescueRestoreRequiresApply(t *testing.T) {
	checkouts := newCheckoutFixture(t)
	dirtyCanonicalClone(t, checkouts)
	code, _, stderr := runCheckoutCommand(t,
		"--projects-root", checkouts.ProjectsRoot, "worktree", "rescue", checkouts.Canonical, "--restore")
	if code == exitOK || !strings.Contains(stderr, "--restore requires --apply") {
		t.Fatalf("--restore ran alone: exit %d, %s", code, stderr)
	}
	if status := runCommand(t, checkouts.Canonical, "git", "status", "--porcelain=v1"); status == "" {
		t.Fatal("the clone was cleaned by a refused invocation")
	}
}

// TestWorktreeRescueRestoreRefusesAnUnpushedBranch keeps the only destructive
// step behind a receipt.
func TestWorktreeRescueRestoreRefusesAnUnpushedBranch(t *testing.T) {
	checkouts := newCheckoutFixture(t)
	dirtyCanonicalClone(t, checkouts)
	code, _, stderr := runCheckoutCommand(t,
		"--projects-root", checkouts.ProjectsRoot, "worktree", "rescue", checkouts.Canonical,
		"--apply", "--restore", "--branch", "rescue/local-only")
	if code == exitOK {
		t.Fatal("restore ran against a branch that exists only on this machine")
	}
	if !strings.Contains(stderr, "only on this machine") {
		t.Fatalf("unexpected refusal: %s", stderr)
	}
	if status := runCommand(t, checkouts.Canonical, "git", "status", "--porcelain=v1"); status == "" {
		t.Fatal("the clone was cleaned despite the refusal")
	}
}

// TestWorktreeRescueFullJourney walks the whole path an operator takes: find,
// preserve, publish, then clean — and confirms the content survived on the
// remote, which is the only place a lost machine cannot take it from.
func TestWorktreeRescueFullJourney(t *testing.T) {
	checkouts := newCheckoutFixture(t)
	dirtyCanonicalClone(t, checkouts)

	code, stdout, _ := runCheckoutCommand(t, "--projects-root", checkouts.ProjectsRoot, "worktree", "rescue", "--fleet")
	if code != exitFindings || !strings.Contains(stdout, checkouts.Canonical) {
		t.Fatalf("the fleet sweep did not find the dirty clone: exit %d\n%s", code, stdout)
	}

	code, stdout, stderr := runCheckoutCommand(t,
		"--projects-root", checkouts.ProjectsRoot, "worktree", "rescue", checkouts.Canonical,
		"--apply", "--push", "--restore", "--branch", "rescue/journey")
	if code != exitOK {
		t.Fatalf("the full rescue exited %d: %s%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "is now clean") {
		t.Fatalf("the rescue did not report a clean clone:\n%s", stdout)
	}
	if status := runCommand(t, checkouts.Canonical, "git", "status", "--porcelain=v1"); status != "" {
		t.Fatalf("the clone is still dirty:\n%s", status)
	}
	remote := runCommand(t, checkouts.Origin, "git", "ls-tree", "-r", "--name-only", "rescue/journey")
	if !strings.Contains(remote, "spec/lessons/unlanded/README.md") {
		t.Fatalf("the rescued lesson never reached the remote:\n%s", remote)
	}

	code, stdout, _ = runCheckoutCommand(t, "--projects-root", checkouts.ProjectsRoot, "worktree", "rescue", "--fleet")
	if code != exitOK || !strings.Contains(stdout, "is clean") {
		t.Fatalf("the fleet sweep still reports work: exit %d\n%s", code, stdout)
	}
}

// TestWorktreeRescueFullJourneyPassesManagedPrePushGuard proves the published
// journey through the same hook installed on real canonical clones. A bare
// fixture with no hook cannot catch rescue trying to push from the deliberately
// dirty checkout and being rejected by its own worktree guard.
func TestWorktreeRescueFullJourneyPassesManagedPrePushGuard(t *testing.T) {
	binary := buildWB(t)
	checkouts := newCheckoutFixture(t)
	environment := append(wbUpgradeEnv(t.TempDir()), "WB_EXECUTABLE="+binary)
	installed := runWBUpgrade(t, binary, environment,
		"--projects-root", checkouts.ProjectsRoot, "hooks", "install", checkouts.Canonical)
	if installed.exitCode != exitOK {
		t.Fatalf("install managed hooks: exit=%d stdout=%s stderr=%s", installed.exitCode, installed.stdout, installed.stderr)
	}
	dirtyCanonicalClone(t, checkouts)
	captured := runWBUpgrade(t, binary, environment,
		"--projects-root", checkouts.ProjectsRoot, "worktree", "rescue", checkouts.Canonical,
		"--apply", "--branch", "rescue/managed-hook-journey")
	if captured.exitCode != exitOK {
		t.Fatalf("capture before managed-hook push: exit=%d stdout=%s stderr=%s", captured.exitCode, captured.stdout, captured.stderr)
	}
	rescueCommit := runCommand(t, checkouts.Canonical, "git", "rev-parse", "refs/heads/rescue/managed-hook-journey")
	runCommand(t, checkouts.Canonical, "git", "branch", "feature/not-a-rescue-push", rescueCommit)
	t.Setenv(canonicalrescue.PushBranchEnv, "rescue/managed-hook-journey")
	t.Setenv(canonicalrescue.PushCommitEnv, rescueCommit)
	output, pushErr := runUpgradeGit(checkouts.Canonical, environment,
		"push", "origin", "refs/heads/feature/not-a-rescue-push:refs/heads/feature/not-a-rescue-push")
	if pushErr == nil || !strings.Contains(output, "must publish only refs/heads/rescue/managed-hook-journey") {
		t.Fatalf("rescue attestation authorized a different ref: err=%v\n%s", pushErr, output)
	}

	result := runWBUpgrade(t, binary, environment,
		"--projects-root", checkouts.ProjectsRoot, "worktree", "rescue", checkouts.Canonical,
		"--apply", "--push", "--restore", "--branch", "rescue/managed-hook-journey")
	if result.exitCode != exitOK {
		t.Fatalf("managed-hook rescue: exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	if status := runCommand(t, checkouts.Canonical, "git", "status", "--porcelain=v1"); status != "" {
		t.Fatalf("managed-hook rescue left canonical dirty:\n%s", status)
	}
	remote := runCommand(t, checkouts.Origin, "git", "rev-parse", "refs/heads/rescue/managed-hook-journey")
	local := runCommand(t, checkouts.Canonical, "git", "rev-parse", "refs/heads/rescue/managed-hook-journey")
	if remote != local {
		t.Fatalf("managed-hook rescue remote=%s local=%s", remote, local)
	}
}

// TestWorktreeRescueRefusesALinkedWorktree keeps the command pointed at the
// checkout where uncommitted work is actually at risk.
func TestWorktreeRescueRefusesALinkedWorktree(t *testing.T) {
	checkouts := newCheckoutFixture(t)
	code, _, stderr := runCheckoutCommand(t, "--projects-root", checkouts.ProjectsRoot, "worktree", "rescue", checkouts.Worktree)
	if code == exitOK || !strings.Contains(stderr, "not a canonical clone") {
		t.Fatalf("a linked worktree was accepted: exit %d, %s", code, stderr)
	}
}
