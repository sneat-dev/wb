package worktrees

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionmove"
)

func TestExternalSourceMessageEventBindsPastedReceiptWithoutClaimingProcessing(t *testing.T) {
	fixture := newExternalSourceFixture(t)
	lock := fixture.lock(t)
	handoffReceipt := fixture.authorizeSeal(t, lock)
	if _, err := SealExternalSessionWorkLog(ExternalSourceSealOptions{
		Store: fixture.store, ExecutionLock: lock, ProjectsRoot: fixture.base.projectsRoot,
		Request: fixture.base.request, RequestDigest: fixture.digest, Receipt: handoffReceipt, SourceSession: fixture.source,
	}); err != nil {
		t.Fatal(err)
	}
	message := messageForWorkLog(fixture.base.request, sessionmove.MessageKindText, "secret message body must not enter the Work Log")
	raw, err := sessionmove.EncodeMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	digest := sessionmove.DigestBytes(raw)
	record := sessionmove.MessageRecord{
		SchemaVersion: sessionmove.MessageRecordSchemaVersion, Direction: sessionmove.MessageDirectionOutgoing,
		MessageID: message.MessageID, MessageDigest: digest, HandoffID: message.HandoffID, RecordedAt: message.SentAt,
	}
	receipt := messageReceiptForWorkLog(message, digest, handoffReceipt)
	options := ExternalSourceMessageOptions{
		ProjectsRoot: fixture.base.projectsRoot, Request: fixture.base.request, RequestDigest: fixture.digest,
		Receipt: handoffReceipt, SourceSession: fixture.source, Message: message, Record: record, MessageReceipt: receipt,
	}
	first, err := RecordExternalSourceMessageSent(options)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := RecordExternalSourceMessageSent(options)
	if err != nil || !sameLocalEvent(first, replayed) {
		t.Fatalf("source message replay=%#v err=%v", replayed, err)
	}
	encoded, _ := json.Marshal(first)
	if bytes.Contains(encoded, []byte(message.Body)) || !strings.Contains(first.Message, "processing is not claimed") ||
		first.Extra["acknowledgement_scope"] != "durable_record_and_tmux_paste_only" {
		t.Fatalf("source Work Log event leaks or overclaims: %s", encoded)
	}
}

func TestExternalTargetMessageEventBindsInboxWithoutClaimingProcessing(t *testing.T) {
	fixture := newExternalTargetFixture(t)
	prepared, err := PrepareExternalSessionWorkLog(context.Background(), fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	handoffReceipt := fixture.receipt(t, prepared)
	if _, err := RecordExternalTargetCompleted(ExternalTargetCompletionOptions{
		ProjectsRoot: fixture.base.projectsRoot, Request: fixture.base.request, RequestDigest: fixture.digest,
		Receipt: handoffReceipt, WorktreeDir: fixture.worktree,
	}); err != nil {
		t.Fatal(err)
	}
	message := messageForWorkLog(fixture.base.request, sessionmove.MessageKindRequestHandoff, "")
	raw, err := sessionmove.EncodeMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	digest := sessionmove.DigestBytes(raw)
	record := sessionmove.MessageRecord{
		SchemaVersion: sessionmove.MessageRecordSchemaVersion, Direction: sessionmove.MessageDirectionIncoming,
		MessageID: message.MessageID, MessageDigest: digest, HandoffID: message.HandoffID,
		RecordedAt: message.SentAt.Add(time.Second),
	}
	first, err := RecordExternalTargetMessageReceived(ExternalTargetMessageOptions{
		ProjectsRoot: fixture.base.projectsRoot, Request: fixture.base.request, RequestDigest: fixture.digest,
		Receipt: handoffReceipt, Message: message, Record: record,
	})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := RecordExternalTargetMessageReceived(ExternalTargetMessageOptions{
		ProjectsRoot: fixture.base.projectsRoot, Request: fixture.base.request, RequestDigest: fixture.digest,
		Receipt: handoffReceipt, Message: message, Record: record,
	})
	if err != nil || !sameLocalEvent(first, replayed) {
		t.Fatalf("target message replay=%#v err=%v", replayed, err)
	}
	encoded, _ := json.Marshal(first)
	if strings.Contains(string(encoded), "processed") || first.Extra["reply_to_wb_session_id"] != fixture.base.request.PredecessorWBSessionID {
		t.Fatalf("target Work Log event overclaims or loses lineage: %s", encoded)
	}
}

func messageForWorkLog(request sessionmove.Request, kind sessionmove.MessageKind, body string) sessionmove.Message {
	return sessionmove.Message{
		SchemaVersion: sessionmove.MessageSchemaVersion, MessageID: "message-worklog", HandoffID: request.HandoffID,
		SenderWBSessionID: request.PredecessorWBSessionID, RecipientWBSessionID: request.SuccessorWBSessionID,
		ReplyToWBSessionID: request.PredecessorWBSessionID, Kind: kind, Body: body,
		SentAt: request.CreatedAt.Add(time.Second),
	}
}

func messageReceiptForWorkLog(message sessionmove.Message, digest sessionmove.Digest, handoff sessionmove.Receipt) sessionmove.MessageReceipt {
	return sessionmove.MessageReceipt{
		SchemaVersion: sessionmove.MessageReceiptSchemaVersion, MessageID: message.MessageID, MessageDigest: digest,
		HandoffID: message.HandoffID, SenderWBSessionID: message.SenderWBSessionID,
		RecipientWBSessionID: message.RecipientWBSessionID, ReplyToWBSessionID: message.ReplyToWBSessionID, Kind: message.Kind,
		TmuxName: handoff.TmuxName, PaneID: "%7", PID: handoff.PID,
		RecordedAt: message.SentAt.Add(time.Second), PastedAt: message.SentAt.Add(2 * time.Second),
	}
}
