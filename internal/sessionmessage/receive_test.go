package sessionmessage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionterminal"
)

type fakeTerminal struct {
	mu       sync.Mutex
	pane     Pane
	loaded   []byte
	calls    []string
	pasteErr error
}

func (fake *fakeTerminal) Inspect(_ context.Context, receipt sessionmove.Receipt, _ session.Record) (Pane, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls = append(fake.calls, "inspect:"+receipt.Terminal.PaneID)
	return fake.pane, nil
}
func (fake *fakeTerminal) Submit(_ context.Context, pane Pane, raw []byte) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls = append(fake.calls, "submit:"+pane.ID)
	fake.loaded = append([]byte(nil), raw...)
	return fake.pasteErr
}

func TestReceiveDurablyRecordsVerifiesAndPastesExactMessageOnce(t *testing.T) {
	t.Parallel()
	fixture := newReceiveFixture(t)
	var workLogCalls atomic.Int32
	fixture.options.RecordReceived = func(record WorkLogRecord) error {
		workLogCalls.Add(1)
		if record.Message.Body == "" || record.Record.Direction != sessionmove.MessageDirectionIncoming {
			t.Fatalf("Work Log record = %#v", record)
		}
		return nil
	}

	result, err := Receive(context.Background(), fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt.MessageID != fixture.message.MessageID || result.Receipt.MessageDigest != sessionmove.DigestBytes(fixture.raw) ||
		result.Receipt.PaneID != "w1:p7" || result.Receipt.PID != fixture.receipt.PID || result.Replay {
		t.Fatalf("result = %#v", result)
	}
	if !bytes.Equal(fixture.terminal.loaded, fixture.raw) {
		t.Fatalf("submitted bytes = %q, want exact canonical bytes", fixture.terminal.loaded)
	}
	if workLogCalls.Load() != 1 {
		t.Fatalf("target Work Log calls = %d, want 1", workLogCalls.Load())
	}
	if got := fixture.terminal.calls; len(got) != 3 || got[0] != "inspect:w1:p7" || got[1] != "inspect:w1:p7" || got[2] != "submit:w1:p7" {
		t.Fatalf("terminal calls=%#v", got)
	}

	fixture.options.LookupSession = func(string, int) (session.Record, bool, error) {
		t.Fatal("receipt replay consulted the session registry")
		return session.Record{}, false, nil
	}
	fixture.options.RecordReceived = func(WorkLogRecord) error {
		t.Fatal("receipt replay duplicated target Work Log evidence")
		return nil
	}
	before := len(fixture.terminal.calls)
	replayed, err := Receive(context.Background(), fixture.options)
	if err != nil || !replayed.Replay || replayed.Receipt != result.Receipt {
		t.Fatalf("replay = %#v, err=%v", replayed, err)
	}
	if len(fixture.terminal.calls) != before {
		t.Fatalf("receipt replay touched tmux: %#v", fixture.terminal.calls[before:])
	}
}

func TestReceiveUsesCanonicalTypedJSONAsTheActionableRequestHandoffPrompt(t *testing.T) {
	t.Parallel()
	fixture := newReceiveFixture(t)
	fixture.message.Kind = sessionmove.MessageKindRequestHandoff
	fixture.message.Body = ""
	raw, err := sessionmove.EncodeMessage(fixture.message)
	if err != nil {
		t.Fatal(err)
	}
	fixture.raw, fixture.options.RawMessage = raw, raw
	result, err := Receive(context.Background(), fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fixture.terminal.loaded, raw) {
		t.Fatalf("request-handoff prompt changed: pasted=%q canonical=%q", fixture.terminal.loaded, raw)
	}
	prompt, err := sessionmove.DecodeMessage(fixture.terminal.loaded)
	if err != nil || prompt.Kind != sessionmove.MessageKindRequestHandoff || prompt.Body != "" ||
		prompt.ReplyToWBSessionID != fixture.request.PredecessorWBSessionID || result.Receipt.Kind != prompt.Kind {
		t.Fatalf("actionable typed prompt=%#v receipt=%#v err=%v", prompt, result.Receipt, err)
	}
}

func TestReceiveUsesReceiptBackedTargetIdentityWithoutManufacturingSourceTransportIndex(t *testing.T) {
	t.Parallel()
	fixture := newReceiveFixture(t)
	if _, err := fixture.store.LoadSuccessorAddress(fixture.request.SuccessorWBSessionID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target unexpectedly began with a source successor index: %v", err)
	}
	if _, err := Receive(context.Background(), fixture.options); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.LoadSuccessorAddress(fixture.request.SuccessorWBSessionID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target receiver manufactured source transport authority: %v", err)
	}
}

func TestReceiveDoesNotRepasteAfterAmbiguousPostIntentFailure(t *testing.T) {
	t.Parallel()
	fixture := newReceiveFixture(t)
	injected := errors.New("crash after paste")
	fixture.options.Hooks.AfterPaste = func() error { return injected }
	if _, err := Receive(context.Background(), fixture.options); !errors.Is(err, injected) || !errors.Is(err, ErrMessagePasteAmbiguous) {
		t.Fatalf("first receive error = %v", err)
	}
	firstPasteCount := countCallPrefix(fixture.terminal.calls, "submit:")
	if firstPasteCount != 1 {
		t.Fatalf("first paste count = %d", firstPasteCount)
	}
	fixture.options.Hooks = Hooks{}
	if _, err := Receive(context.Background(), fixture.options); !errors.Is(err, ErrMessagePasteAmbiguous) {
		t.Fatalf("receiptless intent replay error = %v, want ErrMessagePasteAmbiguous", err)
	}
	if got := countCallPrefix(fixture.terminal.calls, "submit:"); got != 1 {
		t.Fatalf("ambiguous replay pasted %d times", got)
	}
}

func TestReceiveNeverRetriesAnAmbiguousHerdrSubmission(t *testing.T) {
	t.Parallel()
	fixture := newReceiveFixture(t)
	fixture.terminal.pasteErr = errors.New("socket failed after submission")
	if _, err := Receive(context.Background(), fixture.options); !errors.Is(err, ErrMessagePasteAmbiguous) {
		t.Fatalf("submission failure=%v", err)
	}
	fixture.terminal.pasteErr = nil
	if _, err := Receive(context.Background(), fixture.options); !errors.Is(err, ErrMessagePasteAmbiguous) {
		t.Fatalf("ambiguous replay=%v", err)
	}
	if countCallPrefix(fixture.terminal.calls, "submit:") != 1 {
		t.Fatal("ambiguous message was submitted again")
	}
}

func TestReceiveConcurrentExactRetriesPasteAtMostOnce(t *testing.T) {
	t.Parallel()
	fixture := newReceiveFixture(t)
	const callers = 12
	results := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := Receive(context.Background(), fixture.options)
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Errorf("concurrent receive: %v", err)
		}
	}
	if got := countCallPrefix(fixture.terminal.calls, "submit:"); got != 1 {
		t.Fatalf("concurrent retry paste count = %d, want 1", got)
	}
}

