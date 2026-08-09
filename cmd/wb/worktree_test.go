package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestWorktreeHelpExplainsCanonicalAndCentralLayout(t *testing.T) {
	command := newWorktreeCreateCmd()
	for _, wanted := range []string{
		"canonical clone must be clean",
		"pulls",
		"<wb-home>/worktrees/<task>/<owner>/<repository>",
		"WB_HOME",
		"--resume",
	} {
		if !strings.Contains(command.Long, wanted) {
			t.Errorf("worktree create help does not mention %q", wanted)
		}
	}
}

func TestWorktreeCleanupDefaultsToSafeDryRun(t *testing.T) {
	command := newWorktreeCleanupCmd()
	olderThan := command.Flags().Lookup("older-than")
	if olderThan == nil || olderThan.DefValue != (24*time.Hour).String() {
		t.Fatalf("--older-than default = %#v, want %s", olderThan, 24*time.Hour)
	}
	apply := command.Flags().Lookup("apply")
	if apply == nil || apply.DefValue != "false" {
		t.Fatalf("--apply default = %#v, want false", apply)
	}
	if command.Flags().Lookup("report-dir") == nil {
		t.Fatal("cleanup command has no --report-dir")
	}
	if err := command.Args(command, nil); err == nil || !strings.Contains(err.Error(), "--all-merged") {
		t.Fatalf("cleanup without selection error = %v", err)
	}
}

func TestWorktreeLifecycleHelpExplainsNetworkAndCleanupSafety(t *testing.T) {
	list := newWorktreeListCmd()
	for _, wanted := range []string{"only local Git data", "--github", "exact-head"} {
		if !strings.Contains(list.Long, wanted) {
			t.Errorf("worktree list help does not mention %q", wanted)
		}
	}
	cleanup := newWorktreeCleanupCmd()
	for _, wanted := range []string{"default is a dry-run", "recorded head match", "force-with-lease"} {
		if !strings.Contains(cleanup.Long, wanted) {
			t.Errorf("worktree cleanup help does not mention %q", wanted)
		}
	}
}

