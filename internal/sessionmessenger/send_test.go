package sessionmessenger

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessioncourier"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

type fakeMessageDeliverer struct {
	raw     []byte
	receipt func(sessionmove.Message, []byte) sessionmove.MessageReceipt
	err     error
}

func (deliverer *fakeMessageDeliverer) DeliverMessage(_ context.Context, raw []byte) (sessionmove.MessageReceipt, error) {
	deliverer.raw = append([]byte(nil), raw...)
	if deliverer.err != nil {
		return sessionmove.MessageReceipt{}, deliverer.err
	}
	message, err := sessionmove.DecodeMessage(raw)
	if err != nil {
		return sessionmove.MessageReceipt{}, err
	}
	return deliverer.receipt(message, raw), nil
}

func TestSendRejectsNilContextBeforeDurableLookup(t *testing.T) {
	_, err := Send(nilContext(), Options{TargetWBSessionID: "wbs-successor"})
	if err == nil || !strings.Contains(err.Error(), "context is required") {
		t.Fatalf("Send(nil) error = %v, want context requirement", err)
	}
}

func nilContext() context.Context { return nil }

func TestSendPersistsBeforeCourierAndBindsTextAndRequestHandoffLineage(t *testing.T) {
	for _, test := range []struct {
		name string
		kind sessionmove.MessageKind
		body string
	}{
		{"text", sessionmove.MessageKindText, "Please continue with the focused race test."},
		{"request handoff", sessionmove.MessageKindRequestHandoff, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSendFixture(t, sessionmove.CourierSSH)
			deliverer := &fakeMessageDeliverer{receipt: fixture.messageReceipt}
			var workLogCalls int
			var persistedBeforeCourier bool
			options := fixture.options(test.kind, test.body)
			options.Hooks.BeforeCourier = func(state sessionmove.MessageState) error {
				persistedBeforeCourier = state.Message.MessageID == options.MessageID && state.Receipt == nil && len(state.Raw) > 0
				return nil
			}
			options.NewDeliverer = func(address sessionmove.SuccessorAddress, synchestra sessioncourier.MessageSynchestraOptions) (sessioncourier.MessageDeliverer, error) {
				if !reflect.DeepEqual(address, fixture.address) || synchestra.Dispatch != nil || synchestra.SaveDispatch != nil {
					t.Fatalf("deliverer inputs = %#v %#v", address, synchestra)
				}
				return deliverer, nil
			}
			options.RecordSent = func(record WorkLogRecord) error {
				workLogCalls++
				if record.MessageReceipt.MessageID != record.Message.MessageID || record.MessageReceipt.PastedAt.IsZero() {
					t.Fatalf("Work Log record = %#v", record)
				}
				return nil
			}
			result, err := Send(context.Background(), options)
			if err != nil {
				t.Fatal(err)
			}
			message, err := sessionmove.DecodeMessage(deliverer.raw)
			if err != nil {
				t.Fatal(err)
			}
			if message.MessageID != options.MessageID || message.Kind != test.kind || message.Body != test.body ||
				message.HandoffID != fixture.request.HandoffID || message.SenderWBSessionID != fixture.request.PredecessorWBSessionID ||
				message.RecipientWBSessionID != fixture.request.SuccessorWBSessionID || message.ReplyToWBSessionID != fixture.request.PredecessorWBSessionID {
				t.Fatalf("delivered message = %#v", message)
			}
			if result.Receipt.MessageID != options.MessageID || result.Replay || workLogCalls != 1 || !persistedBeforeCourier {
				t.Fatalf("result=%#v workLogCalls=%d persistedBeforeCourier=%v", result, workLogCalls, persistedBeforeCourier)
			}
		})
	}
}

