package sessionmove

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// smCovMsgFixture is one admitted handoff with a held execution fence, used to
// exercise message.go through both its exported and unexported entry points.
type smCovMsgFixture struct {
	store              Store
	request            Request
	digest             Digest
	lock               *ExecutionLock
	message            Message
	messageRaw         []byte
	messageDigest      Digest
	incomingRecordedAt time.Time
}

func smCovMsgNewFixture(t *testing.T, withReceipt, admitIncoming bool, incomingRecordedAt time.Time) smCovMsgFixture {
	t.Helper()
	request := validRequest()
	requestRaw, err := EncodeRequest(request)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	digest := DigestBytes(requestRaw)
	store := NewStore(filepath.Join(t.TempDir(), "handoffs"))
	if _, err := store.Admit(requestRaw, digest); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if withReceipt {
		if _, _, err := store.SaveReceipt(request.HandoffID, digest, validReceipt(request, digest)); err != nil {
			t.Fatalf("SaveReceipt: %v", err)
		}
	}
	lock, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
	if err != nil {
		t.Fatalf("AcquireExecutionLock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	message := validMessage(request)
	messageRaw, err := EncodeMessage(message)
	if err != nil {
		t.Fatalf("EncodeMessage: %v", err)
	}
	fixture := smCovMsgFixture{
		store: store, request: request, digest: digest, lock: lock,
		message: message, messageRaw: messageRaw, messageDigest: DigestBytes(messageRaw),
		incomingRecordedAt: incomingRecordedAt,
	}
	if admitIncoming {
		if _, err := store.AdmitIncomingMessageUnderLock(lock, request.HandoffID, digest, messageRaw, incomingRecordedAt); err != nil {
			t.Fatalf("AdmitIncomingMessageUnderLock: %v", err)
		}
	}
	return fixture
}

func (f smCovMsgFixture) smCovMsgHandoffPath() string {
	return filepath.Join(f.store.Root, f.request.HandoffID)
}

func (f smCovMsgFixture) smCovMsgEntryDir(direction MessageDirection, messageID string) string {
	name := messageInboxDirName
	if direction == MessageDirectionOutgoing {
		name = messageOutboxDirName
	}
	return filepath.Join(f.store.Root, f.request.HandoffID, name, messageID)
}

// smCovMsgDefaultIntent is the intent that exactly matches the fixture message.
func (f smCovMsgFixture) smCovMsgDefaultIntent() MessagePasteIntent {
	return MessagePasteIntent{
		SchemaVersion:        MessagePasteIntentSchemaVersion,
		MessageID:            f.message.MessageID,
		MessageDigest:        f.messageDigest,
		HandoffID:            f.request.HandoffID,
		RecipientWBSessionID: f.message.RecipientWBSessionID,
		TmuxName:             "wb-session-wbs-successor",
		PaneID:               "%7",
		PID:                  1234,
		IntendedAt:           f.incomingRecordedAt.Add(time.Second),
	}
}

func (f smCovMsgFixture) smCovMsgSaveIntent(t *testing.T, intent MessagePasteIntent) {
	t.Helper()
	if _, _, err := f.store.SaveIncomingPasteIntentUnderLock(f.lock, f.request.HandoffID, f.digest, intent); err != nil {
		t.Fatalf("SaveIncomingPasteIntentUnderLock: %v", err)
	}
}

// smCovMsgCorruptHandoffReceipt overwrites the handoff-level receipt with
// undecodable bytes so retainHandoffUnderLock succeeds but loadReceiptAt fails.
func (f smCovMsgFixture) smCovMsgCorruptHandoffReceipt(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.smCovMsgHandoffPath(), receiptFileName), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt handoff receipt: %v", err)
	}
}

func smCovMsgMarshal(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := marshalJSON(value)
	if err != nil {
		t.Fatalf("marshalJSON(%T): %v", value, err)
	}
	return raw
}

func smCovMsgWrite(t *testing.T, path string, raw []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, raw, mode); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func TestSmCovMsgNewMessageID(t *testing.T) {
	t.Parallel()
	first, err := NewMessageID()
	if err != nil {
		t.Fatalf("NewMessageID: %v", err)
	}
	second, err := NewMessageID()
	if err != nil {
		t.Fatalf("NewMessageID second call: %v", err)
	}
	if first == second {
		t.Fatalf("NewMessageID returned the same value twice: %q", first)
	}
	if !strings.HasPrefix(first, "message-") {
		t.Fatalf("NewMessageID = %q, want prefix %q", first, "message-")
	}
	encoded := strings.TrimPrefix(first, "message-")
	if len(encoded) != 32 {
		t.Fatalf("NewMessageID hex length = %d (%q), want 32", len(encoded), encoded)
	}
	decoded, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatalf("NewMessageID suffix %q is not hex: %v", encoded, err)
	}
	if len(decoded) != 16 {
		t.Fatalf("NewMessageID decoded %d bytes, want 16", len(decoded))
	}
	if hex.EncodeToString(decoded) != encoded {
		t.Fatalf("NewMessageID suffix %q is not canonical lowercase hex", encoded)
	}
	if second[:len("message-")] != "message-" {
		t.Fatalf("second NewMessageID = %q, want message- prefix", second)
	}
}