func TestReceiveRefusesSessionOrHerdrIdentityDriftBeforeIntent(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*receiveFixture)
	}{
		{"session", func(fixture *receiveFixture) {
			fixture.session.TmuxName = "wb-session-other"
		}},
		{"pane pid", func(fixture *receiveFixture) {
			fixture.terminal.pane.PID++
		}},
		{"multiple panes", func(fixture *receiveFixture) {
			fixture.terminal.pane.Count = 2
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newReceiveFixture(t)
			test.mutate(fixture)
			if _, err := Receive(context.Background(), fixture.options); err == nil {
				t.Fatal("Receive accepted drifted successor identity")
			}
			lock, err := fixture.store.AcquireExecutionLock(context.Background(), fixture.request.HandoffID, fixture.digest)
			if err != nil {
				t.Fatal(err)
			}
			state, err := fixture.store.LoadIncomingMessageUnderLock(lock, fixture.request.HandoffID, fixture.digest, fixture.message.MessageID)
			_ = lock.Close()
			if err != nil || state.Intent != nil || state.Receipt != nil {
				t.Fatalf("drifted identity crossed paste intent: state=%#v err=%v", state, err)
			}
		})
	}
}

type receiveFixture struct {
	store    sessionmove.Store
	request  sessionmove.Request
	digest   sessionmove.Digest
	receipt  sessionmove.Receipt
	message  sessionmove.Message
	raw      []byte
	session  session.Record
	terminal *fakeTerminal
	options  Options
}

func newReceiveFixture(t *testing.T) *receiveFixture {
	t.Helper()
	return newReceiveFixtureForPID(t, 1234)
}

func newReceiveFixtureForPID(t *testing.T, pid int) *receiveFixture {
	t.Helper()
	return newReceiveFixtureForModel(t, pid, "owned-selected-model")
}

