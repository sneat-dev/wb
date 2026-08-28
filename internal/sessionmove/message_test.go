package sessionmove

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validMessage(request Request) Message {
	return Message{
		SchemaVersion:        MessageSchemaVersion,
		MessageID:            "message-123",
		HandoffID:            request.HandoffID,
		SenderWBSessionID:    request.PredecessorWBSessionID,
		RecipientWBSessionID: request.SuccessorWBSessionID,
		ReplyToWBSessionID:   request.PredecessorWBSessionID,
		Kind:                 MessageKindText,
		Body:                 "Please inspect the latest focused test failure.",
		SentAt:               time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC),
	}
}

func validMessageReceipt(message Message, digest Digest) MessageReceipt {
	return MessageReceipt{
		SchemaVersion:        MessageReceiptSchemaVersion,
		MessageID:            message.MessageID,
		MessageDigest:        digest,
		HandoffID:            message.HandoffID,
		SenderWBSessionID:    message.SenderWBSessionID,
		RecipientWBSessionID: message.RecipientWBSessionID,
		ReplyToWBSessionID:   message.ReplyToWBSessionID,
		Kind:                 message.Kind,
		TmuxName:             "wb-session-wbs-successor",
		PaneID:               "%7",
		PID:                  1234,
		RecordedAt:           time.Date(2026, 8, 25, 12, 0, 1, 0, time.UTC),
		PastedAt:             time.Date(2026, 8, 25, 12, 0, 2, 0, time.UTC),
	}
}

func TestMessageProtocolBindsRequiredLineageAndStandardRequestHandoff(t *testing.T) {
	request := validRequest()
	message := validMessage(request)
	if _, err := EncodeMessage(message); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*Message)
		want   string
	}{
		{"missing handoff", func(value *Message) { value.HandoffID = "" }, "handoff_id"},
		{"missing sender", func(value *Message) { value.SenderWBSessionID = "" }, "sender_wb_session_id"},
		{"missing reply", func(value *Message) { value.ReplyToWBSessionID = "" }, "reply_to_wb_session_id"},
		{"oversized body", func(value *Message) { value.Body = strings.Repeat("x", MaxMessageBodyBytes+1) }, "body"},
		{"invalid UTF-8 body", func(value *Message) { value.Body = string([]byte{0xff}) }, "UTF-8"},
		{"request body", func(value *Message) {
			value.Kind = MessageKindRequestHandoff
			value.Body = "free-form request"
		}, "request_handoff"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := message
			test.mutate(&value)
			if _, err := EncodeMessage(value); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("EncodeMessage error = %v, want %q", err, test.want)
			}
		})
	}

	handoffBack := message
	handoffBack.Kind = MessageKindRequestHandoff
	handoffBack.Body = ""
	raw, err := EncodeMessage(handoffBack)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeMessage(raw)
	if err != nil || decoded != handoffBack {
		t.Fatalf("request_handoff round trip = %#v, err=%v", decoded, err)
	}
}

func TestMessageReceiptStrictlyBindsExactMessageAndPasteAcknowledgement(t *testing.T) {
	message := validMessage(validRequest())
	raw, err := EncodeMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	receipt := validMessageReceipt(message, digest)
	receiptRaw, err := EncodeMessageReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeMessageReceipt(receiptRaw)
	if err != nil || decoded != receipt {
		t.Fatalf("receipt round trip = %#v, err=%v", decoded, err)
	}
	if err := ValidateMessageReceipt(receipt, message, digest, receipt.TmuxName, receipt.PID); err != nil {
		t.Fatal(err)
	}

	mutated := receipt
	mutated.MessageDigest = DigestBytes([]byte("different message"))
	if err := ValidateMessageReceipt(mutated, message, digest, receipt.TmuxName, receipt.PID); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("digest mismatch error = %v, want ErrHandoffConflict", err)
	}
	mutated = receipt
	mutated.PastedAt = time.Time{}
	if _, err := EncodeMessageReceipt(mutated); err == nil || !strings.Contains(err.Error(), "pasted_at") {
		t.Fatalf("missing paste acknowledgement error = %v", err)
	}
}