func TestSmCovMsgReceiptValidateBranches(t *testing.T) {
	t.Parallel()
	message := validMessage(validRequest())
	raw, err := EncodeMessage(message)
	if err != nil {
		t.Fatalf("EncodeMessage: %v", err)
	}
	receipt := validMessageReceipt(message, DigestBytes(raw))
	if _, err := EncodeMessageReceipt(receipt); err != nil {
		t.Fatalf("valid receipt rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*MessageReceipt)
		want   string
	}{
		{"older schema", func(r *MessageReceipt) { r.SchemaVersion = 0 }, "schema_version 0"},
		{"newer schema", func(r *MessageReceipt) { r.SchemaVersion = MessageReceiptSchemaVersion + 1 }, "newer than supported"},
		{"missing message id", func(r *MessageReceipt) { r.MessageID = "" }, "message_id"},
		{"invalid message id", func(r *MessageReceipt) { r.MessageID = "bad id" }, "message_id"},
		{"missing handoff id", func(r *MessageReceipt) { r.HandoffID = "" }, "handoff_id"},
		{"missing sender", func(r *MessageReceipt) { r.SenderWBSessionID = "" }, "sender_wb_session_id"},
		{"missing recipient", func(r *MessageReceipt) { r.RecipientWBSessionID = "" }, "recipient_wb_session_id"},
		{"missing reply to", func(r *MessageReceipt) { r.ReplyToWBSessionID = "" }, "reply_to_wb_session_id"},
		{"missing tmux name", func(r *MessageReceipt) { r.TmuxName = "" }, "tmux_name"},
		{"missing digest", func(r *MessageReceipt) { r.MessageDigest = "" }, "message_digest"},
		{"wrong digest algorithm", func(r *MessageReceipt) { r.MessageDigest = "md5:abcd" }, "message_digest"},
		{"unsupported kind", func(r *MessageReceipt) { r.Kind = MessageKind("telepathy") }, "kind"},
		{"bad pane id", func(r *MessageReceipt) { r.PaneID = "7" }, "pane_id"},
		{"empty pane id", func(r *MessageReceipt) { r.PaneID = "" }, "pane_id"},
		{"zero pid", func(r *MessageReceipt) { r.PID = 0 }, "pid must be positive"},
		{"negative pid", func(r *MessageReceipt) { r.PID = -1 }, "pid must be positive"},
		{"zero recorded at", func(r *MessageReceipt) { r.RecordedAt = time.Time{} }, "recorded_at is required"},
		{"zero pasted at", func(r *MessageReceipt) { r.PastedAt = time.Time{} }, "pasted_at is required"},
		{"pasted before recorded", func(r *MessageReceipt) { r.PastedAt = r.RecordedAt.Add(-time.Second) }, "pasted_at precedes recorded_at"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			mutated := receipt
			test.mutate(&mutated)
			got, err := EncodeMessageReceipt(mutated)
			if err == nil {
				t.Fatalf("EncodeMessageReceipt accepted %#v and produced %s", mutated, got)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("EncodeMessageReceipt error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestSmCovMsgValidateMessageReceiptErrors(t *testing.T) {
	t.Parallel()
	message := validMessage(validRequest())
	raw, err := EncodeMessage(message)
	if err != nil {
		t.Fatalf("EncodeMessage: %v", err)
	}
	digest := DigestBytes(raw)
	receipt := validMessageReceipt(message, digest)
	if err := ValidateMessageReceipt(receipt, message, digest, receipt.TmuxName, receipt.PID); err != nil {
		t.Fatalf("valid receipt rejected: %v", err)
	}

	t.Run("invalid receipt", func(t *testing.T) {
		t.Parallel()
		broken := receipt
		broken.PastedAt = time.Time{}
		if err := ValidateMessageReceipt(broken, message, digest, receipt.TmuxName, receipt.PID); err == nil {
			t.Fatal("ValidateMessageReceipt accepted an invalid receipt")
		}
	})
	t.Run("invalid message", func(t *testing.T) {
		t.Parallel()
		broken := message
		broken.Kind = MessageKind("telepathy")
		if err := ValidateMessageReceipt(receipt, broken, digest, receipt.TmuxName, receipt.PID); err == nil {
			t.Fatal("ValidateMessageReceipt accepted an invalid message")
		}
	})
	t.Run("invalid digest", func(t *testing.T) {
		t.Parallel()
		if err := ValidateMessageReceipt(receipt, message, Digest("not-a-digest"), receipt.TmuxName, receipt.PID); err == nil {
			t.Fatal("ValidateMessageReceipt accepted an invalid digest")
		}
	})

	mismatches := []struct {
		name    string
		receipt func(*MessageReceipt)
		tmux    string
		pid     int
	}{
		{"message id", func(r *MessageReceipt) { r.MessageID = "message-999" }, receipt.TmuxName, receipt.PID},
		{"message digest", func(r *MessageReceipt) { r.MessageDigest = DigestBytes([]byte("other payload")) }, receipt.TmuxName, receipt.PID},
		{"handoff id", func(r *MessageReceipt) { r.HandoffID = "handoff-999" }, receipt.TmuxName, receipt.PID},
		{"sender", func(r *MessageReceipt) { r.SenderWBSessionID = "wbs-other-sender" }, receipt.TmuxName, receipt.PID},
		{"recipient", func(r *MessageReceipt) { r.RecipientWBSessionID = "wbs-other-recipient" }, receipt.TmuxName, receipt.PID},
		{"reply to", func(r *MessageReceipt) { r.ReplyToWBSessionID = "wbs-other-reply" }, receipt.TmuxName, receipt.PID},
		{"kind", func(r *MessageReceipt) { r.Kind = MessageKindRequestHandoff }, receipt.TmuxName, receipt.PID},
		{"tmux name", func(r *MessageReceipt) {}, "wb-session-other", receipt.PID},
		{"pid", func(r *MessageReceipt) {}, receipt.TmuxName, receipt.PID + 1},
	}
	for _, test := range mismatches {
		t.Run("binding mismatch "+test.name, func(t *testing.T) {
			t.Parallel()
			mutated := receipt
			test.receipt(&mutated)
			if _, err := EncodeMessageReceipt(mutated); err != nil {
				t.Fatalf("mutated receipt is not individually valid: %v", err)
			}
			err := ValidateMessageReceipt(mutated, message, digest, test.tmux, test.pid)
			if !errors.Is(err, ErrHandoffConflict) {
				t.Fatalf("ValidateMessageReceipt error = %v, want ErrHandoffConflict", err)
			}
		})
	}
}

func TestSmCovMsgValidateMessageForRequestErrors(t *testing.T) {
	t.Parallel()
	request := validRequest()
	message := validMessage(request)
	if err := ValidateMessageForRequest(message, request); err != nil {
		t.Fatalf("valid message rejected: %v", err)
	}

	t.Run("invalid message", func(t *testing.T) {
		t.Parallel()
		broken := message
		broken.Body = ""
		if err := ValidateMessageForRequest(broken, request); err == nil {
			t.Fatal("ValidateMessageForRequest accepted an invalid message")
		}
	})
	t.Run("invalid request", func(t *testing.T) {
		t.Parallel()
		broken := request
		broken.SchemaVersion = RequestSchemaVersion + 1
		if err := ValidateMessageForRequest(message, broken); err == nil {
			t.Fatal("ValidateMessageForRequest accepted an invalid request")
		}
	})

	lineage := []struct {
		name   string
		mutate func(*Message)
	}{
		{"handoff id", func(m *Message) { m.HandoffID = "handoff-999" }},
		{"sender", func(m *Message) { m.SenderWBSessionID = "wbs-other-source" }},
		{"recipient", func(m *Message) { m.RecipientWBSessionID = "wbs-other-successor" }},
		{"reply to", func(m *Message) { m.ReplyToWBSessionID = "wbs-other-successor" }},
	}
	for _, test := range lineage {
		t.Run("lineage mismatch "+test.name, func(t *testing.T) {
			t.Parallel()
			mutated := message
			test.mutate(&mutated)
			if err := ValidateMessageForRequest(mutated, request); !errors.Is(err, ErrHandoffConflict) {
				t.Fatalf("ValidateMessageForRequest error = %v, want ErrHandoffConflict", err)
			}
		})
	}

	t.Run("message predates handoff", func(t *testing.T) {
		t.Parallel()
		mutated := message
		mutated.SentAt = request.CreatedAt.Add(-time.Second)
		if err := ValidateMessageForRequest(mutated, request); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("pre-handoff message error = %v, want ErrHandoffConflict", err)
		}
	})
}

func TestSmCovMsgAdmitMessageUnderLockErrors(t *testing.T) {
	t.Parallel()
	t.Run("empty payload", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		_, err := fixture.store.AdmitOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, nil, fixture.message.SentAt)
		if err == nil || !strings.Contains(err.Error(), "must be non-empty") {
			t.Fatalf("empty payload error = %v", err)
		}
	})

	t.Run("oversized payload", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		huge := bytes.Repeat([]byte("x"), maxMessageBytes+1)
		_, err := fixture.store.AdmitOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, huge, fixture.message.SentAt)
		if err == nil || !strings.Contains(err.Error(), "at most") {
			t.Fatalf("oversized payload error = %v", err)
		}
	})

	t.Run("malformed JSON payload", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		_, err := fixture.store.AdmitOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, []byte("{not json"), fixture.message.SentAt)
		if err == nil || !strings.Contains(err.Error(), "parse session message") {
			t.Fatalf("malformed payload error = %v", err)
		}
	})

	t.Run("non-canonical JSON payload", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		var compact bytes.Buffer
		if err := json.Compact(&compact, fixture.messageRaw); err != nil {
			t.Fatalf("json.Compact: %v", err)
		}
		if bytes.Equal(compact.Bytes(), fixture.messageRaw) {
			t.Fatal("compacted payload unexpectedly equals the canonical encoding")
		}
		_, err := fixture.store.AdmitOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, compact.Bytes(), fixture.message.SentAt)
		if err == nil || !strings.Contains(err.Error(), "canonical JSON encoding") {
			t.Fatalf("non-canonical payload error = %v", err)
		}
	})

	t.Run("lineage mismatch", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		mutated := fixture.message
		mutated.HandoffID = "handoff-999"
		raw, err := EncodeMessage(mutated)
		if err != nil {
			t.Fatalf("EncodeMessage: %v", err)
		}
		if _, err := fixture.store.AdmitOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, raw, mutated.SentAt); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("lineage mismatch error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("no durable handoff receipt", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, false, false, time.Time{})
		_, err := fixture.store.AdmitOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.messageRaw, fixture.message.SentAt)
		if err == nil || !strings.Contains(err.Error(), "requires a durable completed handoff receipt") {
			t.Fatalf("missing handoff receipt error = %v", err)
		}
	})

	t.Run("corrupt durable handoff receipt", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		fixture.smCovMsgCorruptHandoffReceipt(t)
		_, err := fixture.store.AdmitOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.messageRaw, fixture.message.SentAt)
		if err == nil || !strings.Contains(err.Error(), "decode durable handoff receipt") {
			t.Fatalf("corrupt handoff receipt error = %v", err)
		}
	})

	t.Run("zero recorded at", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		_, err := fixture.store.AdmitOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.messageRaw, time.Time{})
		if err == nil || !strings.Contains(err.Error(), "recorded_at is required") {
			t.Fatalf("zero recorded_at error = %v", err)
		}
	})

	t.Run("outgoing recorded at must equal sent at", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		_, err := fixture.store.AdmitOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.messageRaw, fixture.message.SentAt.Add(time.Second))
		if err == nil || !strings.Contains(err.Error(), "must equal its caller-owned sent_at") {
			t.Fatalf("outgoing recorded_at mismatch error = %v", err)
		}
	})

	t.Run("incoming accepts any non-zero recorded at", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		recordedAt := fixture.message.SentAt.Add(3 * time.Second)
		state, err := fixture.store.AdmitIncomingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.messageRaw, recordedAt)
		if err != nil {
			t.Fatalf("incoming admission: %v", err)
		}
		if state.Record.Direction != MessageDirectionIncoming || !state.Record.RecordedAt.Equal(recordedAt.UTC()) {
			t.Fatalf("incoming record = %#v, want direction incoming recorded at %s", state.Record, recordedAt.UTC())
		}
	})

	t.Run("conflicting exact bytes for same message ID", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		if _, err := fixture.store.AdmitOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.messageRaw, fixture.message.SentAt); err != nil {
			t.Fatalf("first admission: %v", err)
		}
		conflict := fixture.message
		conflict.Body = "different exact bytes"
		conflictRaw, err := EncodeMessage(conflict)
		if err != nil {
			t.Fatalf("EncodeMessage: %v", err)
		}
		if _, err := fixture.store.AdmitOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, conflictRaw, conflict.SentAt); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("conflicting bytes error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("lock held for a different handoff", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		_, err := fixture.store.AdmitOutgoingMessageUnderLock(fixture.lock, "handoff-999", fixture.digest, fixture.messageRaw, fixture.message.SentAt)
		if err == nil || !strings.Contains(err.Error(), "execution lock is for handoff") {
			t.Fatalf("wrong lock error = %v", err)
		}
	})

	t.Run("unsupported direction", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		_, err := fixture.store.admitMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, MessageDirection("sideways"), fixture.messageRaw, fixture.message.SentAt)
		if err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("unsupported direction error = %v", err)
		}
	})

	t.Run("existing payload with wrong mode refuses admission", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		path := filepath.Join(fixture.smCovMsgEntryDir(MessageDirectionOutgoing, fixture.message.MessageID), messagePayloadFileName)
		smCovMsgWrite(t, path, fixture.messageRaw, 0o644)
		_, err := fixture.store.AdmitOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.messageRaw, fixture.message.SentAt)
		if err == nil || !strings.Contains(err.Error(), "durable session message") {
			t.Fatalf("wrong-mode payload error = %v", err)
		}
	})

	t.Run("existing mismatched paste intent refuses admission", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		intent := fixture.smCovMsgDefaultIntent()
		intent.MessageDigest = DigestBytes([]byte("some other payload"))
		smCovMsgWrite(t, filepath.Join(fixture.smCovMsgEntryDir(MessageDirectionOutgoing, fixture.message.MessageID), messageIntentFileName), smCovMsgMarshal(t, intent), 0o600)
		_, err := fixture.store.AdmitOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.messageRaw, fixture.message.SentAt)
		if !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("mismatched durable intent error = %v, want ErrHandoffConflict", err)
		}
	})
}

