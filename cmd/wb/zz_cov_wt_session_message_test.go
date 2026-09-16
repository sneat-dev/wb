package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmessage"
	"github.com/sneat-dev/wb/internal/sessionmessenger"
	"github.com/sneat-dev/wb/internal/sessionmove"
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

func TestCwWtReadBoundedAndRegularFile(t *testing.T) {
	if _, err := readBounded(strings.NewReader(""), 16, "cwWt body"); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("empty bounded read = %v", err)
	}
	if _, err := readBounded(strings.NewReader(strings.Repeat("x", 17)), 16, "cwWt body"); err == nil || !strings.Contains(err.Error(), "exceeds 16 bytes") {
		t.Fatalf("oversized bounded read = %v", err)
	}
	if raw, err := readBounded(strings.NewReader("hello"), 16, "cwWt body"); err != nil || string(raw) != "hello" {
		t.Fatalf("bounded read = (%q, %v)", raw, err)
	}

	directory := t.TempDir()
	file := filepath.Join(directory, "body.txt")
	if err := os.WriteFile(file, []byte("from a file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if raw, err := readBoundedRegularFile(file, 64); err != nil || string(raw) != "from a file\n" {
		t.Fatalf("regular file = (%q, %v)", raw, err)
	}

	// The root itself is not a usable message file.
	if _, err := readBoundedRegularFile(string(filepath.Separator), 64); err == nil || !strings.Contains(err.Error(), "one clean path") {
		t.Fatalf("root path = %v", err)
	}
	// A missing file is an open failure.
	if _, err := readBoundedRegularFile(filepath.Join(directory, "missing.txt"), 64); err == nil {
		t.Fatal("missing file must fail")
	}
	// A directory is not a regular file.
	if _, err := readBoundedRegularFile(directory, 64); err == nil || !strings.Contains(err.Error(), "regular single-link") {
		t.Fatalf("directory = %v", err)
	}
	// An empty file is refused.
	empty := filepath.Join(directory, "empty.txt")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedRegularFile(empty, 64); err == nil {
		t.Fatal("empty file must fail")
	}
	// An oversized file is refused before it is read.
	big := filepath.Join(directory, "big.txt")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", 128)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedRegularFile(big, 64); err == nil {
		t.Fatal("oversized file must fail")
	}
	// A symlink is never followed.
	link := filepath.Join(directory, "link.txt")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedRegularFile(link, 64); err == nil || !strings.Contains(err.Error(), "open message file") {
		t.Fatalf("symlink = %v", err)
	}
	// A hard-linked file is refused: Nlink must be exactly one.
	hardlink := filepath.Join(directory, "hard.txt")
	if err := os.Link(file, hardlink); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedRegularFile(hardlink, 64); err == nil {
		t.Fatal("hard-linked file must fail")
	}
	// A file whose parent cannot be resolved is reported.
	blocker := filepath.Join(directory, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedRegularFile(filepath.Join(blocker, "child.txt"), 64); err == nil {
		t.Fatal("an unresolvable parent must fail")
	}
}

