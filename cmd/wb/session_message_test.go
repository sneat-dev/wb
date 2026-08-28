package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmessage"
	"github.com/sneat-dev/wb/internal/sessionmessenger"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

func TestSessionSendRequiresBoundedInputAndBuildsFreshDurableMessage(t *testing.T) {
	source := session.Record{PID: 123, WBSessionID: "wbs-source", Machine: "laptop", Runtime: "codex", StartedAt: time.Now().UTC()}
	var captured sessionmessenger.Options
	deps := sessionMessageDependencies{
		resolveSource: func() (session.Record, bool, error) { return source, true, nil },
		store:         func(string) (sessionmove.Store, error) { return sessionmove.NewStore(t.TempDir()), nil },
		newMessageID:  func() (string, error) { return "message-stable", nil },
		send: func(_ context.Context, options sessionmessenger.Options) (sessionmessenger.Result, error) {
			captured = options
			return sessionmessenger.Result{Message: sessionmove.Message{MessageID: options.MessageID},
				Receipt: sessionmove.MessageReceipt{MessageID: options.MessageID}}, nil
		},
	}
	command := newSessionSendCmdWithDeps(deps)
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{"wbs-successor", "--message", "Continue the focused race test."})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if captured.TargetWBSessionID != "wbs-successor" || captured.SourceSession.WBSessionID != source.WBSessionID ||
		captured.Kind != sessionmove.MessageKindText || captured.Body != "Continue the focused race test." ||
		captured.MessageID != "message-stable" || captured.ResumeMessageID != "" {
		t.Fatalf("captured send options = %#v", captured)
	}
	if text := output.String(); !strings.Contains(text, "recorded and pasted") || !strings.Contains(text, "does not assert agent processing") {
		t.Fatalf("send acknowledgement = %q", text)
	}

	tooLarge := newSessionSendCmdWithDeps(deps)
	tooLarge.SetArgs([]string{"wbs-successor", "--message", strings.Repeat("x", sessionmove.MaxMessageBodyBytes+1)})
	if err := tooLarge.Execute(); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize send error = %v", err)
	}

	missing := newSessionSendCmdWithDeps(deps)
	missing.SetArgs([]string{"wbs-successor"})
	if err := missing.Execute(); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("missing input error = %v", err)
	}

	directory := t.TempDir()
	messagePath := filepath.Join(directory, "message.txt")
	if err := os.WriteFile(messagePath, []byte("exact file message\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fromFile := newSessionSendCmdWithDeps(deps)
	fromFile.SetOut(&bytes.Buffer{})
	fromFile.SetArgs([]string{"wbs-successor", "--message-file", messagePath})
	if err := fromFile.Execute(); err != nil || captured.Body != "exact file message\n" {
		t.Fatalf("message file send body=%q err=%v", captured.Body, err)
	}
	symlink := filepath.Join(directory, "message-link")
	if err := os.Symlink(messagePath, symlink); err != nil {
		t.Fatal(err)
	}
	linked := newSessionSendCmdWithDeps(deps)
	linked.SetArgs([]string{"wbs-successor", "--message-file", symlink})
	if err := linked.Execute(); err == nil {
		t.Fatal("session send followed a message-file symlink")
	}
}

func TestSessionMessageResumeRejectsReplacementAndReportsExactRetryCommand(t *testing.T) {
	deps := sessionMessageDependencies{
		resolveSource: func() (session.Record, bool, error) {
			return session.Record{PID: 1, WBSessionID: "wbs-source", StartedAt: time.Now()}, true, nil
		},
		store:        func(string) (sessionmove.Store, error) { return sessionmove.NewStore(t.TempDir()), nil },
		newMessageID: sessionmove.NewMessageID,
		send: func(context.Context, sessionmessenger.Options) (sessionmessenger.Result, error) {
			result := sessionmessenger.Result{
				Message: sessionmove.Message{MessageID: "message-stable"},
				Address: sessionmove.SuccessorAddress{SuccessorWBSessionID: "wbs-successor"},
			}
			return result, &sessionmessenger.DeliveryError{MessageID: "message-stable", TargetWBSessionID: "wbs-successor", Cause: errors.New("unknown transport outcome")}
		},
	}
	command := newSessionSendCmdWithDeps(deps)
	command.SetArgs([]string{"wbs-successor", "--message", "exact content"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "wb session send wbs-successor --resume message-stable") {
		t.Fatalf("actionable send error = %v", err)
	}
	if strings.Contains(err.Error(), "exact content") {
		t.Fatalf("delivery error leaked message body: %v", err)
	}

	resume := newSessionSendCmdWithDeps(deps)
	resume.SetArgs([]string{"wbs-successor", "--resume", "message-stable", "--message", "replacement"})
	if err := resume.Execute(); err == nil || !strings.Contains(err.Error(), "does not accept replacement message input") {
		t.Fatalf("replacement resume error = %v", err)
	}
}

func TestSessionRequestHandoffUsesTypedKindAndPreservesKindOnResume(t *testing.T) {
	var calls []sessionmessenger.Options
	deps := sessionMessageDependencies{
		resolveSource: func() (session.Record, bool, error) {
			return session.Record{PID: 1, WBSessionID: "wbs-source", StartedAt: time.Now()}, true, nil
		},
		store:        func(string) (sessionmove.Store, error) { return sessionmove.NewStore(t.TempDir()), nil },
		newMessageID: func() (string, error) { return "message-handoff", nil },
		send: func(_ context.Context, options sessionmessenger.Options) (sessionmessenger.Result, error) {
			calls = append(calls, options)
			return sessionmessenger.Result{Message: sessionmove.Message{MessageID: firstNonempty(options.MessageID, options.ResumeMessageID)},
				Receipt: sessionmove.MessageReceipt{MessageID: firstNonempty(options.MessageID, options.ResumeMessageID)}}, nil
		},
	}
	fresh := newSessionRequestHandoffCmdWithDeps(deps)
	fresh.SetOut(&bytes.Buffer{})
	fresh.SetArgs([]string{"wbs-successor"})
	if err := fresh.Execute(); err != nil {
		t.Fatal(err)
	}
	resume := newSessionRequestHandoffCmdWithDeps(deps)
	resume.SetOut(&bytes.Buffer{})
	resume.SetArgs([]string{"wbs-successor", "--resume", "message-handoff"})
	if err := resume.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0].Kind != sessionmove.MessageKindRequestHandoff || calls[0].Body != "" ||
		calls[0].MessageID != "message-handoff" || calls[1].Kind != sessionmove.MessageKindRequestHandoff ||
		calls[1].ResumeMessageID != "message-handoff" || calls[1].MessageID != "" {
		t.Fatalf("request-handoff calls = %#v", calls)
	}
}

func TestSessionReceiveMessageReturnsCanonicalReceiptOnly(t *testing.T) {
	receipt := sessionmove.MessageReceipt{
		SchemaVersion: sessionmove.MessageReceiptSchemaVersion, MessageID: "message-123",
		MessageDigest: sessionmove.DigestBytes([]byte("message")), HandoffID: "handoff-123",
		SenderWBSessionID: "wbs-source", RecipientWBSessionID: "wbs-successor", ReplyToWBSessionID: "wbs-source",
		Kind: sessionmove.MessageKindRequestHandoff, TmuxName: "wb-session-wbs-successor", PaneID: "%7", PID: 1234,
		RecordedAt: time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC), PastedAt: time.Date(2026, 8, 25, 12, 0, 1, 0, time.UTC),
	}
	rawMessage := []byte("{\"exact\":\"message\"}\n")
	var captured sessionmessage.Options
	deps := sessionReceiveMessageDependencies{
		localMachine: func() (string, error) { return "target-vm", nil },
		store:        func(string) (sessionmove.Store, error) { return sessionmove.NewStore(t.TempDir()), nil },
		sessionDir:   func() (string, error) { return filepath.Join(t.TempDir(), "sessions"), nil },
		receive: func(_ context.Context, options sessionmessage.Options) (sessionmessage.Result, error) {
			captured = options
			return sessionmessage.Result{Receipt: receipt}, nil
		},
	}
	command := newSessionReceiveMessageCmdWithDeps(deps)
	var output bytes.Buffer
	command.SetIn(bytes.NewReader(rawMessage))
	command.SetOut(&output)
	command.SetArgs([]string{"--format", "json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	want, err := sessionmove.EncodeMessageReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), want) || !bytes.Equal(captured.RawMessage, rawMessage) || captured.LocalMachine != "target-vm" || captured.SessionDir == "" {
		t.Fatalf("receiver output=%q want=%q options=%#v", output.Bytes(), want, captured)
	}

	textCommand := newSessionReceiveMessageCmdWithDeps(deps)
	output.Reset()
	textCommand.SetIn(bytes.NewReader(rawMessage))
	textCommand.SetOut(&output)
	if err := textCommand.Execute(); err != nil {
		t.Fatal(err)
	}
	if text := output.String(); !strings.Contains(text, "recorded and pasted") || !strings.Contains(text, "does not assert agent processing") {
		t.Fatalf("receiver text acknowledgement = %q", text)
	}
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