func TestSmCovMsgLoadMessageUnderLockErrors(t *testing.T) {
	t.Parallel()
	t.Run("invalid message id", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		if _, err := fixture.store.LoadIncomingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, ""); err == nil || !strings.Contains(err.Error(), "message_id") {
			t.Fatalf("invalid message id error = %v", err)
		}
	})

	t.Run("no durable handoff receipt", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, false, false, time.Time{})
		_, err := fixture.store.LoadOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.message.MessageID)
		if err == nil || !strings.Contains(err.Error(), "requires a durable completed handoff receipt") {
			t.Fatalf("missing handoff receipt error = %v", err)
		}
	})

	t.Run("corrupt durable handoff receipt", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		fixture.smCovMsgCorruptHandoffReceipt(t)
		_, err := fixture.store.LoadOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.message.MessageID)
		if err == nil || !strings.Contains(err.Error(), "decode durable handoff receipt") {
			t.Fatalf("corrupt handoff receipt error = %v", err)
		}
	})

	t.Run("lock held for a different handoff", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		_, err := fixture.store.LoadOutgoingMessageUnderLock(fixture.lock, "handoff-999", fixture.digest, fixture.message.MessageID)
		if err == nil || !strings.Contains(err.Error(), "execution lock is for handoff") {
			t.Fatalf("wrong lock error = %v", err)
		}
	})

	t.Run("missing message directory", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		_, err := fixture.store.LoadOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.message.MessageID)
		if err == nil || !strings.Contains(err.Error(), "open message") {
			t.Fatalf("missing message directory error = %v", err)
		}
	})

	t.Run("loads admitted outgoing message", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		if _, err := fixture.store.AdmitOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.messageRaw, fixture.message.SentAt); err != nil {
			t.Fatalf("admit: %v", err)
		}
		state, err := fixture.store.LoadOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.message.MessageID)
		if err != nil {
			t.Fatalf("load outgoing: %v", err)
		}
		if state.Record.Direction != MessageDirectionOutgoing || state.Message != fixture.message || state.Digest != fixture.messageDigest {
			t.Fatalf("loaded outgoing state = %#v", state)
		}
	})
}

func TestSmCovMsgResumeOutgoingMessageUnderLockErrors(t *testing.T) {
	t.Parallel()
	outboxDir := func(fixture smCovMsgFixture) string {
		return fixture.smCovMsgEntryDir(MessageDirectionOutgoing, fixture.message.MessageID)
	}

	t.Run("invalid message id", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		if _, err := fixture.store.ResumeOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, ""); err == nil || !strings.Contains(err.Error(), "message_id") {
			t.Fatalf("invalid message id error = %v", err)
		}
	})

	t.Run("no durable handoff receipt", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, false, false, time.Time{})
		_, err := fixture.store.ResumeOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.message.MessageID)
		if err == nil || !strings.Contains(err.Error(), "requires a durable completed handoff receipt") {
			t.Fatalf("missing handoff receipt error = %v", err)
		}
	})

	t.Run("corrupt durable handoff receipt", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		fixture.smCovMsgCorruptHandoffReceipt(t)
		_, err := fixture.store.ResumeOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.message.MessageID)
		if err == nil || !strings.Contains(err.Error(), "decode durable handoff receipt") {
			t.Fatalf("corrupt handoff receipt error = %v", err)
		}
	})

	t.Run("lock held for a different handoff", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		_, err := fixture.store.ResumeOutgoingMessageUnderLock(fixture.lock, "handoff-999", fixture.digest, fixture.message.MessageID)
		if err == nil || !strings.Contains(err.Error(), "execution lock is for handoff") {
			t.Fatalf("wrong lock error = %v", err)
		}
	})

	t.Run("missing message directory", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		_, err := fixture.store.ResumeOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.message.MessageID)
		if err == nil || !strings.Contains(err.Error(), "open message") {
			t.Fatalf("missing entry error = %v", err)
		}
	})

	t.Run("missing payload", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		if err := os.MkdirAll(outboxDir(fixture), 0o700); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		_, err := fixture.store.ResumeOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.message.MessageID)
		if err == nil || !strings.Contains(err.Error(), "durable outgoing session message") {
			t.Fatalf("missing payload error = %v", err)
		}
	})

	t.Run("malformed payload", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		smCovMsgWrite(t, filepath.Join(outboxDir(fixture), messagePayloadFileName), []byte("{not json"), 0o600)
		_, err := fixture.store.ResumeOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.message.MessageID)
		if err == nil || !strings.Contains(err.Error(), "parse session message") {
			t.Fatalf("malformed payload error = %v", err)
		}
	})

	t.Run("non-canonical payload", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		var compact bytes.Buffer
		if err := json.Compact(&compact, fixture.messageRaw); err != nil {
			t.Fatalf("json.Compact: %v", err)
		}
		smCovMsgWrite(t, filepath.Join(outboxDir(fixture), messagePayloadFileName), compact.Bytes(), 0o600)
		if _, err := fixture.store.ResumeOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.message.MessageID); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("non-canonical payload error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("payload message id mismatch", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		otherID := "message-999"
		smCovMsgWrite(t, filepath.Join(fixture.smCovMsgEntryDir(MessageDirectionOutgoing, otherID), messagePayloadFileName), fixture.messageRaw, 0o600)
		if _, err := fixture.store.ResumeOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, otherID); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("payload message id mismatch error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("payload lineage mismatch", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		mutated := fixture.message
		mutated.HandoffID = "handoff-999"
		raw, err := EncodeMessage(mutated)
		if err != nil {
			t.Fatalf("EncodeMessage: %v", err)
		}
		smCovMsgWrite(t, filepath.Join(outboxDir(fixture), messagePayloadFileName), raw, 0o600)
		if _, err := fixture.store.ResumeOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.message.MessageID); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("payload lineage mismatch error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("unreadable existing record", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		smCovMsgWrite(t, filepath.Join(outboxDir(fixture), messagePayloadFileName), fixture.messageRaw, 0o600)
		smCovMsgWrite(t, filepath.Join(outboxDir(fixture), messageRecordFileName), []byte("{}\n"), 0o644)
		_, err := fixture.store.ResumeOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.message.MessageID)
		if err == nil || !strings.Contains(err.Error(), "session message record") {
			t.Fatalf("unreadable record error = %v", err)
		}
	})

	t.Run("repairs the missing record", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		smCovMsgWrite(t, filepath.Join(outboxDir(fixture), messagePayloadFileName), fixture.messageRaw, 0o600)
		state, err := fixture.store.ResumeOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.message.MessageID)
		if err != nil {
			t.Fatalf("repair resume: %v", err)
		}
		if !state.Replay || state.Record.MessageID != fixture.message.MessageID || !state.Record.RecordedAt.Equal(fixture.message.SentAt.UTC()) {
			t.Fatalf("repaired state = %#v", state)
		}
		if _, err := os.Stat(filepath.Join(outboxDir(fixture), messageRecordFileName)); err != nil {
			t.Fatalf("repaired record file: %v", err)
		}
	})
}

