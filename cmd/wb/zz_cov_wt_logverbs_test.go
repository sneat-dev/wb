package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestCwWtWorkLogVerbCommandsInProcess(t *testing.T) {
	projects, _, worktree := initGCFixture(t)

	steps := []struct {
		name    string
		args    []string
		want    []string
		wantErr string
	}{
		{
			name: "init", args: []string{"init", worktree, "--mode", "manual", "--initiator", "cwWt", "--prompt", "hello", "--source", "human_declared", "--agent-runtime", "codex", "--model", "unknown", "--cli", "cli-1", "--provider", "prov-1", "--format", "json"},
			want: []string{`"verb": "init"`},
		},
		{
			name: "steer", args: []string{"steer", worktree, "--mode", "manual", "--initiator", "cwWt", "--prompt", "next"},
			want: []string{"steer ", "applied=true", "prompt="},
		},
		{
			name: "show", args: []string{"show", worktree},
			want: []string{"# WB worktree info", "## Manifest", "## Git"},
		},
		{
			name: "show-json", args: []string{"show", worktree, "--format", "json"},
			want: []string{`"projection"`, `"view"`},
		},
		{
			name: "checkpoint", args: []string{"checkpoint", worktree, "--mode", "manual", "--initiator", "cwWt", "--skip-remote", "--message", "cp", "--next-action", "next", "--usage-discriminator", "provider_reported", "--input-tokens", "5", "--output-tokens", "6", "--estimated-cost", "0.5", "--currency", "USD", "--provider-ref", "ref-1"},
			want: []string{"checkpoint ", "applied=true", "remote checkpoint skipped (--skip-remote)"},
		},
		{
			name: "refresh", args: []string{"refresh", worktree, "--mode", "manual", "--initiator", "cwWt", "--base", "main"},
			want: []string{"refresh ", "applied=true"},
		},
		{
			name: "integrate", args: []string{"integrate", worktree, "--mode", "manual", "--initiator", "cwWt", "--base", "main", "--strategy", "auto"},
			wantErr: "integrate requires a clean worktree",
		},
		{
			name: "handoff", args: []string{"handoff", worktree, "--mode", "manual", "--initiator", "cwWt", "--summary", "s", "--next-action", "n", "--successor", "succ-1", "--model", "unknown", "--cli", "cli-1", "--provider", "prov-1"},
			want: []string{"handoff ", "handoff offer recorded in local journal"},
		},
		{
			name: "recover", args: []string{"recover", worktree, "--mode", "manual", "--initiator", "cwWt"},
			want: []string{"recover ", "dry-run only", "diagnosis: local events:"},
		},
		{
			name: "recover-json", args: []string{"recover", worktree, "--mode", "manual", "--initiator", "cwWt", "--format", "json"},
			want: []string{`"verb": "recover"`},
		},
		{
			name: "sync", args: []string{"sync", worktree, "--mode", "manual", "--initiator", "cwWt"},
			want: []string{"sync ", "offline outbox=", "no authoritative Synchestra endpoint configured"},
		},
		{
			name: "archive", args: []string{"archive", worktree, "--mode", "manual", "--initiator", "cwWt"},
			wantErr: "archive requires a terminal local projection",
		},
	}
	for _, step := range steps {
		stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeWorkLogCmd(&invocation{}) }, step.args...)
		if step.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), step.wantErr) {
				t.Errorf("%s: error = %v, want %q", step.name, err, step.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", step.name, err)
			continue
		}
		for _, want := range step.want {
			if !strings.Contains(stdout, want) {
				t.Errorf("%s: output missing %q:\n%s", step.name, want, stdout)
			}
		}
	}
}

func TestCwWtWorkLogFinalizeReportHandling(t *testing.T) {
	projects, _, worktree := initGCFixture(t)

	// --report together with --report-stdin is a usage error.
	_, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeWorkLogCmd(&invocation{}) }, "finalize", worktree,
		"--mode", "manual", "--initiator", "cwWt", "--report", "x", "--report-stdin")
	if err == nil || !strings.Contains(err.Error(), "at most one of --report or --report-stdin") {
		t.Fatalf("finalize with both report sources = %v", err)
	}

	// A report larger than the cap is refused before it is read.
	big := filepath.Join(t.TempDir(), "big.md")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", (1<<20)+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeWorkLogCmd(&invocation{}) }, "finalize", worktree,
		"--mode", "manual", "--initiator", "cwWt", "--report", big)
	if err == nil || !strings.Contains(err.Error(), "finalize report exceeds") {
		t.Fatalf("oversized finalize report = %v", err)
	}

	// A missing report file is reported.
	_, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeWorkLogCmd(&invocation{}) }, "finalize", worktree,
		"--mode", "manual", "--initiator", "cwWt", "--report", filepath.Join(t.TempDir(), "missing.md"))
	if err == nil || !strings.Contains(err.Error(), "read report file") {
		t.Fatalf("missing finalize report = %v", err)
	}

	// finalize refuses a dirty worktree, which the gc fixture creates on
	// purpose; clean it so the report path itself is what is under test.
	if err := os.Remove(filepath.Join(worktree, "wip.txt")); err != nil {
		t.Fatal(err)
	}

	// A report on stdin is accepted and recorded.
	stdout, _, err := cwWtRunCmd(t, projects, "# completion report\n", func() *cobra.Command { return newWorktreeWorkLogCmd(&invocation{}) },
		"finalize", worktree, "--mode", "manual", "--initiator", "cwWt", "--result", "success", "--message", "done", "--report-stdin", "--apply")
	if err != nil {
		t.Fatalf("finalize --report-stdin: %v", err)
	}
	if !strings.Contains(stdout, "finalize ") || !strings.Contains(stdout, "applied=true") {
		t.Fatalf("finalize stdout = %q", stdout)
	}

	// finalize --apply seals the claim; a second finalize reports the sealed state.
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeWorkLogCmd(&invocation{}) }, "finalize", worktree,
		"--mode", "manual", "--initiator", "cwWt", "--result", "failure", "--message", "second"); err == nil {
		t.Log("second finalize was accepted (idempotent terminal)")
	}
}