func TestSendAmbiguityReturnsExactResumeIdentityAndReusesDurableBytes(t *testing.T) {
	fixture := newSendFixture(t, sessionmove.CourierSSH)
	first := &fakeMessageDeliverer{err: errors.New("transport outcome unknown")}
	options := fixture.options(sessionmove.MessageKindText, "Preserve these exact bytes.")
	options.NewDeliverer = func(sessionmove.SuccessorAddress, sessioncourier.MessageSynchestraOptions) (sessioncourier.MessageDeliverer, error) {
		return first, nil
	}
	result, err := Send(context.Background(), options)
	var deliveryErr *DeliveryError
	if !errors.As(err, &deliveryErr) || deliveryErr.MessageID != options.MessageID || result.Message.MessageID != options.MessageID {
		t.Fatalf("ambiguous result=%#v err=%v", result, err)
	}

	second := &fakeMessageDeliverer{receipt: fixture.messageReceipt}
	resume := fixture.options(sessionmove.MessageKindText, "")
	resume.MessageID = ""
	resume.ResumeMessageID = deliveryErr.MessageID
	resume.NewDeliverer = func(sessionmove.SuccessorAddress, sessioncourier.MessageSynchestraOptions) (sessioncourier.MessageDeliverer, error) {
		return second, nil
	}
	replayed, err := Send(context.Background(), resume)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.raw, second.raw) || replayed.Message.MessageID != deliveryErr.MessageID {
		t.Fatalf("resume changed exact bytes: first=%q second=%q result=%#v", first.raw, second.raw, replayed)
	}
}

