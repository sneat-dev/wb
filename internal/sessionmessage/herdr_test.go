package sessionmessage

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/herdr"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

type messageHerdrFixture struct {
	pane        herdr.Pane
	agent       herdr.Agent
	processes   herdr.PaneProcesses
	stage       string
	submissions int
	text        string
}

func (f *messageHerdrFixture) PaneGet(context.Context, string) (herdr.Pane, error) {
	if f.stage == "pane-error" {
		return herdr.Pane{}, errors.New("pane unavailable")
	}
	return f.pane, nil
}
func (f *messageHerdrFixture) AgentGet(context.Context, string) (herdr.Agent, error) {
	if f.stage == "agent-error" {
		return herdr.Agent{}, errors.New("agent unavailable")
	}
	return f.agent, nil
}
func (f *messageHerdrFixture) PaneProcessInfo(context.Context, string) (herdr.PaneProcesses, error) {
	if f.stage == "process-error" {
		return herdr.PaneProcesses{}, errors.New("process observation unavailable")
	}
	return f.processes, nil
}
func (f *messageHerdrFixture) AgentPromptSocket(_ context.Context, _ string, text string) (herdr.Agent, error) {
	f.submissions++
	f.text = text
	if f.stage == "submit-error" {
		return herdr.Agent{}, errors.New("ambiguous socket submission")
	}
	ack := f.agent
	if f.stage == "ack-drift" {
		ack.PaneID = "foreign"
	}
	return ack, nil
}

func messageHerdrAdapterFixture(t *testing.T) (*osHerdr, *messageHerdrFixture, sessionmove.Message, []byte) {
	t.Helper()
	fixture := newReceiveFixture(t)
	record := fixture.session
	record.PID = os.Getpid()
	record.ProcessStartedAt, _ = session.ObserveProcessGeneration(record.PID)
	if record.ProcessStartedAt.IsZero() {
		t.Fatal("native fixture generation unavailable")
	}
	receipt := fixture.receipt
	receipt.PID = record.PID
	native := &herdr.AgentSession{Agent: "codex", Kind: "id", Value: record.NativeHarnessID, Source: "herdr:codex"}
	fake := &messageHerdrFixture{pane: herdr.Pane{PaneID: receipt.Terminal.PaneID, WorkspaceID: receipt.Terminal.WorkspaceID, AgentKind: record.Runtime, AgentSession: native}, agent: herdr.Agent{PaneID: receipt.Terminal.PaneID, WorkspaceID: receipt.Terminal.WorkspaceID, Kind: record.Runtime, Session: native}, processes: herdr.PaneProcesses{PaneID: receipt.Terminal.PaneID, Foreground: []herdr.ForegroundProcess{{PID: record.PID}}}}
	return &osHerdr{client: fake, receipt: receipt, record: record}, fake, fixture.message, fixture.raw
}

func TestHerdrMessageAdapterRechecksNativeForegroundAndSubmitsExactCanonicalBytesOnce(t *testing.T) {
	t.Parallel()
	adapter, fake, _, raw := messageHerdrAdapterFixture(t)
	pane, err := adapter.Inspect(context.Background(), adapter.receipt, adapter.record)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Submit(context.Background(), pane, raw); err != nil {
		t.Fatal(err)
	}
	if fake.submissions != 1 || !bytes.Equal([]byte(fake.text), raw) {
		t.Fatal("exact admitted bytes or sole submission changed")
	}
	fake.processes.Foreground = nil
	if err := adapter.Submit(context.Background(), pane, raw); err == nil || fake.submissions != 1 {
		t.Fatal("recipient foreground drift was not rechecked before submit")
	}
}

