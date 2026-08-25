package sessioncourier

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionmove"
)

func TestSSHMessageDelivererUsesFixedReceiverAndExactCanonicalBytes(t *testing.T) {
	message, raw := courierTestMessage(t)
	receipt := courierTestMessageReceipt(message, raw)
	receiptRaw, err := sessionmove.EncodeMessageReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeCommandRunner{response: receiptRaw}
	deliverer := newTestSSHMessageDeliverer(t, sessionmove.SSHConfig{
		Host: "hetzner-vm1", WBPath: "/home/ai/go/bin/wb",
	}, runner)
	got, err := deliverer.DeliverMessage(context.Background(), raw)
	if err != nil || got != receipt {
		t.Fatalf("DeliverMessage = %#v, err=%v", got, err)
	}
	wantArgs := []string{
		"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "--",
		"hetzner-vm1", "/home/ai/go/bin/wb", "--non-interactive", "session", "receive-message", "--format", "json",
	}
	if runner.calls != 1 || !reflect.DeepEqual(runner.args, wantArgs) || !bytes.Equal(runner.stdin, raw) {
		t.Fatalf("ssh message invocation args=%#v stdin=%q", runner.args, runner.stdin)
	}
	for _, arg := range runner.args {
		if arg == message.Body {
			t.Fatal("message body appeared in SSH argv")
		}
	}
}

func TestSynchestraMessageDelivererUsesFixedHandlerAndPersistsDispatch(t *testing.T) {
	message, raw := courierTestMessage(t)
	receipt := courierTestMessageReceipt(message, raw)
	receiptRaw, err := sessionmove.EncodeMessageReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	dispatchID := "dsp_message"
	artifact := encodeSynchestraMessageReceiptArtifact(t, message, raw, receiptRaw)
	runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{
		{stdout: encodeSynchestraMessageInvocationOutput(t, message, raw, dispatchID, "queued", "")},
		{stdout: encodeSynchestraMessageStatusOutput(t, message, raw, dispatchID, "completed", artifact)},
	}}
	var persisted []sessionmove.MessageSynchestraDispatch
	deliverer := newTestSynchestraMessageDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
		MessageSynchestraOptions{SaveDispatch: func(value sessionmove.MessageSynchestraDispatch) error {
			persisted = append(persisted, value)
			return nil
		}}, runner)
	got, err := deliverer.DeliverMessage(context.Background(), raw)
	if err != nil || got != receipt {
		t.Fatalf("DeliverMessage = %#v, err=%v", got, err)
	}
	if len(persisted) != 1 || persisted[0].MessageID != message.MessageID || persisted[0].MessageDigest != sessionmove.DigestBytes(raw) ||
		persisted[0].Handler != sessionmove.SynchestraSessionMessageHandler || persisted[0].DispatchID != dispatchID {
		t.Fatalf("persisted dispatches = %#v", persisted)
	}
	wantInvoke := []string{
		"runner", "invoke", "@/dev/stdin", "--runner", "hetzner-vm1",
		"--handler", sessionmove.SynchestraSessionMessageHandler, "--invocation-id", message.MessageID, "--format", "json",
	}
	if len(runner.calls) != 2 || !reflect.DeepEqual(runner.calls[0].args, wantInvoke) || !bytes.Equal(runner.calls[0].stdin, raw) {
		t.Fatalf("Synchestra calls = %#v", runner.calls)
	}
}

func TestSynchestraMessageDelivererResumesPersistedDispatchWithoutReinvoking(t *testing.T) {
	message, raw := courierTestMessage(t)
	receipt := courierTestMessageReceipt(message, raw)
	receiptRaw, err := sessionmove.EncodeMessageReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	dispatchID := "dsp_message_resume"
	requestDigest := sessionmove.DigestBytes([]byte("exact request"))
	identity := sessionmove.MessageSynchestraDispatch{
		SchemaVersion: sessionmove.MessageSynchestraDispatchSchemaVersion,
		HandoffID:     message.HandoffID, RequestDigest: requestDigest, MessageID: message.MessageID,
		MessageDigest: sessionmove.DigestBytes(raw), Runner: "hetzner-vm1", InvocationID: message.MessageID,
		Handler: sessionmove.SynchestraSessionMessageHandler, DispatchID: dispatchID,
	}
	artifact := encodeSynchestraMessageReceiptArtifact(t, message, raw, receiptRaw)
	runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{
		{stdout: encodeSynchestraMessageStatusOutput(t, message, raw, dispatchID, "completed", artifact)},
	}}
	deliverer := newTestSynchestraMessageDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
		MessageSynchestraOptions{RequestDigest: requestDigest, Dispatch: &identity}, runner)
	got, err := deliverer.DeliverMessage(context.Background(), raw)
	if err != nil || got != receipt {
		t.Fatalf("resumed DeliverMessage = %#v, err=%v", got, err)
	}
	want := []string{"runner", "dispatch", "status", dispatchID, "--format", "json"}
	if len(runner.calls) != 1 || !reflect.DeepEqual(runner.calls[0].args, want) || len(runner.calls[0].stdin) != 0 {
		t.Fatalf("resume did not poll exact dispatch only: %#v", runner.calls)
	}
}