func TestSmCovMsgSaveIncomingPasteIntentUnderLockErrors(t *testing.T) {
	t.Parallel()
	baseRecordedAt := time.Date(2026, 8, 25, 12, 0, 1, 0, time.UTC)

	t.Run("no durable handoff receipt", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, false, false, time.Time{})
		intent := MessagePasteIntent{
			SchemaVersion: MessagePasteIntentSchemaVersion, MessageID: fixture.message.MessageID,
			MessageDigest: fixture.messageDigest, HandoffID: fixture.request.HandoffID,
			RecipientWBSessionID: fixture.message.RecipientWBSessionID, TmuxName: "wb-session-wbs-successor",
			PaneID: "%7", PID: 1234, IntendedAt: baseRecordedAt,
		}
		_, _, err := fixture.store.SaveIncomingPasteIntentUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, intent)
		if err == nil || !strings.Contains(err.Error(), "requires a durable handoff receipt") {
			t.Fatalf("missing handoff receipt error = %v", err)
		}
	})

	t.Run("corrupt durable handoff receipt", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, true, baseRecordedAt)
		fixture.smCovMsgCorruptHandoffReceipt(t)
		_, _, err := fixture.store.SaveIncomingPasteIntentUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.smCovMsgDefaultIntent())
		if err == nil || !strings.Contains(err.Error(), "decode durable handoff receipt") {
			t.Fatalf("corrupt handoff receipt error = %v", err)
		}
	})

	t.Run("lock held for a different handoff", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, true, baseRecordedAt)
		_, _, err := fixture.store.SaveIncomingPasteIntentUnderLock(fixture.lock, "handoff-999", fixture.digest, fixture.smCovMsgDefaultIntent())
		if err == nil || !strings.Contains(err.Error(), "execution lock is for handoff") {
			t.Fatalf("wrong lock error = %v", err)
		}
	})

	t.Run("missing inbox entry", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		_, _, err := fixture.store.SaveIncomingPasteIntentUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.smCovMsgDefaultIntent())
		if err == nil || !strings.Contains(err.Error(), "open message") {
			t.Fatalf("missing inbox entry error = %v", err)
		}
	})

	t.Run("unreadable inbox record", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, true, baseRecordedAt)
		if err := os.Remove(filepath.Join(fixture.smCovMsgEntryDir(MessageDirectionIncoming, fixture.message.MessageID), messageRecordFileName)); err != nil {
			t.Fatalf("remove record: %v", err)
		}
		_, _, err := fixture.store.SaveIncomingPasteIntentUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.smCovMsgDefaultIntent())
		if err == nil || !strings.Contains(err.Error(), "session message record") {
			t.Fatalf("unreadable record error = %v", err)
		}
	})

	invalid := []struct {
		name   string
		mutate func(*MessagePasteIntent)
	}{
		{"older schema", func(i *MessagePasteIntent) { i.SchemaVersion = 0 }},
		{"newer schema", func(i *MessagePasteIntent) { i.SchemaVersion = MessagePasteIntentSchemaVersion + 1 }},
		{"bad pane id", func(i *MessagePasteIntent) { i.PaneID = "7" }},
		{"zero pid", func(i *MessagePasteIntent) { i.PID = 0 }},
		{"zero intended at", func(i *MessagePasteIntent) { i.IntendedAt = time.Time{} }},
	}
	for _, test := range invalid {
		t.Run("invalid intent "+test.name, func(t *testing.T) {
			t.Parallel()
			fixture := smCovMsgNewFixture(t, true, true, baseRecordedAt)
			intent := fixture.smCovMsgDefaultIntent()
			test.mutate(&intent)
			_, _, err := fixture.store.SaveIncomingPasteIntentUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, intent)
			if err == nil || !strings.Contains(err.Error(), "message paste intent is invalid") {
				t.Fatalf("invalid intent error = %v", err)
			}
		})
	}

	mismatched := []struct {
		name   string
		mutate func(*MessagePasteIntent)
	}{
		{"message digest", func(i *MessagePasteIntent) { i.MessageDigest = DigestBytes([]byte("other payload")) }},
		{"handoff id", func(i *MessagePasteIntent) { i.HandoffID = "handoff-999" }},
		{"recipient", func(i *MessagePasteIntent) { i.RecipientWBSessionID = "wbs-other-successor" }},
	}
	for _, test := range mismatched {
		t.Run("intent mismatch "+test.name, func(t *testing.T) {
			t.Parallel()
			fixture := smCovMsgNewFixture(t, true, true, baseRecordedAt)
			intent := fixture.smCovMsgDefaultIntent()
			test.mutate(&intent)
			_, _, err := fixture.store.SaveIncomingPasteIntentUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, intent)
			if !errors.Is(err, ErrHandoffConflict) {
				t.Fatalf("intent mismatch error = %v, want ErrHandoffConflict", err)
			}
		})
	}

	t.Run("publishes then replays identical intent", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, true, baseRecordedAt)
		intent := fixture.smCovMsgDefaultIntent()
		stored, replay, err := fixture.store.SaveIncomingPasteIntentUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, intent)
		if err != nil || replay || stored != intent {
			t.Fatalf("first save = %#v replay=%t err=%v", stored, replay, err)
		}
		path := filepath.Join(fixture.smCovMsgEntryDir(MessageDirectionIncoming, fixture.message.MessageID), messageIntentFileName)
		onDisk, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read published intent: %v", err)
		}
		if !bytes.Equal(onDisk, smCovMsgMarshal(t, intent)) {
			t.Fatalf("published intent bytes = %s, want %s", onDisk, smCovMsgMarshal(t, intent))
		}
		replayed, replay, err := fixture.store.SaveIncomingPasteIntentUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, intent)
		if err != nil || !replay || replayed != intent {
			t.Fatalf("replay = %#v replay=%t err=%v", replayed, replay, err)
		}
	})

	t.Run("different intent for the same message conflicts", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, true, baseRecordedAt)
		intent := fixture.smCovMsgDefaultIntent()
		fixture.smCovMsgSaveIntent(t, intent)
		changed := intent
		changed.PaneID = "%8"
		if _, _, err := fixture.store.SaveIncomingPasteIntentUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, changed); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("different intent error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("tampered durable intent blocks a new save", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, true, baseRecordedAt)
		smCovMsgWrite(t, filepath.Join(fixture.smCovMsgEntryDir(MessageDirectionIncoming, fixture.message.MessageID), messageIntentFileName), []byte("{not json"), 0o600)
		_, _, err := fixture.store.SaveIncomingPasteIntentUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.smCovMsgDefaultIntent())
		if err == nil || !strings.Contains(err.Error(), "decode message paste intent") {
			t.Fatalf("tampered intent error = %v", err)
		}
	})
}

