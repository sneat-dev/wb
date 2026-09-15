package sessionmessenger

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessioncourier"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

// sdCovOutboxEntry returns the durable outgoing message entry directory for the
// fixture's handoff and caller-owned message identity.
func sdCovOutboxEntry(fixture *sendFixture, messageID string) string {
	return filepath.Join(fixture.store.Root, fixture.request.HandoffID, "outbox", messageID)
}

// sdCovCountingFactory returns the supplied deliverer and counts courier attempts.
func sdCovCountingFactory(deliverer *fakeMessageDeliverer, calls *int) func(sessionmove.SuccessorAddress, sessioncourier.MessageSynchestraOptions) (sessioncourier.MessageDeliverer, error) {
	return func(sessionmove.SuccessorAddress, sessioncourier.MessageSynchestraOptions) (sessioncourier.MessageDeliverer, error) {
		*calls++
		return deliverer, nil
	}
}

func TestSdCovDeliveryErrorReportsExactResumableIdentityAndUnwraps(t *testing.T) {
	fixture := newSendFixture(t, sessionmove.CourierSSH)
	cause := errors.New("transport outcome unknown")
	options := fixture.options(sessionmove.MessageKindText, "keep these bytes")
	calls := 0
	options.NewDeliverer = sdCovCountingFactory(&fakeMessageDeliverer{err: cause}, &calls)
	_, err := Send(context.Background(), options)
	var deliveryErr *DeliveryError
	if !errors.As(err, &deliveryErr) {
		t.Fatalf("Send error = %v, want *DeliveryError", err)
	}
	if deliveryErr.MessageID != options.MessageID || deliveryErr.TargetWBSessionID != fixture.request.SuccessorWBSessionID {
		t.Fatalf("DeliveryError = %#v", deliveryErr)
	}
	for _, want := range []string{options.MessageID, fixture.request.SuccessorWBSessionID, cause.Error(), "durably resumable"} {
		if !strings.Contains(deliveryErr.Error(), want) {
			t.Fatalf("DeliveryError.Error() = %q, want it to contain %q", deliveryErr.Error(), want)
		}
	}
	if !errors.Is(err, cause) || !errors.Is(deliveryErr.Unwrap(), cause) {
		t.Fatalf("DeliveryError does not unwrap to its cause: %v", err)
	}
}

func TestSdCovSendRejectsBlankTargetSessionIDBeforeStoreUse(t *testing.T) {
	_, err := Send(context.Background(), Options{TargetWBSessionID: "   "})
	if err == nil || !strings.Contains(err.Error(), "target WB session ID is required") {
		t.Fatalf("Send error = %v, want blank target refusal", err)
	}
}

func TestSdCovSendRejectsUnknownSuccessorAddress(t *testing.T) {
	fixture := newSendFixture(t, sessionmove.CourierSSH)
	options := fixture.options(sessionmove.MessageKindText, "hello")
	options.TargetWBSessionID = "wbs-never-indexed"
	_, err := Send(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "resolve durable successor address") {
		t.Fatalf("Send error = %v, want successor resolution failure", err)
	}
}

func TestSdCovSendFailsWhenExecutionLockCannotBeAcquired(t *testing.T) {
	fixture := newSendFixture(t, sessionmove.CourierSSH)
	// A directory where the execution lock file belongs can never be opened as
	// the handoff fence, so no courier attempt may start. The fixture's own lock
	// acquisition left the regular lock file behind; replace it with a
	// directory so the fence can never be acquired.
	lockPath := filepath.Join(fixture.store.Root, fixture.request.HandoffID, "receive.lock")
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	options := fixture.options(sessionmove.MessageKindText, "hello")
	calls := 0
	options.NewDeliverer = sdCovCountingFactory(&fakeMessageDeliverer{}, &calls)
	_, err := Send(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "execution lock") {
		t.Fatalf("Send error = %v, want execution-lock refusal", err)
	}
	if calls != 0 {
		t.Fatalf("courier attempts = %d, want 0 without the handoff fence", calls)
	}
}

