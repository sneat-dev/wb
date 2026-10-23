package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/cli/cmdsession"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmessage"
	"github.com/sneat-dev/wb/internal/sessionmessenger"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionrun"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func cwWtValidReceipt() sessionmove.MessageReceipt {
	now := time.Date(2025, 2, 3, 4, 5, 6, 0, time.UTC)
	return sessionmove.MessageReceipt{
		SchemaVersion: sessionmove.MessageReceiptSchemaVersion,
		MessageID:     "message-abc", HandoffID: "handoff-abc",
		SenderWBSessionID: "wbs-sender", RecipientWBSessionID: "wbs-target", ReplyToWBSessionID: "wbs-sender",
		Kind: sessionmove.MessageKindText, TmuxName: "wb-target",
		MessageDigest: sessionmove.DigestBytes([]byte("body")),
		PaneID:        "%1", PID: 4242, RecordedAt: now, PastedAt: now,
	}
}

func cwWtMessageDeps(source session.Record, ok bool, sourceErr error,
	storeErr error, messageIDErr error,
	send func(context.Context, sessionmessenger.Options) (sessionmessenger.Result, error)) sessionrun.MessageDependencies {
	return sessionrun.MessageDependencies{
		ResolveSource: func(string) (session.Record, bool, error) { return source, ok, sourceErr },
		Store: func(string) (sessionmove.Store, error) {
			if storeErr != nil {
				return sessionmove.Store{}, storeErr
			}
			return sessionmove.Store{}, nil
		},
		NewMessageID: func() (string, error) {
			if messageIDErr != nil {
				return "", messageIDErr
			}
			return "message-generated", nil
		},
		Send: send,
	}
}

func cwWtSendBuilder(t *testing.T, deps sessionrun.MessageDependencies) func() *cobra.Command {
	root := t.TempDir()
	return func() *cobra.Command {
		return cmdsession.NewSend(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: root} }}, cmdsession.Dependencies{Send: sessionrun.NewMessage(deps).Send})
	}
}

