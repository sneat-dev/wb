//go:build e2e

// Package defaultbranch's e2e tier: the genuinely git-behaviour cases that
// task-22 (spec/plans/coverage-to-100/README.md, decision 18) moved out of
// defaultbranch_test.go's unit tier once that file's package-var
// reassignment gave way to Engine + fakes_test.go's fakes. Each case here
// exercises the real `git` binary through a production-wired *Engine, never
// through a fake, and this file therefore never counts against
// internal/quality/testdata/unit_tier.pending: it starts a real process on
// purpose, gated behind the `e2e` build tag that lifts internal/runner's
// task-24 runtime guard (see internal/runner/guard.go).
package defaultbranch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/gitcli"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/testenv"
)

// e2eGitEngine returns an *Engine whose Git port is production-wired over
// real git (internal/gitcli + internal/runner). The other three ports are
// left nil: no case in this file calls gh, fleet discovery or the clock.
func e2eGitEngine() *Engine {
	return &Engine{Git: defaultBranchGitAdapter{client: gitcli.New(runner.New())}}
}

// scratchGit runs one real git subcommand in dir directly through
// os/exec, to build each case's fixture repository. It is deliberately not
// routed through the Engine under test: a fixture helper that used the same
// port the assertions exercise could not tell setup from the assertion, and
// this file already runs outside the runtime guard (e2e-tagged), so a
// direct exec.Command here is not one of task-8's tracked exec sites.
func scratchGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

// TestE2EDefaultBranchAtomicRenameRefsUsesConditionalTransaction proves the
// atomic-rename ref transaction, HEAD reattachment and attachment
// verification all behave as the pre-task-22 exec.CommandContext helpers
// did, against a real repository.
func TestE2EDefaultBranchAtomicRenameRefsUsesConditionalTransaction(t *testing.T) {
	t.Parallel()
	engine := e2eGitEngine()
	dir := t.TempDir()
	run := func(args ...string) string { return scratchGit(t, dir, args...) }
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	run("commit", "--allow-empty", "-qm", "initial")
	run("branch", "-M", "master")
	run("branch", "older")
	run("commit", "--allow-empty", "-qm", "advance")
	if ancestor, err := engine.Git.IsAncestor(context.Background(), dir, "older", "master"); err != nil || !ancestor {
		t.Fatalf("older ancestry = %t err=%v", ancestor, err)
	}
	if ancestor, err := engine.Git.IsAncestor(context.Background(), dir, "master", "older"); err != nil || ancestor {
		t.Fatalf("reverse ancestry = %t err=%v", ancestor, err)
	}
	sha := run("rev-parse", "master")
	older := run("rev-parse", "older")
	if err := engine.Git.AtomicRenameRefs(context.Background(), dir, "master", "main", older); err == nil {
		t.Fatal("stale atomic rename was accepted")
	}
	if err := engine.Git.AttachHead(context.Background(), dir, "master"); err != nil {
		t.Fatal(err)
	}
	run("update-ref", "refs/remotes/origin/main", sha)
	if err := engine.Git.AtomicRenameRefs(context.Background(), dir, "master", "main", sha); err != nil {
		t.Fatal(err)
	}
	if err := engine.Git.AttachHead(context.Background(), dir, "main"); err != nil {
		t.Fatal(err)
	}
	master, err := engine.Git.RefExists(context.Background(), dir, "refs/heads/master")
	if err != nil || master {
		t.Fatalf("master exists=%t err=%v", master, err)
	}
	main, err := engine.Git.RefExists(context.Background(), dir, "refs/heads/main")
	if err != nil || !main {
		t.Fatalf("main exists=%t err=%v", main, err)
	}
	if err := engine.verifyDefaultBranchAttachment(context.Background(), dir, "main", "main", sha); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dirty"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := engine.verifyDefaultBranchAttachment(context.Background(), dir, "main", "main", sha); err == nil {
		t.Fatal("dirty attachment was accepted")
	}
}

// TestE2EDefaultBranchGitHelpersReportExecutionFailures proves every Git
// port method, and verifyDefaultBranchAttachment, fails closed against a
// repository directory that does not exist.
func TestE2EDefaultBranchGitHelpersReportExecutionFailures(t *testing.T) {
	t.Parallel()
	engine := e2eGitEngine()
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := engine.Git.IsAncestor(context.Background(), missing, "master", "main"); err == nil {
		t.Fatal("ancestor check accepted missing repository")
	}
	if err := engine.Git.AtomicRenameRefs(context.Background(), missing, "master", "main", "0123456789012345678901234567890123456789"); err == nil {
		t.Fatal("atomic rename accepted missing repository")
	}
	if err := engine.Git.AttachHead(context.Background(), missing, "main"); err == nil {
		t.Fatal("attach accepted missing repository")
	}
	if _, err := engine.Git.RefExists(context.Background(), missing, "refs/heads/main"); err == nil {
		t.Fatal("ref check accepted missing repository")
	}
	if err := engine.verifyDefaultBranchAttachment(context.Background(), missing, "main", "main", "same"); err == nil {
		t.Fatal("attachment verification accepted missing repository")
	}
}