func TestSmCovMsgSaveMessageReceiptUnderLockErrors(t *testing.T) {
	t.Parallel()
	baseRecordedAt := time.Date(2026, 8, 25, 12, 0, 1, 0, time.UTC)

	t.Run("no durable handoff receipt", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, false, false, time.Time{})
		receipt := validMessageReceipt(fixture.message, fixture.messageDigest)
		_, _, err := fixture.store.SaveOutgoingMessageReceiptUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, receipt)
		if err == nil || !strings.Contains(err.Error(), "requires a durable handoff receipt") {
			t.Fatalf("missing handoff receipt error = %v", err)
		}
	})

	t.Run("corrupt durable handoff receipt", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		fixture.smCovMsgCorruptHandoffReceipt(t)
		receipt := validMessageReceipt(fixture.message, fixture.messageDigest)
		_, _, err := fixture.store.SaveOutgoingMessageReceiptUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, receipt)
		if err == nil || !strings.Contains(err.Error(), "decode durable handoff receipt") {
			t.Fatalf("corrupt handoff receipt error = %v", err)
		}
	})

	t.Run("lock held for a different handoff", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		receipt := validMessageReceipt(fixture.message, fixture.messageDigest)
		_, _, err := fixture.store.SaveOutgoingMessageReceiptUnderLock(fixture.lock, "handoff-999", fixture.digest, receipt)
		if err == nil || !strings.Contains(err.Error(), "execution lock is for handoff") {
			t.Fatalf("wrong lock error = %v", err)
		}
	})

	t.Run("missing message entry", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		receipt := validMessageReceipt(fixture.message, fixture.messageDigest)
		_, _, err := fixture.store.SaveOutgoingMessageReceiptUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, receipt)
		if err == nil || !strings.Contains(err.Error(), "open message") {
			t.Fatalf("missing entry error = %v", err)
		}
	})

	t.Run("unreadable message record", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		if _, err := fixture.store.AdmitOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.messageRaw, fixture.message.SentAt); err != nil {
			t.Fatalf("admit: %v", err)
		}
		if err := os.Remove(filepath.Join(fixture.smCovMsgEntryDir(MessageDirectionOutgoing, fixture.message.MessageID), messageRecordFileName)); err != nil {
			t.Fatalf("remove record: %v", err)
		}
		receipt := validMessageReceipt(fixture.message, fixture.messageDigest)
		_, _, err := fixture.store.SaveOutgoingMessageReceiptUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, receipt)
		if err == nil || !strings.Contains(err.Error(), "session message record") {
			t.Fatalf("unreadable record error = %v", err)
		}
	})

	t.Run("receipt does not match message", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		if _, err := fixture.store.AdmitOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.messageRaw, fixture.message.SentAt); err != nil {
			t.Fatalf("admit: %v", err)
		}
		receipt := validMessageReceipt(fixture.message, fixture.messageDigest)
		receipt.MessageDigest = DigestBytes([]byte("forged payload"))
		if _, _, err := fixture.store.SaveOutgoingMessageReceiptUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, receipt); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("receipt mismatch error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("incoming receipt without durable paste intent", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, true, baseRecordedAt)
		receipt := validMessageReceipt(fixture.message, fixture.messageDigest)
		_, _, err := fixture.store.SaveIncomingMessageReceiptUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, receipt)
		if err == nil || !strings.Contains(err.Error(), "requires a durable paste intent") {
			t.Fatalf("missing paste intent error = %v", err)
		}
	})

	incomingMismatch := []struct {
		name    string
		intent  func(*MessagePasteIntent)
		receipt func(*MessageReceipt)
	}{
		{"recorded at", func(i *MessagePasteIntent) {}, func(r *MessageReceipt) {
			r.RecordedAt = baseRecordedAt.Add(time.Minute)
			r.PastedAt = r.RecordedAt.Add(time.Second)
		}},
		{"pane id", func(i *MessagePasteIntent) {}, func(r *MessageReceipt) { r.PaneID = "%8" }},
		{"tmux name", func(i *MessagePasteIntent) { i.TmuxName = "wb-session-other" }, func(r *MessageReceipt) {}},
		{"pid", func(i *MessagePasteIntent) { i.PID = 4321 }, func(r *MessageReceipt) {}},
		{"pasted before intended", func(i *MessagePasteIntent) { i.IntendedAt = baseRecordedAt.Add(time.Hour) }, func(r *MessageReceipt) {}},
	}
	for _, test := range incomingMismatch {
		t.Run("incoming receipt mismatch "+test.name, func(t *testing.T) {
			t.Parallel()
			fixture := smCovMsgNewFixture(t, true, true, baseRecordedAt)
			intent := fixture.smCovMsgDefaultIntent()
			test.intent(&intent)
			fixture.smCovMsgSaveIntent(t, intent)
			receipt := validMessageReceipt(fixture.message, fixture.messageDigest)
			test.receipt(&receipt)
			if _, _, err := fixture.store.SaveIncomingMessageReceiptUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, receipt); !errors.Is(err, ErrHandoffConflict) {
				t.Fatalf("incoming receipt mismatch error = %v, want ErrHandoffConflict", err)
			}
		})
	}

	t.Run("replays an identical outgoing receipt", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		if _, err := fixture.store.AdmitOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.messageRaw, fixture.message.SentAt); err != nil {
			t.Fatalf("admit: %v", err)
		}
		receipt := validMessageReceipt(fixture.message, fixture.messageDigest)
		stored, replay, err := fixture.store.SaveOutgoingMessageReceiptUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, receipt)
		if err != nil || replay || stored != receipt {
			t.Fatalf("first save = %#v replay=%t err=%v", stored, replay, err)
		}
		replayed, replay, err := fixture.store.SaveOutgoingMessageReceiptUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, receipt)
		if err != nil || !replay || replayed != receipt {
			t.Fatalf("replay = %#v replay=%t err=%v", replayed, replay, err)
		}
	})

	t.Run("different outgoing receipt conflicts", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Time{})
		if _, err := fixture.store.AdmitOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.messageRaw, fixture.message.SentAt); err != nil {
			t.Fatalf("admit: %v", err)
		}
		receipt := validMessageReceipt(fixture.message, fixture.messageDigest)
		if _, _, err := fixture.store.SaveOutgoingMessageReceiptUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, receipt); err != nil {
			t.Fatalf("first save: %v", err)
		}
		changed := receipt
		changed.PastedAt = changed.PastedAt.Add(time.Second)
		if _, _, err := fixture.store.SaveOutgoingMessageReceiptUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, changed); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("different receipt error = %v, want ErrHandoffConflict", err)
		}
	})
}

