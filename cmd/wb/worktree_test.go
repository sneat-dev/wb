package main

import (
	"bytes"
	"encoding/json"
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

func TestWorktreeHelpExplainsCanonicalAndCentralLayout(t *testing.T) {
	command := newWorktreeCreateCmd()
	for _, wanted := range []string{
		"dirty or off-base canonical clone",
		"fetches",
		"without switching or updating any local branch",
		"<wb-home>/worktrees/<task>/<owner>/<repository>",
		"WB_HOME",
		"--resume",
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
	for _, flag := range []string{"model", "cli", "provider"} {
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
}

func TestWorktreeLifecycleHelpExplainsNetworkAndCleanupSafety(t *testing.T) {
	list := newWorktreeListCmd()
	for _, wanted := range []string{"only local Git data", "--github", "exact fetched origin-target", "versioned control-plane envelope", "lifecycle artifacts", "seven-day recent-history"} {
		if !strings.Contains(list.Long, wanted) {
			t.Errorf("worktree list help does not mention %q", wanted)
		}
	}
	cleanup := newWorktreeCleanupCmd()
	for _, wanted := range []string{"default is a dry-run", "freshly fetched exact", "awaiting_push", "force-with-lease", "before any remote or local deletion", "requires --remote", "implicit age window is zero", "--resume-interrupted", "conclusively dead"} {
		if !strings.Contains(cleanup.Long, wanted) {
			t.Errorf("worktree cleanup help does not mention %q", wanted)
		}
	}
}

func TestWorktreeListJSONIncludesArtifactOnlyCleanupBacklog(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	projects := filepath.Join(root, "projects")
	const task = "artifact-json"
	retired := filepath.Join(home, "worktrees", task, ".wb-retired-stage-6b0995eef65f84dace22d24df2644b32")
	if err := os.MkdirAll(retired, 0o700); err != nil {
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
	if outcome.SchemaVersion != 1 || len(outcome.Results) != 0 || len(outcome.Diagnostics) != 0 || len(outcome.Artifacts) != 1 {
		t.Fatalf("JSON control-plane envelope = %#v", outcome)
	}
	if filepath.Base(outcome.Artifacts[0].Path) != filepath.Base(retired) ||
		outcome.Artifacts[0].Task != task || !outcome.Artifacts[0].Eligible ||
		outcome.Artifacts[0].Disposition != "archive_empty_stage" {
		t.Fatalf("JSON lifecycle artifact = %#v", outcome.Artifacts)
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

func TestNamedCleanupApplyRequiresRemoteBranchRetirementBeforeInspection(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--projects-root", t.TempDir(), "worktree", "cleanup", "delivered-task", "--apply"}, &stdout, &stderr)
	if code == exitOK || !strings.Contains(stderr.String(), "requires --remote") {
		t.Fatalf("named cleanup without remote exit=%d stderr=%q", code, stderr.String())
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
	home := os.Getenv(wbhome.EnvOverride)
	newWorktree := filepath.Join(home, "worktrees", "cli-new", "acme", "app")
	if info, statErr := os.Stat(newWorktree); statErr != nil || !info.IsDir() {
		t.Fatalf("renamed worktree missing at %s: %v", newWorktree, statErr)
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
	projectionPath := filepath.Join(os.Getenv(wbhome.EnvOverride), "worktrees", "cli-resume", "acme", "app", ".wb-worklog", "recovery.json")
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
