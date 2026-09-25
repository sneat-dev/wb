package sessionmessage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/testenv"
)

// tailCovTmux is a fully injectable tmux adapter so every paste-pipeline
// failure branch can be exercised without a real terminal.
type tailCovTmux struct {
	inspectFn func(context.Context, string) (Pane, error)
	loadFn    func(context.Context, string, []byte) error
	saveFn    func(context.Context, string) ([]byte, error)
	pasteFn   func(context.Context, string, string) error
	deleteFn  func(context.Context, string) error
	calls     []string
	mu        sync.Mutex
}

func (client *tailCovTmux) record(call string) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.calls = append(client.calls, call)
}

func (client *tailCovTmux) Inspect(_ context.Context, name string) (Pane, error) {
	client.record("inspect:" + name)
	if client.inspectFn == nil {
		return Pane{}, errors.New("tailCovTmux: Inspect is not configured")
	}
	return client.inspectFn(context.Background(), name)
}

func (client *tailCovTmux) LoadBuffer(_ context.Context, name string, raw []byte) error {
	client.record("load:" + name)
	if client.loadFn == nil {
		return nil
	}
	return client.loadFn(context.Background(), name, raw)
}

func (client *tailCovTmux) SaveBuffer(_ context.Context, name string) ([]byte, error) {
	client.record("save:" + name)
	if client.saveFn == nil {
		return nil, errors.New("tailCovTmux: SaveBuffer is not configured")
	}
	return client.saveFn(context.Background(), name)
}

func (client *tailCovTmux) PasteBuffer(_ context.Context, name, paneID string) error {
	client.record("paste:" + name + ":" + paneID)
	if client.pasteFn == nil {
		return nil
	}
	return client.pasteFn(context.Background(), name, paneID)
}

func (client *tailCovTmux) DeleteBuffer(_ context.Context, name string) error {
	client.record("delete:" + name)
	if client.deleteFn == nil {
		return nil
	}
	return client.deleteFn(context.Background(), name)
}

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