func TestCwWtReadSessionMessageBody(t *testing.T) {
	command := newSessionSendCmd()
	command.SetIn(strings.NewReader("from stdin\n"))

	body, err := readSessionMessageBody(command, "inline", "", true)
	if err != nil || body != "inline" {
		t.Fatalf("direct body = (%q, %v)", body, err)
	}
	body, err = readSessionMessageBody(command, "", "-", false)
	if err != nil || body != "from stdin\n" {
		t.Fatalf("stdin body = (%q, %v)", body, err)
	}

	file := filepath.Join(t.TempDir(), "body.txt")
	if err := os.WriteFile(file, []byte("from a file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body, err = readSessionMessageBody(command, "", file, false)
	if err != nil || body != "from a file\n" {
		t.Fatalf("file body = (%q, %v)", body, err)
	}

	// Invalid UTF-8 is refused.
	badUTF8 := filepath.Join(t.TempDir(), "bad.txt")
	if err := os.WriteFile(badUTF8, []byte{0xff, 0xfe, 0xfd}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSessionMessageBody(command, "", badUTF8, false); err == nil || !strings.Contains(err.Error(), "valid UTF-8") {
		t.Fatalf("invalid UTF-8 = %v", err)
	}

	// An oversized direct body is refused.
	if _, err := readSessionMessageBody(command, strings.Repeat("x", sessionmove.MaxMessageBodyBytes+1), "", true); err == nil {
		t.Fatal("oversized direct body must fail")
	}
	// A read error from stdin is reported rather than ignored.
	failing := newSessionSendCmd()
	failing.SetIn(cwWtErrorReader{})
	if _, err := readSessionMessageBody(failing, "", "-", false); err == nil {
		t.Fatal("a failing stdin reader must be reported")
	}
}

// cwWtErrorReader always fails, so readBounded's "read %s: %w" branch runs.
type cwWtErrorReader struct{}

func (cwWtErrorReader) Read([]byte) (int, error) { return 0, errors.New("cwWt: injected read failure") }

func cwWtMessageDeps(source session.Record, ok bool, sourceErr error,
	storeErr error, messageIDErr error,
	send func(context.Context, sessionmessenger.Options) (sessionmessenger.Result, error)) sessionMessageDependencies {
	return sessionMessageDependencies{
		resolveSource: func() (session.Record, bool, error) { return source, ok, sourceErr },
		store: func(string) (sessionmove.Store, error) {
			if storeErr != nil {
				return sessionmove.Store{}, storeErr
			}
			return sessionmove.Store{}, nil
		},
		newMessageID: func() (string, error) {
			if messageIDErr != nil {
				return "", messageIDErr
			}
			return "message-generated", nil
		},
		send: send,
	}
}

func cwWtSendBuilder(deps sessionMessageDependencies) func() *cobra.Command {
	return func() *cobra.Command { return newSessionSendCmdWithDeps(deps) }
}

func TestCwWtRunSessionMessageSuccessAndOptions(t *testing.T) {
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

	stdout, _, err := cwCovExec(t, t.TempDir(), cwWtSendBuilder(deps), "wbs-target", "--message", "hello")
	if err != nil {
		t.Fatalf("session send: %v", err)
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
	stdout, _, err = cwCovExec(t, t.TempDir(), cwWtSendBuilder(deps), "wbs-target", "--message", "hello", "--format", "json")
	if err != nil {
		t.Fatalf("session send json: %v", err)
	}
	if !strings.Contains(stdout, "\"receipt\"") {
		t.Fatalf("session send json = %q", stdout)
	}

	// --resume retries the exact durable bytes: no new ID is minted.
	seen = sessionmessenger.Options{}
	_, _, err = cwCovExec(t, t.TempDir(), cwWtSendBuilder(deps), "wbs-target", "--resume", "message-existing")
	if err != nil {
		t.Fatalf("session send --resume: %v", err)
	}
	if seen.ResumeMessageID != "message-existing" || seen.MessageID != "" {
		t.Fatalf("resume options = %+v", seen)
	}

	// --message-file - reads the body from stdin.
	seen = sessionmessenger.Options{}
	stdout, _, err = cwWtRunCmd(t, t.TempDir(), "body from stdin\n", cwWtSendBuilder(deps), "wbs-target", "--message-file", "-")
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
	if _, _, err := cwCovExec(t, t.TempDir(), cwWtSendBuilder(deps), "wbs-target", "--message-file", bodyFile); err != nil {
		t.Fatalf("session send --message-file: %v", err)
	}
	if seen.Body != "from a file\n" {
		t.Fatalf("file body = %q", seen.Body)
	}
}

func TestCwWtRunSessionMessageErrors(t *testing.T) {
	okSource := session.Record{PID: 1, WBSessionID: "wbs-sender"}
	goodSend := func(context.Context, sessionmessenger.Options) (sessionmessenger.Result, error) {
		return sessionmessenger.Result{
			Message: sessionmove.Message{MessageID: "message-1"},
			Receipt: sessionmove.MessageReceipt{TmuxName: "wb-target"},
		}, nil
	}

	cases := []struct {
		name string
		deps sessionMessageDependencies
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
		_, _, err := cwCovExec(t, t.TempDir(), cwWtSendBuilder(test.deps), test.args...)
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
	_, _, err := cwCovExec(t, t.TempDir(), cwWtSendBuilder(durable), "wbs-target", "--message", "hi")
	if err == nil || !strings.Contains(err.Error(), "wb session send wbs-target --resume message-durable") {
		t.Fatalf("delivery error retry hint = %v", err)
	}

	// A DeliveryError with no durable ID is reported unchanged.
	noID := cwWtMessageDeps(okSource, true, nil, nil, nil,
		func(context.Context, sessionmessenger.Options) (sessionmessenger.Result, error) {
			return sessionmessenger.Result{}, &sessionmessenger.DeliveryError{Cause: errors.New("no identity")}
		})
	_, _, err = cwCovExec(t, t.TempDir(), cwWtSendBuilder(noID), "wbs-target", "--message", "hi")
	if err == nil || strings.Contains(err.Error(), "--resume") {
		t.Fatalf("delivery error without an ID = %v", err)
	}
}

func TestCwWtSessionRecallCmd(t *testing.T) {
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
	stdout, _, err := cwCovExec(t, t.TempDir(), func() *cobra.Command { return newSessionRequestHandoffCmdWithDeps(deps) }, "wbs-target", "--resume", "message-2")
	if err != nil {
		t.Fatalf("session recall: %v", err)
	}
	if !strings.Contains(stdout, "message message-2 acknowledged for successor wbs-target") {
		t.Fatalf("recall stdout = %q", stdout)
	}
}

func TestCwWtDefaultSessionMessageDependencies(t *testing.T) {
	t.Setenv(wbhome.EnvOverride, filepath.Join(t.TempDir(), "wb-home"))
	// No live session is registered for this process, so the resolver says so.
	_, _, err := cwCovExec(t, t.TempDir(), newSessionSendCmd, "wbs-target", "--message", "hi")
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
	_, _, err = cwCovExec(t, t.TempDir(), newSessionSendCmd, "wbs-target", "--message", "hi")
	if err == nil {
		t.Fatal("session send with an unresolvable WB_HOME must fail")
	}
}

func cwWtReceiveDeps(localMachine func() (string, error), store func(string) (sessionmove.Store, error),
	sessionDir func() (string, error),
	receive func(context.Context, sessionmessage.Options) (sessionmessage.Result, error)) sessionReceiveMessageDependencies {
	return sessionReceiveMessageDependencies{
		localMachine: localMachine, store: store, sessionDir: sessionDir, receive: receive,
	}
}

func TestCwWtSessionReceiveMessageBranches(t *testing.T) {
	goodLocal := func() (string, error) { return "machine-a", nil }
	goodStore := func(string) (sessionmove.Store, error) { return sessionmove.Store{}, nil }
	goodDir := func() (string, error) { return "/tmp/sessions", nil }
	goodReceive := func(context.Context, sessionmessage.Options) (sessionmessage.Result, error) {
		return sessionmessage.Result{Message: sessionmove.Message{MessageID: "message-1"}, Receipt: cwWtValidReceipt()}, nil
	}
	build := func(deps sessionReceiveMessageDependencies) func() *cobra.Command {
		return func() *cobra.Command { return newSessionReceiveMessageCmdWithDeps(deps) }
	}

	// Text success.
	stdout, _, err := cwWtRunCmd(t, t.TempDir(), `{"kind":"text"}`, build(cwWtReceiveDeps(goodLocal, goodStore, goodDir, goodReceive)))
	if err != nil {
		t.Fatalf("receive text: %v", err)
	}
	if !strings.Contains(stdout, "message message-abc durably recorded and pasted to tmux wb-target") {
		t.Fatalf("receive text stdout = %q", stdout)
	}

	// JSON success writes the canonical receipt.
	stdout, _, err = cwWtRunCmd(t, t.TempDir(), `{"kind":"text"}`, build(cwWtReceiveDeps(goodLocal, goodStore, goodDir, goodReceive)), "--format", "json")
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
	if _, _, err := cwWtRunCmd(t, t.TempDir(), `{"kind":"text"}`, build(cwWtReceiveDeps(goodLocal, goodStore, goodDir, badReceipt)), "--format", "json"); err == nil {
		t.Fatal("an unencodable receipt must fail")
	}

	cases := []struct {
		name  string
		deps  sessionReceiveMessageDependencies
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
		_, _, err := cwWtRunCmd(t, t.TempDir(), test.stdin, build(test.deps), test.args...)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: error = %v, want %q", test.name, err, test.want)
		}
	}
}

func TestCwWtDefaultSessionReceiveMessageDependencies(t *testing.T) {
	// Without a configured remote the local machine identity cannot be loaded.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv(wbhome.EnvOverride, filepath.Join(t.TempDir(), "wb-home"))
	_, _, err := cwWtRunCmd(t, t.TempDir(), "x", newSessionReceiveMessageCmd)
	if err == nil || !strings.Contains(err.Error(), "load validated local remote.machine") {
		t.Fatalf("default receive dependencies error = %v", err)
	}
}
