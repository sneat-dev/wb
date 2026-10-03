package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/cli/cmdsession"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionrun"
	"github.com/sneat-dev/wb/internal/worktreecollab"
	"github.com/sneat-dev/wb/internal/worktrees"
)

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
	if formatted := formatCollaborationInfo(worktreecollab.View{Checkout: worktreecollab.Checkout{ID: "test-checkout"}, Owner: "peer", OwnerStatus: "live", Joined: []worktreecollab.Participant{{SessionID: "peer", Live: true}}}); !strings.Contains(formatted, "Current owner: peer") || !strings.Contains(formatted, "Joined session: peer") {
		t.Fatalf("format = %q", formatted)
	}
}

func TestCollaborationCommandBoundaryErrorsAndQuietInbox(t *testing.T) {
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
	if command := newWorktreeCmd(&invocation{}); command.CommandPath() == "" {
		t.Fatal("parent command missing")
	}
}

func TestCollaborationSessionPortsBindAncestorRecipientAndCheckout(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ports := defaultCollaborationSessionPorts()
	ports.root = func(string) (string, error) { return filepath.Join(root, ".wb"), nil }
	ports.pid = func() int { return 42 }
	ports.resolveCheckout = func(_ context.Context, _, value string) (worktreecollab.Checkout, error) {
		if value != "worktree" {
			return worktreecollab.Checkout{}, errors.New("unknown checkout")
		}
		return worktreecollab.Checkout{ID: "checkout", Root: root, GitDir: root + "/gitdir", CommonDir: root + "/common"}, nil
	}
	ports.observeLegacy = func(string, string) (worktreecollab.ObservedOwner, error) { return worktreecollab.ObservedOwner{}, nil }
	ports.resolveAncestor = func(string, int) (session.Record, bool) { return session.Record{}, false }
	ports.lookupExact = func(string, int) (session.Record, bool, error) { return session.Record{}, false, nil }
	ports.lookupRecipient = func(string, string) (session.Record, bool) { return session.Record{}, false }
	service, err := ports.service(&invocation{projectsRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Ports.Caller(); err == nil || !strings.Contains(err.Error(), "registered ancestor") {
		t.Fatalf("unregistered caller = %v", err)
	}
	declared := session.Record{PID: 42, WBSessionID: "wbs-owner"}
	ports.resolveAncestor = func(string, int) (session.Record, bool) { return declared, true }
	service, _ = ports.service(&invocation{projectsRoot: root})
	if _, err := service.Ports.Caller(); err == nil || !strings.Contains(err.Error(), "corroborated") {
		t.Fatalf("unverified caller = %v", err)
	}
	ports.lookupExact = func(string, int) (session.Record, bool, error) { return declared, true, nil }
	ports.listSessions = func(string) ([]session.View, error) {
		return []session.View{{Record: declared}}, nil
	}
	service, _ = ports.service(&invocation{projectsRoot: root})
	if id, err := service.Ports.Caller(); err != nil || id != declared.WBSessionID {
		t.Fatalf("exact caller = %q, %v", id, err)
	}
	ports.listSessions = func(string) ([]session.View, error) {
		return []session.View{{Record: declared}, {Record: session.Record{PID: 99, WBSessionID: declared.WBSessionID}}}, nil
	}
	service, _ = ports.service(&invocation{projectsRoot: root})
	if _, err := service.Ports.Caller(); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("duplicate invoking session accepted: %v", err)
	}
	ports.listSessions = func(string) ([]session.View, error) { return []session.View{{Record: declared}}, nil }
	service, _ = ports.service(&invocation{projectsRoot: root})
	if live, err := service.Ports.Live("peer"); err != nil || live {
		t.Fatalf("missing recipient = %t, %v", live, err)
	}
	ports.listSessions = func(string) ([]session.View, error) {
		return []session.View{{Record: declared}, {Record: session.Record{PID: 55, WBSessionID: "peer"}}}, nil
	}
	service, _ = ports.service(&invocation{projectsRoot: root})
	if live, err := service.Ports.Live("peer"); err != nil || live {
		t.Fatalf("listed recipient without exact lookup = %t, %v", live, err)
	}
	ports.listSessions = func(string) ([]session.View, error) { return nil, errors.New("listing unavailable") }
	service, _ = ports.service(&invocation{projectsRoot: root})
	if _, err := service.Ports.Caller(); err == nil || !strings.Contains(err.Error(), "listing unavailable") {
		t.Fatalf("caller ignored registration listing failure: %v", err)
	}
	ports.listSessions = func(string) ([]session.View, error) {
		return []session.View{{Record: declared}, {Record: session.Record{PID: 55, WBSessionID: "peer"}}}, nil
	}
	ports.lookupRecipient = func(string, string) (session.Record, bool) { return session.Record{PID: 55, WBSessionID: "peer"}, true }
	ports.listSessions = func(string) ([]session.View, error) {
		return []session.View{{Record: declared}, {Record: session.Record{PID: 55, WBSessionID: "peer"}}, {Record: session.Record{PID: 56, WBSessionID: "peer"}}}, nil
	}
	service, _ = ports.service(&invocation{projectsRoot: root})
	if live, err := service.Ports.Live("peer"); err != nil || live {
		t.Fatalf("duplicate recipient accepted as live: %t, %v", live, err)
	}
	ports.listSessions = func(string) ([]session.View, error) {
		return []session.View{{Record: declared}, {Record: session.Record{PID: 55, WBSessionID: "peer"}}}, nil
	}
	ports.lookupExact = func(string, int) (session.Record, bool, error) {
		return session.Record{}, false, errors.New("exact read")
	}
	service, _ = ports.service(&invocation{projectsRoot: root})
	if _, err := service.Ports.Live("peer"); err == nil || !strings.Contains(err.Error(), "exact read") {
		t.Fatalf("recipient read failure = %v", err)
	}
	ports.lookupExact = func(_ string, pid int) (session.Record, bool, error) {
		return session.Record{PID: pid, WBSessionID: "peer"}, true, nil
	}
	service, _ = ports.service(&invocation{projectsRoot: root})
	if live, err := service.Ports.Live("peer"); err != nil || !live {
		t.Fatalf("live peer = %t, %v", live, err)
	}
	ports.listSessions = func(string) ([]session.View, error) { return []session.View{{Record: declared}}, nil }
	service, _ = ports.service(&invocation{projectsRoot: root})
	if status, err := service.Ports.OwnerStatus("peer"); err != nil || status != "unknown" {
		t.Fatalf("missing owner evidence = %q, %v", status, err)
	}
	ports.listSessions = func(string) ([]session.View, error) { return nil, errors.New("session listing unavailable") }
	service, _ = ports.service(&invocation{projectsRoot: root})
	if status, err := service.Ports.OwnerStatus("peer"); err != nil || status != "unknown" {
		t.Fatalf("uncertain owner listing incorrectly treated as inactive = %q, %v", status, err)
	}
	ports.listSessions = func(string) ([]session.View, error) {
		return []session.View{{Record: declared}, {Record: session.Record{PID: 55, WBSessionID: "peer"}}}, nil
	}
	ports.lookupExact = func(string, int) (session.Record, bool, error) {
		return session.Record{}, false, errors.New("record unreadable")
	}
	service, _ = ports.service(&invocation{projectsRoot: root})
	if status, err := service.Ports.OwnerStatus("peer"); err != nil || status != "unknown" {
		t.Fatalf("unreadable owner incorrectly dead = %q, %v", status, err)
	}
	ports.lookupExact = func(string, int) (session.Record, bool, error) {
		return session.Record{PID: 55, WBSessionID: "other"}, false, nil
	}
	service, _ = ports.service(&invocation{projectsRoot: root})
	if status, err := service.Ports.OwnerStatus("peer"); err != nil || status != "unknown" {
		t.Fatalf("mismatched owner incorrectly dead = %q, %v", status, err)
	}
	ports.lookupExact = func(string, int) (session.Record, bool, error) {
		return session.Record{PID: 55, WBSessionID: "peer"}, false, nil
	}
	service, _ = ports.service(&invocation{projectsRoot: root})
	if status, err := service.Ports.OwnerStatus("peer"); err != nil || status != "inactive" {
		t.Fatalf("proven dead owner = %q, %v", status, err)
	}
	ports.listSessions = func(string) ([]session.View, error) {
		return []session.View{
			{Record: declared},
			{Record: session.Record{PID: 55, WBSessionID: "peer"}},
			{Record: session.Record{PID: 56, WBSessionID: "peer"}},
		}, nil
	}
	service, _ = ports.service(&invocation{projectsRoot: root})
	if status, err := service.Ports.OwnerStatus("peer"); err != nil || status != "unknown" {
		t.Fatalf("duplicate owner registration incorrectly treated as dead = %q, %v", status, err)
	}
	ports.listSessions = func(string) ([]session.View, error) {
		return []session.View{{Record: declared}, {Record: session.Record{PID: 55, WBSessionID: "peer"}}}, nil
	}
	ports.lookupExact = func(string, int) (session.Record, bool, error) {
		return session.Record{PID: 55, WBSessionID: "peer"}, true, nil
	}
	service, _ = ports.service(&invocation{projectsRoot: root})
	if status, err := service.Ports.OwnerStatus("peer"); err != nil || status != "live" {
		t.Fatalf("proven live owner = %q, %v", status, err)
	}
	if _, err := service.Ports.Resolve(context.Background(), "missing"); err == nil {
		t.Fatal("checkout resolution failure hidden")
	}
	if _, err := service.Ports.ObserveLegacy(worktreecollab.Checkout{Root: root}); err != nil {
		t.Fatal(err)
	}
	ports.root = func(string) (string, error) { return "", errors.New("home unavailable") }
	if _, err := ports.service(&invocation{}); err == nil || !strings.Contains(err.Error(), "home unavailable") {
		t.Fatalf("home resolution = %v", err)
	}
	if _, err := newCollaborationService(&invocation{projectsRoot: root}); err != nil {
		t.Fatal(err)
	}
	if _, err := collaborationFactory(&invocation{projectsRoot: root})(); err != nil {
		t.Fatal(err)
	}
}

func TestCollaborationCommandsRefuseUnauthorizedEffectsAndBoundMessageInput(t *testing.T) {
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

type collaborationFailWriter struct{}

func (collaborationFailWriter) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }

type collaborationNthFailWriter struct{ writes, failAt int }

func (writer *collaborationNthFailWriter) Write(value []byte) (int, error) {
	writer.writes++
	if writer.writes == writer.failAt {
		return 0, errors.New("output unavailable")
	}
	return len(value), nil
}

func TestCollaborationInfoBoundaries(t *testing.T) {
	factory, _ := collaborationCommandFixture(t)
	ports := worktreeInfoPorts{
		load: func(context.Context, worktrees.LoadWorkLogOptions) (worktrees.WorkLogView, error) {
			return worktrees.WorkLogView{}, nil
		},
		lane:          func(string, worktrees.WorkLogView) (*orchestrate.MergeLaneClaim, error) { return nil, nil },
		collaboration: factory,
	}
	inv := &invocation{projectsRoot: t.TempDir()}
	if _, err := executeCollaborationCommand(t, newWorktreeInfoCmdWithPorts(inv, ports), "worktree", "--format", "yaml"); err == nil || !strings.Contains(err.Error(), "format") {
		t.Fatalf("invalid info format = %v", err)
	}
	command := newWorktreeInfoCmdWithPorts(inv, ports)
	command.SetOut(collaborationFailWriter{})
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"worktree"})
	command.SilenceUsage, command.SilenceErrors = true, true
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "output unavailable") {
		t.Fatalf("text output failure = %v", err)
	}
	ports.load = func(context.Context, worktrees.LoadWorkLogOptions) (worktrees.WorkLogView, error) {
		return worktrees.WorkLogView{}, errors.New("view unavailable")
	}
	if _, err := executeCollaborationCommand(t, newWorktreeInfoCmdWithPorts(inv, ports), "worktree"); err == nil || !strings.Contains(err.Error(), "view unavailable") {
		t.Fatalf("view failure = %v", err)
	}
	ports.load = func(context.Context, worktrees.LoadWorkLogOptions) (worktrees.WorkLogView, error) {
		return worktrees.WorkLogView{}, nil
	}
	ports.lane = func(string, worktrees.WorkLogView) (*orchestrate.MergeLaneClaim, error) {
		return nil, errors.New("lane unavailable")
	}
	if _, err := executeCollaborationCommand(t, newWorktreeInfoCmdWithPorts(inv, ports), "worktree"); err == nil || !strings.Contains(err.Error(), "lane unavailable") {
		t.Fatalf("lane failure = %v", err)
	}
	ports.lane = func(string, worktrees.WorkLogView) (*orchestrate.MergeLaneClaim, error) { return nil, nil }
	ports.collaboration = func() (worktreecollab.Service, error) {
		return worktreecollab.Service{}, errors.New("service unavailable")
	}
	if _, err := executeCollaborationCommand(t, newWorktreeInfoCmdWithPorts(inv, ports), "worktree"); err == nil || !strings.Contains(err.Error(), "service unavailable") {
		t.Fatalf("service failure = %v", err)
	}
	ports.collaboration = factory
	if _, err := executeCollaborationCommand(t, newWorktreeInfoCmdWithPorts(inv, ports), filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatal(err)
	}
	service, _ := factory()
	service.Ports.Resolve = func(context.Context, string) (worktreecollab.Checkout, error) {
		return worktreecollab.Checkout{}, errors.New("identity unavailable")
	}
	ports.collaboration = func() (worktreecollab.Service, error) { return service, nil }
	if _, err := executeCollaborationCommand(t, newWorktreeInfoCmdWithPorts(inv, ports), "worktree"); err == nil || !strings.Contains(err.Error(), "identity unavailable") {
		t.Fatalf("collaboration refusal = %v", err)
	}
}

