//go:build e2e

package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/cli/cmdsession"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionrun"
)

func TestE2ECollaborationCLIRegistersAndJoinsRealLinkedWorktree(t *testing.T) {
	projects, _, worktree := initGCFixture(t)
	canonical := filepath.Join(projects, "acme", "app")
	inv := &invocation{projectsRoot: projects}
	canonicalInfo, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeInfoCmd(inv) }, canonical, "--format", "json")
	if err != nil || strings.Contains(canonicalInfo, `"collaboration"`) {
		t.Fatalf("canonical info lost pre-existing behavior or claimed linked coordination = %q, %v", canonicalInfo, err)
	}
	makeRegister := func() *cobra.Command {
		deps := newSessionDependencies(inv)
		registrar := sessionrun.DefaultRegisterDependencies()
		registrar.CurrentPID = func() int { return -1 }
		registrar.RuntimeProcess = func(int, string) bool { return true }
		deps.Register = sessionrun.NewRegister(registrar).Register
		return cmdsession.NewRegister(newCLIRuntime(inv), deps)
	}
	ownerRegister := makeRegister()
	if _, err := executeCollaborationCommand(t, ownerRegister, "--pid", strconv.Itoa(os.Getpid()), "--runtime", "codex", "--native-harness-id", "collab-owner", "--wb-session-id", "wbs-owner"); err != nil {
		t.Fatal(err)
	}
	service, err := newCollaborationService(inv)
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := service.Ports.Resolve(context.Background(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := service.Ports.ObserveLegacy(checkout)
	if err != nil || legacy.ID == "" {
		t.Fatalf("legacy owner = %+v, %v", legacy, err)
	}
	info, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeInfoCmd(inv) }, worktree, "--format", "json")
	if err != nil || !strings.Contains(info, legacy.ID) || !strings.Contains(info, `"joined_sessions"`) {
		t.Fatalf("legacy info = %q, %v", info, err)
	}
	coordinationDir := filepath.Join(projects, ".wb", "worktree-collaboration")
	if _, err := os.Stat(coordinationDir); !os.IsNotExist(err) {
		t.Fatalf("read-only info initialized coordination metadata: %v", err)
	}
	if _, err := executeCollaborationCommand(t, newWorktreeTakeOwnershipCmd(collaborationFactory(inv)), checkout.ID, "--expected-owner", "none", "--force", "--reason", "reviewed"); err == nil {
		t.Fatal("none bypassed legacy owner")
	}
	if _, err := os.Stat(coordinationDir); !os.IsNotExist(err) {
		t.Fatalf("refused wrong-owner takeover initialized coordination metadata: %v", err)
	}
	if _, err := executeCollaborationCommand(t, newWorktreeTakeOwnershipCmd(collaborationFactory(inv)), checkout.ID, "--expected-owner", legacy.ID, "--force", "--reason", "reviewed"); err != nil {
		t.Fatalf("owner took exact legacy observation: %v", err)
	}
	peerRegister := makeRegister()
	output, err := executeCollaborationCommand(t, peerRegister, "--pid", strconv.Itoa(os.Getpid()), "--runtime", "codex", "--native-harness-id", "collab-peer", "--wb-session-id", "wbs-peer", "--join", worktree)
	if err != nil || !strings.Contains(output, "joined") {
		t.Fatalf("registered peer join = %q, %v", output, err)
	}
	view, err := service.Inspect(context.Background(), worktree)
	if err != nil || view.Owner != "wbs-owner" || len(view.Joined) != 2 {
		t.Fatalf("owner and peer view = %+v, %v", view, err)
	}
	if _, err := executeCollaborationCommand(t, newWorktreeTransferOwnershipCmd(collaborationFactory(inv)), worktree, "--to-session", "wbs-peer"); err == nil {
		t.Fatal("peer transferred ownership without being owner")
	}
	failedRegister := makeRegister()
	output, err = executeCollaborationCommand(t, failedRegister, "--pid", strconv.Itoa(os.Getpid()), "--runtime", "codex", "--native-harness-id", "collab-third", "--wb-session-id", "wbs-third", "--join", filepath.Join(t.TempDir(), "missing"))
	if err == nil || !strings.Contains(err.Error(), "registered but not joined") || !strings.Contains(output, "registered") {
		t.Fatalf("partial registration = %q, %v", output, err)
	}
	if record, found := session.ResolveForProcess(filepath.Join(projects, ".wb", session.DirName), os.Getpid()); !found || record.WBSessionID != "wbs-third" {
		t.Fatalf("partial registration lost = %+v, %t", record, found)
	}
	statePath := filepath.Join(coordinationDir, checkout.ID+".json")
	if err := os.WriteFile(statePath, []byte(`{"version":1,"owner":"forged"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeInfoCmd(inv) }, worktree, "--format", "json"); err == nil {
		t.Fatal("corrupt linked-worktree coordination snapshot displayed as valid info")
	}
}
