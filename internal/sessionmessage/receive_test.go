package sessionmessage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

type fakeTmux struct {
	mu        sync.Mutex
	pane      Pane
	buffer    []byte
	loaded    []byte
	calls     []string
	loadErr   error
	pasteErr  error
	deleteErr error
	submitErr error
}

func (fake *fakeTmux) Inspect(_ context.Context, name string) (Pane, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls = append(fake.calls, "inspect:"+name)
	return fake.pane, nil
}

func (fake *fakeTmux) LoadBuffer(_ context.Context, name string, raw []byte) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls = append(fake.calls, "load:"+name)
	fake.buffer = append([]byte(nil), raw...)
	fake.loaded = append([]byte(nil), raw...)
	return fake.loadErr
}

func (fake *fakeTmux) SaveBuffer(_ context.Context, name string) ([]byte, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls = append(fake.calls, "save:"+name)
	return append([]byte(nil), fake.buffer...), nil
}

func (fake *fakeTmux) PasteBuffer(_ context.Context, name, paneID string) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls = append(fake.calls, "paste:"+name+":"+paneID)
	return fake.pasteErr
}

func (fake *fakeTmux) DeleteBuffer(_ context.Context, name string) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls = append(fake.calls, "delete:"+name)
	if fake.deleteErr != nil {
		return fake.deleteErr
	}
	fake.buffer = nil
	return nil
}

func (fake *fakeTmux) Submit(_ context.Context, paneID string) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls = append(fake.calls, "submit:"+paneID)
	return fake.submitErr
}

func TestReceiveDurablyRecordsVerifiesAndPastesExactMessageOnce(t *testing.T) {
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
		result.Receipt.PaneID != "%7" || result.Receipt.PID != fixture.receipt.PID || result.Replay {
		t.Fatalf("result = %#v", result)
	}
	if !bytes.Equal(fixture.tmux.loaded, fixture.raw) || fixture.tmux.buffer != nil {
		t.Fatalf("tmux loaded bytes = %q, retained buffer = %q; want exact bytes then deletion", fixture.tmux.loaded, fixture.tmux.buffer)
	}
	if workLogCalls.Load() != 1 {
		t.Fatalf("target Work Log calls = %d, want 1", workLogCalls.Load())
	}
	if got := fixture.tmux.calls; len(got) != 6 || got[0] != "inspect:wb-session-wbs-successor" ||
		got[1] != "load:wb-message-message-123" || got[2] != "save:wb-message-message-123" ||
		got[3] != "paste:wb-message-message-123:%7" || got[4] != "delete:wb-message-message-123" ||
		got[5] != "submit:%7" {
		t.Fatalf("tmux calls = %#v", got)
	}

	fixture.options.LookupSession = func(string, int) (session.Record, bool, error) {
		t.Fatal("receipt replay consulted the session registry")
		return session.Record{}, false, nil
	}
	fixture.options.RecordReceived = func(WorkLogRecord) error {
		t.Fatal("receipt replay duplicated target Work Log evidence")
		return nil
	}
	before := len(fixture.tmux.calls)
	replayed, err := Receive(context.Background(), fixture.options)
	if err != nil || !replayed.Replay || replayed.Receipt != result.Receipt {
		t.Fatalf("replay = %#v, err=%v", replayed, err)
	}
	if len(fixture.tmux.calls) != before {
		t.Fatalf("receipt replay touched tmux: %#v", fixture.tmux.calls[before:])
	}
}

func TestReceiveUsesCanonicalTypedJSONAsTheActionableRequestHandoffPrompt(t *testing.T) {
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
	if !bytes.Equal(fixture.tmux.loaded, raw) {
		t.Fatalf("request-handoff prompt changed: pasted=%q canonical=%q", fixture.tmux.loaded, raw)
	}
	prompt, err := sessionmove.DecodeMessage(fixture.tmux.loaded)
	if err != nil || prompt.Kind != sessionmove.MessageKindRequestHandoff || prompt.Body != "" ||
		prompt.ReplyToWBSessionID != fixture.request.PredecessorWBSessionID || result.Receipt.Kind != prompt.Kind {
		t.Fatalf("actionable typed prompt=%#v receipt=%#v err=%v", prompt, result.Receipt, err)
	}
}

func TestReceiveUsesReceiptBackedTargetIdentityWithoutManufacturingSourceTransportIndex(t *testing.T) {
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
	fixture := newReceiveFixture(t)
	injected := errors.New("crash after submission")
	fixture.options.Hooks.AfterSubmit = func() error { return injected }
	if _, err := Receive(context.Background(), fixture.options); !errors.Is(err, injected) || !errors.Is(err, ErrMessageDeliveryAmbiguous) {
		t.Fatalf("first receive error = %v", err)
	}
	firstPasteCount := countCallPrefix(fixture.tmux.calls, "paste:")
	if firstPasteCount != 1 {
		t.Fatalf("first paste count = %d", firstPasteCount)
	}
	fixture.options.Hooks = Hooks{}
	if _, err := Receive(context.Background(), fixture.options); !errors.Is(err, ErrMessageDeliveryAmbiguous) {
		t.Fatalf("receiptless intent replay error = %v, want ErrMessageDeliveryAmbiguous", err)
	}
	if got := countCallPrefix(fixture.tmux.calls, "paste:"); got != 1 {
		t.Fatalf("ambiguous replay pasted %d times", got)
	}
}

