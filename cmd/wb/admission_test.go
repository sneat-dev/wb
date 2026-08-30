package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func TestAdmissionFlagsArePresentOnRemainingMutatingVerbs(t *testing.T) {
	// Keep this check at the command boundary: these flags are the explicit
	// contract users select before a backend can inspect WB_HOME or Git.
	checks := []struct {
		name        string
		flagPresent func(string) bool
	}{
		{name: "adopt", flagPresent: func(name string) bool { return newWorktreeAdoptCmd().Flags().Lookup(name) != nil }},
		{name: "rename", flagPresent: func(name string) bool { return newWorktreeRenameCmd().Flags().Lookup(name) != nil }},
		{name: "own", flagPresent: func(name string) bool { return newWorktreeOwnCmd().Flags().Lookup(name) != nil }},
		{name: "correct-identity", flagPresent: func(name string) bool { return newWorktreeCorrectIdentityCmd().Flags().Lookup(name) != nil }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			for _, name := range []string{"mode", "initiator"} {
				if !check.flagPresent(name) {
					t.Fatalf("%s is missing --%s", check.name, name)
				}
			}
		})
	}
	log := newWorktreeWorkLogCmd()
	for _, name := range []string{"mode", "initiator"} {
		if log.PersistentFlags().Lookup(name) == nil {
			t.Fatalf("worktree log is missing persistent --%s", name)
		}
	}
}

func TestAgentAdmissionRejectsRemainingMutationsBeforeWBHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "wb-home")
	t.Setenv(wbhome.EnvOverride, home)
	t.Setenv(worktrees.EnvAgentPID, "")
	t.Setenv(worktrees.EnvAgentRuntime, "")
	t.Setenv(worktrees.EnvAgentModel, "")
	t.Setenv(worktrees.EnvAgentID, "")
	t.Setenv(worktrees.EnvSessionID, "")
	worktrees.SetSessionResolver(func() (worktrees.AgentIdentity, bool) { return worktrees.AgentIdentity{}, false })
	t.Cleanup(func() { worktrees.SetSessionResolver(nil) })
	projects := filepath.Join(t.TempDir(), "projects")
	prompt := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(prompt, []byte("admission test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		args []string
	}{
		{name: "adopt", args: []string{"worktree", "adopt", "/tmp/external", "--apply", "--mode", "agent"}},
		{name: "rename", args: []string{"--projects-root", projects, "worktree", "rename", "old", "new", "--apply", "--mode", "agent", "--original-prompt-file", prompt}},
		{name: "own", args: []string{"--projects-root", projects, "worktree", "own", ".", "--mode", "agent"}},
		{name: "correct-identity", args: []string{"worktree", "correct-identity", "effort", "run", strings.Repeat("a", 64), "--mode", "agent"}},
		{name: "log-init", args: []string{"--projects-root", projects, "worktree", "log", "init", ".", "--mode", "agent"}},
		{name: "log-steer", args: []string{"--projects-root", projects, "worktree", "log", "steer", ".", "--mode", "agent", "--prompt", "test"}},
		{name: "log-checkpoint", args: []string{"--projects-root", projects, "worktree", "log", "checkpoint", ".", "--mode", "agent", "--skip-remote"}},
		{name: "log-refresh", args: []string{"--projects-root", projects, "worktree", "log", "refresh", ".", "--mode", "agent"}},
		{name: "log-integrate", args: []string{"--projects-root", projects, "worktree", "log", "integrate", ".", "--mode", "agent"}},
		{name: "log-handoff", args: []string{"--projects-root", projects, "worktree", "log", "handoff", ".", "--mode", "agent"}},
		{name: "log-recover", args: []string{"--projects-root", projects, "worktree", "log", "recover", ".", "--mode", "agent", "--apply"}},
		{name: "log-finalize", args: []string{"--projects-root", projects, "worktree", "log", "finalize", ".", "--mode", "agent"}},
		{name: "log-sync", args: []string{"--projects-root", projects, "worktree", "log", "sync", ".", "--mode", "agent"}},
		{name: "log-archive", args: []string{"--projects-root", projects, "worktree", "log", "archive", ".", "--mode", "agent", "--apply"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(test.args, &stdout, &stderr); code != exitFindings || !strings.Contains(stderr.String(), "live registered session") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if _, err := os.Stat(filepath.Join(home, "worktrees")); !os.IsNotExist(err) {
				t.Fatalf("agent admission touched WB_HOME: stat err=%v", err)
			}
		})
	}
}

