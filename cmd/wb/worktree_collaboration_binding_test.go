package main

import (
	"github.com/sneat-dev/wb/internal/session"
	"os"
	"path/filepath"
	"testing"
)

func TestWorktreeCollaborationRootBindingUsesLazyNativeFactoryAndRegistry(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	inv := &invocation{projectsRoot: filepath.Join(root, "unparsed")}
	factory := collaborationFactory(inv)
	inv.projectsRoot = root
	record, err := session.Register(filepath.Join(root, ".wb", session.DirName), session.Record{PID: os.Getpid(), Runtime: "codex", WBSessionID: "wbs-root-collaboration"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := collaborationFactory(&invocation{projectsRoot: root})(); err != nil {
		t.Fatal(err)
	}
	service, err := factory()
	if err != nil {
		t.Fatal(err)
	}
	if id, err := service.Ports.Caller(); err != nil || id != record.WBSessionID {
		t.Fatalf("lazy native caller=%q err=%v", id, err)
	}
	if command := newWorktreeCmd(&invocation{}); command.CommandPath() == "" {
		t.Fatal("parent command missing")
	}
	command := newWorktreeCmd(inv)
	for _, path := range [][]string{{"join"}, {"leave"}, {"take-ownership"}, {"transfer-ownership"}, {"message", "send"}, {"message", "inbox"}, {"message", "ack"}} {
		child, _, err := command.Find(path)
		if err != nil || child.CommandPath() == command.CommandPath() {
			t.Fatalf("registered path %v=%v", path, err)
		}
	}
	if _, err := executeCollaborationCommand(t, newWorktreeCmd(inv), "join", filepath.Join(root, "missing")); err == nil {
		t.Fatal("native missing checkout accepted")
	}
}
