package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func TestMutationAdmissionPreservesActualFlagLookupErrors(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"mode", "initiator"} {
		for _, wrongType := range []bool{false, true} {
			command := &cobra.Command{Use: "private"}
			if stage == "initiator" {
				command.Flags().String("mode", "manual", "")
			}
			if wrongType {
				command.Flags().Bool(stage, false, "")
			}
			_, expected := command.Flags().GetString(stage)
			if expected == nil {
				t.Fatal("fixture must produce actual pflag refusal")
			}
			identity, release, err := requireMutationAdmission(command, true)
			if err == nil || err.Error() != expected.Error() {
				t.Fatalf("%s wrongType=%v err=%v want %v", stage, wrongType, err, expected)
			}
			if identity.Registered {
				t.Fatalf("lookup failure returned registered identity: %+v", identity)
			}
			if release == nil {
				t.Fatal("lookup failure omitted harmless release")
			}
			release()
		}
	}
}

func TestMutationInitiatorUsesActualOptionalFlagContract(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{Use: "private"}
	if got := mutationInitiator(command); got != "" {
		t.Fatalf("missing initiator=%q", got)
	}
	command.Flags().String("initiator", "  named human  ", "")
	if got := mutationInitiator(command); got != "named human" {
		t.Fatalf("trimmed initiator=%q", got)
	}
}

func TestRepoStatusRejectsRootFleetSelectorBeforeBackend(t *testing.T) {
	t.Parallel()
	root := newRootCmdFor(&invocation{})
	command, rest, err := root.Find([]string{"repo", "status"})
	if err != nil || len(rest) != 0 || persistentCommandID(command) != "repo status" {
		t.Fatalf("actual child=%v rest=%v err=%v", command, rest, err)
	}
	var out, diagnostics bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&diagnostics)
	if err := root.PersistentFlags().Set("projects-root", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	err = rejectIgnoredPersistentFlags(command, []string{"private-repository-path"})
	if err == nil || err.Error() != "--projects-root is not supported by repo status; see docs/cli-flag-matrix.md" {
		t.Fatalf("selector error=%v", err)
	}
	if out.Len() != 0 || diagnostics.Len() != 0 {
		t.Fatalf("admission wrote output: out=%q err=%q", out.String(), diagnostics.String())
	}
}

//nolint:paralleltest // Genuine os.UserHomeDir fallback contract mutates process-wide HOME/USERPROFILE and WB_PROJECTS_ROOT.
func TestDefaultProjectsRootUsesActualHomeContract(t *testing.T) {
	if runtime.GOOS == "android" || runtime.GOOS == "ios" {
		t.Skip("os.UserHomeDir uses an OS-defined constant fallback on this platform")
	}
	t.Setenv(wbhome.EnvOverride, "")
	homeVariable := "HOME"
	switch runtime.GOOS {
	case "windows":
		homeVariable = "USERPROFILE"
	case "plan9":
		homeVariable = "home"
	}
	t.Setenv(homeVariable, "")
	if _, err := os.UserHomeDir(); err == nil {
		t.Fatal("missing actual home must refuse on this platform")
	}
	if got := defaultProjectsRoot(); got != "" {
		t.Fatalf("missing-home default root=%q", got)
	}
	privateHome := t.TempDir()
	t.Setenv(homeVariable, privateHome)
	if got, want := defaultProjectsRoot(), filepath.Join(privateHome, "projects"); got != want {
		t.Fatalf("private-home default root=%q, want %q", got, want)
	}
	privateOverride := filepath.Join(privateHome, "explicit-projects")
	t.Setenv(wbhome.EnvOverride, "  "+privateOverride+"  ")
	if got := defaultProjectsRoot(); got != privateOverride {
		t.Fatalf("trimmed override root=%q, want %q", got, privateOverride)
	}
}

func TestLandingGuardPreservesExistingAnnotationAndPointer(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{Use: "private", Annotations: map[string]string{"existing": "keep"}}
	got := markLandingGuard(command, landingGuardByReceipt)
	if got != command || command.Annotations["existing"] != "keep" || command.Annotations[landingGuardAnnotation] != landingGuardByReceipt {
		t.Fatalf("guard annotations=%v samePointer=%v", command.Annotations, got == command)
	}
	if strings.TrimSpace(command.Use) != "private" {
		t.Fatal("guard altered command identity")
	}
	empty := &cobra.Command{Use: "empty"}
	if got := markLandingGuard(empty, landingGuardByWorktree); got != empty || empty.Annotations[landingGuardAnnotation] != landingGuardByWorktree {
		t.Fatalf("nil-map guard annotations=%v samePointer=%v", empty.Annotations, got == empty)
	}
}

//nolint:paralleltest // Clears actual process identity environment and changes the existing global session resolver fixture.
func TestMutationAdmissionAutoUsesControlledRegistryObservation(t *testing.T) {
	for _, name := range []string{worktrees.EnvAgentPID, worktrees.EnvAgentRuntime, worktrees.EnvAgentModel, worktrees.EnvAgentID, worktrees.EnvSessionID} {
		t.Setenv(name, "")
	}
	t.Cleanup(func() { worktrees.SetSessionResolver(nil) })
	command := &cobra.Command{Use: "private"}
	addMutationAdmissionFlags(command)
	calls := 0
	worktrees.SetSessionResolver(func() (worktrees.AgentIdentity, bool) { calls++; return worktrees.AgentIdentity{}, false })
	identity, release, err := requireMutationAdmission(command, true)
	if err != nil || identity != (worktrees.AgentIdentity{}) || release == nil || calls != 1 {
		t.Fatalf("plain auto identity=%+v release=%v err=%v calls=%d", identity, release != nil, err, calls)
	}
	release()
	observed := worktrees.AgentIdentity{Registered: true, PID: os.Getpid(), Runtime: "controlled-runtime", AgentID: "controlled-agent", Model: "controlled-model", WBSessionID: "controlled-session"}
	calls = 0
	worktrees.SetSessionResolver(func() (worktrees.AgentIdentity, bool) { calls++; return observed, true })
	identity, release, err = requireMutationAdmission(command, true)
	if err != nil || identity != observed || release == nil || calls != 1 {
		t.Fatalf("registered observation identity=%+v release=%v err=%v calls=%d", identity, release != nil, err, calls)
	}
	release()
	// This tests the existing resolver contract; it does not register a native session or claim custody.
}

func TestMutationAdmissionRejectsUnsupportedModeWithoutRegistry(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{Use: "private"}
	addMutationAdmissionFlags(command)
	if err := command.Flags().Set("mode", "  unsupported  "); err != nil {
		t.Fatal(err)
	}
	identity, release, err := requireMutationAdmission(command, true)
	if err == nil || err.Error() != `unsupported execution mode "unsupported"; use auto, agent, or manual` || identity != (worktrees.AgentIdentity{}) || release == nil {
		t.Fatalf("unsupported identity=%+v release=%v error=%v", identity, release != nil, err)
	}
	release()
}