func TestCwWtRunSessionMessageSuccessAndOptions(t *testing.T) {
	t.Parallel()
	okSource := session.Record{PID: 1, WBSessionID: "wbs-sender"}
	var seen sessionmessenger.Options
	send := func(_ context.Context, options sessionmessenger.Options) (sessionmessenger.Result, error) {
		seen = options
		return sessionmessenger.Result{
			Message: sessionmove.Message{MessageID: "message-1"},
			Receipt: sessionmove.MessageReceipt{TmuxName: "wb-target"},
		}, nil
	}
	deps := cwWtMessageDeps(okSource, true, nil, nil, nil, send)

	stdout, _, err := executeCommand(t, t.TempDir(), cwWtSendBuilder(t, deps), "wbs-target", "--message", "hello")
	if err != nil {
		t.Fatalf("session Send: %v", err)
	}
	if !strings.Contains(stdout, "message message-1 acknowledged for successor wbs-target as durably recorded and pasted to tmux wb-target") {
		t.Fatalf("session send stdout = %q", stdout)
	}
	if seen.Body != "hello" || seen.MessageID != "message-generated" || seen.ResumeMessageID != "" {
		t.Fatalf("send options = %+v", seen)
	}
	if seen.TargetWBSessionID != "wbs-target" || seen.ProjectsRoot == "" {
		t.Fatalf("send options target/root = %+v", seen)
	}
	if seen.Now == nil || seen.Kind != sessionmove.MessageKindText {
		t.Fatalf("send options kind/now = %+v", seen)
	}

	// JSON output.
	stdout, _, err = executeCommand(t, t.TempDir(), cwWtSendBuilder(t, deps), "wbs-target", "--message", "hello", "--format", "json")
	if err != nil {
		t.Fatalf("session send json: %v", err)
	}
	if !strings.Contains(stdout, "\"receipt\"") {
		t.Fatalf("session send json = %q", stdout)
	}

	// --resume retries the exact durable bytes: no new ID is minted.
	seen = sessionmessenger.Options{}
	_, _, err = executeCommand(t, t.TempDir(), cwWtSendBuilder(t, deps), "wbs-target", "--resume", "message-existing")
	if err != nil {
		t.Fatalf("session send --resume: %v", err)
	}
	if seen.ResumeMessageID != "message-existing" || seen.MessageID != "" {
		t.Fatalf("resume options = %+v", seen)
	}

	// --message-file - reads the body from stdin.
	seen = sessionmessenger.Options{}
	stdout, _, err = executeInputCommand(t, t.TempDir(), "body from stdin\n", cwWtSendBuilder(t, deps), "wbs-target", "--message-file", "-")
	if err != nil {
		t.Fatalf("session send --message-file -: %v", err)
	}
	if seen.Body != "body from stdin\n" {
		t.Fatalf("stdin body = %q", seen.Body)
	}
	if !strings.Contains(stdout, "acknowledged") {
		t.Fatalf("stdin send stdout = %q", stdout)
	}

	// A regular file supplies the body too.
	bodyFile := filepath.Join(t.TempDir(), "body.txt")
	if err := os.WriteFile(bodyFile, []byte("from a file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seen = sessionmessenger.Options{}
	if _, _, err := executeCommand(t, t.TempDir(), cwWtSendBuilder(t, deps), "wbs-target", "--message-file", bodyFile); err != nil {
		t.Fatalf("session send --message-file: %v", err)
	}
	if seen.Body != "from a file\n" {
		t.Fatalf("file body = %q", seen.Body)
	}
}

func TestCwWtRunSessionMessageErrors(t *testing.T) {
	t.Parallel()
	okSource := session.Record{PID: 1, WBSessionID: "wbs-sender"}
	goodSend := func(context.Context, sessionmessenger.Options) (sessionmessenger.Result, error) {
		return sessionmessenger.Result{
			Message: sessionmove.Message{MessageID: "message-1"},
			Receipt: sessionmove.MessageReceipt{TmuxName: "wb-target"},
		}, nil
	}

	cases := []struct {
		name string
		deps sessionrun.MessageDependencies
		args []string
		want string
	}{
		{name: "bogus format", deps: cwWtMessageDeps(okSource, true, nil, nil, nil, goodSend),
			args: []string{"wbs-target", "--message", "hi", "--format", "yaml"}, want: "unsupported format"},
		{name: "blank target", deps: cwWtMessageDeps(okSource, true, nil, nil, nil, goodSend),
			args: []string{"   ", "--message", "hi"}, want: "successor WB session ID is required"},
		{name: "no source", deps: cwWtMessageDeps(session.Record{}, false, nil, nil, nil, goodSend),
			args: []string{"wbs-target", "--message", "hi"}, want: "requires the live registered predecessor session"},
		{name: "source error", deps: cwWtMessageDeps(session.Record{}, false, errors.New("cwWt: source down"), nil, nil, goodSend),
			args: []string{"wbs-target", "--message", "hi"}, want: "cwWt: source down"},
		{name: "store error", deps: cwWtMessageDeps(okSource, true, nil, errors.New("cwWt: store down"), nil, goodSend),
			args: []string{"wbs-target", "--message", "hi"}, want: "cwWt: store down"},
		{name: "message id error", deps: cwWtMessageDeps(okSource, true, nil, nil, errors.New("cwWt: id down"), goodSend),
			args: []string{"wbs-target", "--message", "hi"}, want: "cwWt: id down"},
		{name: "send error", deps: cwWtMessageDeps(okSource, true, nil, nil, nil,
			func(context.Context, sessionmessenger.Options) (sessionmessenger.Result, error) {
				return sessionmessenger.Result{}, errors.New("cwWt: send down")
			}),
			args: []string{"wbs-target", "--message", "hi"}, want: "cwWt: send down"},
		{name: "resume conflict", deps: cwWtMessageDeps(okSource, true, nil, nil, nil, goodSend),
			args: []string{"wbs-target", "--resume", "message-1", "--message", "hi"}, want: "does not accept replacement message input"},
		{name: "no input", deps: cwWtMessageDeps(okSource, true, nil, nil, nil, goodSend),
			args: []string{"wbs-target"}, want: "requires exactly one of --message or --message-file"},
		{name: "both inputs", deps: cwWtMessageDeps(okSource, true, nil, nil, nil, goodSend),
			args: []string{"wbs-target", "--message", "hi", "--message-file", "x"}, want: "requires exactly one of --message or --message-file"},
	}
	for _, test := range cases {
		_, _, err := executeCommand(t, t.TempDir(), cwWtSendBuilder(t, test.deps), test.args...)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: error = %v, want %q", test.name, err, test.want)
		}
	}

	// A DeliveryError with a durable message ID names the exact retry verb.
	durable := cwWtMessageDeps(okSource, true, nil, nil, nil,
		func(context.Context, sessionmessenger.Options) (sessionmessenger.Result, error) {
			return sessionmessenger.Result{}, &sessionmessenger.DeliveryError{
				MessageID: "message-durable", TargetWBSessionID: "wbs-target", Cause: errors.New("ambiguous"),
			}
		})
	_, _, err := executeCommand(t, t.TempDir(), cwWtSendBuilder(t, durable), "wbs-target", "--message", "hi")
	if err == nil || !strings.Contains(err.Error(), "wb session send wbs-target --resume message-durable") {
		t.Fatalf("delivery error retry hint = %v", err)
	}

	// A DeliveryError with no durable ID is reported unchanged.
	noID := cwWtMessageDeps(okSource, true, nil, nil, nil,
		func(context.Context, sessionmessenger.Options) (sessionmessenger.Result, error) {
			return sessionmessenger.Result{}, &sessionmessenger.DeliveryError{Cause: errors.New("no identity")}
		})
	_, _, err = executeCommand(t, t.TempDir(), cwWtSendBuilder(t, noID), "wbs-target", "--message", "hi")
	if err == nil || strings.Contains(err.Error(), "--resume") {
		t.Fatalf("delivery error without an ID = %v", err)
	}
}

func TestCwWtSessionRecallCmd(t *testing.T) {
	t.Parallel()
	deps := cwWtMessageDeps(session.Record{PID: 1, WBSessionID: "wbs-sender"}, true, nil, nil, nil,
		func(_ context.Context, options sessionmessenger.Options) (sessionmessenger.Result, error) {
			if options.Kind != sessionmove.MessageKindRequestHandoff {
				return sessionmessenger.Result{}, errors.New("cwWt: wrong kind")
			}
			return sessionmessenger.Result{
				Message: sessionmove.Message{MessageID: "message-2"},
				Receipt: sessionmove.MessageReceipt{TmuxName: "wb-predecessor"},
			}, nil
		})
	stdout, _, err := executeCommand(t, t.TempDir(), func() *cobra.Command { return recallCommand(deps) }, "wbs-target", "--resume", "message-2")
	if err != nil {
		t.Fatalf("session recall: %v", err)
	}
	if !strings.Contains(stdout, "message message-2 acknowledged for successor wbs-target") {
		t.Fatalf("recall stdout = %q", stdout)
	}
}

//nolint:paralleltest // Process-wide environment changes in TestCwWtDefaultSessionMessageDependencies; these rows share their parent environment and remain sequential.
func TestCwWtDefaultSessionMessageDependencies(t *testing.T) {
	t.Setenv(wbhome.EnvOverride, filepath.Join(t.TempDir(), "wb-home"))
	// No live session is registered for this process, so the resolver says so.
	_, _, err := executeCommand(t, t.TempDir(), func() *cobra.Command { return sendCommand(sessionrun.DefaultMessageDependencies()) }, "wbs-target", "--message", "hi")
	if err == nil {
		t.Fatal("session send without a registered session must fail")
	}
	if !strings.Contains(err.Error(), "requires the live registered predecessor session") {
		t.Fatalf("default resolver error = %v", err)
	}

	// A WB_HOME that cannot be resolved fails in the store closure instead.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(wbhome.EnvOverride, filepath.Join(blocker, "home"))
	_, _, err = executeCommand(t, t.TempDir(), func() *cobra.Command { return sendCommand(sessionrun.DefaultMessageDependencies()) }, "wbs-target", "--message", "hi")
	if err == nil {
		t.Fatal("session send with an unresolvable WB_HOME must fail")
	}
}

func cwWtReceiveDeps(localMachine func() (string, error), store func(string) (sessionmove.Store, error),
	sessionDir func() (string, error),
	receive func(context.Context, sessionmessage.Options) (sessionmessage.Result, error)) sessionrun.ReceiveMessageDependencies {
	return sessionrun.ReceiveMessageDependencies{
		LocalMachine: localMachine, Store: store, Directory: func(string) (string, error) { return sessionDir() }, Receive: receive,
	}
}

func TestCwWtSessionReceiveMessageBranches(t *testing.T) {
	t.Parallel()
	goodLocal := func() (string, error) { return "machine-a", nil }
	goodStore := func(string) (sessionmove.Store, error) { return sessionmove.Store{}, nil }
	goodDir := func() (string, error) { return "/tmp/sessions", nil }
	goodReceive := func(context.Context, sessionmessage.Options) (sessionmessage.Result, error) {
		return sessionmessage.Result{Message: sessionmove.Message{MessageID: "message-1"}, Receipt: cwWtValidReceipt()}, nil
	}
	build := func(deps sessionrun.ReceiveMessageDependencies) func() *cobra.Command {
		return func() *cobra.Command { return messageReceiver(deps) }
	}

	// Text success.
	stdout, _, err := executeInputCommand(t, t.TempDir(), `{"kind":"text"}`, build(cwWtReceiveDeps(goodLocal, goodStore, goodDir, goodReceive)))
	if err != nil {
		t.Fatalf("receive text: %v", err)
	}
	if !strings.Contains(stdout, "message message-abc durably recorded and pasted to tmux wb-target") {
		t.Fatalf("receive text stdout = %q", stdout)
	}

	// JSON success writes the canonical receipt.
	stdout, _, err = executeInputCommand(t, t.TempDir(), `{"kind":"text"}`, build(cwWtReceiveDeps(goodLocal, goodStore, goodDir, goodReceive)), "--format", "json")
	if err != nil {
		t.Fatalf("receive json: %v", err)
	}
	if !strings.Contains(stdout, "\"tmux_name\": \"wb-target\"") {
		t.Fatalf("receive json stdout = %q", stdout)
	}

	// A receipt that cannot be encoded is reported.
	badReceipt := func(context.Context, sessionmessage.Options) (sessionmessage.Result, error) {
		return sessionmessage.Result{Receipt: sessionmove.MessageReceipt{}}, nil
	}
	if _, _, err := executeInputCommand(t, t.TempDir(), `{"kind":"text"}`, build(cwWtReceiveDeps(goodLocal, goodStore, goodDir, badReceipt)), "--format", "json"); err == nil {
		t.Fatal("an unencodable receipt must fail")
	}

	cases := []struct {
		name  string
		deps  sessionrun.ReceiveMessageDependencies
		stdin string
		args  []string
		want  string
	}{
		{name: "bogus format", deps: cwWtReceiveDeps(goodLocal, goodStore, goodDir, goodReceive),
			stdin: "x", args: []string{"--format", "yaml"}, want: "unsupported format"},
		{name: "empty stdin", deps: cwWtReceiveDeps(goodLocal, goodStore, goodDir, goodReceive),
			stdin: "", args: nil, want: "must not be empty"},
		{name: "machine error", deps: cwWtReceiveDeps(func() (string, error) { return "", errors.New("cwWt: no machine") }, goodStore, goodDir, goodReceive),
			stdin: "x", args: nil, want: "load validated local remote.machine"},
		{name: "store error", deps: cwWtReceiveDeps(goodLocal, func(string) (sessionmove.Store, error) { return sessionmove.Store{}, errors.New("cwWt: store") }, goodDir, goodReceive),
			stdin: "x", args: nil, want: "cwWt: store"},
		{name: "session dir error", deps: cwWtReceiveDeps(goodLocal, goodStore, func() (string, error) { return "", errors.New("cwWt: dir") }, goodReceive),
			stdin: "x", args: nil, want: "cwWt: dir"},
		{name: "receive error", deps: cwWtReceiveDeps(goodLocal, goodStore, goodDir,
			func(context.Context, sessionmessage.Options) (sessionmessage.Result, error) {
				return sessionmessage.Result{}, errors.New("cwWt: receive")
			}),
			stdin: "x", args: nil, want: "cwWt: receive"},
	}
	for _, test := range cases {
		_, _, err := executeInputCommand(t, t.TempDir(), test.stdin, build(test.deps), test.args...)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: error = %v, want %q", test.name, err, test.want)
		}
	}
}

