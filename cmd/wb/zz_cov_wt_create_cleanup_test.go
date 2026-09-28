package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestCwWtWorktreeCleanupApplyOnCleanFixture(t *testing.T) {
	projects, _, worktree := initGCFixture(t)
	if err := os.Remove(filepath.Join(worktree, "wip.txt")); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCleanupCmd(&invocation{projectsRoot: projects}) }, "gc-cli")
	if err != nil {
		t.Fatalf("cleanup dry run: %v", err)
	}
	if !strings.Contains(stdout, "would remove gc-cli acme/app") {
		t.Fatalf("cleanup dry-run stdout = %q", stdout)
	}
	if !strings.Contains(stdout, "1 eligible; dry-run only, pass --apply to remove") {
		t.Fatalf("cleanup dry-run footer = %q", stdout)
	}

	// Applying retires the checkout and reports the count; the named-task
	// release path then runs to completion.
	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCleanupCmd(&invocation{projectsRoot: projects}) }, "gc-cli", "--apply")
	if err != nil {
		t.Fatalf("cleanup --apply: %v", err)
	}
	if !strings.Contains(stdout, "removed gc-cli acme/app") {
		t.Fatalf("cleanup --apply stdout = %q", stdout)
	}
	if !strings.Contains(stdout, "1 removed") {
		t.Fatalf("cleanup --apply footer = %q", stdout)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("cleanup --apply left the checkout behind: %v", err)
	}
}

func TestCwWtWorktreeCleanupApplyJSONWithReportDir(t *testing.T) {
	projects, _, worktree := initGCFixture(t)
	if err := os.Remove(filepath.Join(worktree, "wip.txt")); err != nil {
		t.Fatal(err)
	}
	reportDir := filepath.Join(t.TempDir(), "reports")

	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCleanupCmd(&invocation{projectsRoot: projects}) }, "gc-cli", "--apply", "--format", "json", "--report-dir", reportDir)
	if err != nil {
		t.Fatalf("cleanup --apply json: %v", err)
	}
	if !strings.Contains(stdout, `"applied": true`) {
		t.Fatalf("cleanup --apply json = %q", stdout)
	}
	if entries, err := os.ReadDir(reportDir); err != nil || len(entries) == 0 {
		t.Fatalf("cleanup --report-dir wrote nothing: entries=%v err=%v", entries, err)
	}
}

func TestCwWtWorktreeCleanupTextReportPathLine(t *testing.T) {
	projects, _, worktree := initGCFixture(t)
	if err := os.Remove(filepath.Join(worktree, "wip.txt")); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCleanupCmd(&invocation{projectsRoot: projects}) }, "gc-cli", "--report-dir", filepath.Join(t.TempDir(), "reports"))
	if err != nil {
		t.Fatalf("cleanup with a report dir: %v", err)
	}
	if !strings.Contains(stdout, "would remove gc-cli acme/app") {
		t.Fatalf("cleanup stdout = %q", stdout)
	}
}

func TestCwWtWorktreeCreateCommandValidation(t *testing.T) {
	seed := t.TempDir()
	projects := t.TempDir()
	clone := filepath.Join(projects, "acme", "app")
	cwCovCloneWithOrigin(t, seed, "app", clone)
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))
	prompt := filepath.Join(t.TempDir(), "prompt.txt")
	cwWtWriteFile(t, prompt, "the exact task request\n")

	// A bad format is refused before anything else.
	_, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCreateCmd(&invocation{projectsRoot: projects}) }, "t", "acme/app", "--original-prompt-file", prompt, "--format", "yaml")
	if err == nil || !strings.Contains(err.Error(), "unsupported format") {
		t.Fatalf("create --format yaml = %v", err)
	}
	// An unsupported execution mode is refused.
	_, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCreateCmd(&invocation{projectsRoot: projects}) }, "t", "acme/app", "--original-prompt-file", prompt, "--mode", "yolo")
	if err == nil || !strings.Contains(err.Error(), "unsupported execution mode") {
		t.Fatalf("create --mode yolo = %v", err)
	}
	// Manual mode without an initiator is refused.
	_, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCreateCmd(&invocation{projectsRoot: projects}) }, "t", "acme/app", "--original-prompt-file", prompt, "--mode", "manual")
	if err == nil || !strings.Contains(err.Error(), "--initiator") {
		t.Fatalf("create --mode manual = %v", err)
	}
	// Agent mode without a live registered session is refused.
	_, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCreateCmd(&invocation{projectsRoot: projects}) }, "t", "acme/app", "--original-prompt-file", prompt, "--mode", "agent")
	if err == nil || !strings.Contains(err.Error(), "live registered session") {
		t.Fatalf("create --mode agent = %v", err)
	}
	// A missing prompt file is reported.
	_, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCreateCmd(&invocation{projectsRoot: projects}) }, "t", "acme/app", "--model", "unknown",
		"--mode", "manual", "--initiator", "cwWt", "--original-prompt-file", filepath.Join(t.TempDir(), "missing.txt"))
	if err == nil {
		t.Fatal("create with a missing prompt file must fail")
	}
	// A malformed repository slug is refused.
	_, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCreateCmd(&invocation{projectsRoot: projects}) }, "t", "not-a-slug", "--model", "unknown",
		"--mode", "manual", "--initiator", "cwWt", "--original-prompt-file", prompt)
	if err == nil {
		t.Fatal("create with a malformed repository must fail")
	}
	// --branch and --branch-prefix together are refused.
	_, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCreateCmd(&invocation{projectsRoot: projects}) }, "t", "acme/app", "--model", "unknown",
		"--mode", "manual", "--initiator", "cwWt", "--original-prompt-file", prompt,
		"--branch", "b", "--branch-prefix", "p")
	if err == nil || !strings.Contains(err.Error(), "cannot be used together") {
		t.Fatalf("create --branch with --branch-prefix = %v", err)
	}
	// The task argument is required.
	_, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCreateCmd(&invocation{projectsRoot: projects}) }, "--original-prompt-file", prompt)
	if err == nil {
		t.Fatal("create without a task must fail")
	}
}