func TestSessionRegisterJoinBoundaryReports(t *testing.T) {
	makeCommand := func(inv *invocation, join collaborationServiceFactory) *cobra.Command {
		deps := sessionrun.DefaultRegisterDependencies()
		deps.CurrentPID = func() int { return -1 }
		deps.RuntimeProcess = func(int, string) bool { return true }
		return cmdsession.NewRegister(newCLIRuntime(inv), cmdsession.Dependencies{Register: sessionrun.NewRegister(deps).Register, Join: func(ctx context.Context, path string) error {
			service, err := join()
			if err != nil {
				return err
			}
			_, err = service.Join(ctx, path)
			return err
		}})
	}
	args := []string{"--pid", strconv.Itoa(os.Getpid()), "--runtime", "codex", "--native-harness-id", "collab-registration", "--wb-session-id", "wbs-registration"}
	inv := &invocation{projectsRoot: t.TempDir()}
	if _, err := executeCollaborationCommand(t, makeCommand(inv, nil), "--pid", "0", "--runtime", "codex"); err == nil {
		t.Fatal("invalid registration accepted")
	}
	loop := filepath.Join(t.TempDir(), "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	if _, err := executeCollaborationCommand(t, makeCommand(&invocation{projectsRoot: loop}, nil), args...); err == nil {
		t.Fatal("invalid projects root accepted")
	}
	command := makeCommand(inv, nil)
	command.SetOut(collaborationFailWriter{})
	command.SetErr(&bytes.Buffer{})
	command.SetArgs(args)
	command.SilenceUsage, command.SilenceErrors = true, true
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "output unavailable") {
		t.Fatalf("registration output failure = %v", err)
	}
	failing := func() (worktreecollab.Service, error) {
		return worktreecollab.Service{}, errors.New("join unavailable")
	}
	if out, err := executeCollaborationCommand(t, makeCommand(inv, failing), append(args, "--join", "worktree")...); err == nil || !strings.Contains(err.Error(), "registered but not joined") || !strings.Contains(out, "registered") {
		t.Fatalf("post-registration service failure = %q, %v", out, err)
	}
	factory, _ := collaborationCommandFixture(t)
	if out, err := executeCollaborationCommand(t, makeCommand(inv, factory), append(args, "--join", "worktree")...); err == nil || !strings.Contains(err.Error(), "registered but not joined") || !strings.Contains(out, "registered") {
		t.Fatalf("post-registration join failure = %q, %v", out, err)
	}
	service, _ := factory()
	if _, err := service.Take(context.Background(), "worktree", "none", false, ""); err != nil {
		t.Fatal(err)
	}
	joined := makeCommand(inv, factory)
	writer := &collaborationNthFailWriter{failAt: 2}
	joined.SetOut(writer)
	joined.SetErr(&bytes.Buffer{})
	joined.SetArgs(append(args, "--join", "worktree"))
	joined.SilenceUsage, joined.SilenceErrors = true, true
	if err := joined.Execute(); err == nil || !strings.Contains(err.Error(), "output unavailable") || writer.writes != 2 {
		t.Fatalf("joined output failure = %v; writes=%d", err, writer.writes)
	}
}