func newReceiveFixtureForModel(t *testing.T, pid int, model string) *receiveFixture {
	t.Helper()
	request := receiveRequest()
	request.SourceModel = model
	requestRaw, err := sessionmove.EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := sessionmove.DigestBytes(requestRaw)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	if _, err := store.Admit(requestRaw, digest); err != nil {
		t.Fatal(err)
	}
	receipt := receiveReceipt(request, digest)
	receipt.PID = pid
	if _, _, err := store.SaveReceipt(request.HandoffID, digest, receipt); err != nil {
		t.Fatal(err)
	}
	message := sessionmove.Message{
		SchemaVersion: sessionmove.MessageSchemaVersion, MessageID: "message-123", HandoffID: request.HandoffID,
		SenderWBSessionID: request.PredecessorWBSessionID, RecipientWBSessionID: request.SuccessorWBSessionID,
		ReplyToWBSessionID: request.PredecessorWBSessionID, Kind: sessionmove.MessageKindText, Body: "Focus on the failing race test.",
		SentAt: time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC),
	}
	raw, err := sessionmove.EncodeMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	record := session.Record{
		PID: receipt.PID, WBSessionID: receipt.SuccessorWBSessionID, Machine: receipt.TargetMachine,
		Runtime: receipt.Runtime, Model: receipt.Model, NativeHarnessID: receipt.NativeHarnessID,
		Terminal: receipt.Terminal, PredecessorWBSessionID: receipt.PredecessorWBSessionID,
		HandoffID: receipt.HandoffID, StartedAt: receipt.StartedAt,
	}
	terminal := &fakeTerminal{pane: Pane{ID: receipt.Terminal.PaneID, PID: receipt.PID, Count: 1, Terminal: receipt.Terminal, NativeHarnessID: receipt.NativeHarnessID}}
	clock := &testClock{next: time.Date(2026, 8, 25, 12, 0, 1, 0, time.UTC)}
	fixture := &receiveFixture{store: store, request: request, digest: digest, receipt: receipt, message: message, raw: raw, session: record, terminal: terminal}
	fixture.options = Options{
		Store: store, ProjectsRoot: t.TempDir(), LocalMachine: request.TargetMachine,
		SessionDir: filepath.Join(t.TempDir(), "sessions"), RawMessage: raw, Terminal: terminal, Now: clock.Now,
		LookupSession: func(_ string, pid int) (session.Record, bool, error) {
			if pid != fixture.session.PID {
				return session.Record{}, false, fmt.Errorf("unexpected PID %d", pid)
			}
			return fixture.session, true, nil
		},
		RecordReceived: func(WorkLogRecord) error { return nil },
		// This fixture deliberately uses a synthetic PID; registry barrier tests use real generations.
		WithRecipient: func(_ context.Context, _ string, _ session.Record, submit func() error) error { return submit() },
	}
	return fixture
}

type testClock struct {
	mu   sync.Mutex
	next time.Time
}

func (clock *testClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	result := clock.next
	clock.next = clock.next.Add(time.Second)
	return result
}

func receiveRequest() sessionmove.Request {
	message, next := sessionmove.NormalizeSourceOfferContent("ready", "continue")
	return sessionmove.Request{
		SchemaVersion: sessionmove.RequestSchemaVersion, HandoffID: "handoff-123",
		SuccessorWBSessionID: "wbs-successor", PredecessorWBSessionID: "wbs-source",
		SourceMachine: "laptop", TargetMachine: "target-vm", RepositoryRemote: "git@github.com:acme/widgets.git",
		Branch: "agent/session-move", SourceWorkCommit: repeat("a", 40), BundleCommit: repeat("b", 40),
		HandoverPath: ".wb/handoffs/handoff-123.md", HandoverDigest: sessionmove.DigestBytes([]byte("handover")),
		SourceRuntime: "codex", WorkLogReference: "worklog:effort/run/" + repeat("c", 64),
		SourceOfferMessage: message, SourceOfferNextAction: next, SourceOfferDigest: sessionmove.DigestSourceOffer(message, next),
		CreatedAt: time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC),
	}
}

