package integration

import (
	"bytes"
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/cli/cmdworktree"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktreecollab"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type collaborationServiceFactory func() (worktreecollab.Service, error)

func collaborationChild(factory collaborationServiceFactory, path ...string) *cobra.Command {
	root := &cobra.Command{Use: "worktree"}
	root.AddGroup(&cobra.Group{ID: "recover", Title: "Recovery"})
	root.AddCommand(cmdworktree.CollaborationCommands(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{} }}, func(string) (cmdworktree.CollaborationOperations, error) { return factory() })...)
	child, _, err := root.Find(path)
	if err != nil {
		panic(err)
	}
	child.Parent().RemoveCommand(child)
	return child
}
func newWorktreeJoinCmd(factory collaborationServiceFactory) *cobra.Command {
	return collaborationChild(factory, "join")
}
func newWorktreeLeaveCmd(factory collaborationServiceFactory) *cobra.Command {
	return collaborationChild(factory, "leave")
}
func newWorktreeTakeOwnershipCmd(factory collaborationServiceFactory) *cobra.Command {
	return collaborationChild(factory, "take-ownership")
}
func newWorktreeTransferOwnershipCmd(factory collaborationServiceFactory) *cobra.Command {
	return collaborationChild(factory, "transfer-ownership")
}
func newWorktreeMessageCmd(factory collaborationServiceFactory) *cobra.Command {
	return collaborationChild(factory, "message")
}
func newWorktreeMessageSendCmd(factory collaborationServiceFactory) *cobra.Command {
	return collaborationChild(factory, "message", "send")
}
func newWorktreeMessageInboxCmd(factory collaborationServiceFactory) *cobra.Command {
	return collaborationChild(factory, "message", "inbox")
}
func newWorktreeMessageAckCmd(factory collaborationServiceFactory) *cobra.Command {
	return collaborationChild(factory, "message", "ack")
}
func collaborationCommandFixture(t *testing.T) (collaborationServiceFactory, *string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	caller := "owner"
	service := worktreecollab.Service{Store: worktreecollab.NewStore(filepath.Join(root, ".wb")), Ports: worktreecollab.ServicePorts{
		Resolve: func(context.Context, string) (worktreecollab.Checkout, error) {
			return worktreecollab.Checkout{ID: "test-checkout", Root: root, GitDir: filepath.Join(root, "gitdir"), CommonDir: filepath.Join(root, "common")}, nil
		},
		Caller:      func() (string, error) { return caller, nil },
		Live:        func(string) (bool, error) { return true, nil },
		OwnerStatus: func(string) (string, error) { return "live", nil },
		ObserveLegacy: func(worktreecollab.Checkout) (worktreecollab.ObservedOwner, error) {
			return worktreecollab.ObservedOwner{}, nil
		},
		ObserveLegacyForInspection: func(worktreecollab.Checkout) (worktreecollab.ObservedOwner, error) {
			return worktreecollab.ObservedOwner{}, nil
		},
		Now: time.Now, NewMessageID: func() (string, error) { return "message-one", nil },
	}}
	return func() (worktreecollab.Service, error) { return service, nil }, &caller
}

func executeCollaborationCommand(t *testing.T, command *cobra.Command, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&bytes.Buffer{})
	command.SetArgs(args)
	command.SilenceUsage, command.SilenceErrors = true, true
	err := command.Execute()
	return out.String(), err
}

