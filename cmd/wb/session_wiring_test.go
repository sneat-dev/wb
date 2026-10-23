package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionrun"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionRootDefaultsUseLazyPrivateRootAndDetachedReadPolicy(t *testing.T) {
	t.Parallel()
	inv := &invocation{projectsRoot: t.TempDir()}
	command := newSessionCmd(inv)
	root := t.TempDir()
	inv.projectsRoot = root
	var out, errOut bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	command.SetContext(ctx)
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SetArgs([]string{"list", "--format", "json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "[]" || !strings.Contains(errOut.String(), "no session has registered") {
		t.Fatalf("out=%q stderr=%q", out.String(), errOut.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".wb")); !os.IsNotExist(err) {
		t.Fatalf("read created home: %v", err)
	}
	command = newSessionCmd(inv)
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SetArgs([]string{"register", "--pid", fmt.Sprint(os.Getpid()), "--runtime", "codex"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "WB itself") {
		t.Fatalf("own PID=%v", err)
	}
}

func TestSessionRootJoinUsesActualLazyCollaborationFactoryAndRefusals(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	inv := &invocation{projectsRoot: root}
	deps := newSessionDependencies(inv)
	// The real collaboration service must resolve its current invocation root;
	// a symlink loop is a genuine private filesystem refusal before Join.
	loop := filepath.Join(t.TempDir(), "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	inv.projectsRoot = loop
	if err := deps.Join(context.Background(), "missing"); err == nil {
		t.Fatal("invalid home accepted")
	}
	inv.projectsRoot = root
	err := deps.Join(context.Background(), filepath.Join(root, "missing"))
	var pathError *os.PathError
	if err == nil || (!errors.As(err, &pathError) && !strings.Contains(err.Error(), "resolve")) {
		t.Fatalf("missing checkout=%v", err)
	}
}

func TestSessionRootInstallerResolvesActualPrivateRegistryAndCurrentRoot(t *testing.T) {
	root := t.TempDir()
	inv := &invocation{projectsRoot: root}
	directory, err := sessionrun.DirForWrite(root)
	if err != nil {
		t.Fatal(err)
	}
	record, err := session.Register(directory, session.Record{PID: os.Getpid(), WBSessionID: "wbs-root-binding", Runtime: "codex", NativeHarnessID: "native-private", Model: "model-private"})
	if err != nil {
		t.Fatal(err)
	}
	installSessionResolver(inv)
	t.Cleanup(func() { worktrees.SetSessionResolver(nil) })
	identity, ok := worktrees.RegisteredIdentity()
	if !ok || identity.WBSessionID != record.WBSessionID || identity.PID != record.PID || identity.Runtime != record.Runtime || identity.AgentID != record.NativeHarnessID || identity.Model != record.Model {
		t.Fatalf("identity=%+v registered=%v", identity, ok)
	}
	// Genuine stored legacy records are normalized by session.Lookup: the
	// nonempty legacy ID checks compatibility, while absent IDs reach the
	// root's empty fallback without manufacturing a resolver response.
	for _, legacyID := range []string{"legacy-private", ""} {
		legacy := record
		legacy.NativeHarnessID = ""
		legacy.AgentID = legacyID
		raw, err := json.Marshal(legacy)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, fmt.Sprint(record.PID)+".json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		identity, ok := worktrees.RegisteredIdentity()
		if !ok || identity.AgentID != legacyID || identity.WBSessionID != record.WBSessionID {
			t.Fatalf("legacy identity=%+v registered=%v", identity, ok)
		}
	}

	inv.projectsRoot = filepath.Join(root, "different-private-root")
	if _, ok := worktrees.RegisteredIdentity(); ok {
		t.Fatal("resolver captured old projects root")
	}
	loop := filepath.Join(t.TempDir(), "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	inv.projectsRoot = loop
	if _, ok := worktrees.RegisteredIdentity(); ok {
		t.Fatal("invalid directory resolved identity")
	}
}