func receiveReceipt(request sessionmove.Request, digest sessionmove.Digest) sessionmove.Receipt {
	target, err := sessionmove.ExpectedTargetWorkLogReference(request, digest)
	if err != nil {
		panic(err)
	}
	return sessionmove.Receipt{
		SchemaVersion: sessionmove.ReceiptSchemaVersion, HandoffID: request.HandoffID, RequestDigest: digest,
		SuccessorWBSessionID: request.SuccessorWBSessionID, PredecessorWBSessionID: request.PredecessorWBSessionID,
		TargetMachine: request.TargetMachine, Terminal: sessionterminal.Identity{Kind: "herdr", Socket: "/tmp/wb-owned-herdr.sock", WorkspaceID: "w1", PaneID: "w1:p7"}, NativeHarnessID: "native-receiver",
		Runtime: request.SourceRuntime, Model: request.SourceModel, AttemptID: "000001-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", AttemptIndex: 1, PID: 1234,
		TargetWorkLogReference: target.String(), PinnedCommit: request.BundleCommit,
		StartedAt: time.Date(2026, 8, 25, 10, 1, 0, 0, time.UTC),
	}
}

func repeat(value string, count int) string {
	var output bytes.Buffer
	for range count {
		output.WriteString(value)
	}
	return output.String()
}

func countCallPrefix(calls []string, prefix string) int {
	count := 0
	for _, call := range calls {
		if len(call) >= len(prefix) && call[:len(prefix)] == prefix {
			count++
		}
	}
	return count
}