func TestAdmissionRequiresInitiatorForManualAndUsesLiveIdentityForAgent(t *testing.T) {
	manual := newWorktreeOwnCmd()
	if err := manual.Flags().Set("mode", "manual"); err != nil {
		t.Fatal(err)
	}
	if _, release, err := requireMutationAdmission(manual, true); err == nil || !strings.Contains(err.Error(), "--initiator") {
		t.Fatalf("manual admission error = %v, want --initiator", err)
	} else {
		release()
	}
	if err := manual.Flags().Set("initiator", "human-operator"); err != nil {
		t.Fatal(err)
	}
	if _, release, err := requireMutationAdmission(manual, true); err != nil {
		t.Fatalf("manual admission with initiator: %v", err)
	} else {
		release()
	}

	worktrees.SetSessionResolver(func() (worktrees.AgentIdentity, bool) {
		return worktrees.AgentIdentity{Runtime: "codex", AgentID: "real", Model: "gpt-5", PID: 4242, WBSessionID: "wbs-real", Registered: true}, true
	})
	t.Cleanup(func() { worktrees.SetSessionResolver(nil) })
	agent := newWorktreeOwnCmd()
	if err := agent.Flags().Set("mode", "agent"); err != nil {
		t.Fatal(err)
	}
	if err := agent.Flags().Set("agent-id", "forged"); err != nil {
		t.Fatal(err)
	}
	if err := agent.Flags().Set("runtime", "forged-runtime"); err != nil {
		t.Fatal(err)
	}
	if err := agent.Flags().Set("model", "forged-model"); err != nil {
		t.Fatal(err)
	}
	if err := agent.Flags().Set("pid", "9999"); err != nil {
		t.Fatal(err)
	}
	identity, release, err := requireMutationAdmission(agent, true)
	defer release()
	if err != nil {
		t.Fatalf("registered agent admission: %v", err)
	}
	if identity.WBSessionID != "wbs-real" || identity.AgentID != "real" || identity.Runtime != "codex" || identity.Model != "gpt-5" || identity.PID != 4242 {
		t.Fatalf("admitted identity = %+v, want resolver identity", identity)
	}
}

func TestAutoAdmissionInfersAgentModeFromIdentityFlags(t *testing.T) {
	worktrees.SetSessionResolver(func() (worktrees.AgentIdentity, bool) { return worktrees.AgentIdentity{}, false })
	t.Cleanup(func() { worktrees.SetSessionResolver(nil) })
	command := newWorktreeOwnCmd()
	if err := command.Flags().Set("runtime", "codex"); err != nil {
		t.Fatal(err)
	}
	if _, release, err := requireMutationAdmission(command, true); err == nil || !strings.Contains(err.Error(), "live registered session") {
		t.Fatalf("auto identity admission error = %v, want live-session refusal", err)
	} else {
		release()
	}
}

func TestAutoAdmissionInfersAgentModeFromAmbientIdentity(t *testing.T) {
	t.Setenv(worktrees.EnvAgentRuntime, "codex")
	worktrees.SetSessionResolver(func() (worktrees.AgentIdentity, bool) { return worktrees.AgentIdentity{}, false })
	t.Cleanup(func() { worktrees.SetSessionResolver(nil) })
	command := newWorktreeOwnCmd()
	if _, release, err := requireMutationAdmission(command, true); err == nil || !strings.Contains(err.Error(), "live registered session") {
		t.Fatalf("ambient identity admission error = %v, want live-session refusal", err)
	} else {
		release()
	}
}

func TestAdmissionAllowsExplicitReadOnlyDryRunsWithoutSession(t *testing.T) {
	home := filepath.Join(t.TempDir(), "wb-home")
	t.Setenv(wbhome.EnvOverride, home)
	worktrees.SetSessionResolver(func() (worktrees.AgentIdentity, bool) { return worktrees.AgentIdentity{}, false })
	t.Cleanup(func() { worktrees.SetSessionResolver(nil) })
	log := newWorktreeWorkLogCmd()
	recoverCommand, _, err := log.Find([]string{"recover"})
	if err != nil {
		t.Fatal(err)
	}
	archiveCommand, _, err := log.Find([]string{"archive"})
	if err != nil {
		t.Fatal(err)
	}
	commands := []*cobra.Command{newWorktreeAdoptCmd(), newWorktreeRenameCmd(), recoverCommand, archiveCommand}
	for _, command := range commands {
		if command == recoverCommand || command == archiveCommand {
			if err := log.PersistentFlags().Set("mode", "agent"); err != nil {
				t.Fatal(err)
			}
		} else if err := command.Flags().Set("mode", "agent"); err != nil {
			t.Fatal(err)
		}
		if _, release, err := requireMutationAdmission(command, false); err != nil {
			t.Fatalf("%s dry-run admission: %v", command.Name(), err)
		} else {
			release()
		}
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("dry-run admission touched WB_HOME: stat err=%v", err)
	}
}