func TestSmCovMsgLoadMessageStateAtDirectErrors(t *testing.T) {
	t.Parallel()
	baseRecordedAt := time.Date(2026, 8, 25, 12, 0, 1, 0, time.UTC)

	type smCovMsgLoadedFixture struct {
		smCovMsgFixture
		directory *os.File
		entry     string
	}

	openEntry := func(t *testing.T, recordedAt time.Time) smCovMsgLoadedFixture {
		t.Helper()
		fixture := smCovMsgNewFixture(t, true, true, recordedAt)
		entry := fixture.smCovMsgEntryDir(MessageDirectionIncoming, fixture.message.MessageID)
		directory, err := os.Open(entry)
		if err != nil {
			t.Fatalf("open entry directory: %v", err)
		}
		t.Cleanup(func() { _ = directory.Close() })
		return smCovMsgLoadedFixture{smCovMsgFixture: fixture, directory: directory, entry: entry}
	}

	authority := func(fixture smCovMsgLoadedFixture) *Receipt {
		receipt := validReceipt(fixture.request, fixture.digest)
		return &receipt
	}

	t.Run("payload read error", func(t *testing.T) {
		t.Parallel()
		fixture := openEntry(t, baseRecordedAt)
		if err := os.Remove(filepath.Join(fixture.entry, messagePayloadFileName)); err != nil {
			t.Fatalf("remove payload: %v", err)
		}
		_, err := loadMessageStateAt(fixture.directory, fixture.request, MessageDirectionIncoming, fixture.message.MessageID, authority(fixture))
		if err == nil || !strings.Contains(err.Error(), "durable session message") {
			t.Fatalf("payload read error = %v", err)
		}
	})

	t.Run("undecodable payload", func(t *testing.T) {
		t.Parallel()
		fixture := openEntry(t, baseRecordedAt)
		smCovMsgWrite(t, filepath.Join(fixture.entry, messagePayloadFileName), []byte("{not json"), 0o600)
		_, err := loadMessageStateAt(fixture.directory, fixture.request, MessageDirectionIncoming, fixture.message.MessageID, authority(fixture))
		if err == nil || !strings.Contains(err.Error(), "parse session message") {
			t.Fatalf("undecodable payload error = %v", err)
		}
	})

	t.Run("payload message id mismatch", func(t *testing.T) {
		t.Parallel()
		fixture := openEntry(t, baseRecordedAt)
		_, err := loadMessageStateAt(fixture.directory, fixture.request, MessageDirectionIncoming, "message-999", authority(fixture))
		if !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("message id mismatch error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("payload lineage mismatch", func(t *testing.T) {
		t.Parallel()
		fixture := openEntry(t, baseRecordedAt)
		mutated := fixture.message
		mutated.HandoffID = "handoff-999"
		raw, err := EncodeMessage(mutated)
		if err != nil {
			t.Fatalf("EncodeMessage: %v", err)
		}
		smCovMsgWrite(t, filepath.Join(fixture.entry, messagePayloadFileName), raw, 0o600)
		if _, err := loadMessageStateAt(fixture.directory, fixture.request, MessageDirectionIncoming, fixture.message.MessageID, authority(fixture)); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("lineage mismatch error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("missing record", func(t *testing.T) {
		t.Parallel()
		fixture := openEntry(t, baseRecordedAt)
		if err := os.Remove(filepath.Join(fixture.entry, messageRecordFileName)); err != nil {
			t.Fatalf("remove record: %v", err)
		}
		_, err := loadMessageStateAt(fixture.directory, fixture.request, MessageDirectionIncoming, fixture.message.MessageID, authority(fixture))
		if err == nil || !strings.Contains(err.Error(), "session message record") {
			t.Fatalf("missing record error = %v", err)
		}
	})

	t.Run("malformed record", func(t *testing.T) {
		t.Parallel()
		fixture := openEntry(t, baseRecordedAt)
		smCovMsgWrite(t, filepath.Join(fixture.entry, messageRecordFileName), []byte("{not json"), 0o600)
		_, err := loadMessageStateAt(fixture.directory, fixture.request, MessageDirectionIncoming, fixture.message.MessageID, authority(fixture))
		if err == nil || !strings.Contains(err.Error(), "decode message record") {
			t.Fatalf("malformed record error = %v", err)
		}
	})

	t.Run("record does not match payload", func(t *testing.T) {
		t.Parallel()
		fixture := openEntry(t, baseRecordedAt)
		forged := MessageRecord{
			SchemaVersion: MessageRecordSchemaVersion, Direction: MessageDirectionIncoming,
			MessageID: fixture.message.MessageID, MessageDigest: DigestBytes([]byte("forged payload")),
			HandoffID: fixture.request.HandoffID, RecordedAt: fixture.incomingRecordedAt.UTC(),
		}
		smCovMsgWrite(t, filepath.Join(fixture.entry, messageRecordFileName), smCovMsgMarshal(t, forged), 0o600)
		if _, err := loadMessageStateAt(fixture.directory, fixture.request, MessageDirectionIncoming, fixture.message.MessageID, authority(fixture)); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("record mismatch error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("malformed paste intent", func(t *testing.T) {
		t.Parallel()
		fixture := openEntry(t, baseRecordedAt)
		smCovMsgWrite(t, filepath.Join(fixture.entry, messageIntentFileName), []byte("{not json"), 0o600)
		_, err := loadMessageStateAt(fixture.directory, fixture.request, MessageDirectionIncoming, fixture.message.MessageID, authority(fixture))
		if err == nil || !strings.Contains(err.Error(), "decode message paste intent") {
			t.Fatalf("malformed intent error = %v", err)
		}
	})

	t.Run("paste intent does not match message", func(t *testing.T) {
		t.Parallel()
		fixture := openEntry(t, baseRecordedAt)
		intent := fixture.smCovMsgDefaultIntent()
		intent.MessageDigest = DigestBytes([]byte("some other payload"))
		smCovMsgWrite(t, filepath.Join(fixture.entry, messageIntentFileName), smCovMsgMarshal(t, intent), 0o600)
		if _, err := loadMessageStateAt(fixture.directory, fixture.request, MessageDirectionIncoming, fixture.message.MessageID, authority(fixture)); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("intent mismatch error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("unreadable paste intent", func(t *testing.T) {
		t.Parallel()
		fixture := openEntry(t, baseRecordedAt)
		path := filepath.Join(fixture.entry, messageIntentFileName)
		smCovMsgWrite(t, path, smCovMsgMarshal(t, fixture.smCovMsgDefaultIntent()), 0o600)
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatalf("chmod intent: %v", err)
		}
		_, err := loadMessageStateAt(fixture.directory, fixture.request, MessageDirectionIncoming, fixture.message.MessageID, authority(fixture))
		if err == nil || !strings.Contains(err.Error(), "message paste intent") {
			t.Fatalf("unreadable intent error = %v", err)
		}
	})

	t.Run("malformed receipt", func(t *testing.T) {
		t.Parallel()
		fixture := openEntry(t, baseRecordedAt)
		smCovMsgWrite(t, filepath.Join(fixture.entry, messageReceiptFileName), []byte("{not json"), 0o600)
		_, err := loadMessageStateAt(fixture.directory, fixture.request, MessageDirectionIncoming, fixture.message.MessageID, authority(fixture))
		if err == nil || !strings.Contains(err.Error(), "parse session message receipt") {
			t.Fatalf("malformed receipt error = %v", err)
		}
	})

	t.Run("receipt without handoff authority", func(t *testing.T) {
		t.Parallel()
		fixture := openEntry(t, baseRecordedAt)
		receiptRaw, err := EncodeMessageReceipt(validMessageReceipt(fixture.message, fixture.messageDigest))
		if err != nil {
			t.Fatalf("EncodeMessageReceipt: %v", err)
		}
		smCovMsgWrite(t, filepath.Join(fixture.entry, messageReceiptFileName), receiptRaw, 0o600)
		_, err = loadMessageStateAt(fixture.directory, fixture.request, MessageDirectionIncoming, fixture.message.MessageID, nil)
		if err == nil || !strings.Contains(err.Error(), "no completed handoff receipt authority") {
			t.Fatalf("nil handoff authority error = %v", err)
		}
	})

	t.Run("incoming receipt does not match record and intent", func(t *testing.T) {
		t.Parallel()
		fixture := openEntry(t, baseRecordedAt)
		intent := fixture.smCovMsgDefaultIntent()
		smCovMsgWrite(t, filepath.Join(fixture.entry, messageIntentFileName), smCovMsgMarshal(t, intent), 0o600)
		receipt := validMessageReceipt(fixture.message, fixture.messageDigest)
		receipt.RecordedAt = baseRecordedAt.Add(time.Minute)
		receipt.PastedAt = receipt.RecordedAt.Add(time.Second)
		receiptRaw, err := EncodeMessageReceipt(receipt)
		if err != nil {
			t.Fatalf("EncodeMessageReceipt: %v", err)
		}
		smCovMsgWrite(t, filepath.Join(fixture.entry, messageReceiptFileName), receiptRaw, 0o600)
		if _, err := loadMessageStateAt(fixture.directory, fixture.request, MessageDirectionIncoming, fixture.message.MessageID, authority(fixture)); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("incoming receipt mismatch error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("receipt does not match payload", func(t *testing.T) {
		t.Parallel()
		fixture := openEntry(t, baseRecordedAt)
		receipt := validMessageReceipt(fixture.message, fixture.messageDigest)
		receipt.MessageDigest = DigestBytes([]byte("forged payload"))
		receiptRaw, err := EncodeMessageReceipt(receipt)
		if err != nil {
			t.Fatalf("EncodeMessageReceipt: %v", err)
		}
		smCovMsgWrite(t, filepath.Join(fixture.entry, messageReceiptFileName), receiptRaw, 0o600)
		if _, err := loadMessageStateAt(fixture.directory, fixture.request, MessageDirectionIncoming, fixture.message.MessageID, authority(fixture)); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("receipt mismatch error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("unreadable receipt", func(t *testing.T) {
		t.Parallel()
		fixture := openEntry(t, baseRecordedAt)
		path := filepath.Join(fixture.entry, messageReceiptFileName)
		receiptRaw, err := EncodeMessageReceipt(validMessageReceipt(fixture.message, fixture.messageDigest))
		if err != nil {
			t.Fatalf("EncodeMessageReceipt: %v", err)
		}
		smCovMsgWrite(t, path, receiptRaw, 0o600)
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatalf("chmod receipt: %v", err)
		}
		_, err = loadMessageStateAt(fixture.directory, fixture.request, MessageDirectionIncoming, fixture.message.MessageID, authority(fixture))
		if err == nil || !strings.Contains(err.Error(), "message receipt") {
			t.Fatalf("unreadable receipt error = %v", err)
		}
	})

	t.Run("loads incoming message with intent and receipt", func(t *testing.T) {
		t.Parallel()
		fixture := openEntry(t, baseRecordedAt)
		intent := fixture.smCovMsgDefaultIntent()
		smCovMsgWrite(t, filepath.Join(fixture.entry, messageIntentFileName), smCovMsgMarshal(t, intent), 0o600)
		receipt := validMessageReceipt(fixture.message, fixture.messageDigest)
		receipt.RecordedAt = baseRecordedAt
		receipt.PastedAt = baseRecordedAt.Add(time.Second)
		receiptRaw, err := EncodeMessageReceipt(receipt)
		if err != nil {
			t.Fatalf("EncodeMessageReceipt: %v", err)
		}
		smCovMsgWrite(t, filepath.Join(fixture.entry, messageReceiptFileName), receiptRaw, 0o600)
		state, err := loadMessageStateAt(fixture.directory, fixture.request, MessageDirectionIncoming, fixture.message.MessageID, authority(fixture))
		if err != nil {
			t.Fatalf("loadMessageStateAt: %v", err)
		}
		if state.Intent == nil || *state.Intent != intent || state.Receipt == nil || *state.Receipt != receipt || state.Message != fixture.message {
			t.Fatalf("loaded state = %#v", state)
		}
	})
}

func TestSmCovMsgDecodeMessageRecordErrors(t *testing.T) {
	t.Parallel()
	base := MessageRecord{
		SchemaVersion: MessageRecordSchemaVersion,
		Direction:     MessageDirectionOutgoing,
		MessageID:     "message-123",
		MessageDigest: DigestBytes([]byte("exact payload")),
		HandoffID:     "handoff-123",
		RecordedAt:    time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC),
	}
	if decoded, err := decodeMessageRecord(smCovMsgMarshal(t, base)); err != nil || decoded != base {
		t.Fatalf("valid record decode = %#v, err=%v", decoded, err)
	}

	tests := []struct {
		name   string
		mutate func(*MessageRecord)
		want   string
	}{
		{"older schema", func(r *MessageRecord) { r.SchemaVersion = 0 }, "durable message record is invalid"},
		{"newer schema", func(r *MessageRecord) { r.SchemaVersion = MessageRecordSchemaVersion + 1 }, "durable message record is invalid"},
		{"unsupported direction", func(r *MessageRecord) { r.Direction = MessageDirection("sideways") }, "durable message record is invalid"},
		{"empty direction", func(r *MessageRecord) { r.Direction = "" }, "durable message record is invalid"},
		{"zero recorded at", func(r *MessageRecord) { r.RecordedAt = time.Time{} }, "durable message record is invalid"},
		{"invalid message id", func(r *MessageRecord) { r.MessageID = "" }, "message_id"},
		{"invalid handoff id", func(r *MessageRecord) { r.HandoffID = "bad id" }, "handoff_id"},
		{"invalid digest", func(r *MessageRecord) { r.MessageDigest = "sha256:zz" }, "digest"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			record := base
			test.mutate(&record)
			decoded, err := decodeMessageRecord(smCovMsgMarshal(t, record))
			if err == nil {
				t.Fatalf("decodeMessageRecord accepted %#v as %#v", record, decoded)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("decodeMessageRecord error = %v, want substring %q", err, test.want)
			}
		})
	}

	t.Run("malformed JSON", func(t *testing.T) {
		t.Parallel()
		if _, err := decodeMessageRecord([]byte("{not json")); err == nil || !strings.Contains(err.Error(), "decode message record") {
			t.Fatalf("malformed record error = %v", err)
		}
	})
	t.Run("unknown field", func(t *testing.T) {
		t.Parallel()
		raw := append([]byte(`{"schema_version":1,"direction":"outgoing","message_id":"message-123","message_digest":"`+string(base.MessageDigest)+`","handoff_id":"handoff-123","recorded_at":"2026-08-25T12:00:00Z",`), []byte(`"extra":true}`)...)
		if _, err := decodeMessageRecord(raw); err == nil {
			t.Fatal("decodeMessageRecord accepted an unknown field")
		}
	})
}

func TestSmCovMsgDecodeMessagePasteIntentErrors(t *testing.T) {
	t.Parallel()
	base := MessagePasteIntent{
		SchemaVersion:        MessagePasteIntentSchemaVersion,
		MessageID:            "message-123",
		MessageDigest:        DigestBytes([]byte("exact payload")),
		HandoffID:            "handoff-123",
		RecipientWBSessionID: "wbs-successor",
		TmuxName:             "wb-session-wbs-successor",
		PaneID:               "%7",
		PID:                  1234,
		IntendedAt:           time.Date(2026, 8, 25, 12, 0, 1, 0, time.UTC),
	}
	if decoded, err := decodeMessagePasteIntent(smCovMsgMarshal(t, base)); err != nil || decoded != base {
		t.Fatalf("valid intent decode = %#v, err=%v", decoded, err)
	}

	tests := []struct {
		name   string
		mutate func(*MessagePasteIntent)
		want   string
	}{
		{"older schema", func(i *MessagePasteIntent) { i.SchemaVersion = 0 }, "message paste intent is invalid"},
		{"newer schema", func(i *MessagePasteIntent) { i.SchemaVersion = MessagePasteIntentSchemaVersion + 1 }, "message paste intent is invalid"},
		{"zero intended at", func(i *MessagePasteIntent) { i.IntendedAt = time.Time{} }, "message paste intent is invalid"},
		{"zero pid", func(i *MessagePasteIntent) { i.PID = 0 }, "message paste intent is invalid"},
		{"negative pid", func(i *MessagePasteIntent) { i.PID = -3 }, "message paste intent is invalid"},
		{"bad pane id", func(i *MessagePasteIntent) { i.PaneID = "7" }, "message paste intent is invalid"},
		{"empty pane id", func(i *MessagePasteIntent) { i.PaneID = "" }, "message paste intent is invalid"},
		{"invalid message id", func(i *MessagePasteIntent) { i.MessageID = "" }, "message_id"},
		{"invalid handoff id", func(i *MessagePasteIntent) { i.HandoffID = "bad id" }, "handoff_id"},
		{"invalid recipient", func(i *MessagePasteIntent) { i.RecipientWBSessionID = "" }, "recipient_wb_session_id"},
		{"invalid tmux name", func(i *MessagePasteIntent) { i.TmuxName = "bad tmux" }, "tmux_name"},
		{"invalid digest", func(i *MessagePasteIntent) { i.MessageDigest = "" }, "digest"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			intent := base
			test.mutate(&intent)
			decoded, err := decodeMessagePasteIntent(smCovMsgMarshal(t, intent))
			if err == nil {
				t.Fatalf("decodeMessagePasteIntent accepted %#v as %#v", intent, decoded)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("decodeMessagePasteIntent error = %v, want substring %q", err, test.want)
			}
		})
	}

	t.Run("malformed JSON", func(t *testing.T) {
		t.Parallel()
		if _, err := decodeMessagePasteIntent([]byte("{not json")); err == nil || !strings.Contains(err.Error(), "decode message paste intent") {
			t.Fatalf("malformed intent error = %v", err)
		}
	})
}

func TestSmCovMsgValidatePasteIntentBranches(t *testing.T) {
	t.Parallel()
	request := validRequest()
	message := validMessage(request)
	raw, err := EncodeMessage(message)
	if err != nil {
		t.Fatalf("EncodeMessage: %v", err)
	}
	digest := DigestBytes(raw)
	state := MessageState{Message: message, Digest: digest}
	intent := MessagePasteIntent{
		SchemaVersion: MessagePasteIntentSchemaVersion, MessageID: message.MessageID, MessageDigest: digest,
		HandoffID: request.HandoffID, RecipientWBSessionID: message.RecipientWBSessionID,
		TmuxName: "wb-session-wbs-successor", PaneID: "%7", PID: 1234,
		IntendedAt: time.Date(2026, 8, 25, 12, 0, 1, 0, time.UTC),
	}
	if err := validatePasteIntent(intent, state); err != nil {
		t.Fatalf("valid intent rejected: %v", err)
	}

	invalid := []struct {
		name   string
		mutate func(*MessagePasteIntent)
	}{
		{"older schema", func(i *MessagePasteIntent) { i.SchemaVersion = 0 }},
		{"newer schema", func(i *MessagePasteIntent) { i.SchemaVersion = MessagePasteIntentSchemaVersion + 1 }},
		{"zero intended at", func(i *MessagePasteIntent) { i.IntendedAt = time.Time{} }},
		{"zero pid", func(i *MessagePasteIntent) { i.PID = 0 }},
		{"bad pane id", func(i *MessagePasteIntent) { i.PaneID = "pane-7" }},
	}
	for _, test := range invalid {
		t.Run("invalid field "+test.name, func(t *testing.T) {
			t.Parallel()
			mutated := intent
			test.mutate(&mutated)
			if err := validatePasteIntent(mutated, state); err == nil || !strings.Contains(err.Error(), "message paste intent is invalid") {
				t.Fatalf("validatePasteIntent error = %v", err)
			}
		})
	}

	mismatched := []struct {
		name   string
		mutate func(*MessagePasteIntent)
	}{
		{"message id", func(i *MessagePasteIntent) { i.MessageID = "message-999" }},
		{"message digest", func(i *MessagePasteIntent) { i.MessageDigest = DigestBytes([]byte("other payload")) }},
		{"handoff id", func(i *MessagePasteIntent) { i.HandoffID = "handoff-999" }},
		{"recipient", func(i *MessagePasteIntent) { i.RecipientWBSessionID = "wbs-other-successor" }},
	}
	for _, test := range mismatched {
		t.Run("mismatch "+test.name, func(t *testing.T) {
			t.Parallel()
			mutated := intent
			test.mutate(&mutated)
			if err := validatePasteIntent(mutated, state); !errors.Is(err, ErrHandoffConflict) {
				t.Fatalf("validatePasteIntent error = %v, want ErrHandoffConflict", err)
			}
		})
	}
}

func TestSmCovMsgOpenMessageEntryAtErrors(t *testing.T) {
	t.Parallel()
	fixture := smCovMsgNewFixture(t, true, false, time.Time{})
	handoff, err := os.Open(fixture.smCovMsgHandoffPath())
	if err != nil {
		t.Fatalf("open handoff directory: %v", err)
	}
	t.Cleanup(func() { _ = handoff.Close() })

	t.Run("nil handoff authority", func(t *testing.T) {
		t.Parallel()
		directory, err := openMessageEntryAt(nil, MessageDirectionOutgoing, fixture.message.MessageID, false)
		if err == nil || !strings.Contains(err.Error(), "handoff authority is required") {
			t.Fatalf("nil handoff error = %v (directory=%v)", err, directory)
		}
	})

	t.Run("invalid message id", func(t *testing.T) {
		t.Parallel()
		directory, err := openMessageEntryAt(handoff, MessageDirectionOutgoing, "", false)
		if err == nil || !strings.Contains(err.Error(), "message_id") {
			t.Fatalf("invalid message id error = %v (directory=%v)", err, directory)
		}
	})

	t.Run("unsupported direction", func(t *testing.T) {
		t.Parallel()
		directory, err := openMessageEntryAt(handoff, MessageDirection("sideways"), fixture.message.MessageID, false)
		if err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("unsupported direction error = %v (directory=%v)", err, directory)
		}
	})

	t.Run("missing entry without create", func(t *testing.T) {
		t.Parallel()
		directory, err := openMessageEntryAt(handoff, MessageDirectionOutgoing, fixture.message.MessageID, false)
		if err == nil || !strings.Contains(err.Error(), "open message") {
			t.Fatalf("missing entry error = %v (directory=%v)", err, directory)
		}
	})

	t.Run("creates then reopens the entry", func(t *testing.T) {
		t.Parallel()
		created, err := openMessageEntryAt(handoff, MessageDirectionOutgoing, fixture.message.MessageID, true)
		if err != nil {
			t.Fatalf("create entry: %v", err)
		}
		info, err := created.Stat()
		if err != nil {
			t.Fatalf("stat created entry: %v", err)
		}
		_ = created.Close()
		if !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("created entry mode = %v, want directory mode 0700", info.Mode())
		}
		reopened, err := openMessageEntryAt(handoff, MessageDirectionOutgoing, fixture.message.MessageID, false)
		if err != nil {
			t.Fatalf("reopen entry: %v", err)
		}
		defer func() { _ = reopened.Close() }()
		reopenedInfo, err := reopened.Stat()
		if err != nil {
			t.Fatalf("stat reopened entry: %v", err)
		}
		if !os.SameFile(info, reopenedInfo) {
			t.Fatalf("reopened entry %v is not the created inode %v", reopenedInfo, info)
		}
	})
}

