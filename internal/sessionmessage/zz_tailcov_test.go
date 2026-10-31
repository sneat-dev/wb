package sessionmessage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

// The injected terminal exposes the real inspect/sole-submission boundary.
type tailCovTerminal struct {
	inspectFn func(context.Context, string) (Pane, error)
}

func (client *tailCovTerminal) Inspect(ctx context.Context, receipt sessionmove.Receipt, _ session.Record) (Pane, error) {
	return client.inspectFn(ctx, receipt.Terminal.PaneID)
}
func (client *tailCovTerminal) Submit(context.Context, Pane, []byte) error { return nil }

// tailCovClock returns scripted instants so receipt/intent ordering can be
// driven deterministically.
type tailCovClock struct {
	mu     sync.Mutex
	values []time.Time
	next   int
}

func (clock *tailCovClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	if len(clock.values) == 0 {
		return time.Now().UTC()
	}
	value := clock.values[clock.next%len(clock.values)]
	clock.next++
	return value
}

func tailCovReceiveTerminal(fixture *receiveFixture) *tailCovTerminal {
	return &tailCovTerminal{inspectFn: func(context.Context, string) (Pane, error) { return fixture.terminal.pane, nil }}
}

func TestTailCovReceiveRejectsMalformedAdmissionInput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// The nil context is exactly what this test asserts is rejected.
	//nolint:staticcheck // SA1012: passing nil is the behaviour under test.
	if _, err := Receive(nil, Options{}); err == nil || !strings.Contains(err.Error(), "context is required") {
		t.Fatalf("nil context err = %v", err)
	}

	fixture := newReceiveFixture(t)
	fixture.options.RawMessage = []byte("{not json")
	if _, err := Receive(ctx, fixture.options); err == nil {
		t.Fatal("malformed JSON must be rejected")
	}

	fixture = newReceiveFixture(t)
	nonCanonical := append([]byte(" "), fixture.raw...)
	fixture.options.RawMessage = nonCanonical
	if _, err := Receive(ctx, fixture.options); err == nil || !strings.Contains(err.Error(), "canonical JSON encoding") {
		t.Fatalf("non-canonical bytes err = %v", err)
	}

	fixture = newReceiveFixture(t)
	fixture.options.Store = sessionmove.NewStore(filepath.Join(t.TempDir(), "empty"))
	if _, err := Receive(ctx, fixture.options); err == nil {
		t.Fatal("an unadmitted handoff must be rejected")
	}
}

func TestTailCovReceiveRejectsWrongMachineAndMessageLineage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	fixture := newReceiveFixture(t)
	fixture.options.LocalMachine = "elsewhere"
	if _, err := Receive(ctx, fixture.options); err == nil || !strings.Contains(err.Error(), "targets machine") {
		t.Fatalf("wrong machine err = %v", err)
	}

	fixture = newReceiveFixture(t)
	fixture.options.LocalMachine = ""
	if _, err := Receive(ctx, fixture.options); err == nil || !strings.Contains(err.Error(), "targets machine") {
		t.Fatalf("blank machine err = %v", err)
	}

	fixture = newReceiveFixture(t)
	fixture.message.SentAt = fixture.request.CreatedAt.Add(-time.Hour)
	raw, err := sessionmove.EncodeMessage(fixture.message)
	if err != nil {
		t.Fatal(err)
	}
	fixture.options.RawMessage = raw
	if _, err := Receive(ctx, fixture.options); err == nil || !strings.Contains(err.Error(), "predates") {
		t.Fatalf("stale message err = %v", err)
	}
}