func TestLegacyAdmittedMessageIsReadableButNeverAutomaticallySubmitted(t *testing.T) {
	t.Parallel()
	fixture := newReceiveFixture(t)
	fixture.message.SchemaVersion = sessionmove.LegacyMessageSchemaVersion
	raw, err := sessionmove.EncodeMessage(fixture.message)
	if err != nil {
		t.Fatal(err)
	}
	fixture.options.RawMessage = raw
	if _, err := Receive(context.Background(), fixture.options); err == nil || !strings.Contains(err.Error(), "new message ID") {
		t.Fatalf("legacy active delivery did not refuse actionably: %v", err)
	}
	if len(fixture.terminal.calls) != 0 {
		t.Fatalf("legacy message reached terminal: %#v", fixture.terminal.calls)
	}
	lock, err := fixture.store.AcquireExecutionLock(context.Background(), fixture.request.HandoffID, fixture.digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	admitted, err := fixture.store.LoadIncomingMessageUnderLock(lock, fixture.request.HandoffID, fixture.digest, fixture.message.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(admitted.Raw, raw) || admitted.Intent != nil || admitted.Receipt != nil {
		t.Fatal("legacy payload was rewritten, reclassified, or submitted")
	}
}

func TestReceiveRejectsLogicalRecipientReplacementAfterObservationWithoutResubmitting(t *testing.T) {
	t.Parallel()
	fixture := newReceiveFixtureForPID(t, os.Getpid())
	scope, err := session.NativeScope(fixture.session.Runtime, fixture.session.NativeHarnessID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.session.Scope = scope
	registered, err := session.Register(fixture.options.SessionDir, fixture.session)
	if err != nil {
		t.Fatal(err)
	}
	fixture.session = registered
	fixture.options.WithRecipient = nil // Exercise the mandatory production barrier, even with an injected terminal.
	successor := registered
	successor.WBSessionID = "wbs-current-pickup-replacement"
	successor.PredecessorWBSessionID = registered.WBSessionID
	successor.HandoffID = "park-current-replacement"
	successor.StartedAt = registered.StartedAt.Add(time.Second)
	var records atomic.Int32
	fixture.options.RecordReceived = func(WorkLogRecord) error {
		records.Add(1)
		return session.WithCurrentTransition(fixture.options.SessionDir, registered, successor, func(commit func() error) error { return commit() })
	}
	if _, err := Receive(context.Background(), fixture.options); !errors.Is(err, ErrMessagePasteAmbiguous) {
		t.Fatalf("replacement after observation must retain an ambiguous intent: %v", err)
	}
	if got := countCallPrefix(fixture.terminal.calls, "submit:"); got != 0 {
		t.Fatalf("stale message submitted %d times", got)
	}
	if _, err := Receive(context.Background(), fixture.options); !errors.Is(err, ErrMessagePasteAmbiguous) {
		t.Fatalf("automatic replay was allowed: %v", err)
	}
	if records.Load() != 1 || countCallPrefix(fixture.terminal.calls, "inspect:") != 1 {
		t.Fatal("ambiguous replay observed or recorded the recipient again")
	}
	current, live, err := session.LookupActiveRecordExact(fixture.options.SessionDir, successor)
	if err != nil || !live || current.WBSessionID != successor.WBSessionID {
		t.Fatalf("replacement registry changed: %#v %v", current, err)
	}
}

func TestReceiveRetainsIntentWhenRecipientChangesDuringFinalInspection(t *testing.T) {
	t.Parallel()
	fixture := newReceiveFixture(t)
	fixture.options.WithRecipient = func(_ context.Context, _ string, _ session.Record, submit func() error) error {
		fixture.terminal.pane.NativeHarnessID = "other-native"
		return submit()
	}
	if _, err := Receive(context.Background(), fixture.options); !errors.Is(err, ErrMessagePasteAmbiguous) {
		t.Fatalf("changed final pane: %v", err)
	}
	if countCallPrefix(fixture.terminal.calls, "submit:") != 0 {
		t.Fatal("changed final pane received input")
	}
	if _, err := Receive(context.Background(), fixture.options); !errors.Is(err, ErrMessagePasteAmbiguous) {
		t.Fatalf("replay after changed pane: %v", err)
	}
}

func TestReceiveCanceledBehindRecipientBarrierReturnsBeforeHolderAndNeverReplays(t *testing.T) {
	t.Parallel()
	fixture := newReceiveFixtureForPID(t, os.Getpid())
	scope, err := session.NativeScope(fixture.session.Runtime, fixture.session.NativeHarnessID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.session.Scope = scope
	fixture.session, err = session.Register(fixture.options.SessionDir, fixture.session)
	if err != nil {
		t.Fatal(err)
	}
	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unhold := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unhold)
	holder := make(chan error, 1)
	go func() {
		holder <- session.WithActiveRecipient(context.Background(), fixture.options.SessionDir, fixture.session, func() error { close(held); <-release; return nil })
	}()
	select {
	case <-held:
	case err := <-holder:
		t.Fatalf("holder did not enter: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("holder acquisition timed out")
	}
	waiting := make(chan struct{})
	fixture.options.WithRecipient = func(ctx context.Context, dir string, record session.Record, submit func() error) error {
		close(waiting) // Receive has already persisted its intent at this boundary.
		return session.WithActiveRecipient(ctx, dir, record, submit)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { _, err := Receive(ctx, fixture.options); finished <- err }()
	select {
	case <-waiting:
	case err := <-finished:
		t.Fatalf("receiver never reached intent barrier: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("receiver did not reach intent barrier")
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrMessagePasteAmbiguous) {
			t.Fatalf("canceled intent outcome: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled waiter did not return while holder retained the barrier")
	}
	select {
	case err := <-holder:
		t.Fatalf("holder released before cancellation proof: %v", err)
	default:
	}
	if countCallPrefix(fixture.terminal.calls, "submit:") != 0 {
		t.Fatal("canceled waiter submitted input")
	}
	fixture.options.WithRecipient = func(context.Context, string, session.Record, func() error) error {
		t.Fatal("ambiguous replay reacquired recipient")
		return nil
	}
	if _, err := Receive(context.Background(), fixture.options); !errors.Is(err, ErrMessagePasteAmbiguous) {
		t.Fatalf("canceled intent replayed: %v", err)
	}
	unhold()
	select {
	case err := <-holder:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("holder cleanup timed out")
	}
}

func TestReceiveAcceptsObservedModelEnrichmentOnlyWhenHerdrSelectionWasUnspecified(t *testing.T) {
	t.Parallel()
	fixture := newReceiveFixtureForModel(t, 1234, session.Unknown)
	fixture.session.Model = "observed-after-native-binding"
	if _, err := Receive(context.Background(), fixture.options); err != nil {
		t.Fatalf("ordinary startup metadata enrichment prevented receipt-bound delivery: %v", err)
	}
	if countCallPrefix(fixture.terminal.calls, "submit:") != 1 {
		t.Fatal("metadata enrichment lost the sole authorized submission")
	}
	selected := newReceiveFixture(t)
	selected.session.Model = "different-from-selected-model"
	if _, err := Receive(context.Background(), selected.options); err == nil {
		t.Fatal("selected known model mismatch accepted")
	}
	if countCallPrefix(selected.terminal.calls, "submit:") != 0 {
		t.Fatal("wrong selected model received input")
	}
	legacy := selected.receipt
	legacy.Terminal = sessionterminal.Identity{}
	record := selected.session
	record.Terminal = sessionterminal.Identity{}
	if matchesRecipient(record, selected.request, legacy) {
		t.Fatal("legacy model identity became observationally permissive")
	}
}