func tailCovReceiveTmux(fixture *receiveFixture) *tailCovTmux {
	var buffer []byte
	client := &tailCovTmux{}
	client.inspectFn = func(_ context.Context, _ string) (Pane, error) {
		return fixture.tmux.pane, nil
	}
	client.loadFn = func(_ context.Context, _ string, raw []byte) error {
		buffer = append([]byte(nil), raw...)
		return nil
	}
	client.saveFn = func(_ context.Context, _ string) ([]byte, error) {
		return append([]byte(nil), buffer...), nil
	}
	client.deleteFn = func(_ context.Context, _ string) error {
		buffer = nil
		return nil
	}
	return client
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

func TestTailCovReceiveFailsWhenNoTmuxExecutableExists(t *testing.T) {
	fixture := newReceiveFixture(t)
	fixture.options.Tmux = nil
	t.Setenv("PATH", t.TempDir())
	if _, err := Receive(context.Background(), fixture.options); err == nil || !strings.Contains(err.Error(), "tmux executable") {
		t.Fatalf("missing tmux err = %v", err)
	}
}

func TestTailCovReceiveRejectsTmuxInspectorFailure(t *testing.T) {
	t.Parallel()
	fixture := newReceiveFixture(t)
	fixture.options.Tmux = &tailCovTmux{
		inspectFn: func(context.Context, string) (Pane, error) {
			return Pane{}, errors.New("tmux server gone")
		},
	}
	if _, err := Receive(context.Background(), fixture.options); err == nil || !strings.Contains(err.Error(), "tmux server gone") {
		t.Fatalf("inspect failure err = %v", err)
	}
}

func TestTailCovReceiveUsesDefaultWorkLogRecorder(t *testing.T) {
	t.Parallel()
	fixture := newReceiveFixture(t)
	fixture.options.RecordReceived = nil
	fixture.options.Tmux = tailCovReceiveTmux(fixture)
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
	fixture.options.Tmux = tailCovReceiveTmux(fixture)
	if _, err := Receive(context.Background(), fixture.options); !errors.Is(err, injected) {
		t.Fatalf("recorder failure err = %v", err)
	}
	if got := countCallPrefix(fixture.tmux.calls, "paste:"); got != 0 {
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
	fixture.options.Tmux = tailCovReceiveTmux(fixture)
	if _, err := Receive(context.Background(), fixture.options); err == nil {
		t.Fatal("a receipt pasted before its intent must be rejected")
	} else if !errors.Is(err, ErrMessagePasteAmbiguous) {
		t.Fatalf("receipt ordering err = %v, want ambiguous paste outcome", err)
	}
}

func TestTailCovPasteExactCleansUpOnEveryFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	raw := []byte(`{"kind":"text"}`)

	t.Run("load", func(t *testing.T) {
		t.Parallel()
		injected := errors.New("load failed")
		client := &tailCovTmux{loadFn: func(context.Context, string, []byte) error { return injected }}
		err := pasteExact(ctx, client, "wb-message-x", "%7", raw)
		if !errors.Is(err, injected) {
			t.Fatalf("err = %v", err)
		}
		if got := countCallPrefix(client.calls, "delete:"); got != 1 {
			t.Fatalf("delete attempts = %d", got)
		}
	})

	t.Run("save", func(t *testing.T) {
		t.Parallel()
		injected := errors.New("save failed")
		client := &tailCovTmux{saveFn: func(context.Context, string) ([]byte, error) { return nil, injected }}
		err := pasteExact(ctx, client, "wb-message-x", "%7", raw)
		if !errors.Is(err, injected) {
			t.Fatalf("err = %v", err)
		}
		if got := countCallPrefix(client.calls, "delete:"); got != 1 {
			t.Fatalf("delete attempts = %d", got)
		}
	})

	t.Run("save mismatch", func(t *testing.T) {
		t.Parallel()
		client := &tailCovTmux{saveFn: func(context.Context, string) ([]byte, error) {
			return []byte("tampered"), nil
		}}
		err := pasteExact(ctx, client, "wb-message-x", "%7", raw)
		if err == nil || !strings.Contains(err.Error(), "exact message bytes") {
			t.Fatalf("err = %v", err)
		}
		if got := countCallPrefix(client.calls, "delete:"); got != 1 {
			t.Fatalf("delete attempts = %d", got)
		}
	})

	t.Run("paste", func(t *testing.T) {
		t.Parallel()
		injected := errors.New("paste failed")
		client := &tailCovTmux{
			saveFn:  func(context.Context, string) ([]byte, error) { return raw, nil },
			pasteFn: func(context.Context, string, string) error { return injected },
		}
		err := pasteExact(ctx, client, "wb-message-x", "%7", raw)
		if !errors.Is(err, injected) {
			t.Fatalf("err = %v", err)
		}
		if got := countCallPrefix(client.calls, "delete:"); got != 1 {
			t.Fatalf("delete attempts = %d", got)
		}
	})

	t.Run("delete after paste", func(t *testing.T) {
		t.Parallel()
		injected := errors.New("delete failed")
		client := &tailCovTmux{
			saveFn:   func(context.Context, string) ([]byte, error) { return raw, nil },
			deleteFn: func(context.Context, string) error { return injected },
		}
		err := pasteExact(ctx, client, "wb-message-x", "%7", raw)
		if !errors.Is(err, injected) {
			t.Fatalf("err = %v", err)
		}
		if got := countCallPrefix(client.calls, "paste:"); got != 1 {
			t.Fatalf("paste attempts = %d", got)
		}
	})

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		client := &tailCovTmux{saveFn: func(context.Context, string) ([]byte, error) { return raw, nil }}
		if err := pasteExact(ctx, client, "wb-message-x", "%7", raw); err != nil {
			t.Fatal(err)
		}
	})
}