func TestSdCovSendFailsWhenAggregateEventsAreUnreadable(t *testing.T) {
	fixture := newSendFixture(t, sessionmove.CourierSSH)
	// A regular file where the events directory belongs makes the exact
	// aggregate projection unreadable while leaving address resolution intact.
	if err := os.WriteFile(filepath.Join(fixture.store.Root, fixture.request.HandoffID, "events"), []byte("no"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := fixture.options(sessionmove.MessageKindText, "hello")
	calls := 0
	options.NewDeliverer = sdCovCountingFactory(&fakeMessageDeliverer{}, &calls)
	_, err := Send(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "events directory") {
		t.Fatalf("Send error = %v, want unreadable-aggregate failure", err)
	}
	if calls != 0 {
		t.Fatalf("courier attempts = %d, want 0 without the exact aggregate", calls)
	}
}

func TestSdCovSendRejectsSourceSessionThatDoesNotMatchPredecessor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*session.Record)
		wantErr bool
	}{
		{name: "mismatched native harness", mutate: func(s *session.Record) { s.NativeHarnessID = "other-native" }, wantErr: true},
		{name: "mismatched model", mutate: func(s *session.Record) { s.Model = "gpt-4" }, wantErr: true},
		{name: "missing start time", mutate: func(s *session.Record) { s.StartedAt = time.Time{} }, wantErr: true},
		{name: "agent id fallback accepted", mutate: func(s *session.Record) {
			s.AgentID = s.NativeHarnessID
			s.NativeHarnessID = ""
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSendFixture(t, sessionmove.CourierSSH)
			options := fixture.options(sessionmove.MessageKindText, "hello")
			tc.mutate(&options.SourceSession)
			calls := 0
			options.NewDeliverer = sdCovCountingFactory(&fakeMessageDeliverer{err: errors.New("courier stopped")}, &calls)
			_, err := Send(context.Background(), options)
			if err == nil {
				t.Fatal("Send = nil, want the courier stub to stop delivery")
			}
			if tc.wantErr {
				if !strings.Contains(err.Error(), "does not match the recorded predecessor identity") {
					t.Fatalf("Send error = %v, want source identity refusal", err)
				}
				if calls != 0 {
					t.Fatalf("courier attempts = %d, want 0 for a mismatched source session", calls)
				}
				return
			}
			if strings.Contains(err.Error(), "does not match the recorded predecessor identity") {
				t.Fatalf("Send error = %v, want the native-agent fallback to be accepted", err)
			}
			if calls != 1 {
				t.Fatalf("courier attempts = %d, want 1 once the source identity was accepted", calls)
			}
		})
	}
}

func TestSdCovSendRejectsResumeThatReintroducesMessageIdentity(t *testing.T) {
	fixture := newSendFixture(t, sessionmove.CourierSSH)
	options := fixture.options(sessionmove.MessageKindText, "body must not be resent")
	options.ResumeMessageID = options.MessageID
	_, err := Send(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "resume accepts only the durable message ID") {
		t.Fatalf("Send error = %v, want resume refusal", err)
	}
}

func TestSdCovSendRejectsResumeOfUnknownOrReinterpretedMessage(t *testing.T) {
	fixture := newSendFixture(t, sessionmove.CourierSSH)
	calls := 0
	deliverer := &fakeMessageDeliverer{receipt: fixture.messageReceipt}
	options := fixture.options(sessionmove.MessageKindText, "first body")
	options.NewDeliverer = sdCovCountingFactory(deliverer, &calls)
	if _, err := Send(context.Background(), options); err != nil {
		t.Fatal(err)
	}

	unknown := fixture.options(sessionmove.MessageKindText, "")
	unknown.MessageID = ""
	unknown.ResumeMessageID = "message-never-admitted"
	unknown.NewDeliverer = sdCovCountingFactory(deliverer, &calls)
	if _, err := Send(context.Background(), unknown); err == nil {
		t.Fatal("Send resume of an unknown message = nil, want failure")
	}

	reinterpreted := fixture.options(sessionmove.MessageKindRequestHandoff, "")
	reinterpreted.MessageID = ""
	reinterpreted.ResumeMessageID = options.MessageID
	reinterpreted.NewDeliverer = sdCovCountingFactory(deliverer, &calls)
	_, err := Send(context.Background(), reinterpreted)
	if !errors.Is(err, sessionmove.ErrHandoffConflict) || !strings.Contains(err.Error(), "has kind") {
		t.Fatalf("Send error = %v, want durable-kind conflict", err)
	}
}

