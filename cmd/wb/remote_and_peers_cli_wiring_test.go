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
	out, err := remoteCLIExecute(t, newRemoteClaimCmd(&invocation{projectsRoot: root}), "task-7")
	if err == nil || !strings.Contains(err.Error(), "remote:\n  provider: git") {
		t.Fatalf("wb remote claim wiring: err=%v out=%q", err, out)
	}
}

func TestRemoteReleaseCLIWiresIntoRunRemoteRelease(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, newRemoteReleaseCmd(&invocation{projectsRoot: root}), "task-7")
	if err == nil || !strings.Contains(err.Error(), "remote:\n  provider: git") {
		t.Fatalf("wb remote release wiring: err=%v out=%q", err, out)
	}
}

func TestRemoteClaimsCLIWiresIntoRunRemoteClaims(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, newRemoteClaimsCmd(&invocation{projectsRoot: root}))
	if err == nil || !strings.Contains(err.Error(), "remote:\n  provider: git") {
		t.Fatalf("wb remote claims wiring: err=%v out=%q", err, out)
	}
}

func TestRemoteMachinesCLIWiresIntoRunRemoteMachines(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, newRemoteMachinesCmd(&invocation{projectsRoot: root}))
	if err == nil || !strings.Contains(err.Error(), "remote:\n  provider: git") {
		t.Fatalf("wb remote machines wiring: err=%v out=%q", err, out)
	}
}

func TestRemoteStatusCLIWiresIntoRunRemoteStatus(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, newRemoteStatusCmd(&invocation{projectsRoot: root}))
	if err == nil || !strings.Contains(err.Error(), "remote:\n  provider: git") {
		t.Fatalf("wb remote status wiring: err=%v out=%q", err, out)
	}
}

func TestRemoteEnrollCLIWiresIntoRunRemoteEnroll(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, newRemoteEnrollCmd(&invocation{projectsRoot: root}))
	if err == nil || !strings.Contains(err.Error(), "--machine is required") {
		t.Fatalf("wb remote enroll wiring: err=%v out=%q", err, out)
	}
}

func TestPeersJoinCLIWiresIntoRunPeersJoin(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, newPeersJoinCmd(&invocation{projectsRoot: root}), "not-a-url")
	if err == nil || !strings.Contains(err.Error(), "http") {
		t.Fatalf("wb peers join wiring: err=%v out=%q", err, out)
	}
}

func TestPeersInviteCLIWiresIntoRunPeersInvite(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, newPeersInviteCmd(&invocation{projectsRoot: root}), "laptop")
	if err == nil || !strings.Contains(err.Error(), "no hub is configured") {
		t.Fatalf("wb peers invite wiring: err=%v out=%q", err, out)
	}
}

func TestPeersBlockCLIWiresIntoRunPeersTrustChange(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, newPeersBlockCmd(&invocation{projectsRoot: root}), "laptop")
	if err == nil || !strings.Contains(err.Error(), "no hub is configured") {
		t.Fatalf("wb peers block wiring: err=%v out=%q", err, out)
	}
}

func TestPeersUnblockCLIWiresIntoRunPeersTrustChange(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, newPeersUnblockCmd(&invocation{projectsRoot: root}), "laptop")
	if err == nil || !strings.Contains(err.Error(), "no hub is configured") {
		t.Fatalf("wb peers unblock wiring: err=%v out=%q", err, out)
	}
}

func TestPeersDisconnectCLIWiresIntoRunPeersDisconnect(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, newPeersDisconnectCmd(&invocation{projectsRoot: root}), "laptop")
	if err == nil || !strings.Contains(err.Error(), "no hub is configured") {
		t.Fatalf("wb peers disconnect wiring: err=%v out=%q", err, out)
	}
}

func TestPeersGetCLIWiresIntoRunPeersGet(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, newPeersGetCmd(&invocation{projectsRoot: root}), "upstream")
	if err == nil || !strings.Contains(err.Error(), "no upstream is configured") {
		t.Fatalf("wb peers get wiring: err=%v out=%q", err, out)
	}
}

func TestPeersListCLIWiresIntoRunPeersList(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, newPeersListCmd(&invocation{projectsRoot: root}))
	if err != nil {
		t.Fatalf("wb peers list wiring: err=%v out=%q", err, out)
	}
	if !strings.Contains(out, "{") && !strings.Contains(out, "no peers") && !strings.Contains(out, "\n") {
		t.Fatalf("wb peers list produced no observable output: %q", out)
	}
}