func TestCollaborationCommandsOwnerJoinMessageAndTransfer(t *testing.T) {
	t.Parallel()
	factory, caller := collaborationCommandFixture(t)
	if _, err := executeCollaborationCommand(t, newWorktreeJoinCmd(factory), "worktree"); err == nil {
		t.Fatal("join initialized ownership")
	}
	if _, err := executeCollaborationCommand(t, newWorktreeTakeOwnershipCmd(factory), "worktree"); err == nil || !strings.Contains(err.Error(), "expected-owner") {
		t.Fatalf("missing expected owner = %v", err)
	}
	out, err := executeCollaborationCommand(t, newWorktreeTakeOwnershipCmd(factory), "worktree", "--expected-owner", "none")
	if err != nil || !strings.Contains(out, "owner owner") {
		t.Fatalf("take = %q, %v", out, err)
	}
	*caller = "peer"
	out, err = executeCollaborationCommand(t, newWorktreeJoinCmd(factory), "worktree")
	if err != nil || !strings.Contains(out, "joined test-checkout") {
		t.Fatalf("join = %q, %v", out, err)
	}
	if _, err := executeCollaborationCommand(t, newWorktreeLeaveCmd(factory), "worktree"); err != nil {
		t.Fatal(err)
	}
	if _, err := executeCollaborationCommand(t, newWorktreeJoinCmd(factory), "worktree"); err != nil {
		t.Fatal(err)
	}
	*caller = "owner"
	if _, err := executeCollaborationCommand(t, newWorktreeTransferOwnershipCmd(factory), "worktree"); err == nil || !strings.Contains(err.Error(), "to-session") {
		t.Fatalf("missing successor = %v", err)
	}
	if _, err := executeCollaborationCommand(t, newWorktreeMessageSendCmd(factory), "worktree"); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("missing send flags = %v", err)
	}
	file := filepath.Join(t.TempDir(), "message.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = executeCollaborationCommand(t, newWorktreeMessageSendCmd(factory), "worktree", "--to-session", "peer", "--idempotency-key", "one", "--message-file", file)
	if err != nil || !strings.Contains(out, "message message-one") || strings.Contains(out, "hello") {
		t.Fatalf("send = %q, %v", out, err)
	}
	*caller = "peer"
	out, err = executeCollaborationCommand(t, newWorktreeMessageInboxCmd(factory), "worktree")
	if err != nil || !strings.Contains(out, "hello") {
		t.Fatalf("inbox = %q, %v", out, err)
	}
	out, err = executeCollaborationCommand(t, newWorktreeMessageAckCmd(factory), "worktree", "message-one")
	if err != nil || !strings.Contains(out, "consumed through") {
		t.Fatalf("ack = %q, %v", out, err)
	}
	*caller = "owner"
	out, err = executeCollaborationCommand(t, newWorktreeTransferOwnershipCmd(factory), "worktree", "--to-session", "peer")
	if err != nil || !strings.Contains(out, "owner peer") {
		t.Fatalf("transfer = %q, %v", out, err)
	}
	out, err = executeCollaborationCommand(t, newWorktreeLeaveCmd(factory), "worktree")
	if err != nil || !strings.Contains(out, "left test-checkout") {
		t.Fatalf("leave = %q, %v", out, err)
	}
	*caller = "peer"
	out, err = executeCollaborationCommand(t, newWorktreeMessageInboxCmd(factory), "worktree")
	if err != nil || !strings.Contains(out, "owner_notice") {
		t.Fatalf("owner notice = %q, %v", out, err)
	}
	// Exact formatter assertion moved to TestOriginalCollaborationInfoFormatter in the pure CLI package.
}

