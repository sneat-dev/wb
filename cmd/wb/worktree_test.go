package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func writeOriginalPromptFixture(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "original-prompt.txt")
	if err := os.WriteFile(path, []byte(contents+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWorktreeHelpExplainsDefaultAndSharedLayout(t *testing.T) {
	command := newWorktreeCreateCmd()
	for _, wanted := range []string{
		"dirty or off-base canonical clone",
		"fetches",
		"without switching or updating any local branch",
		"<canonical-repository>/.worktrees/<task>",
		"WB_HOME",
		"--resume",
		"wb worktree merge <worktree...> --route auto --cleanup",
	} {
		if !strings.Contains(command.Long, wanted) {
			t.Errorf("worktree create help does not mention %q", wanted)
		}
	}
}

func TestWorktreeIdentityHelpAndCreatePreflightRequireExplicitModel(t *testing.T) {
	create := newWorktreeCreateCmd()
	for _, flag := range []string{"model", "cli", "provider"} {
		if create.Flags().Lookup(flag) == nil {
			t.Fatalf("create is missing --%s", flag)
		}
	}
	correct := newWorktreeCorrectIdentityCmd()
	for _, flag := range []string{"event-id", "actor", "reason", "model", "cli", "provider"} {
		if correct.Flags().Lookup(flag) == nil {
			t.Fatalf("correct-identity is missing --%s", flag)
		}
	}
	abort := newWorktreeAbortCmd()
	for _, flag := range []string{"model", "cli", "provider", "absorbed-by"} {
		if abort.Flags().Lookup(flag) == nil {
			t.Fatalf("abort is missing --%s", flag)
		}
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"--projects-root", t.TempDir(), "worktree", "create", "identity-required", "acme/app", "--original-prompt-file", writeOriginalPromptFixture(t, "identity")}, &stdout, &stderr)
	if code == exitOK || !strings.Contains(stderr.String(), "--model is required") {
		t.Fatalf("missing model CLI preflight = code %d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"--projects-root", t.TempDir(), "worktree", "abort", "identity-required", "--disposition", "handoff", "--successor", "next-run", "--apply"}, &stdout, &stderr)
	if code == exitOK || !strings.Contains(stderr.String(), "--model is required") {
		t.Fatalf("missing successor model CLI preflight = code %d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestWorktreeBranchFlagsRejectBeforeAnyWorkStarts(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "create exact and prefix", args: []string{"worktree", "create", "task", "--branch", "", "--branch-prefix", "team/"}, want: "cannot be used together"},
		{name: "create empty exact", args: []string{"worktree", "create", "task", "--branch", ""}, want: "must not be empty"},
		{name: "rename exact and prefix", args: []string{"worktree", "rename", "old", "new", "--branch", "", "--branch-prefix", "team/"}, want: "cannot be used together"},
		{name: "rename empty exact", args: []string{"worktree", "rename", "old", "new", "--branch", ""}, want: "must not be empty"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(test.args, &stdout, &stderr); code != exitUsage || !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("branch flag validation = code %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
		})
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
	resumeInterrupted := command.Flags().Lookup("resume-interrupted")
	if resumeInterrupted == nil || resumeInterrupted.DefValue != "false" {
		t.Fatalf("--resume-interrupted = %#v, want default false", resumeInterrupted)
	}
	if err := command.Args(command, nil); err == nil || !strings.Contains(err.Error(), "--all-merged") {
		t.Fatalf("cleanup without selection error = %v", err)
	}
	retireShells := command.Flags().Lookup("retire-shells")
	if retireShells == nil || retireShells.DefValue != "false" {
		t.Fatalf("--retire-shells = %#v, want default false", retireShells)
	}
	recoverStages := command.Flags().Lookup("recover-stages")
	if recoverStages == nil || recoverStages.DefValue != "false" {
		t.Fatalf("--recover-stages = %#v, want default false", recoverStages)
	}
}

func TestWorktreeCleanupAcceptsSeveralExplicitTaskNames(t *testing.T) {
	command := newWorktreeCleanupCmd()
	if err := command.Args(command, []string{"landed-app", "landed-lib"}); err != nil {
		t.Fatalf("cleanup should accept an exact set of task names: %v", err)
	}
}

// TestWorktreeCleanupRetireShellsRejectsIncompatibleSelectors proves
// --retire-shells sweeps every task on its own: it cannot be pointed at one
// named task or combined with --all-merged, both of which select worktrees
// to inspect rather than task directories to sweep.
func TestWorktreeCleanupRetireShellsRejectsIncompatibleSelectors(t *testing.T) {
	command := newWorktreeCleanupCmd()
	if err := command.Flags().Set("retire-shells", "true"); err != nil {
		t.Fatal(err)
	}
	if err := command.Args(command, []string{"some-task"}); err == nil || !strings.Contains(err.Error(), "--retire-shells") {
		t.Fatalf("--retire-shells with a task argument error = %v", err)
	}
	if err := command.Flags().Set("all-merged", "true"); err != nil {
		t.Fatal(err)
	}
	if err := command.Args(command, nil); err == nil || !strings.Contains(err.Error(), "--all-merged") {
		t.Fatalf("--retire-shells with --all-merged error = %v", err)
	}
}

// TestWorktreeCleanupRetireShellsPlansThenAppliesAnEmptyPreExistingShell is
// the CLI-level regression for the founder's fleet audit: 626 of 755 task
// directories had no real checkout under them, each left with an empty
// owner-namespace directory by a cleanup that predates the terminal-
// namespace-residue fix. `wb worktree cleanup --retire-shells` is the
// retroactive fix for those pre-existing shells, dry-run by default.
func TestWorktreeCleanupRetireShellsPlansThenAppliesAnEmptyPreExistingShell(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	worktreesRoot := filepath.Join(home, "worktrees")
	shellDir := filepath.Join(worktreesRoot, "old-task", "sneat-co")
	if err := os.MkdirAll(shellDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(wbhome.EnvOverride, home)
	t.Setenv(wbhome.EnvMigrationCompat, "")

	var stdout, stderr bytes.Buffer
	planArgs := []string{"worktree", "cleanup", "--retire-shells", "--projects-root", filepath.Join(root, "projects")}
	if code := run(planArgs, &stdout, &stderr); code != exitOK {
		t.Fatalf("run(%q) exit = %d, stdout=%s stderr=%s", planArgs, code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "would retire") || !strings.Contains(stdout.String(), "old-task") {
		t.Fatalf("dry-run stdout = %q, want it to name old-task as would-retire", stdout.String())
	}
	if _, statErr := os.Stat(shellDir); statErr != nil {
		t.Fatalf("dry run removed the shell: %v", statErr)
	}

	stdout.Reset()
	stderr.Reset()
	applyArgs := []string{"worktree", "cleanup", "--retire-shells", "--apply", "--projects-root", filepath.Join(root, "projects")}
	if code := run(applyArgs, &stdout, &stderr); code != exitOK {
		t.Fatalf("run(%q) exit = %d, stdout=%s stderr=%s", applyArgs, code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "retired") || !strings.Contains(stdout.String(), "old-task") || !strings.Contains(stdout.String(), "1 retired") {
		t.Fatalf("apply stdout = %q, want it to name old-task as retired", stdout.String())
	}
	if _, statErr := os.Stat(filepath.Join(worktreesRoot, "old-task")); !os.IsNotExist(statErr) {
		t.Fatalf("task directory still exists after --retire-shells --apply: err=%v", statErr)
	}
}

func TestWorktreeLifecycleHelpExplainsNetworkAndCleanupSafety(t *testing.T) {
	list := newWorktreeListCmd()
	for _, wanted := range []string{"only local Git data", "--github", "exact fetched origin-target", "versioned control-plane envelope", "lifecycle artifacts", "seven-day recent-history"} {
		if !strings.Contains(list.Long, wanted) {
			t.Errorf("worktree list help does not mention %q", wanted)
		}
	}
	cleanup := newWorktreeCleanupCmd()
	for _, wanted := range []string{"default is a dry-run", "freshly fetched exact", "awaiting_push", "force-with-lease", "before any remote or local deletion", "requires --remote", "implicit age window is zero", "--resume-interrupted", "conclusively dead", "--superseded-by", "trusted-reviewer receipt"} {
		if !strings.Contains(cleanup.Long, wanted) {
			t.Errorf("worktree cleanup help does not mention %q", wanted)
		}
	}
}

// An empty retired stage is terminal debris and never reaches the envelope as
// backlog: the read path purges it, reports it once in the purge receipt, and
// prints nothing on stderr. That silence is the requirement — one workstation
// carried 55 of these and printed 55 `info:` lines before every single table.
func TestWorktreeListPurgesEmptyStageSilentlyAndRecordsIt(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	projects := filepath.Join(root, "projects")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatal(err)
	}
	const task = "artifact-json"
	retired := filepath.Join(home, "worktrees", task, ".wb-retired-stage-6b0995eef65f84dace22d24df2644b32")
	if err := os.MkdirAll(retired, 0o700); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(home, "worktrees", task, ".wb-retired-lock-6b0995eef65f84dace22d24df2644b33")
	if err := os.WriteFile(lock, []byte("operation=artifact-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(wbhome.EnvOverride, home)
	previousProjectsRoot := projectsRoot
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })

	var stdout, stderr bytes.Buffer
	code := run([]string{"--projects-root", projects, "worktree", "list", task, "--format", "json"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("artifact-only JSON inventory exit=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var outcome worktrees.ListOutcome
	if err := json.Unmarshal(stdout.Bytes(), &outcome); err != nil {
		t.Fatalf("decode JSON inventory: %v\n%s", err, stdout.String())
	}
	if outcome.SchemaVersion != 1 || len(outcome.Results) != 0 ||
		len(outcome.Diagnostics) != 0 || len(outcome.Artifacts) != 0 {
		t.Fatalf("JSON control-plane envelope = %#v", outcome)
	}
	if len(outcome.Purged) != 2 {
		t.Fatalf("purge receipt = %#v, want the stage and the lock", outcome.Purged)
	}
	for _, path := range []string{retired, lock} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("%s survived the read path: %v", path, err)
		}
	}
	if strings.Contains(stderr.String(), "info:") {
		t.Fatalf("a terminal artefact must never become a per-invocation log line: %s", stderr.String())
	}
}

func TestNamedCleanupApplyReturnsFindingsForNonEmptyArtifactOnlyBacklog(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	projects := filepath.Join(root, "projects")
	const task = "artifact-only-blocked"
	retired := filepath.Join(home, "worktrees", task, ".wb-retired-stage-6b0995eef65f84dace22d24df2644b32")
	if err := os.MkdirAll(retired, 0o700); err != nil {
		t.Fatal(err)
	}
	evidence := filepath.Join(retired, "recovery-evidence")
	if err := os.WriteFile(evidence, []byte("preserve\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(wbhome.EnvOverride, home)
	previousProjectsRoot := projectsRoot
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })

	var stdout, stderr bytes.Buffer
	code := run([]string{"--projects-root", projects, "worktree", "cleanup", task, "--apply", "--remote", "--format", "json"}, &stdout, &stderr)
	if code != exitFindings {
		t.Fatalf("non-empty artifact-only cleanup exit=%d, want %d; stdout=%s stderr=%s", code, exitFindings, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "did not satisfy cleanup safety") ||
		!strings.Contains(stderr.String(), "non-empty") {
		t.Fatalf("artifact-only cleanup did not explain blocking backlog: %s", stderr.String())
	}
	if contents, err := os.ReadFile(evidence); err != nil || string(contents) != "preserve\n" {
		t.Fatalf("blocking evidence changed: contents=%q err=%v", contents, err)
	}
}

// Named terminal cleanup used to refuse without --remote from the flag shape
// alone, before WB inspected anything — so a task whose origin branch had
// already been deleted by the merge that landed it still refused. The
// requirement now lives in cleanupEligibility, where the remote branch WB
// actually observed decides it (see
// internal/worktrees/cleanup_remote_retirement_test.go). What the CLI still
// owns is that a named --apply run which retires nothing exits non-zero
// rather than reporting success.
func TestNamedCleanupApplyFailsWhenNothingWasRetired(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--projects-root", t.TempDir(), "worktree", "cleanup", "delivered-task", "--apply"}, &stdout, &stderr)
	if code == exitOK {
		t.Fatalf("named cleanup of an absent task succeeded: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "delivered-task") {
		t.Fatalf("named cleanup failure did not name the task: %q", stderr.String())
	}
}

func TestNamedCleanupApplySatisfiedUsesResolvedPhysicalTasks(t *testing.T) {
	logical := "logical-session-effort"
	physical := []string{
		"session-resume-resume-cli-m-002-bbbbbbbb",
		"session-resume-resume-cli-m-001-aaaaaaaa",
	}
	outcome := worktrees.CleanupOutcome{
		ResolvedTasks: physical,
		Results: []worktrees.CleanupResult{
			{ListResult: worktrees.ListResult{Task: physical[0]}, Applied: true, WorktreeGone: true, BranchDeleted: true},
			{ListResult: worktrees.ListResult{Task: physical[1]}, Applied: true, WorktreeGone: true, BranchDeleted: true},
		},
	}
	if !namedCleanupApplySatisfied([]string{logical}, outcome) {
		t.Fatal("logical selector whose resolved session-resume members all applied must satisfy named cleanup")
	}
	if namedCleanupApplySatisfied([]string{logical}, worktrees.CleanupOutcome{ResolvedTasks: physical}) {
		t.Fatal("logical selector with no applied members must not satisfy named cleanup")
	}
	if namedCleanupApplySatisfied([]string{"delivered-task"}, worktrees.CleanupOutcome{}) {
		t.Fatal("an unresolved named selector that applied nothing must not satisfy named cleanup")
	}
}

func TestWorktreeCreatePreflightsFormatAndPromptBeforeMutation(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "format", args: []string{"worktree", "create", "bad-format", "acme/app", "--format", "yaml"}, want: "unsupported format"},
		{name: "missing-required-prompt", args: []string{"worktree", "create", "missing-prompt", "acme/app", "--model", "unknown"}, want: "--original-prompt-file is required"},
		{name: "prompt", args: []string{"worktree", "create", "bad-prompt", "acme/app", "--model", "unknown", "--original-prompt-file", "missing-private-prompt.txt"}, want: "open original prompt"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, ".wb")
			t.Setenv("WB_HOME", home)
			args := append([]string{"--projects-root", filepath.Join(root, "projects")}, test.args...)
			var stdout, stderr bytes.Buffer
			if code := run(args, &stdout, &stderr); code == exitOK {
				t.Fatalf("invalid preflight succeeded: stdout=%s", stdout.String())
			}
			if !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), test.want)
			}
			task := test.args[2]
			if _, err := os.Stat(filepath.Join(home, "worktrees", task)); !os.IsNotExist(err) {
				t.Fatalf("invalid preflight created task state: %v", err)
			}
		})
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
	runGit(canonical, "worktree", "add", "-b", "feature/stale-task", stale, "main")
	return projects, home
}

func TestWorktreeRenameHelpExplainsRecyclingAndBranchSafety(t *testing.T) {
	command := newWorktreeRenameCmd()
	for _, wanted := range []string{
		"descriptor-relative", "no-replace", "git worktree repair", "node_modules",
		"always deleted", "--force", "dry-run",
	} {
		if !strings.Contains(command.Long, wanted) {
			t.Errorf("worktree rename help does not mention %q", wanted)
		}
	}
	if err := command.Args(command, []string{"only-one"}); err == nil {
		t.Fatal("rename must require exactly two positional arguments")
	}
	apply := command.Flags().Lookup("apply")
	if apply == nil || apply.DefValue != "false" {
		t.Fatalf("--apply default = %#v, want false", apply)
	}
	if deleteOldBranch := command.Flags().Lookup("delete-old-branch"); deleteOldBranch != nil {
		t.Fatalf("obsolete optional --delete-old-branch is still advertised: %#v", deleteOldBranch)
	}
}

// setUpRenameCLIFixture creates a real canonical repository with a working
// origin remote — `worktree create`'s canonical sync needs one to pull from —
// and points WB_HOME and related XDG state at an isolated root.
func setUpRenameCLIFixture(t *testing.T) (projects string) {
	t.Helper()
	root := t.TempDir()
	runGit := func(dir string, args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
	remote := filepath.Join(root, "remote.git")
	if output, err := exec.Command("git", "init", "--bare", "--initial-branch=main", remote).CombinedOutput(); err != nil {
		t.Fatalf("init bare remote: %v\n%s", err, output)
	}
	projects = filepath.Join(root, "projects")
	canonical := filepath.Join(projects, "acme", "app")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "clone", remote, canonical).CombinedOutput(); err != nil {
		t.Fatalf("clone canonical: %v\n%s", err, output)
	}
	runGit(canonical, "config", "user.name", "WB Test")
	runGit(canonical, "config", "user.email", "wb-test@example.test")
	if err := os.WriteFile(filepath.Join(canonical, "README.md"), []byte("# app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(canonical, "add", "README.md")
	runGit(canonical, "commit", "-m", "initial")
	runGit(canonical, "push", "-u", "origin", "main")

	t.Setenv(wbhome.EnvOverride, filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	return projects
}

// TestWorktreeRenameCLIAppliesMoveAndReportsExitOK drives the whole verb
// through run(), the same entry point main() uses, proving the cobra wiring
// (flags, positional args, --apply) actually reaches worktrees.Rename and
// that a successful rename is reported as exit 0 with the new path on stdout.
func TestWorktreeRenameCLIAppliesMoveAndReportsExitOK(t *testing.T) {
	projects := setUpRenameCLIFixture(t)
	oldPrompt := writeOriginalPromptFixture(t, "create original request")
	newPrompt := writeOriginalPromptFixture(t, "recycle original request")
	previousProjectsRoot := projectsRoot
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--projects-root", projects, "worktree", "create", "cli-old", "acme/app", "--model", "unknown", "--original-prompt-file", oldPrompt}, &stdout, &stderr); code != exitOK {
		t.Fatalf("worktree create failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code := run([]string{"--projects-root", projects, "worktree", "rename", "cli-old", "cli-new", "--apply", "--remote", "--non-interactive", "--model", "unknown", "--original-prompt-file", newPrompt}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("worktree rename failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "renamed cli-old acme/app ->") {
		t.Fatalf("rename stdout = %s", stdout.String())
	}
	newWorktree := filepath.Join(projects, "acme", "app", ".worktrees", "cli-new")
	if info, statErr := os.Stat(newWorktree); statErr != nil || !info.IsDir() {
		t.Fatalf("renamed worktree missing at %s: %v", newWorktree, statErr)
	}
}

func TestWorktreeSummaryCLIRequiresTaskAndPrintsBriefOverview(t *testing.T) {
	projects := setUpRenameCLIFixture(t)
	prompt := writeOriginalPromptFixture(t, "summarize this coordinated task")
	previousProjectsRoot := projectsRoot
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--projects-root", projects, "worktree", "summary"}, &stdout, &stderr); code == exitOK {
		t.Fatalf("summary without task should fail; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--projects-root", projects, "worktree", "create", "cli-summary", "acme/app", "--model", "unknown", "--original-prompt-file", prompt}, &stdout, &stderr); code != exitOK {
		t.Fatalf("create failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--projects-root", projects, "worktree", "summary", "cli-summary"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("summary failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"# WB worktree summary: cli-summary",
		"1 worktree(s)",
		"## acme/app",
		"worktree:",
		"branch:",
		"state:",
		"target:",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary missing %q; stdout=%s stderr=%s", want, out, stderr.String())
		}
	}
	if strings.Contains(out, "pr:") {
		t.Fatalf("local summary should omit pr lines without --github; stdout=%s", out)
	}
	if strings.Contains(out, "summarize this coordinated task") {
		t.Fatalf("summary leaked prompt body; stdout=%s", out)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--projects-root", projects, "worktree", "summary", "cli-summary", "--format", "json"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("summary json failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var document map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("summary json: %v\n%s", err, stdout.String())
	}
	results, ok := document["results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("summary json results = %#v", document["results"])
	}
}

func TestWorktreeInfoCLIRedactsPromptBodies(t *testing.T) {
	projects := setUpRenameCLIFixture(t)
	prompt := writeOriginalPromptFixture(t, "private original request must stay hidden")
	previousProjectsRoot := projectsRoot
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--projects-root", projects, "worktree", "create", "cli-info", "acme/app", "--model", "unknown", "--original-prompt-file", prompt}, &stdout, &stderr); code != exitOK {
		t.Fatalf("create failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	worktree := filepath.Join(projects, "acme", "app", ".worktrees", "cli-info")
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--projects-root", projects, "worktree", "info", worktree}, &stdout, &stderr); code != exitOK {
		t.Fatalf("info failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"# WB worktree info",
		"## Claim",
		"cli-info",
		"wb worktree log",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("info text missing %q; stdout=%s stderr=%s", want, out, stderr.String())
		}
	}
	if strings.Contains(out, "private original request must stay hidden") {
		t.Fatalf("info leaked prompt body; stdout=%s", out)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--projects-root", projects, "worktree", "info", worktree, "--format", "json"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("info json failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "private original request must stay hidden") {
		t.Fatalf("info json leaked prompt body; stdout=%s", stdout.String())
	}
	var document map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("info json: %v\n%s", err, stdout.String())
	}
	if document["original_prompt"] != nil {
		t.Fatalf("redacted json still has original_prompt: %#v", document["original_prompt"])
	}
}

// TestWorktreeInfoCLIReportsAnActiveMergerLaneClaim covers the lesson
// merger-lane-branch-race: once a merger lane has prepared a batch that
// includes this worktree's branch, 'wb worktree info' must surface that
// claim so an agent decides whether to push (a revert included) with the
// same information the lane already has, instead of finding out only after
// a merge landed something else.
func TestWorktreeInfoCLIReportsAnActiveMergerLaneClaim(t *testing.T) {
	projects := setUpRenameCLIFixture(t)
	prompt := writeOriginalPromptFixture(t, "claim fixture original prompt")
	previousProjectsRoot := projectsRoot
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--projects-root", projects, "worktree", "create", "cli-claimed", "acme/app", "--model", "unknown", "--original-prompt-file", prompt}, &stdout, &stderr); code != exitOK {
		t.Fatalf("create failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	worktree := filepath.Join(projects, "acme", "app", ".worktrees", "cli-claimed")
	if err := os.WriteFile(filepath.Join(worktree, "claimed.txt"), []byte("claimed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", worktree}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
	runGit("add", "claimed.txt")
	runGit("commit", "-m", "feat: add claimed file")

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--projects-root", projects, "worktree", "merge", "prepare", worktree, "--target", "main", "--model", "unknown", "--agent-runtime", "test"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("merge prepare failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--projects-root", projects, "worktree", "info", worktree}, &stdout, &stderr); code != exitOK {
		t.Fatalf("info failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"## Merger lane", "claimed: true", "target: main"} {
		if !strings.Contains(out, want) {
			t.Fatalf("info text missing %q; stdout=%s stderr=%s", want, out, stderr.String())
		}
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--projects-root", projects, "worktree", "info", worktree, "--format", "json"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("info json failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var document map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("info json: %v\n%s", err, stdout.String())
	}
	claim, ok := document["merger_lane_claim"].(map[string]any)
	if !ok {
		t.Fatalf("info json missing merger_lane_claim: %#v", document)
	}
	if claim["target"] != "main" || claim["lane"] == "" || claim["receipt_path"] == "" {
		t.Fatalf("merger_lane_claim = %#v", claim)
	}
}

func TestWorktreeWorkLogCLIDumpsInitialPromptAndClaim(t *testing.T) {
	projects := setUpRenameCLIFixture(t)
	prompt := writeOriginalPromptFixture(t, "agent needs the original request")
	previousProjectsRoot := projectsRoot
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--projects-root", projects, "worktree", "create", "cli-worklog", "acme/app", "--model", "unknown", "--original-prompt-file", prompt}, &stdout, &stderr); code != exitOK {
		t.Fatalf("create failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	worktree := filepath.Join(projects, "acme", "app", ".worktrees", "cli-worklog")
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--projects-root", projects, "worktree", "log", worktree}, &stdout, &stderr); code != exitOK {
		t.Fatalf("log failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		"# WB work log",
		"## Original prompt",
		"agent needs the original request",
		"## Claim",
		"cli-worklog",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("log text missing %q; stdout=%s stderr=%s", want, stdout.String(), stderr.String())
		}
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--projects-root", projects, "worktree", "worklog", worktree, "--format", "json"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("worklog alias json failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var document map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("log json: %v\n%s", err, stdout.String())
	}
	original, ok := document["original_prompt"].(map[string]any)
	if !ok || !strings.Contains(fmt.Sprint(original["body"]), "agent needs the original request") {
		t.Fatalf("json original_prompt = %#v", document["original_prompt"])
	}
}

func TestWorktreeLogMutatingVerbsCLI(t *testing.T) {
	projects := setUpRenameCLIFixture(t)
	prompt := writeOriginalPromptFixture(t, "mutating verbs journey")
	previousProjectsRoot := projectsRoot
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--projects-root", projects, "worktree", "create", "cli-log-verbs", "acme/app", "--model", "unknown", "--original-prompt-file", prompt}, &stdout, &stderr); code != exitOK {
		t.Fatalf("create failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	worktree := filepath.Join(projects, "acme", "app", ".worktrees", "cli-log-verbs")

	var checkpointOutput string
	for _, args := range [][]string{
		{"--projects-root", projects, "worktree", "log", "init", worktree, "--format", "json"},
		{"--projects-root", projects, "worktree", "log", "steer", worktree, "--prompt", "next slice", "--format", "json"},
		{"--projects-root", projects, "worktree", "log", "checkpoint", worktree, "--message", "progress", "--format", "json"},
		{"--projects-root", projects, "worktree", "log", "show", worktree, "--format", "json"},
		{"--projects-root", projects, "worktree", "log", "sync", worktree, "--format", "json"},
	} {
		stdout.Reset()
		stderr.Reset()
		if code := run(args, &stdout, &stderr); code != exitOK {
			t.Fatalf("%v failed: code=%d stdout=%s stderr=%s", args, code, stdout.String(), stderr.String())
		}
		if args[4] == "checkpoint" {
			checkpointOutput = stdout.String()
		}
	}
	if strings.Contains(stdout.String(), "mutating verbs journey") {
		t.Fatalf("log show leaked private prompt body: %s", stdout.String())
	}

	// The checkpoint verb defaults to pushing refs/wb/checkpoints/<task>;
	// checkpoint-fetch is the cross-machine retrieval side of that same push,
	// exercised here end to end through the CLI.
	var checkpointResult struct {
		RemoteCheckpoint struct {
			Ref    string `json:"ref"`
			SHA    string `json:"sha"`
			Notice string `json:"notice"`
		} `json:"remote_checkpoint"`
	}
	if err := json.Unmarshal([]byte(checkpointOutput), &checkpointResult); err != nil {
		t.Fatalf("decode checkpoint output: %v\n%s", err, checkpointOutput)
	}
	if checkpointResult.RemoteCheckpoint.Ref != "refs/wb/checkpoints/cli-log-verbs" || checkpointResult.RemoteCheckpoint.Notice != worktrees.NotALandingReceiptNotice {
		t.Fatalf("checkpoint did not report a remote checkpoint: %+v", checkpointResult)
	}

	stdout.Reset()
	stderr.Reset()
	fetchArgs := []string{"worktree", "checkpoint-fetch", worktree, "--task", "cli-log-verbs", "--format", "json"}
	if code := run(fetchArgs, &stdout, &stderr); code != exitOK {
		t.Fatalf("checkpoint-fetch failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var fetchResult struct {
		Ref    string `json:"ref"`
		SHA    string `json:"sha"`
		Notice string `json:"notice"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &fetchResult); err != nil {
		t.Fatalf("decode checkpoint-fetch output: %v\n%s", err, stdout.String())
	}
	if fetchResult.SHA != checkpointResult.RemoteCheckpoint.SHA || fetchResult.Notice != worktrees.NotALandingReceiptNotice {
		t.Fatalf("checkpoint-fetch = %+v, want SHA %s and the not-a-landing-receipt notice", fetchResult, checkpointResult.RemoteCheckpoint.SHA)
	}
}

func TestWorktreeLogRecoverReconcileBranchFlagsWireAndRequireInputs(t *testing.T) {
	command := newWorktreeLogRecoverCmd()
	for _, flag := range []string{"reconcile-branch", "expected-head", "remote", "actor", "reason", "event-id", "apply"} {
		if command.Flags().Lookup(flag) == nil {
			t.Fatalf("recover is missing --%s", flag)
		}
	}
	var stdout, stderr bytes.Buffer
	args := []string{"worktree", "log", "recover", ".", "--reconcile-branch", "codex/live", "--expected-head", strings.Repeat("a", 40), "--remote", "--actor", "alex", "--reason", "claim collision", "--event-id", "reconcile-cli"}
	if code := run(args, &stdout, &stderr); code == exitUsage || strings.Contains(stderr.String(), "unknown flag") {
		t.Fatalf("reconcile flags did not reach LogRecover: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"worktree", "log", "recover", ".", "--reconcile-branch", "codex/live"}, &stdout, &stderr); code == exitOK || !strings.Contains(stderr.String(), "--expected-head") {
		t.Fatalf("missing reconciliation inputs = code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestWorktreeCreateCLIResumePreservesImplicitActiveRun(t *testing.T) {
	projects := setUpRenameCLIFixture(t)
	prompt := writeOriginalPromptFixture(t, "resume the original request")
	previousProjectsRoot := projectsRoot
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })

	var stdout, stderr bytes.Buffer
	args := []string{"--projects-root", projects, "worktree", "create", "cli-resume", "acme/app", "--model", "unknown", "--original-prompt-file", prompt}
	if code := run(args, &stdout, &stderr); code != exitOK {
		t.Fatalf("initial create failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	projectionPath := filepath.Join(projects, "acme", "app", ".worktrees", "cli-resume", ".wb-worklog", "recovery.json")
	before, err := os.ReadFile(projectionPath)
	if err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	args = append(args, "--resume")
	if code := run(args, &stdout, &stderr); code != exitOK {
		t.Fatalf("resume failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "resumed acme/app") {
		t.Fatalf("resume stdout = %s", stdout.String())
	}
	after, err := os.ReadFile(projectionPath)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("CLI resume replaced implicit active run projection: err=%v before=%s after=%s", err, before, after)
	}
}

// TestWorktreeRenameCLIRefusesDestinationCollisionAsFindings proves a
// destination collision surfaces as a documented "findings" exit code (1),
// not success and not a bare usage error, through the same run() entry point.
func TestWorktreeRenameCLIRefusesDestinationCollisionAsFindings(t *testing.T) {
	projects := setUpRenameCLIFixture(t)
	takenPrompt := writeOriginalPromptFixture(t, "taken original request")
	sourcePrompt := writeOriginalPromptFixture(t, "source original request")
	newPrompt := writeOriginalPromptFixture(t, "collision recycle request")
	previousProjectsRoot := projectsRoot
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--projects-root", projects, "worktree", "create", "taken", "acme/app", "--model", "unknown", "--original-prompt-file", takenPrompt}, &stdout, &stderr); code != exitOK {
		t.Fatalf("seed worktree create failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--projects-root", projects, "worktree", "create", "source", "acme/app", "--model", "unknown", "--original-prompt-file", sourcePrompt}, &stdout, &stderr); code != exitOK {
		t.Fatalf("source worktree create failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code := run([]string{"--projects-root", projects, "worktree", "rename", "source", "taken", "--apply", "--remote", "--non-interactive", "--model", "unknown", "--original-prompt-file", newPrompt}, &stdout, &stderr)
	if code != exitFindings {
		t.Fatalf("collision rename exit code = %d, want %d (findings); stdout=%s stderr=%s", code, exitFindings, stdout.String(), stderr.String())
	}
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
// canonical origin-base fetch step: the failure was reported to the shell
// as exit code 0. WB's own documented contract (see rootLongHelp) is that a
// command which ran and found a real problem exits 1 ("findings"), not 0
// ("success") — a caller, human or agent, that only checks the exit code
// would otherwise believe the worktree was created when it was not.
//
// This drives the real end-to-end path — worktrees.Create ->
// synchronizeCanonical -> gitCanonical -> the re-exec'd secure canonical Git
// helper -> a real sandboxed `git fetch` — through run(), the same function
// main() uses, against a canonical clone whose origin cannot be fetched. That
// makes the fetch failure genuine and reproducible without a
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
	// An origin that cannot be fetched from makes `git fetch` fail for
	// a real, reproducible reason, without needing SSH or a reachable remote.
	runCanonicalGit("remote", "add", "origin", filepath.Join(root, "does-not-exist.git"))

	previousProjectsRoot := projectsRoot
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })
	prompt := writeOriginalPromptFixture(t, "create request whose origin fetch fails")
	var stdout, stderr bytes.Buffer
	code := run([]string{"--projects-root", projects, "worktree", "create", "sync-fail", "acme/app", "--non-interactive", "--model", "unknown", "--original-prompt-file", prompt}, &stdout, &stderr)
	if code != exitFindings {
		t.Fatalf("canonical sync failure exit code = %d, want %d (findings); stdout=%s stderr=%s",
			code, exitFindings, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "fetch verified origin base") {
		t.Fatalf("stderr does not explain the canonical sync failure: %s", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("a failed create wrote %q to stdout", stdout.String())
	}
}

// TestWorktreeCreateKeepsRemoteClaimNotesOffStdout pins the stream, not just
// the helper that chooses it. A remote-claim note is a diagnostic about a side
// channel: routed to stdout it pads a text result with unrelated lines, and on
// a failing command it leaves output on stdout that callers parse as a result.
func TestWorktreeCreateKeepsRemoteClaimNotesOffStdout(t *testing.T) {
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
	runCanonicalGit("remote", "add", "origin", filepath.Join(root, "does-not-exist.git"))

	previousProjectsRoot := projectsRoot
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })
	prompt := writeOriginalPromptFixture(t, "create request that emits a remote-claim note")

	var stdout, stderr bytes.Buffer
	run([]string{"--projects-root", projects, "worktree", "create", "claim-stream", "acme/app",
		"--non-interactive", "--model", "unknown", "--original-prompt-file", prompt}, &stdout, &stderr)

	if strings.Contains(stdout.String(), "remote claim") {
		t.Errorf("a remote-claim note reached stdout: %q", stdout.String())
	}
}

// findOriginalPromptArchive locates the single Work Log run's archived prompt
// file for task under home, matching the layout WB_HOME/worklogs/<effort>/
// runs/<run>/original-prompt.txt. It fails the test if there is not exactly
// one run, so a stampede regression (multiple orphaned runs) is caught here
// too rather than only in the internal/worktrees package tests.
func findOriginalPromptArchive(t *testing.T, home, task string) string {
	t.Helper()
	runsRoot := filepath.Join(home, "worklogs", task, "runs")
	entries, err := os.ReadDir(runsRoot)
	if err != nil {
		t.Fatalf("read work-log runs directory %s: %v", runsRoot, err)
	}
	if len(entries) != 1 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("work-log runs for %s = %d, want exactly 1: %v", task, len(entries), names)
	}
	return filepath.Join(runsRoot, entries[0].Name(), "original-prompt.txt")
}

// TestWorktreeCreateAcceptsOriginalPromptFromStdin is the CLI-level regression
// test for issue #88: --original-prompt-file - reads the exact prompt from
// stdin instead of a caller-staged file, and WB itself writes the private
// 0600 archive under WB_HOME. This proves the whole path end to end: stdin in,
// an exact-content 0600 archive on disk, and that same body recoverable from
// the Work Log exactly as a file-based --original-prompt-file would record it.
func TestWorktreeCreateAcceptsOriginalPromptFromStdin(t *testing.T) {
	projects := setUpRenameCLIFixture(t)
	previousProjectsRoot := projectsRoot
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })
	home := os.Getenv(wbhome.EnvOverride)

	const prompt = "stdin-sourced original request, never staged to a shared path\n"
	var stdout, stderr bytes.Buffer
	code := runWithStdin(
		[]string{"--projects-root", projects, "worktree", "create", "cli-stdin-prompt", "acme/app", "--model", "unknown", "--original-prompt-file", "-"},
		strings.NewReader(prompt), &stdout, &stderr,
	)
	if code != exitOK {
		t.Fatalf("create failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), prompt) || strings.Contains(stderr.String(), prompt) {
		t.Fatalf("create echoed the stdin prompt back: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	archive := findOriginalPromptArchive(t, home, "cli-stdin-prompt")
	info, err := os.Stat(archive)
	if err != nil {
		t.Fatalf("stat archived stdin prompt: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("archived stdin prompt mode = %o, want 0600", info.Mode().Perm())
	}
	content, err := os.ReadFile(archive)
	if err != nil {
		t.Fatalf("read archived stdin prompt: %v", err)
	}
	if string(content) != prompt {
		t.Fatalf("archived stdin prompt = %q, want %q", content, prompt)
	}

	worktree := filepath.Join(projects, "acme", "app", ".worktrees", "cli-stdin-prompt")
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--projects-root", projects, "worktree", "log", worktree}, &stdout, &stderr); code != exitOK {
		t.Fatalf("log failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "stdin-sourced original request") {
		t.Fatalf("work log did not recover the stdin-sourced prompt: %s", stdout.String())
	}
}

// TestWorktreeCreateRefusesEmptyStdinPrompt proves --original-prompt-file -
// fails closed exactly like an empty --original-prompt-file: no worktree,
// task directory, or Work Log state is created for an empty or
// whitespace-only stdin.
func TestWorktreeCreateRefusesEmptyStdinPrompt(t *testing.T) {
	for _, test := range []struct {
		name  string
		stdin string
	}{
		{name: "empty", stdin: ""},
		{name: "whitespace-only", stdin: "   \n\t\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, ".wb")
			t.Setenv("WB_HOME", home)
			task := "empty-stdin-" + test.name
			var stdout, stderr bytes.Buffer
			code := runWithStdin(
				[]string{"--projects-root", filepath.Join(root, "projects"), "worktree", "create", task, "acme/app", "--model", "unknown", "--original-prompt-file", "-"},
				strings.NewReader(test.stdin), &stdout, &stderr,
			)
			if code == exitOK {
				t.Fatalf("empty stdin prompt succeeded: stdout=%s", stdout.String())
			}
			if !strings.Contains(stderr.String(), "non-empty stdin") {
				t.Fatalf("stderr = %q, want a clear non-empty-stdin refusal", stderr.String())
			}
			if _, err := os.Stat(filepath.Join(home, "worktrees", task)); !os.IsNotExist(err) {
				t.Fatalf("empty stdin preflight created task state: %v", err)
			}
			if _, err := os.Stat(filepath.Join(home, "worklogs", task)); !os.IsNotExist(err) {
				t.Fatalf("empty stdin preflight created work-log state: %v", err)
			}
		})
	}
}