func TestSdCovSendRequiresCallerOwnedMessageIdentityAndClock(t *testing.T) {
	fixture := newSendFixture(t, sessionmove.CourierSSH)
	withoutID := fixture.options(sessionmove.MessageKindText, "hello")
	withoutID.MessageID = "  "
	if _, err := Send(context.Background(), withoutID); err == nil || !strings.Contains(err.Error(), "caller-owned message ID is required") {
		t.Fatalf("Send error = %v, want message ID requirement", err)
	}
	withoutClock := fixture.options(sessionmove.MessageKindText, "hello")
	withoutClock.Now = nil
	if _, err := Send(context.Background(), withoutClock); err == nil || !strings.Contains(err.Error(), "session message clock is required") {
		t.Fatalf("Send error = %v, want clock requirement", err)
	}
}

func TestSdCovSendRejectsUnsupportedMessageKind(t *testing.T) {
	fixture := newSendFixture(t, sessionmove.CourierSSH)
	options := fixture.options(sessionmove.MessageKind("telepathy"), "hello")
	calls := 0
	options.NewDeliverer = sdCovCountingFactory(&fakeMessageDeliverer{}, &calls)
	_, err := Send(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), `message kind "telepathy" is unsupported`) {
		t.Fatalf("Send error = %v, want unsupported-kind refusal", err)
	}
	if calls != 0 {
		t.Fatalf("courier attempts = %d, want 0 for an unencodable message", calls)
	}
}

