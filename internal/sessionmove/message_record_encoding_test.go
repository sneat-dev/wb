package sessionmove

import (
	"context"
	"strings"
	"testing"
	"time"
)

// wantYearOutOfRangeSubstring is the substring encoding/json's
// time.Time.MarshalJSON puts in its error when a time.Time's year falls
// outside [0, 9999] -- the shared assertion every encoding-failure test in
// this file checks for, instead of a bare err != nil.
const wantYearOutOfRangeSubstring = "year outside of range"

// TestAdmitIncomingMessageReportsRecordEncodingFailure drives
// admitMessageUnderLock's marshalJSON(record) error branch (message.go):
// encoding/json's time.Time.MarshalJSON refuses a year outside [0, 9999],
// so a caller-supplied recordedAt past that bound makes the durable
// MessageRecord fail to encode -- a real, reachable failure with no new
// production seam, since RecordedAt is a caller-controlled parameter all
// the way through to the record encoded just before publication.
func TestAdmitIncomingMessageReportsRecordEncodingFailure(t *testing.T) {
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
	unencodableRecordedAt := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)

	if _, err := store.AdmitIncomingMessageUnderLock(lock, request.HandoffID, digest, messageRaw, unencodableRecordedAt); err == nil ||
		!strings.Contains(err.Error(), wantYearOutOfRangeSubstring) {
		t.Fatalf("AdmitIncomingMessageUnderLock(recorded_at year 10000) = %v, want a %q error", err, wantYearOutOfRangeSubstring)
	}
}

// TestSaveIncomingPasteIntentReportsEncodingFailure drives the
// marshalJSON(intent) error branch of SaveIncomingPasteIntentUnderLock
// (message.go): validatePasteIntent only requires a non-zero IntendedAt, so
// a caller-supplied IntendedAt past encoding/json's year bound reaches
// publication and fails to encode.
func TestSaveIncomingPasteIntentReportsEncodingFailure(t *testing.T) {
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
	if _, err := store.AdmitIncomingMessageUnderLock(lock, request.HandoffID, digest, messageRaw, recordedAt); err != nil {
		t.Fatal(err)
	}

	intent := MessagePasteIntent{
		SchemaVersion: MessagePasteIntentSchemaVersion,
		MessageID:     message.MessageID, MessageDigest: messageDigest, HandoffID: request.HandoffID,
		RecipientWBSessionID: message.RecipientWBSessionID, TmuxName: "wb-session-wbs-successor",
		PaneID: "%7", PID: 1234, IntendedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if _, _, err := store.SaveIncomingPasteIntentUnderLock(lock, request.HandoffID, digest, intent); err == nil ||
		!strings.Contains(err.Error(), wantYearOutOfRangeSubstring) {
		t.Fatalf("SaveIncomingPasteIntentUnderLock(intended_at year 10000) = %v, want a %q error", err, wantYearOutOfRangeSubstring)
	}
}

// TestSaveOutgoingMessageReceiptReportsEncodingFailure drives the
// EncodeMessageReceipt error branch of saveMessageReceiptUnderLock
// (message.go): for the outgoing direction the receipt's timestamps are not
// cross-checked against a durable paste intent, so a caller-supplied
// PastedAt past encoding/json's year bound reaches publication and fails to
// encode.
func TestSaveOutgoingMessageReceiptReportsEncodingFailure(t *testing.T) {
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
	if _, err := store.AdmitOutgoingMessageUnderLock(lock, request.HandoffID, digest, messageRaw, message.SentAt); err != nil {
		t.Fatal(err)
	}

	receipt := validMessageReceipt(message, messageDigest)
	receipt.PastedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, _, err := store.SaveOutgoingMessageReceiptUnderLock(lock, request.HandoffID, digest, receipt); err == nil ||
		!strings.Contains(err.Error(), wantYearOutOfRangeSubstring) {
		t.Fatalf("SaveOutgoingMessageReceiptUnderLock(pasted_at year 10000) = %v, want a %q error", err, wantYearOutOfRangeSubstring)
	}
}