// TestWorktreeCleanupWarnsOnMalformedCandidateInsteadOfAborting is the CLI-level
// regression test for the "matchups renamed to competios" defect: a worktree
// whose on-disk repository-name segment no longer matches its canonical
// clone's current name (the leftover of a real GitHub repository rename)
// must not abort `wb worktree cleanup`. It must instead be reported as a
// clear warning on stderr while the command still exits 0.
func TestWorktreeCleanupWarnsOnMalformedCandidateInsteadOfAborting(t *testing.T) {
	root := t.TempDir()
	projects, _ := setUpMismatchedWorktreeFixture(t, root)

	previousProjectsRoot := projectsRoot
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })
	var stdout, stderr bytes.Buffer
	code := run([]string{"--projects-root", projects, "worktree", "cleanup", "--all-merged", "--non-interactive"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("cleanup with a malformed candidate must not fail the command: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "warning: cleanup skipped malformed candidate") ||
		!strings.Contains(stderr.String(), `"old-repo-name"`) ||
		!strings.Contains(stderr.String(), `"renamed-repo"`) {
		t.Fatalf("cleanup did not clearly warn about the malformed candidate: stderr=%s", stderr.String())
	}
}

// TestWorktreeCleanupFilterExcludesMalformedCandidateOutsideSelection proves
// the companion half of the same fix: --filter scopes which candidates are
// even validated, so a malformed candidate that --filter does not select
// produces no warning at all and cannot affect the run.
func TestWorktreeCleanupFilterExcludesMalformedCandidateOutsideSelection(t *testing.T) {
	root := t.TempDir()
	projects, _ := setUpMismatchedWorktreeFixture(t, root)

	previousProjectsRoot := projectsRoot
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })
	var stdout, stderr bytes.Buffer
	code := run([]string{"--projects-root", projects, "--filter", "unrelated", "worktree", "cleanup", "--all-merged", "--non-interactive"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("filtered cleanup must not fail: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), "warning:") {
		t.Fatalf("malformed candidate outside --filter must not be reported at all: stderr=%s", stderr.String())
	}
}

// setUpMismatchedWorktreeFixture creates a canonical repository and a linked
// worktree registered under a repository-name path segment that does not
// match it — the same shape a GitHub repository rename leaves behind — and
// points WB_HOME and related XDG state at the given root so the test never
// touches the real environment. It returns the projects root and WB home.
func setUpMismatchedWorktreeFixture(t *testing.T, root string) (projects, home string) {
	t.Helper()
	projects = filepath.Join(root, "projects")
	canonical := filepath.Join(projects, "acme", "renamed-repo")
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatal(err)
	}
	home = filepath.Join(root, "home")
	t.Setenv(wbhome.EnvOverride, home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))

	runGit := func(dir string, args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
	runGit(canonical, "init", "-b", "main")
	runGit(canonical, "config", "user.name", "WB Test")
	runGit(canonical, "config", "user.email", "wb-test@example.test")
	if err := os.WriteFile(filepath.Join(canonical, "README.md"), []byte("# renamed-repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(canonical, "add", "README.md")
	runGit(canonical, "commit", "-m", "initial")
	stale := filepath.Join(home, "worktrees", "stale-task", "acme", "old-repo-name")
	runGit(canonical, "worktree", "add", "-b", "codex/stale-task", stale, "main")
	return projects, home
}

func TestWorktreeCreateRejectsTraversalBeforeRefreshingExternalHooks(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	canonical := filepath.Join(projects, "acme", "app")
	external := filepath.Join(root, "evil")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(wbhome.EnvOverride, filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	prepareStaleManagedHook := func(repository string) (string, []byte) {
		t.Helper()
		if err := os.MkdirAll(repository, 0o755); err != nil {
			t.Fatal(err)
		}
		command := exec.Command("git", "-C", repository, "init", "-b", "main")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("init %s: %v\n%s", repository, err, output)
		}
		if _, err := hooks.Apply(hooks.ApplyOptions{
			RepoPath: repository, WBExecutable: hookExecutable(), ProjectsRoot: projects,
		}); err != nil {
			t.Fatal(err)
		}
		preCommit := filepath.Join(repository, ".git", "wb-hooks", "pre-commit")
		if err := os.Chmod(preCommit, 0o600); err != nil {
			t.Fatal(err)
		}
		contents, err := os.ReadFile(preCommit)
		if err != nil {
			t.Fatal(err)
		}
		return preCommit, contents
	}
	canonicalPreCommit, canonicalBefore := prepareStaleManagedHook(canonical)
	externalPreCommit, externalBefore := prepareStaleManagedHook(external)

	previousProjectsRoot := projectsRoot
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--projects-root", projects, "worktree", "create", "traversal", "acme/app", "../evil"}, &stdout, &stderr); code == exitOK {
		t.Fatalf("traversal create unexpectedly succeeded: stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "must be owner/name") {
		t.Fatalf("traversal rejection = %s", stderr.String())
	}
	for _, hook := range []struct {
		name   string
		path   string
		before []byte
	}{
		{name: "canonical", path: canonicalPreCommit, before: canonicalBefore},
		{name: "external", path: externalPreCommit, before: externalBefore},
	} {
		after, err := os.ReadFile(hook.path)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(hook.before) {
			t.Fatalf("traversal input rewrote %s managed hook", hook.name)
		}
		info, err := os.Stat(hook.path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("traversal input refreshed %s managed hook mode to %o", hook.name, info.Mode().Perm())
		}
	}
}

func TestWorktreeCreateRejectsCaseVariantDuplicateBeforeRefreshingManagedHook(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	canonical := filepath.Join(projects, "acme", "app")
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(wbhome.EnvOverride, filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	command := exec.Command("git", "-C", canonical, "init", "-b", "main")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("init canonical: %v\n%s", err, output)
	}
	if _, err := hooks.Apply(hooks.ApplyOptions{
		RepoPath: canonical, WBExecutable: hookExecutable(), ProjectsRoot: projects,
	}); err != nil {
		t.Fatal(err)
	}
	preCommit := filepath.Join(canonical, ".git", "wb-hooks", "pre-commit")
	if err := os.Chmod(preCommit, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(preCommit)
	if err != nil {
		t.Fatal(err)
	}
	previousProjectsRoot := projectsRoot
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--projects-root", projects, "worktree", "create", "duplicate", "acme/app", "ACME/app"}, &stdout, &stderr); code == exitOK {
		t.Fatalf("duplicate create unexpectedly succeeded: stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "duplicates case-insensitive identity") {
		t.Fatalf("duplicate rejection = %s", stderr.String())
	}
	after, err := os.ReadFile(preCommit)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("duplicate input refreshed managed hook")
	}
	info, err := os.Stat(preCommit)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("duplicate input refreshed managed hook mode to %o", info.Mode().Perm())
	}
}

// TestWorktreeCreateReportsCanonicalSyncFailureAsFindings is the regression
// test for a second bug found alongside a real production outage in the
// canonical `git pull --ff-only` step: the failure was reported to the shell
// as exit code 0. WB's own documented contract (see rootLongHelp) is that a
// command which ran and found a real problem exits 1 ("findings"), not 0
// ("success") — a caller, human or agent, that only checks the exit code
// would otherwise believe the worktree was created when it was not.
//
// This drives the real end-to-end path — worktrees.Create ->
// synchronizeCanonical -> gitCanonical -> the re-exec'd secure canonical Git
// helper -> a real sandboxed `git pull` — through run(), the same function
// main() uses, against a canonical clone whose origin can never fast-forward
// pull. That makes the pull failure genuine and reproducible without a
// network dependency, rather than a simulated error return.
func TestWorktreeCreateReportsCanonicalSyncFailureAsFindings(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	canonical := filepath.Join(projects, "acme", "app")
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(wbhome.EnvOverride, filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))

	runCanonicalGit := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", canonical}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
	if output, err := exec.Command("git", "-C", canonical, "init", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("init canonical: %v\n%s", err, output)
	}
	runCanonicalGit("config", "user.name", "WB Test")
	runCanonicalGit("config", "user.email", "wb-test@example.test")
	if err := os.WriteFile(filepath.Join(canonical, "README.md"), []byte("# app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCanonicalGit("add", "README.md")
	runCanonicalGit("commit", "-m", "initial")
	// An origin that cannot be fetched from makes `git pull --ff-only` fail for
	// a real, reproducible reason, without needing SSH or a reachable remote.
	runCanonicalGit("remote", "add", "origin", filepath.Join(root, "does-not-exist.git"))

	previousProjectsRoot := projectsRoot
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })
	var stdout, stderr bytes.Buffer
	code := run([]string{"--projects-root", projects, "worktree", "create", "sync-fail", "acme/app", "--non-interactive"}, &stdout, &stderr)
	if code != exitFindings {
		t.Fatalf("canonical sync failure exit code = %d, want %d (findings); stdout=%s stderr=%s",
			code, exitFindings, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "update canonical") {
		t.Fatalf("stderr does not explain the canonical sync failure: %s", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("a failed create wrote %q to stdout", stdout.String())
	}
}