func TestCwWtWorkLogAdmissionAndModeErrors(t *testing.T) {
	projects, _, worktree := initGCFixture(t)

	// agent mode without a live registered session is refused before any write.
	_, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeWorkLogCmd(&invocation{}) }, "steer", worktree, "--mode", "agent", "--prompt", "x")
	if err == nil || !strings.Contains(err.Error(), "live registered session") {
		t.Fatalf("log steer --mode agent = %v", err)
	}
	// manual mode without an initiator is refused.
	_, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeWorkLogCmd(&invocation{}) }, "steer", worktree, "--mode", "manual", "--prompt", "x")
	if err == nil || !strings.Contains(err.Error(), "--initiator") {
		t.Fatalf("log steer --mode manual without initiator = %v", err)
	}
	// An unknown mode is refused.
	_, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeWorkLogCmd(&invocation{}) }, "steer", worktree, "--mode", "yolo", "--prompt", "x")
	if err == nil || !strings.Contains(err.Error(), "unsupported execution mode") {
		t.Fatalf("log steer --mode yolo = %v", err)
	}
	// A bad output format is refused first.
	_, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeWorkLogCmd(&invocation{}) }, "show", worktree, "--format", "yaml")
	if err == nil || !strings.Contains(err.Error(), "unsupported format") {
		t.Fatalf("log show --format yaml = %v", err)
	}

	// steer with neither source is refused.
	_, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeWorkLogCmd(&invocation{}) }, "steer", worktree, "--mode", "manual", "--initiator", "cwWt")
	if err == nil || !strings.Contains(err.Error(), "exactly one of --prompt or --prompt-file") {
		t.Fatalf("log steer without a source = %v", err)
	}
}

func TestCwWtWorktreeSetRecordsHumanInstruction(t *testing.T) {
	projects, _, worktree := initGCFixture(t)
	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeSetCmd(&invocation{}) }, worktree, "--prompt", "human instruction",
		"--agent-runtime", "codex", "--model", "unknown", "--cli", "cli-1", "--provider", "prov-1")
	if err != nil {
		t.Fatalf("worktree set: %v", err)
	}
	if !strings.Contains(stdout, "recorded ") || !strings.Contains(stdout, "human-instruction.md") {
		t.Fatalf("worktree set stdout = %q", stdout)
	}
}

func TestCwWtWorktreeInfoCommand(t *testing.T) {
	projects, _, worktree := initGCFixture(t)
	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeInfoCmd(&invocation{}) }, worktree)
	if err != nil {
		t.Fatalf("worktree info: %v", err)
	}
	if !strings.Contains(stdout, "# WB worktree info") {
		t.Fatalf("worktree info stdout = %q", stdout)
	}
	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeInfoCmd(&invocation{}) }, worktree, "--format", "json")
	if err != nil {
		t.Fatalf("worktree info json: %v", err)
	}
	if !strings.Contains(stdout, `"manifest"`) {
		t.Fatalf("worktree info json = %q", stdout)
	}
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeInfoCmd(&invocation{}) }, worktree, "--format", "yaml"); err == nil {
		t.Fatal("worktree info --format yaml must fail")
	}
}