func TestReceiveAttemptsBoundedBufferCleanupOnPastePipelineFailures(t *testing.T) {
	for _, test := range []struct {
		name     string
		mutate   func(*fakeTmux)
		pastes   int
		submits  int
		retained bool
	}{
		{"load", func(fake *fakeTmux) { fake.loadErr = errors.New("load failed") }, 0, 0, false},
		{"paste", func(fake *fakeTmux) { fake.pasteErr = errors.New("paste failed") }, 1, 0, false},
		{"delete after paste", func(fake *fakeTmux) { fake.deleteErr = errors.New("delete failed") }, 1, 0, true},
		{"submit after paste", func(fake *fakeTmux) { fake.submitErr = errors.New("submit failed") }, 1, 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReceiveFixture(t)
			test.mutate(fixture.tmux)
			if _, err := Receive(context.Background(), fixture.options); !errors.Is(err, ErrMessageDeliveryAmbiguous) {
				t.Fatalf("pipeline failure = %v, want ErrMessageDeliveryAmbiguous", err)
			}
			if got := countCallPrefix(fixture.tmux.calls, "delete:"); got != 1 {
				t.Fatalf("delete-buffer attempts = %d, want 1", got)
			}
			if got := countCallPrefix(fixture.tmux.calls, "paste:"); got != test.pastes {
				t.Fatalf("paste-buffer attempts = %d, want %d", got, test.pastes)
			}
			if got := countCallPrefix(fixture.tmux.calls, "submit:"); got != test.submits {
				t.Fatalf("submit attempts = %d, want %d", got, test.submits)
			}
			if retained := fixture.tmux.buffer != nil; retained != test.retained {
				t.Fatalf("retained named buffer = %v, want %v", retained, test.retained)
			}
			fixture.tmux.loadErr, fixture.tmux.pasteErr, fixture.tmux.deleteErr, fixture.tmux.submitErr = nil, nil, nil, nil
			if _, err := Receive(context.Background(), fixture.options); !errors.Is(err, ErrMessageDeliveryAmbiguous) {
				t.Fatalf("ambiguous replay error = %v", err)
			}
			if got := countCallPrefix(fixture.tmux.calls, "paste:"); got != test.pastes {
				t.Fatalf("ambiguous replay pasted again: %d", got)
			}
		})
	}
}

func TestReceiveConcurrentExactRetriesPasteAtMostOnce(t *testing.T) {
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
	if got := countCallPrefix(fixture.tmux.calls, "paste:"); got != 1 {
		t.Fatalf("concurrent retry paste count = %d, want 1", got)
	}
	if got := countCallPrefix(fixture.tmux.calls, "submit:"); got != 1 {
		t.Fatalf("concurrent retry submit count = %d, want 1", got)
	}
}

func TestReceiveRefusesSessionOrTmuxIdentityDriftBeforeIntent(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*receiveFixture)
	}{
		{"session", func(fixture *receiveFixture) {
			fixture.session.TmuxName = "wb-session-other"
		}},
		{"pane pid", func(fixture *receiveFixture) {
			fixture.tmux.pane.PID++
		}},
		{"multiple panes", func(fixture *receiveFixture) {
			fixture.tmux.pane.Count = 2
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
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
	store   sessionmove.Store
	request sessionmove.Request
	digest  sessionmove.Digest
	receipt sessionmove.Receipt
	message sessionmove.Message
	raw     []byte
	session session.Record
	tmux    *fakeTmux
	options Options
}

func newReceiveFixture(t *testing.T) *receiveFixture {
	t.Helper()
	request := receiveRequest()
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
		TmuxName: receipt.TmuxName, PredecessorWBSessionID: receipt.PredecessorWBSessionID,
		HandoffID: receipt.HandoffID, StartedAt: receipt.StartedAt,
	}
	tmux := &fakeTmux{pane: Pane{SessionName: receipt.TmuxName, ID: "%7", PID: receipt.PID, Count: 1}}
	clock := &testClock{next: time.Date(2026, 8, 25, 12, 0, 1, 0, time.UTC)}
	fixture := &receiveFixture{store: store, request: request, digest: digest, receipt: receipt, message: message, raw: raw, session: record, tmux: tmux}
	fixture.options = Options{
		Store: store, ProjectsRoot: t.TempDir(), LocalMachine: request.TargetMachine,
		SessionDir: filepath.Join(t.TempDir(), "sessions"), RawMessage: raw, Tmux: tmux, Now: clock.Now,
		LookupSession: func(_ string, pid int) (session.Record, bool, error) {
			if pid != fixture.session.PID {
				return session.Record{}, false, fmt.Errorf("unexpected PID %d", pid)
			}
			return fixture.session, true, nil
		},
		RecordReceived: func(WorkLogRecord) error { return nil },
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
		TargetMachine: request.TargetMachine, TmuxName: "wb-session-" + request.SuccessorWBSessionID,
		Runtime: request.SourceRuntime, AttemptID: "000001-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", AttemptIndex: 1, PID: 1234,
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