func TestSdCovSendReplaysDurableMessageReceiptWithoutCourier(t *testing.T) {
	fixture := newSendFixture(t, sessionmove.CourierSSH)
	deliverer := &fakeMessageDeliverer{receipt: fixture.messageReceipt}
	calls := 0
	first := fixture.options(sessionmove.MessageKindText, "replay me")
	first.NewDeliverer = sdCovCountingFactory(deliverer, &calls)
	accepted, err := Send(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	recorded := 0
	second := fixture.options(sessionmove.MessageKindText, "replay me")
	second.RecordSent = func(record WorkLogRecord) error {
		recorded++
		if record.MessageReceipt != accepted.Receipt {
			t.Fatalf("replay Work Log record receipt = %#v, want %#v", record.MessageReceipt, accepted.Receipt)
		}
		return nil
	}
	second.NewDeliverer = func(sessionmove.SuccessorAddress, sessioncourier.MessageSynchestraOptions) (sessioncourier.MessageDeliverer, error) {
		t.Fatal("idempotent replay invoked the courier again")
		return nil, nil
	}
	replayed, err := Send(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replay || replayed.Receipt != accepted.Receipt || calls != 1 {
		t.Fatalf("replayed=%#v calls=%d", replayed, calls)
	}
	if recorded != 1 {
		t.Fatalf("replay Work Log records = %d, want 1", recorded)
	}
}

func TestSdCovSendRefusesCourierAttemptWhenBoundaryHookFails(t *testing.T) {
	fixture := newSendFixture(t, sessionmove.CourierSSH)
	blocked := errors.New("crash before courier")
	options := fixture.options(sessionmove.MessageKindText, "durable before courier")
	options.Hooks.BeforeCourier = func(state sessionmove.MessageState) error {
		if state.Message.MessageID != options.MessageID || len(state.Raw) == 0 || state.Receipt != nil {
			t.Fatalf("hook state = %#v", state)
		}
		return blocked
	}
	calls := 0
	options.NewDeliverer = sdCovCountingFactory(&fakeMessageDeliverer{}, &calls)
	result, err := Send(context.Background(), options)
	var deliveryErr *DeliveryError
	if !errors.As(err, &deliveryErr) || !errors.Is(err, blocked) {
		t.Fatalf("Send error = %v, want resumable boundary failure", err)
	}
	if calls != 0 {
		t.Fatalf("courier attempts = %d, want 0 after the boundary hook failed", calls)
	}
	if result.Message.MessageID != options.MessageID {
		t.Fatalf("result = %#v, want the caller-owned durable identity", result)
	}
}

func TestSdCovSendReportsCourierFactoryFailure(t *testing.T) {
	fixture := newSendFixture(t, sessionmove.CourierSSH)
	options := fixture.options(sessionmove.MessageKindText, "hello")
	factoryErr := errors.New("courier factory refused")
	options.NewDeliverer = func(sessionmove.SuccessorAddress, sessioncourier.MessageSynchestraOptions) (sessioncourier.MessageDeliverer, error) {
		return nil, factoryErr
	}
	_, err := Send(context.Background(), options)
	if !errors.Is(err, factoryErr) {
		t.Fatalf("Send error = %v, want courier factory failure", err)
	}
}

func TestSdCovSendRejectsTargetAcknowledgementThatDoesNotMatchExactMessage(t *testing.T) {
	fixture := newSendFixture(t, sessionmove.CourierSSH)
	options := fixture.options(sessionmove.MessageKindText, "hello")
	options.NewDeliverer = func(sessionmove.SuccessorAddress, sessioncourier.MessageSynchestraOptions) (sessioncourier.MessageDeliverer, error) {
		return &fakeMessageDeliverer{receipt: func(message sessionmove.Message, raw []byte) sessionmove.MessageReceipt {
			receipt := fixture.messageReceipt(message, raw)
			receipt.PID = fixture.address.PID + 1
			return receipt
		}}, nil
	}
	_, err := Send(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "validate target acknowledgement") {
		t.Fatalf("Send error = %v, want acknowledgement validation failure", err)
	}
}

func TestSdCovSendFailsWhenOutgoingReceiptPublicationIsBlocked(t *testing.T) {
	fixture := newSendFixture(t, sessionmove.CourierSSH)
	deliverer := &fakeMessageDeliverer{receipt: fixture.messageReceipt}
	options := fixture.options(sessionmove.MessageKindText, "hello")
	options.Hooks.BeforeCourier = func(sessionmove.MessageState) error {
		return os.MkdirAll(filepath.Join(sdCovOutboxEntry(fixture, options.MessageID), "receipt.json"), 0o700)
	}
	calls := 0
	options.NewDeliverer = sdCovCountingFactory(deliverer, &calls)
	_, err := Send(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "receipt") {
		t.Fatalf("Send error = %v, want blocked receipt publication", err)
	}
	if calls != 1 {
		t.Fatalf("courier attempts = %d, want exactly one before publication failed", calls)
	}
}

func TestSdCovSendReportsSourceRecordingFailureAsResumable(t *testing.T) {
	fixture := newSendFixture(t, sessionmove.CourierSSH)
	recordErr := errors.New("work log unavailable")
	options := fixture.options(sessionmove.MessageKindText, "hello")
	options.RecordSent = func(record WorkLogRecord) error {
		if record.Message.MessageID != options.MessageID || record.MessageReceipt.PastedAt.IsZero() {
			t.Fatalf("Work Log record = %#v", record)
		}
		return recordErr
	}
	calls := 0
	options.NewDeliverer = sdCovCountingFactory(&fakeMessageDeliverer{receipt: fixture.messageReceipt}, &calls)
	result, err := Send(context.Background(), options)
	if !errors.Is(err, recordErr) {
		t.Fatalf("Send error = %v, want source recording failure", err)
	}
	if result.Receipt.MessageID != options.MessageID {
		t.Fatalf("result = %#v, want the durable acknowledgement preserved for retry", result)
	}
}

func TestSdCovSendUsesProductionRecordingSeamWhenUnset(t *testing.T) {
	fixture := newSendFixture(t, sessionmove.CourierSSH)
	options := fixture.options(sessionmove.MessageKindText, "hello")
	options.RecordSent = nil
	calls := 0
	options.NewDeliverer = sdCovCountingFactory(&fakeMessageDeliverer{receipt: fixture.messageReceipt}, &calls)
	result, err := Send(context.Background(), options)
	var deliveryErr *DeliveryError
	if err == nil || !errors.As(err, &deliveryErr) {
		t.Fatalf("Send error = %v, want resumable production recording failure", err)
	}
	if result.Receipt.MessageID != options.MessageID || calls != 1 {
		t.Fatalf("result=%#v courier attempts=%d, want durable acknowledgement after one attempt", result, calls)
	}
}

func TestSdCovSendReusesDurableSynchestraDispatchOnRetry(t *testing.T) {
	fixture := newSendFixture(t, sessionmove.CourierSynchestra)
	var persistedDigest sessionmove.Digest
	first := fixture.options(sessionmove.MessageKindText, "dispatch once")
	first.Hooks.BeforeCourier = func(state sessionmove.MessageState) error {
		persistedDigest = state.Digest
		return nil
	}
	first.NewDeliverer = func(address sessionmove.SuccessorAddress, synchestra sessioncourier.MessageSynchestraOptions) (sessioncourier.MessageDeliverer, error) {
		if synchestra.SaveDispatch == nil || synchestra.Dispatch != nil {
			t.Fatalf("fresh Synchestra options = %#v", synchestra)
		}
		if err := synchestra.SaveDispatch(sessionmove.MessageSynchestraDispatch{
			HandoffID: address.HandoffID, RequestDigest: address.RequestDigest,
			MessageID: first.MessageID, MessageDigest: persistedDigest,
			Runner: address.Route.Synchestra.Runner, InvocationID: first.MessageID,
			Handler: sessionmove.SynchestraSessionMessageHandler, DispatchID: "dsp_durable_456",
		}); err != nil {
			t.Fatal(err)
		}
		return nil, errors.New("ambiguous Synchestra delivery")
	}
	if _, err := Send(context.Background(), first); err == nil {
		t.Fatal("Send = nil, want ambiguous Synchestra delivery failure")
	}

	reused := 0
	second := fixture.options(sessionmove.MessageKindText, "")
	second.MessageID = ""
	second.ResumeMessageID = first.MessageID
	second.NewDeliverer = func(_ sessionmove.SuccessorAddress, synchestra sessioncourier.MessageSynchestraOptions) (sessioncourier.MessageDeliverer, error) {
		reused++
		if synchestra.Dispatch == nil || synchestra.Dispatch.DispatchID != "dsp_durable_456" || synchestra.SaveDispatch != nil {
			t.Fatalf("replayed Synchestra options = %#v", synchestra)
		}
		if synchestra.RequestDigest != fixture.digest {
			t.Fatalf("replayed Synchestra request digest = %q, want %q", synchestra.RequestDigest, fixture.digest)
		}
		return &fakeMessageDeliverer{receipt: fixture.messageReceipt}, nil
	}
	result, err := Send(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if reused != 1 || !result.Replay {
		t.Fatalf("reused=%d result=%#v, want one dispatch-reusing attempt", reused, result)
	}
}

func TestSdCovSendReportsUnreadableDurableSynchestraDispatch(t *testing.T) {
	fixture := newSendFixture(t, sessionmove.CourierSynchestra)
	var persistedDigest sessionmove.Digest
	first := fixture.options(sessionmove.MessageKindText, "dispatch then fail")
	first.Hooks.BeforeCourier = func(state sessionmove.MessageState) error {
		persistedDigest = state.Digest
		return nil
	}
	first.NewDeliverer = func(address sessionmove.SuccessorAddress, synchestra sessioncourier.MessageSynchestraOptions) (sessioncourier.MessageDeliverer, error) {
		if err := synchestra.SaveDispatch(sessionmove.MessageSynchestraDispatch{
			HandoffID: address.HandoffID, RequestDigest: address.RequestDigest,
			MessageID: first.MessageID, MessageDigest: persistedDigest,
			Runner: address.Route.Synchestra.Runner, InvocationID: first.MessageID,
			Handler: sessionmove.SynchestraSessionMessageHandler, DispatchID: "dsp_unreadable_789",
		}); err != nil {
			t.Fatal(err)
		}
		return nil, errors.New("ambiguous Synchestra delivery")
	}
	if _, err := Send(context.Background(), first); err == nil {
		t.Fatal("Send = nil, want ambiguous Synchestra delivery failure")
	}
	dispatchPath := filepath.Join(sdCovOutboxEntry(fixture, first.MessageID), "synchestra-dispatch.json")
	if err := os.Chmod(dispatchPath, 0o644); err != nil {
		t.Fatal(err)
	}

	second := fixture.options(sessionmove.MessageKindText, "")
	second.MessageID = ""
	second.ResumeMessageID = first.MessageID
	second.NewDeliverer = func(sessionmove.SuccessorAddress, sessioncourier.MessageSynchestraOptions) (sessioncourier.MessageDeliverer, error) {
		t.Fatal("courier invoked despite an unreadable durable Synchestra dispatch")
		return nil, nil
	}
	_, err := Send(context.Background(), second)
	if err == nil || !strings.Contains(err.Error(), "synchestra dispatch") {
		t.Fatalf("Send error = %v, want unreadable-dispatch failure", err)
	}
}

func TestSdCovNewDelivererRejectsIncompleteOrUnsupportedCouriers(t *testing.T) {
	address := sessionmove.SuccessorAddress{
		HandoffID: "handoff-123", RequestDigest: sessionmove.Digest("sha256:" + strings.Repeat("a", 64)),
		SuccessorWBSessionID: "wbs-successor",
	}
	address.Route.Courier = sessionmove.CourierSSH
	if _, err := newDeliverer(Options{}, address, sessioncourier.MessageSynchestraOptions{}); err == nil ||
		!strings.Contains(err.Error(), "SSH successor address is incomplete") {
		t.Fatalf("newDeliverer SSH error = %v, want incomplete SSH address", err)
	}
	address.Route.Courier = sessionmove.CourierSynchestra
	if _, err := newDeliverer(Options{}, address, sessioncourier.MessageSynchestraOptions{}); err == nil ||
		!strings.Contains(err.Error(), "Synchestra successor address is incomplete") {
		t.Fatalf("newDeliverer Synchestra error = %v, want incomplete Synchestra address", err)
	}
	address.Route.Courier = sessionmove.Courier("carrier-pigeon")
	if _, err := newDeliverer(Options{}, address, sessioncourier.MessageSynchestraOptions{}); err == nil ||
		!strings.Contains(err.Error(), `courier "carrier-pigeon" is unsupported`) {
		t.Fatalf("newDeliverer unsupported error = %v, want unsupported courier", err)
	}

	// A fake executable per courier keeps construction hermetic and proves the
	// real production constructors are reachable through the recorded route.
	binDir := t.TempDir()
	for _, name := range []string{"ssh", "synchestra"} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	address.Route.Courier = sessionmove.CourierSSH
	address.Route.Synchestra = nil
	address.Route.SSH = &sessionmove.SSHConfig{Host: "target-vm", WBPath: "/usr/local/bin/wb"}
	deliverer, err := newDeliverer(Options{}, address, sessioncourier.MessageSynchestraOptions{})
	if err != nil {
		t.Fatalf("newDeliverer SSH error = %v, want a constructed SSH message deliverer", err)
	}
	if deliverer == nil {
		t.Fatal("newDeliverer SSH = nil, want a usable message deliverer")
	}
	if _, err := deliverer.DeliverMessage(context.Background(), nil); err == nil ||
		!strings.Contains(err.Error(), "validate SSH session message") {
		t.Fatalf("deliverer.DeliverMessage(nil) error = %v, want payload validation refusal", err)
	}

	address.Route.Courier = sessionmove.CourierSynchestra
	address.Route.SSH = nil
	address.Route.Synchestra = &sessionmove.SynchestraConfig{Runner: "target-vm"}
	recorder := func(sessionmove.MessageSynchestraDispatch) error { return nil }
	synchestraDeliverer, err := newDeliverer(Options{}, address, sessioncourier.MessageSynchestraOptions{SaveDispatch: recorder})
	if err != nil {
		t.Fatalf("newDeliverer Synchestra error = %v, want a constructed Synchestra message deliverer", err)
	}
	if synchestraDeliverer == nil {
		t.Fatal("newDeliverer Synchestra = nil, want a usable message deliverer")
	}
	if _, err := synchestraDeliverer.DeliverMessage(context.Background(), nil); err == nil ||
		!strings.Contains(err.Error(), "validate Synchestra session message") {
		t.Fatalf("synchestraDeliverer.DeliverMessage(nil) error = %v, want payload validation refusal", err)
	}
}

func TestSdCovSendReportsRecordingFailureOnDurableReceiptReplay(t *testing.T) {
	fixture := newSendFixture(t, sessionmove.CourierSSH)
	calls := 0
	first := fixture.options(sessionmove.MessageKindText, "replay then fail to record")
	first.NewDeliverer = sdCovCountingFactory(&fakeMessageDeliverer{receipt: fixture.messageReceipt}, &calls)
	if _, err := Send(context.Background(), first); err != nil {
		t.Fatal(err)
	}

	recordErr := errors.New("work log unavailable on replay")
	second := fixture.options(sessionmove.MessageKindText, "replay then fail to record")
	second.RecordSent = func(record WorkLogRecord) error {
		if record.MessageReceipt.MessageID != second.MessageID {
			t.Fatalf("replay Work Log record = %#v", record)
		}
		return recordErr
	}
	second.NewDeliverer = func(sessionmove.SuccessorAddress, sessioncourier.MessageSynchestraOptions) (sessioncourier.MessageDeliverer, error) {
		t.Fatal("durable receipt replay invoked the courier")
		return nil, nil
	}
	result, err := Send(context.Background(), second)
	var deliveryErr *DeliveryError
	if !errors.As(err, &deliveryErr) || !errors.Is(err, recordErr) {
		t.Fatalf("Send error = %v, want resumable replay recording failure", err)
	}
	if !result.Replay || result.Receipt.MessageID != second.MessageID {
		t.Fatalf("result = %#v, want the replayed durable receipt preserved", result)
	}
}