func TestHerdrMessageAdapterRefusesEndpointNativeAndGenerationDrift(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"pane-error", "agent-error", "process-error", "bound-record", "bound-receipt", "invalid-terminal", "missing-native", "missing-generation", "pane-id", "workspace", "pane-runtime", "pane-native", "agent-id", "agent-workspace", "agent-runtime", "agent-native", "process-pane", "missing-pid", "duplicate-pid", "generation"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			adapter, fake, _, _ := messageHerdrAdapterFixture(t)
			record := adapter.record
			receipt := adapter.receipt
			fake.stage = kind
			switch kind {
			case "bound-record":
				record.Model = "foreign"
			case "bound-receipt":
				receipt.SuccessorWBSessionID = "wbs-foreign"
			case "invalid-terminal":
				receipt.Terminal.Socket = "relative"
				adapter.receipt = receipt
			case "missing-native":
				receipt.NativeHarnessID = ""
				adapter.receipt = receipt
			case "missing-generation":
				record.ProcessStartedAt = time.Time{}
				adapter.record = record
			case "pane-id":
				fake.pane.PaneID = "foreign"
			case "workspace":
				fake.pane.WorkspaceID = "foreign"
			case "pane-runtime":
				fake.pane.AgentKind = "foreign"
			case "pane-native":
				fake.pane.AgentSession = nil
			case "agent-id":
				fake.agent.PaneID = "foreign"
			case "agent-workspace":
				fake.agent.WorkspaceID = "foreign"
			case "agent-runtime":
				fake.agent.Kind = "foreign"
			case "agent-native":
				fake.agent.Session = nil
			case "process-pane":
				fake.processes.PaneID = "foreign"
			case "missing-pid":
				fake.processes.Foreground = nil
			case "duplicate-pid":
				fake.processes.Foreground = append(fake.processes.Foreground, fake.processes.Foreground[0])
			case "generation":
				record.ProcessStartedAt = record.ProcessStartedAt.Add(-time.Second)
				adapter.record = record
			}
			if _, err := adapter.Inspect(context.Background(), receipt, record); err == nil || fake.submissions != 0 {
				t.Fatal("unverified recipient accepted or submitted")
			}
		})
	}
}

func TestHerdrMessageSubmissionRefusesLegacyMutationAndAmbiguousAcknowledgment(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"malformed", "legacy", "noncanonical", "different-pane", "submit-error", "ack-drift"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			adapter, fake, message, raw := messageHerdrAdapterFixture(t)
			pane, err := adapter.Inspect(context.Background(), adapter.receipt, adapter.record)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "malformed":
				raw = []byte("{bad")
			case "legacy":
				message.SchemaVersion = sessionmove.LegacyMessageSchemaVersion
				raw, err = sessionmove.EncodeMessage(message)
				if err != nil {
					t.Fatal(err)
				}
			case "noncanonical":
				raw = append(raw, '\n')
			case "different-pane":
				pane.ID = "foreign"
			default:
				fake.stage = kind
			}
			if err := adapter.Submit(context.Background(), pane, raw); err == nil {
				t.Fatal("unverified message or ack accepted")
			}
			want := 0
			if kind == "submit-error" || kind == "ack-drift" {
				want = 1
			}
			if fake.submissions != want {
				t.Fatalf("automatic submissions=%d want%d", fake.submissions, want)
			}
		})
	}
	var absent *osHerdr
	if _, err := absent.Inspect(context.Background(), sessionmove.Receipt{}, session.Record{}); err == nil {
		t.Fatal("nil adapter inspect accepted")
	}
	if err := absent.Submit(context.Background(), Pane{}, nil); err == nil {
		t.Fatal("nil adapter submit accepted")
	}
	if !sameHarness("claude-code", "claude") || sameHarness("codex", "claude") {
		t.Fatal("closed runtime mapping changed")
	}
}

type messageVersionRunner struct {
	version string
	failure error
}

func (r messageVersionRunner) Run(context.Context, string, []string, []string) ([]byte, []byte, error) {
	return []byte(r.version), nil, r.failure
}

func TestHerdrMessageConstructorPinsVersionAndRecordedSocket(t *testing.T) {
	t.Parallel()
	adapter, _, _, _ := messageHerdrAdapterFixture(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"supported", "wrong-version", "version-error", "resolve-error"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			resolve := func(socket string) (*herdr.Client, error) {
				if socket != adapter.receipt.Terminal.Socket {
					t.Fatal("constructor used ambient socket")
				}
				if kind == "resolve-error" {
					return nil, errors.New("resolver unavailable")
				}
				runner := messageVersionRunner{version: "herdr 0.9.1\n"}
				if kind == "wrong-version" {
					runner.version = "herdr 0.9.2\n"
				}
				if kind == "version-error" {
					runner.failure = errors.New("version failed")
				}
				return herdr.NewClient(func(key string) (string, bool) { return executable, key == "HERDR_BIN_PATH" }, herdr.WithSocketPath(socket), herdr.WithRunner(runner))
			}
			built, err := newOSHerdrWithResolver(context.Background(), adapter.receipt, adapter.record, resolve)
			if kind == "supported" {
				if err != nil || built == nil {
					t.Fatalf("supported constructor: %v", err)
				}
			} else if err == nil {
				t.Fatal("unverified constructor accepted")
			}
		})
	}
	invalid := adapter.receipt
	invalid.Terminal.Socket = "relative"
	if _, err := newOSHerdrWithResolver(context.Background(), invalid, adapter.record, func(string) (*herdr.Client, error) { t.Fatal("invalid identity reached resolver"); return nil, nil }); err == nil {
		t.Fatal("invalid endpoint accepted")
	}
}