func TestTailCovReceiveWithoutInjectedClockUsesWallTime(t *testing.T) {
	t.Parallel()
	fixture := newReceiveFixture(t)
	fixture.options.Now = nil
	fixture.options.Tmux = tailCovReceiveTmux(fixture)
	result, err := Receive(context.Background(), fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt.PastedAt.IsZero() || result.Receipt.PastedAt.Before(result.Receipt.RecordedAt) {
		t.Fatalf("receipt = %#v", result.Receipt)
	}
}

// --- tmux adapter ---------------------------------------------------------

func TestTailCovExecTmuxCommandRunnerPropagatesExitFailures(t *testing.T) {
	t.Parallel()
	runner := execTmuxCommandRunner{}
	var stdout, stderr bytes.Buffer
	err := runner.Run(context.Background(), filepath.Join(t.TempDir(), "does-not-exist"), nil, nil, &stdout, &stderr)
	if err == nil {
		t.Fatal("running a missing executable must fail")
	}
}

func TestTailCovNewOSTmuxResolution(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "tmux")
	if err := testenv.WriteExecutableFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	client, err := newOSTmux()
	if err != nil {
		t.Fatal(err)
	}
	if client.executable != script {
		t.Fatalf("executable = %q, want %q", client.executable, script)
	}
	if _, ok := client.runner.(execTmuxCommandRunner); !ok {
		t.Fatalf("runner = %T, want execTmuxCommandRunner", client.runner)
	}

	t.Setenv("PATH", t.TempDir())
	if _, err := newOSTmux(); err == nil || !strings.Contains(err.Error(), "resolve fixed tmux executable") {
		t.Fatalf("missing tmux err = %v", err)
	}
}

func TestTailCovNewOSTmuxRejectsRelativePATHEntry(t *testing.T) {
	base := t.TempDir()
	bin := filepath.Join(base, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(base)
	t.Setenv("PATH", "bin")

	// Modern Go refuses a relative PATH hit outright.
	if _, err := newOSTmux(); err == nil {
		t.Fatal("a relative PATH entry must be refused")
	}

	// With the legacy execerrdot behaviour restored, LookPath hands back the
	// relative path and newOSTmux must reject it explicitly.
	t.Setenv("GODEBUG", "execerrdot=0")
	if _, err := newOSTmux(); err == nil || !strings.Contains(err.Error(), "clean absolute path") {
		t.Fatalf("relative resolved path err = %v", err)
	}
}

func TestTailCovOSTmuxInspectRejectsMalformedPaneListings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		stdout string
		err    error
		want   string
	}{
		{name: "run error", err: errors.New("boom"), want: "inspect exact tmux successor"},
		{name: "empty", stdout: "", want: "want exactly one"},
		{name: "blank line", stdout: "\n", want: "want exactly one"},
		{name: "wrong session", stdout: "other\t%7\t1234\n", want: "invalid exact-pane identity"},
		{name: "bad pane id", stdout: "wb-session-wbs-successor\t7\t1234\n", want: "invalid exact-pane identity"},
		{name: "missing field", stdout: "wb-session-wbs-successor\t%7\n", want: "invalid exact-pane identity"},
		{name: "bad pid", stdout: "wb-session-wbs-successor\t%7\tnope\n", want: "invalid pane PID"},
		{name: "zero pid", stdout: "wb-session-wbs-successor\t%7\t0\n", want: "invalid pane PID"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := &osTmux{executable: "/usr/bin/tmux", runner: &scriptedTmuxRunner{
				runs: []tmuxRun{{stdout: []byte(test.stdout), err: test.err}},
			}}
			pane, err := client.Inspect(context.Background(), "wb-session-wbs-successor")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Inspect = %#v, err = %v, want %q", pane, err, test.want)
			}
			if pane.Count != 0 {
				t.Fatalf("pane = %#v, want zero value", pane)
			}
		})
	}
}