func TestCwWtWorktreeCreateSucceedsInProcess(t *testing.T) {
	seed := t.TempDir()
	projects := t.TempDir()
	clone := filepath.Join(projects, "acme", "app")
	remote := cwCovCloneWithOrigin(t, seed, "app", clone)
	// The clone stays at the legacy two-level path, but its origin names a
	// forge: the central store must still embed that literal host.
	cwCovPointOriginAtForge(t, clone, remote, "github.com", "acme/app")
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	prompt := filepath.Join(t.TempDir(), "prompt.txt")
	cwWtWriteFile(t, prompt, "the exact task request\n")

	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCreateCmd(&invocation{projectsRoot: projects}) }, "cw-wt-task", "acme/app",
		"--model", "unknown", "--mode", "manual", "--initiator", "cwWt",
		"--original-prompt-file", prompt, "--summary", "a short summary", "--no-claim")
	if err != nil {
		t.Fatalf("worktree create: %v", err)
	}
	// No store mode is configured, so the default central store applies: the
	// task is the first level below <projects>/.worktrees, followed by the
	// literal host the canonical clone's origin names.
	wantDir := filepath.Join(projects, ".worktrees", "cw-wt-task", "github.com", "acme", "app")
	if !strings.Contains(stdout, wantDir) {
		t.Fatalf("create stdout = %q, want it to name %s", stdout, wantDir)
	}
	if _, err := os.Stat(wantDir); err != nil {
		t.Fatalf("create did not make the checkout: %v", err)
	}
}

func TestCwWtWorktreeCreateJSONAndStdinPromptInProcess(t *testing.T) {
	seed := t.TempDir()
	projects := t.TempDir()
	clone := filepath.Join(projects, "acme", "app")
	cwCovCloneWithOrigin(t, seed, "app", clone)
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))

	stdout, _, err := cwWtRunCmd(t, projects, "the exact prompt from stdin\n", func() *cobra.Command { return newWorktreeCreateCmd(&invocation{projectsRoot: projects}) },
		"cw-wt-stdin", "acme/app", "--model", "unknown", "--mode", "manual", "--initiator", "cwWt",
		"--original-prompt-file", "-", "--format", "json", "--no-claim")
	if err != nil {
		t.Fatalf("worktree create from stdin: %v", err)
	}
	if !strings.Contains(stdout, `"repository": "acme/app"`) {
		t.Fatalf("create json stdout = %q", stdout)
	}

	// Whitespace-only stdin is refused.
	_, _, err = cwWtRunCmd(t, projects, "   \n", func() *cobra.Command { return newWorktreeCreateCmd(&invocation{projectsRoot: projects}) },
		"cw-wt-blank", "acme/app", "--model", "unknown", "--mode", "manual", "--initiator", "cwWt",
		"--original-prompt-file", "-")
	if err == nil {
		t.Fatal("create from blank stdin must fail")
	}
}

func TestCwWtRefreshManagedHooksBeforeWorktreeCreate(t *testing.T) {
	seed := t.TempDir()
	projects := t.TempDir()
	clone := filepath.Join(projects, "acme", "app")
	cwCovCloneWithOrigin(t, seed, "app", clone)

	if err := refreshManagedHooksBeforeWorktreeCreate(&invocation{projectsRoot: projects}, []string{"acme/app"}); err != nil {
		t.Fatalf("refreshManagedHooksBeforeWorktreeCreate: %v", err)
	}
	// A malformed slug cannot be resolved to a canonical repository.
	if err := refreshManagedHooksBeforeWorktreeCreate(&invocation{projectsRoot: projects}, []string{"not-a-slug"}); err == nil {
		t.Fatal("a malformed repository slug must fail")
	}
	// A well-formed but absent repository cannot be resolved either.
	if err := refreshManagedHooksBeforeWorktreeCreate(&invocation{projectsRoot: projects}, []string{"acme/absent"}); err == nil {
		t.Fatal("an absent canonical repository must fail")
	}
}
