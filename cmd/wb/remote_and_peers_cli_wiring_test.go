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
// tests exercise the wiring itself — that invocation.projectsRoot and the
// command's own flags actually reach the inner call — by driving each
// command through Execute() far enough to observe the inner function's own
// early, deterministic refusal (an unconfigured remote/hub), never a real
// network or daemon call.

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

func TestRemoteClaimCLIWiresProjectsRootAndArgsIntoRunRemoteClaim(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, newRemoteClaimCmd(&invocation{projectsRoot: root}), "task-7")
	if err == nil || !strings.Contains(err.Error(), "remote:\n  provider: git") {
		t.Fatalf("wb remote claim wiring: err=%v out=%q", err, out)
	}
}

func TestRemoteReleaseCLIWiresProjectsRootAndArgsIntoRunRemoteRelease(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, newRemoteReleaseCmd(&invocation{projectsRoot: root}), "task-7")
	if err == nil || !strings.Contains(err.Error(), "remote:\n  provider: git") {
		t.Fatalf("wb remote release wiring: err=%v out=%q", err, out)
	}
}

func TestRemoteClaimsCLIWiresProjectsRootIntoRunRemoteClaims(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, newRemoteClaimsCmd(&invocation{projectsRoot: root}))
	if err == nil || !strings.Contains(err.Error(), "remote:\n  provider: git") {
		t.Fatalf("wb remote claims wiring: err=%v out=%q", err, out)
	}
}

func TestRemoteMachinesCLIWiresProjectsRootIntoRunRemoteMachines(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, newRemoteMachinesCmd(&invocation{projectsRoot: root}))
	if err == nil || !strings.Contains(err.Error(), "remote:\n  provider: git") {
		t.Fatalf("wb remote machines wiring: err=%v out=%q", err, out)
	}
}

func TestRemoteStatusCLIWiresProjectsRootIntoRunRemoteStatus(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, newRemoteStatusCmd(&invocation{projectsRoot: root}))
	if err == nil || !strings.Contains(err.Error(), "remote:\n  provider: git") {
		t.Fatalf("wb remote status wiring: err=%v out=%q", err, out)
	}
}

func TestRemoteEnrollCLIWiresProjectsRootIntoRunRemoteEnroll(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, newRemoteEnrollCmd(&invocation{projectsRoot: root}))
	if err == nil || !strings.Contains(err.Error(), "--machine is required") {
		t.Fatalf("wb remote enroll wiring: err=%v out=%q", err, out)
	}
}

func TestPeersJoinCLIWiresProjectsRootAndArgsIntoRunPeersJoin(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, newPeersJoinCmd(&invocation{projectsRoot: root}), "not-a-url")
	if err == nil || !strings.Contains(err.Error(), "http") {
		t.Fatalf("wb peers join wiring: err=%v out=%q", err, out)
	}
}

func TestPeersInviteCLIWiresProjectsRootAndArgsIntoRunPeersInvite(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, newPeersInviteCmd(&invocation{projectsRoot: root}), "laptop")
	if err == nil || !strings.Contains(err.Error(), "no hub is configured") {
		t.Fatalf("wb peers invite wiring: err=%v out=%q", err, out)
	}
}

func TestPeersBlockCLIWiresProjectsRootAndArgsIntoRunPeersTrustChange(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, newPeersBlockCmd(&invocation{projectsRoot: root}), "laptop")
	if err == nil || !strings.Contains(err.Error(), "no hub is configured") {
		t.Fatalf("wb peers block wiring: err=%v out=%q", err, out)
	}
}

func TestPeersUnblockCLIWiresProjectsRootAndArgsIntoRunPeersTrustChange(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, newPeersUnblockCmd(&invocation{projectsRoot: root}), "laptop")
	if err == nil || !strings.Contains(err.Error(), "no hub is configured") {
		t.Fatalf("wb peers unblock wiring: err=%v out=%q", err, out)
	}
}

func TestPeersDisconnectCLIWiresProjectsRootAndArgsIntoRunPeersDisconnect(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, newPeersDisconnectCmd(&invocation{projectsRoot: root}), "laptop")
	if err == nil || !strings.Contains(err.Error(), "no hub is configured") {
		t.Fatalf("wb peers disconnect wiring: err=%v out=%q", err, out)
	}
}

func TestPeersGetCLIWiresProjectsRootAndArgsIntoRunPeersGet(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, newPeersGetCmd(&invocation{projectsRoot: root}), "upstream")
	if err == nil || !strings.Contains(err.Error(), "no upstream is configured") {
		t.Fatalf("wb peers get wiring: err=%v out=%q", err, out)
	}
}

func TestPeersListCLIWiresProjectsRootIntoRunPeersList(t *testing.T) {
	root := t.TempDir()
	out, err := remoteCLIExecute(t, newPeersListCmd(&invocation{projectsRoot: root}))
	if err != nil {
		t.Fatalf("wb peers list wiring: err=%v out=%q", err, out)
	}
	if !strings.Contains(out, "{") && !strings.Contains(out, "no peers") && !strings.Contains(out, "\n") {
		t.Fatalf("wb peers list produced no observable output: %q", out)
	}
}
