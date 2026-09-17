package sessioncourier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/sessionmove"
)

func TestSDCovValidateMessagePayloadBranches(t *testing.T) {
	if _, err := validateMessagePayload(nil); err == nil || !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("empty payload error = %v", err)
	}
	if _, err := validateMessagePayload(make([]byte, maxMessageCourierBytes+1)); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("oversized payload error = %v", err)
	}
	if _, err := validateMessagePayload([]byte("not-json")); err == nil || !strings.Contains(err.Error(), "parse session message") {
		t.Fatalf("undecodable payload error = %v", err)
	}

	message, raw := courierTestMessage(t)
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := validateMessagePayload(compact.Bytes()); err == nil || !strings.Contains(err.Error(), "canonical JSON") {
		t.Fatalf("noncanonical payload error = %v", err)
	}

	got, err := validateMessagePayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got != message {
		t.Fatalf("validated message = %#v, want %#v", got, message)
	}
}

func TestSDCovDecodeMessageReceiptBranches(t *testing.T) {
	message, raw := courierTestMessage(t)
	receipt := courierTestMessageReceipt(message, raw)
	receiptRaw, err := sessionmove.EncodeMessageReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := decodeMessageReceipt([]byte("not-json"), message, raw); err == nil || !strings.Contains(err.Error(), "parse session message receipt") {
		t.Fatalf("undecodable receipt error = %v", err)
	}

	var compact bytes.Buffer
	if err := json.Compact(&compact, receiptRaw); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeMessageReceipt(compact.Bytes(), message, raw); err == nil || !strings.Contains(err.Error(), "canonical JSON") {
		t.Fatalf("noncanonical receipt error = %v", err)
	}

	mismatched := receipt
	mismatched.TmuxName = "wb-session-other"
	mismatchedRaw, err := sessionmove.EncodeMessageReceipt(mismatched)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeMessageReceipt(mismatchedRaw, message, raw); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched receipt error = %v", err)
	}

	got, err := decodeMessageReceipt(receiptRaw, message, raw)
	if err != nil {
		t.Fatal(err)
	}
	if got != receipt {
		t.Fatalf("decoded receipt = %#v, want %#v", got, receipt)
	}
}

func TestSDCovSSHMessageDelivererFailureBranches(t *testing.T) {
	message, raw := courierTestMessage(t)
	receipt := courierTestMessageReceipt(message, raw)
	receiptRaw, err := sessionmove.EncodeMessageReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("default remote wb command", func(t *testing.T) {
		runner := &fakeCommandRunner{response: receiptRaw}
		deliverer := newTestSSHMessageDeliverer(t, sessionmove.SSHConfig{Host: "target"}, runner)
		got, err := deliverer.DeliverMessage(context.Background(), raw)
		if err != nil || got != receipt {
			t.Fatalf("DeliverMessage = %#v err=%v", got, err)
		}
		if len(runner.args) < 8 || runner.args[7] != defaultRemoteWBCommand {
			t.Fatalf("remote command argv = %#v", runner.args)
		}
	})
	t.Run("payload refusal", func(t *testing.T) {
		runner := &fakeCommandRunner{}
		deliverer := newTestSSHMessageDeliverer(t, sessionmove.SSHConfig{Host: "target"}, runner)
		if _, err := deliverer.DeliverMessage(context.Background(), []byte("{}")); err == nil || !strings.Contains(err.Error(), "validate SSH session message") {
			t.Fatalf("DeliverMessage error = %v", err)
		}
		if runner.calls != 0 {
			t.Fatalf("ssh calls = %d after local refusal", runner.calls)
		}
	})
	t.Run("cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		runner := &fakeCommandRunner{err: errors.New("signal: killed")}
		deliverer := newTestSSHMessageDeliverer(t, sessionmove.SSHConfig{Host: "target"}, runner)
		_, err := deliverer.DeliverMessage(ctx, raw)
		if err == nil || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "SSH session message delivery to target") {
			t.Fatalf("DeliverMessage error = %v, want wrapped cancellation", err)
		}
	})
	t.Run("silent failure", func(t *testing.T) {
		runner := &fakeCommandRunner{err: errors.New("exit status 255")}
		deliverer := newTestSSHMessageDeliverer(t, sessionmove.SSHConfig{Host: "target"}, runner)
		_, err := deliverer.DeliverMessage(context.Background(), raw)
		if err == nil || err.Error() != "SSH session message delivery to target: exit status 255" {
			t.Fatalf("DeliverMessage error = %v", err)
		}
	})
	t.Run("stderr failure", func(t *testing.T) {
		runner := &fakeCommandRunner{err: errors.New("exit status 255"), stderr: []byte("remote closed connection\n")}
		deliverer := newTestSSHMessageDeliverer(t, sessionmove.SSHConfig{Host: "target"}, runner)
		_, err := deliverer.DeliverMessage(context.Background(), raw)
		if err == nil || err.Error() != "SSH session message delivery to target: exit status 255: remote closed connection" {
			t.Fatalf("DeliverMessage error = %v", err)
		}
	})
	t.Run("oversized receipt", func(t *testing.T) {
		runner := &fakeCommandRunner{response: bytes.Repeat([]byte("z"), maxMessageCourierBytes+1)}
		deliverer := newTestSSHMessageDeliverer(t, sessionmove.SSHConfig{Host: "target"}, runner)
		_, err := deliverer.DeliverMessage(context.Background(), raw)
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("DeliverMessage error = %v, want bounded-output refusal", err)
		}
	})
	t.Run("undecodable receipt", func(t *testing.T) {
		runner := &fakeCommandRunner{response: []byte("not-a-receipt")}
		deliverer := newTestSSHMessageDeliverer(t, sessionmove.SSHConfig{Host: "target"}, runner)
		_, err := deliverer.DeliverMessage(context.Background(), raw)
		if err == nil || !strings.Contains(err.Error(), "validate SSH message receipt from target") {
			t.Fatalf("DeliverMessage error = %v", err)
		}
	})
	t.Run("receipt for another message", func(t *testing.T) {
		other := receipt
		other.MessageID = "message-other"
		otherRaw, err := sessionmove.EncodeMessageReceipt(other)
		if err != nil {
			t.Fatal(err)
		}
		runner := &fakeCommandRunner{response: otherRaw}
		deliverer := newTestSSHMessageDeliverer(t, sessionmove.SSHConfig{Host: "target"}, runner)
		_, err = deliverer.DeliverMessage(context.Background(), raw)
		if err == nil || !strings.Contains(err.Error(), "validate SSH message receipt from target") {
			t.Fatalf("DeliverMessage error = %v", err)
		}
	})
}

func TestSDCovNewSSHMessageDelivererConstructorError(t *testing.T) {
	lookups := 0
	if _, err := newSSHMessageDeliverer(sessionmove.SSHConfig{Host: "target;touch"}, func(string) (string, error) {
		lookups++
		return testExecutable(t), nil
	}, &fakeCommandRunner{}); err == nil || lookups != 0 {
		t.Fatalf("newSSHMessageDeliverer error = %v lookups = %d", err, lookups)
	}
}