//nolint:paralleltest // Process-wide environment changes in TestCwWtDefaultSessionReceiveMessageDependencies; these rows share their parent environment and remain sequential.
func TestCwWtDefaultSessionReceiveMessageDependencies(t *testing.T) {
	// Without a configured remote the local machine identity cannot be loaded.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv(wbhome.EnvOverride, filepath.Join(t.TempDir(), "wb-home"))
	_, _, err := executeInputCommand(t, t.TempDir(), "x", func() *cobra.Command { return messageReceiver(sessionrun.DefaultReceiveMessageDependencies()) })
	if err == nil || !strings.Contains(err.Error(), "load validated local remote.machine") {
		t.Fatalf("default receive dependencies error = %v", err)
	}
}

// TestCwWtDefaultSessionReceiveMessageDependenciesReachesSessionDir proves
// the default sessionDir dependency (session_message.go:190, which wires
// sessionDirForRead(inv) into "wb session receive-message") is actually
// invoked: the sibling test above stops at the earlier local-machine check,
// so the sessionDir closure - and the real deps.receive call beyond it -
// were never exercised with the real default dependencies.
//
//nolint:paralleltest // Process-wide environment changes in TestCwWtDefaultSessionReceiveMessageDependenciesReachesSessionDir; these rows share their parent environment and remain sequential.
func TestCwWtDefaultSessionReceiveMessageDependenciesReachesSessionDir(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(configHome, "wb"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "wb", "wb.yaml"), []byte("remote:\n  provider: git\n  repo: acme/wb-state\n  machine: cwwt-machine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "projects")
	t.Setenv(wbhome.EnvOverride, root)
	_, _, err := executeInputCommand(t, root, "not a valid session message", func() *cobra.Command {
		return cmdsession.NewReceiveMessage(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: root} }}, cmdsession.Dependencies{ReceiveMessage: sessionrun.NewReceiveMessage(sessionrun.DefaultReceiveMessageDependencies()).ReceiveMessage})
	})
	if err == nil {
		t.Fatal("an invalid session message body must be refused")
	}
	if strings.Contains(err.Error(), "load validated local remote.machine") {
		t.Fatalf("receive-message error = %v, want past the local-machine check", err)
	}
}