func TestTailCovOSTmuxRunGuardsAndDiagnostics(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	if _, err := (&osTmux{}).run(ctx, nil, nil, 16); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("zero client err = %v", err)
	}
	if _, err := (&osTmux{executable: "/usr/bin/tmux"}).run(ctx, nil, nil, 16); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("nil runner err = %v", err)
	}
	if _, err := (&osTmux{runner: execTmuxCommandRunner{}}).run(ctx, nil, nil, 16); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("empty executable err = %v", err)
	}

	silent := &osTmux{executable: "/usr/bin/tmux", runner: &scriptedTmuxRunner{
		runs: []tmuxRun{{err: errors.New("silent failure")}},
	}}
	if _, err := silent.run(ctx, nil, nil, 16); err == nil || err.Error() != "silent failure" {
		t.Fatalf("silent failure err = %v", err)
	}

	noisy := &osTmux{executable: "/usr/bin/tmux", runner: &tailCovFailingRunner{stderr: "  fatal:   lost  server  \n"}}
	if _, err := noisy.run(ctx, nil, nil, 16); err == nil || !strings.Contains(err.Error(), "lost server") {
		t.Fatalf("noisy failure err = %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	blocked := &osTmux{executable: "/usr/bin/tmux", runner: &tailCovFailingRunner{waitForContext: true}}
	if _, err := blocked.run(cancelled, nil, nil, 16); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled err = %v, want context.Canceled", err)
	}

	limited := &osTmux{executable: "/usr/bin/tmux", runner: &tailCovFailingRunner{overflow: true}}
	if _, err := limited.run(ctx, nil, nil, 4); err == nil || !strings.Contains(err.Error(), "exceeds 4 bytes") {
		t.Fatalf("overflow err = %v", err)
	}

	echo := &osTmux{executable: "/usr/bin/tmux", runner: &tailCovFailingRunner{stdout: "hello", succeed: true}}
	got, err := echo.run(ctx, nil, nil, 16)
	if err != nil || string(got) != "hello" {
		t.Fatalf("run = %q, err = %v", got, err)
	}
}

type tailCovFailingRunner struct {
	stdout         string
	stderr         string
	overflow       bool
	succeed        bool
	waitForContext bool
}

func (runner *tailCovFailingRunner) Run(ctx context.Context, _ string, _ []string, _ []byte, stdout, stderr io.Writer) error {
	if runner.waitForContext {
		<-ctx.Done()
		return ctx.Err()
	}
	if runner.overflow {
		_, _ = io.WriteString(stdout, "0123456789")
		return nil
	}
	if runner.stdout != "" {
		_, _ = io.WriteString(stdout, runner.stdout)
	}
	if runner.stderr != "" {
		_, _ = io.WriteString(stderr, runner.stderr)
	}
	if runner.succeed {
		return nil
	}
	return errors.New("tmux failed")
}

func TestTailCovLimitedBufferTruncatesAndFlagsOverflow(t *testing.T) {
	t.Parallel()
	var buffer limitedBuffer
	buffer.limit = 4

	if written, err := buffer.Write([]byte("ab")); err != nil || written != 2 {
		t.Fatalf("Write = %d, %v", written, err)
	}
	if buffer.exceeded {
		t.Fatal("a write within the limit must not flag overflow")
	}

	if written, err := buffer.Write([]byte("cdef")); err != nil || written != 4 {
		t.Fatalf("Write = %d, %v", written, err)
	}
	if buffer.buffer.String() != "abcd" || !buffer.exceeded {
		t.Fatalf("buffer = %q, exceeded = %t", buffer.buffer.String(), buffer.exceeded)
	}

	// The buffer is now full: further writes are dropped but reported as
	// accepted so the command under observation is never blocked.
	before := buffer.buffer.Len()
	if written, err := buffer.Write([]byte("zzz")); err != nil || written != 3 {
		t.Fatalf("Write = %d, %v", written, err)
	}
	if buffer.buffer.Len() != before {
		t.Fatalf("full buffer grew to %d bytes", buffer.buffer.Len())
	}

	empty := &limitedBuffer{limit: 4}
	if written, err := empty.Write(nil); err != nil || written != 0 {
		t.Fatalf("empty Write = %d, %v", written, err)
	}
	if empty.exceeded {
		t.Fatal("an empty write must not flag overflow")
	}
}