// TestE2EReconcileDefaultBranchCanonicalRealGitFastForwardsAndRenames
// exercises the complete successful local recovery against a real bare
// origin. The canonical clone starts on master at an ancestor of
// origin/main, exactly the state left by a previously renamed remote
// default branch.
//
//nolint:paralleltest // sets commit-identity env vars via t.Setenv, which Go's testing package forbids combining with t.Parallel
func TestE2EReconcileDefaultBranchCanonicalRealGitFastForwardsAndRenames(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	engine := e2eGitEngine()
	root := t.TempDir()
	seed := filepath.Join(root, "seed")
	origin := filepath.Join(root, "origin.git")
	canonical := filepath.Join(root, "canonical")
	for _, directory := range []string{seed, canonical} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	scratchGit(t, seed, "init", "-q", "-b", "master")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	scratchGit(t, seed, "add", "README.md")
	scratchGit(t, seed, "commit", "-qm", "base")
	base := scratchGit(t, seed, "rev-parse", "HEAD")
	testenv.InitBareRemoteForTest(t, origin)
	scratchGit(t, seed, "remote", "add", "origin", origin)
	scratchGit(t, seed, "push", "-q", "origin", "master:main")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("first\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	scratchGit(t, seed, "commit", "-am", "advance remote main")
	scratchGit(t, seed, "push", "-q", "origin", "master:main")
	remoteHead := scratchGit(t, origin, "rev-parse", "refs/heads/main")

	scratchGit(t, root, "clone", "-q", origin, canonical)
	scratchGit(t, canonical, "checkout", "-q", "-b", "master", base)
	scratchGit(t, canonical, "branch", "-D", "main")

	repository := Repository{Repository: "acme/app", Desired: "main"}
	entry := Canonical{Path: canonical}
	checkpoints := 0
	if err := engine.reconcileDefaultBranchCanonical(context.Background(), &repository, &entry, "master", remoteHead, func() error {
		checkpoints++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if entry.Disposition != "compliant" {
		t.Fatalf("disposition = %q, want compliant: %#v", entry.Disposition, entry)
	}
	if got, want := strings.Join(entry.Actions, " | "), "fast-forwarded local master to origin/main | renamed local master to main | set upstream to origin/main"; got != want {
		t.Fatalf("actions = %q, want %q", got, want)
	}
	if checkpoints != 5 {
		t.Fatalf("checkpoints = %d, want 5", checkpoints)
	}
	for _, ref := range []string{"HEAD", "main", "origin/main"} {
		if got := scratchGit(t, canonical, "rev-parse", ref); got != remoteHead {
			t.Fatalf("%s = %s, want %s", ref, got, remoteHead)
		}
	}
	if got := scratchGit(t, canonical, "branch", "--show-current"); got != "main" {
		t.Fatalf("current branch = %q, want main", got)
	}
	if got := scratchGit(t, canonical, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); got != "origin/main" {
		t.Fatalf("upstream = %q, want origin/main", got)
	}
	if output, err := exec.Command("git", "-C", canonical, "show-ref", "--verify", "--quiet", "refs/heads/master").CombinedOutput(); err == nil {
		t.Fatalf("local master remains after reconciliation: %s", output)
	}
	if got := scratchGit(t, origin, "rev-parse", "refs/heads/main"); got != remoteHead {
		t.Fatalf("origin/main changed from %s to %s", remoteHead, got)
	}
	if output, err := exec.Command("git", "-C", origin, "show-ref", "--verify", "--quiet", "refs/heads/master").CombinedOutput(); err == nil {
		t.Fatalf("origin master was created: %s", output)
	}
}

// TestE2EDefaultBranchGitRunsAndReportsFailures proves the generic Git.Run
// passthrough succeeds for an ordinary read and fails for an unresolvable
// ref, against a real repository.
func TestE2EDefaultBranchGitRunsAndReportsFailures(t *testing.T) {
	t.Parallel()
	engine := e2eGitEngine()
	dir := t.TempDir()
	command := exec.Command("git", "init", "-q")
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	if _, err := engine.Git.Run(context.Background(), dir, "status", "--porcelain"); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Git.Run(context.Background(), dir, "rev-parse", "--verify", "refs/heads/missing"); err == nil {
		t.Fatal("missing ref was accepted")
	}
}