func courierTestMessage(t *testing.T) (sessionmove.Message, []byte) {
	t.Helper()
	message := sessionmove.Message{
		SchemaVersion: sessionmove.MessageSchemaVersion, MessageID: "message-123", HandoffID: "handoff-123",
		SenderWBSessionID: "wbs-source", RecipientWBSessionID: "wbs-successor", ReplyToWBSessionID: "wbs-source",
		Kind: sessionmove.MessageKindRequestHandoff, SentAt: time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC),
	}
	raw, err := sessionmove.EncodeMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	return message, raw
}

func courierTestMessageReceipt(message sessionmove.Message, raw []byte) sessionmove.MessageReceipt {
	return sessionmove.MessageReceipt{
		SchemaVersion: sessionmove.MessageReceiptSchemaVersion, MessageID: message.MessageID,
		MessageDigest: sessionmove.DigestBytes(raw), HandoffID: message.HandoffID,
		SenderWBSessionID: message.SenderWBSessionID, RecipientWBSessionID: message.RecipientWBSessionID,
		ReplyToWBSessionID: message.ReplyToWBSessionID, Kind: message.Kind,
		TmuxName: "wb-session-wbs-successor", PaneID: "%7", PID: 1234,
		RecordedAt: time.Date(2026, 8, 25, 12, 0, 1, 0, time.UTC),
		PastedAt:   time.Date(2026, 8, 25, 12, 0, 2, 0, time.UTC),
	}
}

func newTestSSHMessageDeliverer(t *testing.T, config sessionmove.SSHConfig, runner commandRunner) MessageDeliverer {
	t.Helper()
	deliverer, err := newSSHMessageDeliverer(config, func(name string) (string, error) {
		if name != sshExecutableName {
			return "", fmt.Errorf("unexpected executable %q", name)
		}
		return testExecutable(t), nil
	}, runner)
	if err != nil {
		t.Fatal(err)
	}
	return deliverer
}

func newTestSynchestraMessageDeliverer(t *testing.T, config sessionmove.SynchestraConfig, options MessageSynchestraOptions, runner commandRunner) MessageDeliverer {
	t.Helper()
	deliverer, err := newSynchestraMessageDeliverer(config, options, func(name string) (string, error) {
		if name != synchestraExecutableName {
			return "", fmt.Errorf("unexpected executable %q", name)
		}
		return testExecutable(t), nil
	}, runner, func(context.Context, time.Duration) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return deliverer
}

func encodeSynchestraMessageInvocationOutput(t *testing.T, message sessionmove.Message, raw []byte, dispatchID, status, artifact string) []byte {
	t.Helper()
	request, _ := courierTestRequest(t)
	encoded := encodeSynchestraInvocationOutput(t, request, raw, dispatchID, status, artifact)
	return mutateSynchestraOutput(t, encoded, func(output *synchestraInvocationOutput) {
		output.Resolved.Repository = nil
		output.Resolved.Invocation.ID = message.MessageID
		output.Resolved.Invocation.Handler = sessionmove.SynchestraSessionMessageHandler
	})
}

func encodeSynchestraMessageStatusOutput(t *testing.T, message sessionmove.Message, raw []byte, dispatchID, status, artifact string) []byte {
	t.Helper()
	return mutateSynchestraOutput(t, encodeSynchestraMessageInvocationOutput(t, message, raw, dispatchID, status, artifact),
		func(output *synchestraInvocationOutput) {
			output.Resolved.Operation = "status"
			output.Resolved.DispatchID = dispatchID
		})
}

func encodeSynchestraMessageReceiptArtifact(t *testing.T, message sessionmove.Message, raw, receipt []byte) string {
	t.Helper()
	artifact := synchestraReceiptArtifact{
		ProtocolVersion: synchestraReceiptArtifactVersion, InvocationID: message.MessageID,
		Handler: sessionmove.SynchestraSessionMessageHandler, PayloadDigest: string(sessionmove.DigestBytes(raw)),
		ReceiptDigest: string(sessionmove.DigestBytes(receipt)), Receipt: receipt,
		CompletedAt: time.Date(2026, 8, 25, 15, 0, 0, 0, time.UTC),
	}
	encoded, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	return synchestraReceiptArtifactPrefix + base64.RawURLEncoding.EncodeToString(encoded)
}