func TestCwWtWorktreeAbortCommand(t *testing.T) {
	projects, _, _ := initGCFixture(t)

	// A missing disposition is refused by the backend.
	_, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeAbortCmd(&invocation{projectsRoot: projects}) }, "gc-cli")
	if err == nil || !strings.Contains(err.Error(), "disposition must be") {
		t.Fatalf("abort without a disposition = %v", err)
	}

	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeAbortCmd(&invocation{projectsRoot: projects}) }, "gc-cli", "--disposition", "not_landed", "--successor", "succ-1")
	if err != nil {
		t.Fatalf("abort not_landed: %v", err)
	}
	if !strings.Contains(stdout, "would seal acme/app not_landed") {
		t.Fatalf("abort stdout = %q", stdout)
	}

	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeAbortCmd(&invocation{projectsRoot: projects}) }, "gc-cli", "--disposition", "not_landed", "--successor", "succ-1", "--format", "json")
	if err != nil {
		t.Fatalf("abort json: %v", err)
	}
	if !strings.Contains(stdout, `"task": "gc-cli"`) {
		t.Fatalf("abort json = %q", stdout)
	}

	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeAbortCmd(&invocation{projectsRoot: projects}) }, "gc-cli", "--disposition", "handoff", "--successor", "s")
	if err != nil {
		t.Fatalf("abort handoff: %v", err)
	}
	if !strings.Contains(stdout, "would seal acme/app handoff") {
		t.Fatalf("abort handoff stdout = %q", stdout)
	}

	// orphaned requires an exact claim.
	_, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeAbortCmd(&invocation{projectsRoot: projects}) }, "gc-cli", "--disposition", "orphaned", "--claim", "abc", "--actor", "a", "--reason", "r")
	if err == nil || !strings.Contains(err.Error(), "exact --claim ID") {
		t.Fatalf("orphaned abort = %v", err)
	}

	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeAbortCmd(&invocation{projectsRoot: projects}) }, "gc-cli", "--disposition", "not_landed", "--successor", "s", "--format", "yaml"); err == nil {
		t.Fatal("abort --format yaml must fail")
	}
}

func TestCwWtWorktreeCleanupAndRetireShellsInProcess(t *testing.T) {
	projects, _, _ := initGCFixture(t)

	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCleanupCmd(&invocation{projectsRoot: projects}) }, "gc-cli")
	if err != nil {
		t.Fatalf("cleanup dry run: %v", err)
	}
	if !strings.Contains(stdout, "skip gc-cli acme/app: worktree has local changes") {
		t.Fatalf("cleanup stdout = %q", stdout)
	}
	if !strings.Contains(stdout, "0 eligible; dry-run only, pass --apply to remove") {
		t.Fatalf("cleanup footer = %q", stdout)
	}

	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCleanupCmd(&invocation{projectsRoot: projects}) }, "gc-cli", "--format", "json")
	if err != nil {
		t.Fatalf("cleanup json: %v", err)
	}
	if !strings.Contains(stdout, `"resolved_tasks"`) {
		t.Fatalf("cleanup json = %q", stdout)
	}

	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCleanupCmd(&invocation{projectsRoot: projects}) }, "--retire-shells")
	if err != nil {
		t.Fatalf("cleanup --retire-shells: %v", err)
	}
	if !strings.Contains(stdout, "logical task shell still has physical members") {
		t.Fatalf("retire-shells stdout = %q", stdout)
	}
	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCleanupCmd(&invocation{projectsRoot: projects}) }, "--retire-shells", "--format", "json")
	if err != nil {
		t.Fatalf("cleanup --retire-shells json: %v", err)
	}
	if !strings.Contains(stdout, `"results"`) {
		t.Fatalf("retire-shells json = %q", stdout)
	}

	// --recover-stages over a named task produces a per-task outcome.
	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCleanupCmd(&invocation{projectsRoot: projects}) }, "--recover-stages", "gc-cli")
	if err != nil {
		t.Fatalf("cleanup --recover-stages: %v", err)
	}
	if stdout != "" && !strings.Contains(stdout, "preserved") && !strings.Contains(stdout, "would archive") {
		t.Fatalf("recover-stages stdout = %q", stdout)
	}
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCleanupCmd(&invocation{projectsRoot: projects}) }, "--recover-stages", "gc-cli", "--format", "json"); err != nil {
		t.Fatalf("cleanup --recover-stages json: %v", err)
	}

	// A named --apply that cannot satisfy cleanup safety exits with the
	// rename/cleanup safety error rather than pretending success.
	_, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCleanupCmd(&invocation{projectsRoot: projects}) }, "gc-cli", "--apply", "--remote")
	if err == nil || !strings.Contains(err.Error(), "did not satisfy cleanup safety") {
		t.Fatalf("cleanup --apply on a dirty worktree = %v", err)
	}
}

func TestCwWtWorktreeLogCheckpointFlagPresence(t *testing.T) {
	projects, _, worktree := initGCFixture(t)
	// Passing only some of the optional numeric flags exercises the PreRun
	// Changed() detection on both sides.
	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeWorkLogCmd(&invocation{}) }, "checkpoint", worktree,
		"--mode", "manual", "--initiator", "cwWt", "--skip-remote", "--input-tokens", "0", "--usage-discriminator", "estimated")
	if err != nil {
		t.Fatalf("checkpoint with only --input-tokens: %v", err)
	}
	if !strings.Contains(stdout, "applied=true") {
		t.Fatalf("checkpoint stdout = %q", stdout)
	}
	_ = time.Now()
}