func TestMessageStorePersistsExactOutboxInboxIntentAndReceipts(t *testing.T) {
	request := validRequest()
	requestRaw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(requestRaw)
	store := NewStore(t.TempDir())
	if _, err := store.Admit(requestRaw, digest); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SaveReceipt(request.HandoffID, digest, validReceipt(request, digest)); err != nil {
		t.Fatal(err)
	}
	lock, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()

	message := validMessage(request)
	messageRaw, err := EncodeMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	messageDigest := DigestBytes(messageRaw)
	recordedAt := time.Date(2026, 8, 25, 12, 0, 1, 0, time.UTC)

	outgoing, err := store.AdmitOutgoingMessageUnderLock(lock, request.HandoffID, digest, messageRaw, message.SentAt)
	if err != nil || outgoing.Replay || outgoing.Record.Direction != MessageDirectionOutgoing || outgoing.Digest != messageDigest {
		t.Fatalf("outgoing admission = %#v, err=%v", outgoing, err)
	}
	replayed, err := store.AdmitOutgoingMessageUnderLock(lock, request.HandoffID, digest, messageRaw, message.SentAt)
	if err != nil || !replayed.Replay || replayed.Record != outgoing.Record {
		t.Fatalf("outgoing replay = %#v, err=%v", replayed, err)
	}
	conflict := message
	conflict.Body = "different exact bytes"
	conflictRaw, err := EncodeMessage(conflict)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitOutgoingMessageUnderLock(lock, request.HandoffID, digest, conflictRaw, conflict.SentAt); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("outgoing conflict error = %v, want ErrHandoffConflict", err)
	}

	incoming, err := store.AdmitIncomingMessageUnderLock(lock, request.HandoffID, digest, messageRaw, recordedAt)
	if err != nil || incoming.Replay || incoming.Record.Direction != MessageDirectionIncoming {
		t.Fatalf("incoming admission = %#v, err=%v", incoming, err)
	}
	intent := MessagePasteIntent{
		SchemaVersion: MessagePasteIntentSchemaVersion,
		MessageID:     message.MessageID, MessageDigest: messageDigest, HandoffID: request.HandoffID,
		RecipientWBSessionID: message.RecipientWBSessionID, TmuxName: "wb-session-wbs-successor",
		PaneID: "%7", PID: 1234, IntendedAt: recordedAt.Add(time.Second),
	}
	storedIntent, replay, err := store.SaveIncomingPasteIntentUnderLock(lock, request.HandoffID, digest, intent)
	if err != nil || replay || storedIntent != intent {
		t.Fatalf("paste intent = %#v replay=%t err=%v", storedIntent, replay, err)
	}
	receipt := validMessageReceipt(message, messageDigest)
	storedReceipt, replay, err := store.SaveIncomingMessageReceiptUnderLock(lock, request.HandoffID, digest, receipt)
	if err != nil || replay || storedReceipt != receipt {
		t.Fatalf("incoming receipt = %#v replay=%t err=%v", storedReceipt, replay, err)
	}
	loaded, err := store.LoadIncomingMessageUnderLock(lock, request.HandoffID, digest, message.MessageID)
	if err != nil || loaded.Intent == nil || *loaded.Intent != intent || loaded.Receipt == nil || *loaded.Receipt != receipt {
		t.Fatalf("loaded incoming = %#v, err=%v", loaded, err)
	}
	storedOutgoingReceipt, replay, err := store.SaveOutgoingMessageReceiptUnderLock(lock, request.HandoffID, digest, receipt)
	if err != nil || replay || storedOutgoingReceipt != receipt {
		t.Fatalf("outgoing receipt = %#v replay=%t err=%v", storedOutgoingReceipt, replay, err)
	}

	receiptPath := filepath.Join(store.Root, request.HandoffID, messageInboxDirName, message.MessageID, messageReceiptFileName)
	originalReceipt, err := EncodeMessageReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*MessageReceipt)
	}{
		{"payload digest", func(value *MessageReceipt) { value.MessageDigest = DigestBytes([]byte("forged payload")) }},
		{"handoff receipt", func(value *MessageReceipt) { value.TmuxName = "wb-session-other" }},
		{"inbox record", func(value *MessageReceipt) { value.RecordedAt = value.RecordedAt.Add(time.Second) }},
		{"paste intent", func(value *MessageReceipt) { value.PaneID = "%8" }},
	} {
		t.Run("load refuses forged "+test.name, func(t *testing.T) {
			forged := receipt
			test.mutate(&forged)
			raw, err := EncodeMessageReceipt(forged)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(receiptPath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.LoadIncomingMessageUnderLock(lock, request.HandoffID, digest, message.MessageID); !errors.Is(err, ErrHandoffConflict) {
				t.Fatalf("forged %s receipt load error = %v, want ErrHandoffConflict", test.name, err)
			}
			if err := os.WriteFile(receiptPath, originalReceipt, 0o600); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMessageStoreRepairsPayloadOnlyAdmissionAndRefusesConflictingRecord(t *testing.T) {
	fixture := func(t *testing.T) (Store, Request, Digest, *ExecutionLock, Message, []byte) {
		t.Helper()
		request := validRequest()
		requestRaw, err := EncodeRequest(request)
		if err != nil {
			t.Fatal(err)
		}
		digest := DigestBytes(requestRaw)
		store := NewStore(filepath.Join(t.TempDir(), "handoffs"))
		if _, err := store.Admit(requestRaw, digest); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.SaveReceipt(request.HandoffID, digest, validReceipt(request, digest)); err != nil {
			t.Fatal(err)
		}
		lock, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
		if err != nil {
			t.Fatal(err)
		}
		message := validMessage(request)
		raw, err := EncodeMessage(message)
		if err != nil {
			_ = lock.Close()
			t.Fatal(err)
		}
		entry := filepath.Join(store.Root, request.HandoffID, messageOutboxDirName, message.MessageID)
		if err := os.MkdirAll(entry, 0o700); err != nil {
			_ = lock.Close()
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(entry, messagePayloadFileName), raw, 0o600); err != nil {
			_ = lock.Close()
			t.Fatal(err)
		}
		return store, request, digest, lock, message, raw
	}

	t.Run("repairs missing record", func(t *testing.T) {
		store, request, digest, lock, message, _ := fixture(t)
		defer func() { _ = lock.Close() }()
		state, err := store.ResumeOutgoingMessageUnderLock(lock, request.HandoffID, digest, message.MessageID)
		if err != nil || !state.Replay || state.Record.MessageID != message.MessageID || state.Record.RecordedAt != message.SentAt {
			t.Fatalf("repaired state = %#v, err=%v", state, err)
		}
	})

	t.Run("refuses conflicting existing record", func(t *testing.T) {
		store, request, digest, lock, message, _ := fixture(t)
		defer func() { _ = lock.Close() }()
		entry := filepath.Join(store.Root, request.HandoffID, messageOutboxDirName, message.MessageID)
		forged := MessageRecord{
			SchemaVersion: MessageRecordSchemaVersion, Direction: MessageDirectionOutgoing,
			MessageID: message.MessageID, MessageDigest: DigestBytes([]byte("forged")), HandoffID: request.HandoffID,
			RecordedAt: message.SentAt,
		}
		encoded, err := json.MarshalIndent(forged, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded, '\n')
		if err := os.WriteFile(filepath.Join(entry, messageRecordFileName), encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ResumeOutgoingMessageUnderLock(lock, request.HandoffID, digest, message.MessageID); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("conflicting record error = %v, want ErrHandoffConflict", err)
		}
	})
}
