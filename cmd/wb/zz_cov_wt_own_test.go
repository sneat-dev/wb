package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestCwWtWorktreeOwnRecordsFlagsAsCustody(t *testing.T) {
	checkout := cwWtGitRepo(t, filepath.Join(t.TempDir(), "checkout"))
	var out, errOut bytes.Buffer
	err := cwWtExecOut(t, t.TempDir(), newWorktreeOwnCmd, &out, &errOut,
		checkout, "--mode", "manual", "--initiator", "cwWt-operator",
		"--pid", "4242", "--runtime", "claude-code", "--model", "sonnet", "--agent-id", "native-1")
	if err != nil {
		t.Fatalf("worktree own: %v (stderr=%s)", err, errOut.String())
	}
	text := out.String()
	if !strings.Contains(text, checkout+": owner recorded as ") || !strings.Contains(text, "(pid 4242)") {
		t.Fatalf("worktree own stdout = %q", text)
	}
	if !strings.Contains(text, "claude-code") {
		t.Fatalf("worktree own did not name the runtime: %q", text)
	}
}

func TestCwWtWorktreeOwnAdmissionAndDeclarationErrors(t *testing.T) {
	checkout := cwWtGitRepo(t, filepath.Join(t.TempDir(), "checkout"))

	// Manual mode without an initiator is refused before anything is written.
	_, _, err := cwCovExec(t, t.TempDir(), newWorktreeOwnCmd, checkout, "--mode", "manual")
	if err == nil || !strings.Contains(err.Error(), "--initiator") {
		t.Fatalf("manual without initiator error = %v", err)
	}

	// An unknown mode is refused.
	_, _, err = cwCovExec(t, t.TempDir(), newWorktreeOwnCmd, checkout, "--mode", "yolo")
	if err == nil || !strings.Contains(err.Error(), "unsupported execution mode") {
		t.Fatalf("unknown mode error = %v", err)
	}

	// Nothing declared at all is a usage-style refusal.
	_, _, err = cwCovExec(t, t.TempDir(), newWorktreeOwnCmd, checkout, "--mode", "manual", "--initiator", "cwWt-operator")
	if err == nil || !strings.Contains(err.Error(), "nothing to declare") {
		t.Fatalf("undeclared identity error = %v", err)
	}
}

func TestCwWtWorktreeOwnUsesLiveRegisteredIdentity(t *testing.T) {
	checkout := cwWtGitRepo(t, filepath.Join(t.TempDir(), "checkout"))
	worktrees.SetSessionResolver(func() (worktrees.AgentIdentity, bool) {
		return worktrees.AgentIdentity{
			Runtime: "codex", AgentID: "native-real", Model: "gpt-5",
			PID: 777, WBSessionID: "wbs-real", Registered: true,
		}, true
	})
	t.Cleanup(func() { worktrees.SetSessionResolver(nil) })

	var out, errOut bytes.Buffer
	if err := cwWtExecOut(t, t.TempDir(), newWorktreeOwnCmd, &out, &errOut, checkout, "--pid", "1"); err != nil {
		t.Fatalf("worktree own with a live session: %v (stderr=%s)", err, errOut.String())
	}
	if !strings.Contains(out.String(), "(pid 777)") {
		t.Fatalf("the live session identity must win over ambient flags: %q", out.String())
	}
}

func TestCwWtWorktreeOwnRecoversFromEnvironment(t *testing.T) {
	checkout := cwWtGitRepo(t, filepath.Join(t.TempDir(), "checkout"))
	t.Setenv(worktrees.EnvAgentRuntime, "copilot-cli")
	t.Setenv(worktrees.EnvAgentModel, "gpt-5-mini")
	t.Setenv(worktrees.EnvAgentPID, "31337")

	// Not testenv.Isolate: this test's whole point is the ambient declaration,
	// which Isolate deliberately clears.
	previousRoot := projectsRoot
	projectsRoot = t.TempDir()
	t.Cleanup(func() { projectsRoot = previousRoot })

	command := newWorktreeOwnCmd()
	command.SilenceUsage = true
	command.SilenceErrors = true
	var out, errOut bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SetArgs([]string{checkout, "--mode", "manual", "--initiator", "cwWt-operator"})
	if err := command.Execute(); err != nil {
		t.Fatalf("worktree own from the environment: %v (stderr=%s)", err, errOut.String())
	}
	if !strings.Contains(out.String(), "(pid 31337)") {
		t.Fatalf("the environment identity was not recorded: %q", out.String())
	}
}

func TestCwWtWorktreeOwnRecordCustodyFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	_, _, err := cwCovExec(t, t.TempDir(), newWorktreeOwnCmd, missing,
		"--mode", "manual", "--initiator", "cwWt-operator", "--pid", "9")
	if err == nil {
		t.Fatal("worktree own against a missing path must fail")
	}
	if _, statErr := os.Stat(missing); !os.IsNotExist(statErr) {
		t.Fatalf("the missing path was created: %v", statErr)
	}
}