func TestCollaborationCommandBoundaryErrorsAndQuietInbox(t *testing.T) {
	t.Parallel()
	factory, caller := collaborationCommandFixture(t)
	failing := func() (worktreecollab.Service, error) { return worktreecollab.Service{}, errors.New("factory failed") }
	for _, build := range []func(collaborationServiceFactory) *cobra.Command{newWorktreeJoinCmd, newWorktreeLeaveCmd, newWorktreeTakeOwnershipCmd, newWorktreeTransferOwnershipCmd, newWorktreeMessageSendCmd, newWorktreeMessageInboxCmd, newWorktreeMessageAckCmd} {
		command := build(failing)
		args := []string{"worktree"}
		switch command.Name() {
		case "take-ownership":
			args = append(args, "--expected-owner", "none")
		case "transfer-ownership":
			args = append(args, "--to-session", "peer")
		case "send":
			args = append(args, "--to-session", "peer", "--idempotency-key", "one", "--message-file", "-")
		case "ack":
			args = append(args, "message-one")
		}
		if _, err := executeCollaborationCommand(t, command, args...); err == nil || !strings.Contains(err.Error(), "factory failed") {
			t.Fatalf("%s factory refusal = %v", command.Name(), err)
		}
	}
	if out, err := executeCollaborationCommand(t, newWorktreeMessageInboxCmd(factory), "worktree"); err == nil || out != "" {
		t.Fatalf("uninitialized inbox = %q, %v", out, err)
	}
	service, _ := factory()
	if _, err := service.Take(context.Background(), "worktree", "none", false, ""); err != nil {
		t.Fatal(err)
	}
	*caller = "peer"
	if _, err := service.Join(context.Background(), "worktree"); err != nil {
		t.Fatal(err)
	}
	if out, err := executeCollaborationCommand(t, newWorktreeMessageInboxCmd(factory), "worktree"); err != nil || out != "" {
		t.Fatalf("empty inbox = %q, %v", out, err)
	}
	if _, err := executeCollaborationCommand(t, newWorktreeMessageSendCmd(factory), "worktree", "--to-session", "owner", "--idempotency-key", "key", "--message-file", filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing body file accepted")
	}
	if command := newWorktreeMessageCmd(factory); len(command.Commands()) != 3 {
		t.Fatalf("message commands = %d", len(command.Commands()))
	}
	// Actual root command registration stays in the root binding gate.
}

func TestCollaborationCommandsRefuseUnauthorizedEffectsAndBoundMessageInput(t *testing.T) {
	t.Parallel()
	factory, caller := collaborationCommandFixture(t)
	service, err := factory()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Take(context.Background(), "worktree", "none", false, ""); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		caller string
		build  func(collaborationServiceFactory) *cobra.Command
		args   []string
	}{
		{"owner", newWorktreeLeaveCmd, []string{"worktree"}},
		{"outsider", newWorktreeTakeOwnershipCmd, []string{"worktree", "--expected-owner", "none"}},
		{"outsider", newWorktreeTransferOwnershipCmd, []string{"worktree", "--to-session", "outsider"}},
		{"outsider", newWorktreeMessageAckCmd, []string{"worktree", "missing"}},
	} {
		*caller = tc.caller
		if _, err := executeCollaborationCommand(t, tc.build(factory), tc.args...); err == nil {
			t.Fatalf("%v accepted unauthorized effect", tc.args)
		}
	}
	*caller = "owner"
	input := newWorktreeMessageSendCmd(factory)
	input.SetIn(strings.NewReader("hello from stdin"))
	if out, err := executeCollaborationCommand(t, input, "worktree", "--to-session", "owner", "--idempotency-key", "stdin", "--message-file", "-"); err != nil || !strings.Contains(out, "message-one") {
		t.Fatalf("stdin send = %q, %v", out, err)
	}
	large := newWorktreeMessageSendCmd(factory)
	large.SetIn(strings.NewReader(strings.Repeat("x", worktreecollab.MaxMessageBodyBytes+1)))
	if _, err := executeCollaborationCommand(t, large, "worktree", "--to-session", "owner", "--idempotency-key", "large", "--message-file", "-"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("large send = %v", err)
	}
	file := filepath.Join(t.TempDir(), "message")
	if err := os.WriteFile(file, []byte("wrong retry"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := executeCollaborationCommand(t, newWorktreeMessageSendCmd(factory), "worktree", "--to-session", "owner", "--idempotency-key", "stdin", "--message-file", file); err == nil {
		t.Fatal("changed body under accepted key")
	}
	largeFile := filepath.Join(t.TempDir(), "sparse-message")
	handle, err := os.Create(largeFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Truncate(1 << 30); err != nil {
		_ = handle.Close()
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := executeCollaborationCommand(t, newWorktreeMessageSendCmd(factory), "worktree", "--to-session", "owner", "--idempotency-key", "sparse", "--message-file", largeFile); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("large file was not refused through bounded read: %v", err)
	}
}