func TestSmCovMsgOpenSecureDirectoryAtBranches(t *testing.T) {
	t.Parallel()
	parentDir := t.TempDir()
	parent, err := os.Open(parentDir)
	if err != nil {
		t.Fatalf("open parent: %v", err)
	}
	t.Cleanup(func() { _ = parent.Close() })

	t.Run("missing directory without create", func(t *testing.T) {
		t.Parallel()
		directory, err := openSecureDirectoryAt(parent, "absent", false, "message outgoing")
		if err == nil || !strings.Contains(err.Error(), "open message outgoing directory") {
			t.Fatalf("missing directory error = %v (directory=%v)", err, directory)
		}
	})

	t.Run("directory is not mode 0700", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(parentDir, "loose")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("Mkdir: %v", err)
		}
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatalf("Chmod: %v", err)
		}
		directory, err := openSecureDirectoryAt(parent, "loose", false, "message entry")
		if err == nil || !strings.Contains(err.Error(), "is not mode 0700") {
			t.Fatalf("wrong mode error = %v (directory=%v)", err, directory)
		}
	})

	t.Run("regular file without create", func(t *testing.T) {
		t.Parallel()
		if err := os.WriteFile(filepath.Join(parentDir, "plain-file"), []byte("payload"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		directory, err := openSecureDirectoryAt(parent, "plain-file", false, "message entry")
		if err == nil || !strings.Contains(err.Error(), "open message entry directory") {
			t.Fatalf("regular file error = %v (directory=%v)", err, directory)
		}
	})

	t.Run("regular file with create", func(t *testing.T) {
		t.Parallel()
		if err := os.WriteFile(filepath.Join(parentDir, "create-file"), []byte("payload"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		directory, err := openSecureDirectoryAt(parent, "create-file", true, "message entry")
		if err == nil || !strings.Contains(err.Error(), "open message entry directory") {
			t.Fatalf("create over file error = %v (directory=%v)", err, directory)
		}
		if info, statErr := os.Stat(filepath.Join(parentDir, "create-file")); statErr != nil || info.IsDir() {
			t.Fatalf("existing file was replaced: info=%v err=%v", info, statErr)
		}
	})

	t.Run("create success returns the created inode", func(t *testing.T) {
		t.Parallel()
		directory, err := openSecureDirectoryAt(parent, "created", true, "message entry")
		if err != nil {
			t.Fatalf("create directory: %v", err)
		}
		defer func() { _ = directory.Close() }()
		info, err := directory.Stat()
		if err != nil {
			t.Fatalf("stat created directory: %v", err)
		}
		if !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("created directory mode = %v, want directory mode 0700", info.Mode())
		}
		onDisk, err := os.Stat(filepath.Join(parentDir, "created"))
		if err != nil {
			t.Fatalf("stat on-disk directory: %v", err)
		}
		if !os.SameFile(info, onDisk) {
			t.Fatalf("returned directory %v is not the created inode %v", info, onDisk)
		}
	})

	t.Run("create is idempotent", func(t *testing.T) {
		t.Parallel()
		first, err := openSecureDirectoryAt(parent, "idempotent", true, "message entry")
		if err != nil {
			t.Fatalf("first create: %v", err)
		}
		firstInfo, err := first.Stat()
		if err != nil {
			t.Fatalf("stat first: %v", err)
		}
		_ = first.Close()
		second, err := openSecureDirectoryAt(parent, "idempotent", true, "message entry")
		if err != nil {
			t.Fatalf("second create: %v", err)
		}
		defer func() { _ = second.Close() }()
		secondInfo, err := second.Stat()
		if err != nil {
			t.Fatalf("stat second: %v", err)
		}
		if !os.SameFile(firstInfo, secondInfo) {
			t.Fatalf("re-create returned %v, want existing inode %v", secondInfo, firstInfo)
		}
	})
}
