package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// These commands' RunE bodies are a single pass-through line into an
// independently-tested runXxx function; every existing test for that inner
// function calls it directly, bypassing the cobra wiring entirely. These
// tests exercise the wiring itself — that Execute() actually reaches the
// inner call at all, with the command's own parsed flags and args — by
// observing the inner function's own early, deterministic refusal (an
// unconfigured remote/hub), never a real network or daemon call.
//
// That refusal is the same text whichever projectsRoot reaches the inner
// call (an unconfigured remote/hub refuses identically regardless of root),
// so despite each test's *invocation carrying a real fixture root, none of
// these proves inv.projectsRoot itself was threaded through - only that
// Execute() reached the runXxx call at all. Do not read "Wires...Into..."
// here as a root-specific assertion (sneat-dev/wb#760 review B5); a test
// that must prove the root value itself reached its callee builds a fixture
// where a wrong root is observably different, the way
// TestSessionListCLIWiresProjectsRootIntoRunSessionList and
// TestDefaultTaskOffloadDependenciesStoreOpensUnderProjectsRoot do.

func remoteCLIExecute(t *testing.T, command *cobra.Command, args ...string) (string, error) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	command.SetArgs(args)
	err := command.Execute()
	return out.String(), err
}

func TestRemoteClaimCLIWiresIntoRunRemoteClaim(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, remoteCommandForTest(&invocation{projectsRoot: root}, "claim"), "task-7")
	if err == nil || !strings.Contains(err.Error(), "remote:\n  provider: git") {
		t.Fatalf("wb remote claim wiring: err=%v out=%q", err, out)
	}
}

func TestRemoteReleaseCLIWiresIntoRunRemoteRelease(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, remoteCommandForTest(&invocation{projectsRoot: root}, "release"), "task-7")
	if err == nil || !strings.Contains(err.Error(), "remote:\n  provider: git") {
		t.Fatalf("wb remote release wiring: err=%v out=%q", err, out)
	}
}

func TestRemoteClaimsCLIWiresIntoRunRemoteClaims(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, remoteCommandForTest(&invocation{projectsRoot: root}, "claims"))
	if err == nil || !strings.Contains(err.Error(), "remote:\n  provider: git") {
		t.Fatalf("wb remote claims wiring: err=%v out=%q", err, out)
	}
}

func TestRemoteMachinesCLIWiresIntoRunRemoteMachines(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, remoteCommandForTest(&invocation{projectsRoot: root}, "machines"))
	if err == nil || !strings.Contains(err.Error(), "remote:\n  provider: git") {
		t.Fatalf("wb remote machines wiring: err=%v out=%q", err, out)
	}
}

func TestRemoteStatusCLIWiresIntoRunRemoteStatus(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, remoteCommandForTest(&invocation{projectsRoot: root}, "status"))
	if err == nil || !strings.Contains(err.Error(), "remote:\n  provider: git") {
		t.Fatalf("wb remote status wiring: err=%v out=%q", err, out)
	}
}

func TestRemoteEnrollCLIWiresIntoRunRemoteEnroll(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, remoteCommandForTest(&invocation{projectsRoot: root}, "enroll"))
	if err == nil || !strings.Contains(err.Error(), "--machine is required") {
		t.Fatalf("wb remote enroll wiring: err=%v out=%q", err, out)
	}
}
