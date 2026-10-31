package sessionmessage

import (
	"bytes"
	"context"
	"fmt"
	"os"

	"github.com/sneat-dev/wb/internal/herdr"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

type messageHerdrClient interface {
	PaneGet(context.Context, string) (herdr.Pane, error)
	AgentGet(context.Context, string) (herdr.Agent, error)
	PaneProcessInfo(context.Context, string) (herdr.PaneProcesses, error)
	AgentPromptSocket(context.Context, string, string) (herdr.Agent, error)
}

type osHerdr struct {
	client  messageHerdrClient
	receipt sessionmove.Receipt
	record  session.Record
}

func newOSHerdr(ctx context.Context, receipt sessionmove.Receipt, record session.Record) (*osHerdr, error) {
	return newOSHerdrWithResolver(ctx, receipt, record, func(socket string) (*herdr.Client, error) {
		return herdr.NewClient(os.LookupEnv, herdr.WithSocketPath(socket))
	})
}
func newOSHerdrWithResolver(ctx context.Context, receipt sessionmove.Receipt, record session.Record, resolve func(string) (*herdr.Client, error)) (*osHerdr, error) {
	if err := receipt.Terminal.Validate(); err != nil {
		return nil, err
	}
	client, err := resolve(receipt.Terminal.Socket)
	if err != nil {
		return nil, err
	}
	if err := client.EnsureTUIProtocol(ctx); err != nil {
		return nil, err
	}
	return &osHerdr{client: client, receipt: receipt, record: record}, nil
}

func sameHarness(runtime, kind string) bool {
	return runtime == kind || runtime == "claude-code" && kind == "claude"
}

func (client *osHerdr) Inspect(ctx context.Context, receipt sessionmove.Receipt, record session.Record) (Pane, error) {
	if client == nil || client.client == nil {
		return Pane{}, fmt.Errorf("Herdr message adapter is unavailable")
	}
	if receipt.Terminal != client.receipt.Terminal || receipt.SuccessorWBSessionID != client.receipt.SuccessorWBSessionID || record != client.record {
		return Pane{}, fmt.Errorf("message recipient differs from bound Herdr adapter")
	}
	if err := receipt.Terminal.Validate(); err != nil {
		return Pane{}, err
	}
	if receipt.NativeHarnessID == "" || record.ProcessStartedAt.IsZero() {
		return Pane{}, fmt.Errorf("Herdr message recipient lacks exact native session/process generation")
	}
	pane, err := client.client.PaneGet(ctx, receipt.Terminal.PaneID)
	if err != nil {
		return Pane{}, err
	}
	if pane.PaneID != receipt.Terminal.PaneID || pane.WorkspaceID != receipt.Terminal.WorkspaceID || !sameHarness(receipt.Runtime, pane.AgentKind) || pane.AgentSession.HarnessSessionID() != receipt.NativeHarnessID {
		return Pane{}, fmt.Errorf("Herdr pane differs from recorded workspace/native harness")
	}
	agent, err := client.client.AgentGet(ctx, pane.PaneID)
	if err != nil {
		return Pane{}, err
	}
	if agent.PaneID != pane.PaneID || agent.WorkspaceID != pane.WorkspaceID || !sameHarness(receipt.Runtime, agent.Kind) || agent.HarnessSessionID() != receipt.NativeHarnessID {
		return Pane{}, fmt.Errorf("Herdr agent differs from exact recorded session")
	}
	processes, err := client.client.PaneProcessInfo(ctx, pane.PaneID)
	if err != nil {
		return Pane{}, err
	}
	if processes.PaneID != pane.PaneID {
		return Pane{}, fmt.Errorf("Herdr foreground belongs to another pane/workspace")
	}
	matches := 0
	for _, process := range processes.Foreground {
		if process.PID == record.PID {
			matches++
		}
	}
	observed, known := session.ObserveProcessGeneration(record.PID)
	if matches != 1 || !known || !observed.Equal(record.ProcessStartedAt) {
		return Pane{}, fmt.Errorf("Herdr foreground does not contain the exact recipient process generation")
	}
	return Pane{ID: pane.PaneID, PID: record.PID, Count: 1, Terminal: receipt.Terminal, NativeHarnessID: receipt.NativeHarnessID}, nil
}

func (client *osHerdr) Submit(ctx context.Context, expected Pane, raw []byte) error {
	if client == nil || client.client == nil {
		return fmt.Errorf("Herdr message adapter is unavailable")
	}
	message, err := sessionmove.DecodeMessage(raw)
	if err != nil {
		return err
	}
	if err := sessionmove.RequireActionableMessage(message); err != nil {
		return err
	}
	canonical, err := sessionmove.EncodeMessage(message)
	if err != nil {
		return err
	}
	if !bytes.Equal(canonical, raw) {
		return fmt.Errorf("Herdr submission requires exact admitted canonical bytes")
	}
	current, err := client.Inspect(ctx, client.receipt, client.record)
	if err != nil {
		return err
	}
	if current != expected {
		return fmt.Errorf("Herdr message recipient changed after durable intent")
	}
	ack, err := client.client.AgentPromptSocket(ctx, expected.ID, string(raw))
	if err != nil {
		return err
	}
	if ack.PaneID != expected.ID || ack.WorkspaceID != expected.Terminal.WorkspaceID || ack.HarnessSessionID() != expected.NativeHarnessID || !sameHarness(client.record.Runtime, ack.Kind) {
		return fmt.Errorf("Herdr submission acknowledgment differs from exact recipient")
	}
	return nil
}