func TestTailCovReceiveRequiresCompletedHandoffReceipt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	fixture := newReceiveFixture(t)
	fixture.store = sessionmove.NewStore(filepath.Join(t.TempDir(), "noreceipt"))
	requestRaw, err := sessionmove.EncodeRequest(fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	digest := sessionmove.DigestBytes(requestRaw)
	if _, err := fixture.store.Admit(requestRaw, digest); err != nil {
		t.Fatal(err)
	}
	fixture.options.Store = fixture.store
	fixture.options.LocalMachine = fixture.request.TargetMachine
	if _, err := Receive(ctx, fixture.options); err == nil || !strings.Contains(err.Error(), "durable completed successor receipt") {
		t.Fatalf("missing receipt err = %v", err)
	}

	fixture = newReceiveFixture(t)
	// A receipt whose immutable target identity does not match the admitted
	// request can never be published through the store, so plant it directly
	// the way a corrupted store would present it.
	broken := receiveReceipt(fixture.request, fixture.digest)
	broken.Model = "a-different-model"
	brokenRaw, err := sessionmove.EncodeReceipt(broken)
	if err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(fixture.store.Root, fixture.request.HandoffID, "receipt.json")
	if err := os.WriteFile(receiptPath, brokenRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Receive(ctx, fixture.options); err == nil {
		t.Fatal("a receipt that disagrees with its request must be rejected")
	}
}

func TestTailCovReceiveFailsWhenStoreProjectionIsUnreadable(t *testing.T) {
	t.Parallel()
	fixture := newReceiveFixture(t)
	// Replace the events directory with a regular file so the locked projection
	// load fails after the execution fence is held but before any durable paste
	// intent exists.
	eventsPath := filepath.Join(fixture.store.Root, fixture.request.HandoffID, "events")
	if err := os.RemoveAll(eventsPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(eventsPath, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Receive(context.Background(), fixture.options); err == nil {
		t.Fatal("Receive must fail when the locked projection cannot be loaded")
	}
}

func TestTailCovReceiveFailsWhenExecutionFenceCannotBeOpened(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permission bits do not deny writes")
	}
	fixture := newReceiveFixture(t)
	handoffDir := filepath.Join(fixture.store.Root, fixture.request.HandoffID)
	// Remove any pre-created fence so the acquisition has to create it.
	_ = os.Remove(filepath.Join(handoffDir, "receive.lock"))
	if err := os.Chmod(handoffDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(handoffDir, 0o700) })

	if _, err := Receive(context.Background(), fixture.options); err == nil {
		t.Fatal("Receive must fail when the execution fence cannot be created")
	}
}

func TestTailCovReceiveCorroboratesTheLiveRegisteredRecipient(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	fixture := newReceiveFixture(t)
	fixture.options.LookupSession = func(string, int) (session.Record, bool, error) {
		return session.Record{}, false, errors.New("registry unavailable")
	}
	if _, err := Receive(ctx, fixture.options); err == nil || !strings.Contains(err.Error(), "corroborate registered message recipient") {
		t.Fatalf("lookup error = %v", err)
	}

	fixture = newReceiveFixture(t)
	fixture.options.LookupSession = func(string, int) (session.Record, bool, error) {
		return fixture.session, false, nil
	}
	if _, err := Receive(ctx, fixture.options); err == nil || !strings.Contains(err.Error(), "does not match the live completed handoff recipient") {
		t.Fatalf("dead session err = %v", err)
	}

	fixture = newReceiveFixture(t)
	fixture.session.Model = "a-different-model"
	fixture.options.LookupSession = func(string, int) (session.Record, bool, error) {
		return fixture.session, true, nil
	}
	if _, err := Receive(ctx, fixture.options); err == nil || !strings.Contains(err.Error(), "does not match the live completed handoff recipient") {
		t.Fatalf("drifted session err = %v", err)
	}
}

func TestTailCovMatchesRecipientIdentityRules(t *testing.T) {
	t.Parallel()
	fixture := newReceiveFixture(t)
	base := fixture.session

	if !matchesRecipient(base, fixture.request, fixture.receipt) {
		t.Fatal("the exact registered successor must match")
	}

	fallback := base
	fallback.NativeHarnessID = ""
	fallback.AgentID = "agent-7"
	withAgent := fixture.receipt
	withAgent.NativeHarnessID = "agent-7"
	if !matchesRecipient(fallback, fixture.request, withAgent) {
		t.Fatal("an agent-ID-only record must match the receipt's harness identity")
	}

	drifted := base
	drifted.StartedAt = base.StartedAt.Add(time.Second)
	if matchesRecipient(drifted, fixture.request, fixture.receipt) {
		t.Fatal("a record started at another instant must not match")
	}

	predecessor := base
	predecessor.PredecessorWBSessionID = "wbs-other-source"
	if matchesRecipient(predecessor, fixture.request, fixture.receipt) {
		t.Fatal("a record from another handoff lineage must not match")
	}
}

func TestTailCovReceiveUsesDefaultSessionLookup(t *testing.T) {
	t.Parallel()
	fixture := newReceiveFixture(t)
	fixture.options.LookupSession = nil
	fixture.options.SessionDir = t.TempDir()
	if _, err := Receive(context.Background(), fixture.options); err == nil {
		t.Fatal("the default exact lookup must not invent a registered session")
	}
}

func TestReceiveFailsWhenNoHerdrExecutableExists(t *testing.T) {
	fixture := newReceiveFixture(t)
	fixture.options.Terminal = nil
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HERDR_BIN_PATH", "")
	if _, err := Receive(context.Background(), fixture.options); err == nil || !strings.Contains(err.Error(), "herdr") {
		t.Fatalf("missing Herdr err = %v", err)
	}
}

func TestReceiveRejectsHerdrInspectorFailure(t *testing.T) {
	t.Parallel()
	fixture := newReceiveFixture(t)
	fixture.options.Terminal = &tailCovTerminal{
		inspectFn: func(context.Context, string) (Pane, error) {
			return Pane{}, errors.New("Herdr server gone")
		},
	}
	if _, err := Receive(context.Background(), fixture.options); err == nil || !strings.Contains(err.Error(), "Herdr server gone") {
		t.Fatalf("inspect failure err = %v", err)
	}
}

func TestTailCovReceiveUsesDefaultWorkLogRecorder(t *testing.T) {
	t.Parallel()
	fixture := newReceiveFixture(t)
	fixture.options.RecordReceived = nil
	fixture.options.Terminal = tailCovReceiveTerminal(fixture)
	if _, err := Receive(context.Background(), fixture.options); err == nil {
		t.Fatal("the default recorder must fail without a durable external target claim")
	} else if !strings.Contains(err.Error(), "record target session message receipt") {
		t.Fatalf("default recorder err = %v", err)
	}
}

func TestTailCovReceiveReportsWorkLogRecorderFailure(t *testing.T) {
	t.Parallel()
	fixture := newReceiveFixture(t)
	injected := errors.New("work log down")
	fixture.options.RecordReceived = func(WorkLogRecord) error { return injected }
	fixture.options.Terminal = tailCovReceiveTerminal(fixture)
	if _, err := Receive(context.Background(), fixture.options); !errors.Is(err, injected) {
		t.Fatalf("recorder failure err = %v", err)
	}
	if got := countCallPrefix(fixture.terminal.calls, "submit:"); got != 0 {
		t.Fatalf("recorder failure pasted %d times", got)
	}
}

func TestTailCovReceiveRejectsConflictingDurableInboxBytes(t *testing.T) {
	t.Parallel()
	fixture := newReceiveFixture(t)
	other := fixture.message
	other.Body = "a completely different instruction"
	otherRaw, err := sessionmove.EncodeMessage(other)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := fixture.store.AcquireExecutionLock(context.Background(), fixture.request.HandoffID, fixture.digest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.AdmitIncomingMessageUnderLock(lock, fixture.request.HandoffID, fixture.digest, otherRaw, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	_ = lock.Close()

	if _, err := Receive(context.Background(), fixture.options); err == nil {
		t.Fatal("a conflicting durable message ID must be rejected")
	}
}

func TestTailCovReceiveRejectsReceiptThatPredatesItsPasteIntent(t *testing.T) {
	t.Parallel()
	fixture := newReceiveFixture(t)
	base := time.Date(2026, 8, 25, 12, 0, 1, 0, time.UTC)
	// recordedAt, then intent one second later, then a pasted-at that moves
	// backwards so the durable receipt cannot match its paste intent.
	fixture.options.Now = (&tailCovClock{values: []time.Time{base, base.Add(time.Second), base}}).Now
	fixture.options.Terminal = tailCovReceiveTerminal(fixture)
	if _, err := Receive(context.Background(), fixture.options); err == nil {
		t.Fatal("a receipt pasted before its intent must be rejected")
	} else if !errors.Is(err, ErrMessagePasteAmbiguous) {
		t.Fatalf("receipt ordering err = %v, want ambiguous paste outcome", err)
	}
}

func TestTailCovReceiveWithoutInjectedClockUsesWallTime(t *testing.T) {
	t.Parallel()
	fixture := newReceiveFixture(t)
	fixture.options.Now = nil
	fixture.options.Terminal = tailCovReceiveTerminal(fixture)
	result, err := Receive(context.Background(), fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt.PastedAt.IsZero() || result.Receipt.PastedAt.Before(result.Receipt.RecordedAt) {
		t.Fatalf("receipt = %#v", result.Receipt)
	}
}