func TestSendUsesOnlyRecordedSynchestraRouteAndDurableDispatch(t *testing.T) {
	fixture := newSendFixture(t, sessionmove.CourierSynchestra)
	deliverer := &fakeMessageDeliverer{receipt: fixture.messageReceipt}
	options := fixture.options(sessionmove.MessageKindText, "Continue on Synchestra only.")
	var persistedDigest sessionmove.Digest
	options.Hooks.BeforeCourier = func(state sessionmove.MessageState) error {
		persistedDigest = state.Digest
		return nil
	}
	options.NewDeliverer = func(address sessionmove.SuccessorAddress, synchestra sessioncourier.MessageSynchestraOptions) (sessioncourier.MessageDeliverer, error) {
		if address.Route.Courier != sessionmove.CourierSynchestra || address.Route.Synchestra == nil || synchestra.SaveDispatch == nil {
			t.Fatalf("recorded Synchestra route/options = %#v %#v", address, synchestra)
		}
		identity := sessionmove.MessageSynchestraDispatch{
			HandoffID: address.HandoffID, RequestDigest: address.RequestDigest,
			MessageID: options.MessageID, MessageDigest: persistedDigest,
			Runner: address.Route.Synchestra.Runner, InvocationID: options.MessageID,
			Handler: sessionmove.SynchestraSessionMessageHandler, DispatchID: "dsp_message_123",
		}
		if err := synchestra.SaveDispatch(identity); err != nil {
			t.Fatal(err)
		}
		return deliverer, nil
	}
	if _, err := Send(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	lock, err := fixture.store.AcquireExecutionLock(context.Background(), fixture.request.HandoffID, fixture.digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	dispatch, err := fixture.store.LoadOutgoingMessageSynchestraDispatchUnderLock(lock, fixture.request.HandoffID, fixture.digest, options.MessageID)
	if err != nil || dispatch.DispatchID != "dsp_message_123" {
		t.Fatalf("durable message dispatch = %#v err=%v", dispatch, err)
	}
}

type sendFixture struct {
	store   sessionmove.Store
	request sessionmove.Request
	digest  sessionmove.Digest
	receipt sessionmove.Receipt
	address sessionmove.SuccessorAddress
	source  session.Record
}

func newSendFixture(t *testing.T, courier sessionmove.Courier) *sendFixture {
	t.Helper()
	request := sendRequest()
	raw, err := sessionmove.EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := sessionmove.DigestBytes(raw)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	route := sessionmove.Route{
		HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine, Courier: courier,
	}
	if courier == sessionmove.CourierSSH {
		route.SSH = &sessionmove.SSHConfig{Host: "target-vm", WBPath: "/usr/local/bin/wb"}
	} else {
		route.Synchestra = &sessionmove.SynchestraConfig{Runner: "target-vm"}
	}
	if _, _, err := store.SaveRoute(route); err != nil {
		t.Fatal(err)
	}
	receipt := sendReceipt(request, digest)
	lock, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SaveReceiptUnderLock(lock, request.HandoffID, digest, receipt); err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	address, _, err := store.SaveSuccessorAddressUnderLock(lock, request.HandoffID, digest, receipt)
	_ = lock.Close()
	if err != nil {
		t.Fatal(err)
	}
	source := session.Record{
		PID: 777, WBSessionID: request.PredecessorWBSessionID, Machine: request.SourceMachine,
		Runtime: request.SourceRuntime, Model: request.SourceModel, NativeHarnessID: request.SourceNativeHarnessID,
		StartedAt: request.CreatedAt.Add(-time.Hour),
	}
	return &sendFixture{store: store, request: request, digest: digest, receipt: receipt, address: address, source: source}
}

func (fixture *sendFixture) options(kind sessionmove.MessageKind, body string) Options {
	return Options{
		Store: fixture.store, ProjectsRoot: filepath.Dir(filepath.Dir(fixture.store.Root)),
		TargetWBSessionID: fixture.request.SuccessorWBSessionID, SourceSession: fixture.source,
		Kind: kind, Body: body, MessageID: "message-123",
		Now:        func() time.Time { return time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC) },
		RecordSent: func(WorkLogRecord) error { return nil },
	}
}

func (fixture *sendFixture) messageReceipt(message sessionmove.Message, raw []byte) sessionmove.MessageReceipt {
	return sessionmove.MessageReceipt{
		SchemaVersion: sessionmove.MessageReceiptSchemaVersion, MessageID: message.MessageID,
		MessageDigest: sessionmove.DigestBytes(raw), HandoffID: message.HandoffID,
		SenderWBSessionID: message.SenderWBSessionID, RecipientWBSessionID: message.RecipientWBSessionID,
		ReplyToWBSessionID: message.ReplyToWBSessionID, Kind: message.Kind,
		TmuxName: fixture.address.TmuxName, PaneID: "%7", PID: fixture.address.PID,
		RecordedAt: message.SentAt.Add(time.Second), PastedAt: message.SentAt.Add(2 * time.Second),
	}
}

func sendRequest() sessionmove.Request {
	message, next := sessionmove.NormalizeSourceOfferContent("ready", "continue")
	return sessionmove.Request{
		SchemaVersion: sessionmove.RequestSchemaVersion, HandoffID: "handoff-123",
		SuccessorWBSessionID: "wbs-successor", PredecessorWBSessionID: "wbs-source",
		SourceMachine: "laptop", TargetMachine: "target-vm", RepositoryRemote: "git@github.com:acme/widgets.git",
		Branch: "agent/session-move", SourceWorkCommit: strings.Repeat("a", 40), BundleCommit: strings.Repeat("b", 40),
		HandoverPath: ".wb/handoffs/handoff-123.md", HandoverDigest: sessionmove.DigestBytes([]byte("handover")),
		SourceRuntime: "codex", WorkLogReference: "worklog:effort/run/" + strings.Repeat("c", 64),
		SourceOfferMessage: message, SourceOfferNextAction: next, SourceOfferDigest: sessionmove.DigestSourceOffer(message, next),
		CreatedAt: time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC),
	}
}

func sendReceipt(request sessionmove.Request, digest sessionmove.Digest) sessionmove.Receipt {
	target, err := sessionmove.ExpectedTargetWorkLogReference(request, digest)
	if err != nil {
		panic(err)
	}
	return sessionmove.Receipt{
		SchemaVersion: sessionmove.ReceiptSchemaVersion, HandoffID: request.HandoffID, RequestDigest: digest,
		SuccessorWBSessionID: request.SuccessorWBSessionID, PredecessorWBSessionID: request.PredecessorWBSessionID,
		TargetMachine: request.TargetMachine, TmuxName: "wb-session-" + request.SuccessorWBSessionID,
		Runtime: request.SourceRuntime, AttemptID: "000001-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", AttemptIndex: 1, PID: 1234,
		TargetWorkLogReference: target.String(), PinnedCommit: request.BundleCommit,
		StartedAt: time.Date(2026, 8, 25, 10, 1, 0, 0, time.UTC),
	}
}
