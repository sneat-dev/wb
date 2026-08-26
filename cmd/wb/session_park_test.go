package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestSessionParkCapturesAllOwnedWorktrees(t *testing.T) {
	source := session.Record{PID: 41, WBSessionID: "wbs-source", StartedAt: time.Unix(10, 0)}
	results := []worktrees.ListResult{{WorktreeDir: "/tmp/a", Owners: []worktrees.OwnerView{{OwnerRegistration: worktrees.OwnerRegistration{PID: 41, At: time.Unix(11, 0)}}}}, {WorktreeDir: "/tmp/b", Owners: []worktrees.OwnerView{{OwnerRegistration: worktrees.OwnerRegistration{PID: 41, At: time.Unix(12, 0)}}}}, {WorktreeDir: "/tmp/old", Owners: []worktrees.OwnerView{{OwnerRegistration: worktrees.OwnerRegistration{PID: 41, At: time.Unix(9, 0)}}}}}
	count := 0
	for _, result := range results {
		if ownedBySession(result, source) {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("owned worktrees = %d, want 2", count)
	}
}

func TestSessionParkDoesNotTreatDifferentPIDAsOwned(t *testing.T) {
	source := session.Record{PID: 41, WBSessionID: "wbs-source", StartedAt: time.Unix(10, 0)}
	result := worktrees.ListResult{WorktreeDir: "/tmp/other", Owners: []worktrees.OwnerView{{OwnerRegistration: worktrees.OwnerRegistration{PID: 42, At: time.Unix(11, 0)}}}}
	if ownedBySession(result, source) {
		t.Fatal("different session worktree attributed")
	}
}

func TestSessionParkRemoteReconstructabilityRefusesDirtyEvidence(t *testing.T) {
	bundle := sessionpark.Bundle{ParkedSessionID: "park-dirty", Worktrees: []sessionpark.Worktree{{WorktreeDir: "/tmp/dirty", Head: "a", RemoteHead: "a", Dirty: true}}}
	err := validateParkedRemoteBundle(bundle, "hetzner-vm1")
	if err == nil || !strings.Contains(err.Error(), "/tmp/dirty") || !strings.Contains(err.Error(), "dirty=true") {
		t.Fatalf("error = %v", err)
	}
}

// These command-level cases deliberately exercise the public park/resume
// journey rather than the old reconstructability helper.  They are the
// regression boundary for delayed SSH handoff: a remotely reconstructable
// parked bundle must reach the courier/receiver path instead of being rejected
// by the former hard-coded cross-machine gate.
func TestSessionResumeRemoteSingleWorktreeReachesTransport(t *testing.T) {
	parkedID, config := remoteResumeFixture(t, []sessionpark.Worktree{cleanParkedWorktree("/tmp/one", "feature/one")})
	command := newSessionResumeCmd()
	command.SetArgs([]string{parkedID, "--to", "target", "--via", "ssh", "--config", config})
	command.SetOut(new(bytes.Buffer))
	if err := command.Execute(); err != nil {
		t.Fatalf("remote single-worktree resume = %v; want delivery through the session courier/receiver boundary", err)
	}
}

func TestSessionResumeRemoteBundleReachesTransportAsOneSession(t *testing.T) {
	parkedID, config := remoteResumeFixture(t, []sessionpark.Worktree{
		cleanParkedWorktree("/tmp/one", "feature/one"),
		cleanParkedWorktree("/tmp/two", "feature/two"),
	})
	command := newSessionResumeCmd()
	command.SetArgs([]string{parkedID, "--to", "target", "--via", "ssh", "--config", config})
	command.SetOut(new(bytes.Buffer))
	if err := command.Execute(); err != nil {
		t.Fatalf("remote bundled resume = %v; want one successor transport request for the complete parked bundle", err)
	}
}

func remoteResumeFixture(t *testing.T, worktrees []sessionpark.Worktree) (string, string) {
	t.Helper()
	previousProjectsRoot := projectsRoot
	projectsRoot = t.TempDir()
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })
	home := filepath.Join(t.TempDir(), "wb-home")
	t.Setenv("WB_HOME", home)
	parkedID := "park-remote"
	store := sessionpark.NewStore(filepath.Join(home, "parked-sessions"))
	bundle := sessionpark.Bundle{
		SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: parkedID,
		Source:       session.Record{PID: 41, WBSessionID: "wbs-parked-source", Machine: "source", Runtime: "codex", StartedAt: time.Unix(10, 0).UTC()},
		Continuation: "continue the parked task", Worktrees: worktrees, ParkedAt: time.Unix(11, 0).UTC(),
	}
	if _, err := store.Create(bundle); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(config, []byte("session_move:\n  targets:\n    target:\n      default_courier: ssh\n      ssh:\n        host: target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return parkedID, config
}

func cleanParkedWorktree(path, branch string) sessionpark.Worktree {
	head := strings.Repeat("a", 40)
	return sessionpark.Worktree{
		Repository: "acme/app", RepositoryRemote: "https://github.com/acme/app.git",
		WorktreeDir: path, Branch: branch, Head: head, RemoteHead: head,
		WorkLogReference: "worklog:parked/remote/" + strings.Repeat("b", 64),
	}
}
