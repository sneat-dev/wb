package agentmessage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionmove"
)

func relayRunner(t *testing.T, m Message, mode string, calls *int) CommandRunner {
	t.Helper()
	return func(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		*calls++
		joined := strings.Join(args, " ")
		if name != "ssh" || !strings.Contains(joined, "StrictHostKeyChecking=yes") {
			t.Fatal(args)
		}
		if mode == "transport-error" {
			return errors.New("offline")
		}
		if strings.Contains(joined, "message receive") {
			if !strings.Contains(joined, "--wait 1m") {
				t.Fatal(args)
			}
			switch mode {
			case "malformed":
				_, _ = io.WriteString(stdout, "broken")
			case "wrong-agent":
				m.To = "other"
				_ = json.NewEncoder(stdout).Encode([]Message{m})
			case "bad-message":
				m.Version = 99
				_ = json.NewEncoder(stdout).Encode([]Message{m})
			case "too-many":
				messages := make([]Message, 101)
				_ = json.NewEncoder(stdout).Encode(messages)
			default:
				_ = json.NewEncoder(stdout).Encode([]Message{m})
			}
			return nil
		}
		if !strings.Contains(joined, "message ack "+m.ID+" --agent "+m.To) {
			t.Fatal(args)
		}
		if mode == "ack-error" {
			return errors.New("ack lost")
		}
		if mode == "bad-ack" {
			_, _ = io.WriteString(stdout, `{}`)
		} else {
			_, _ = fmt.Fprintf(stdout, `{"id":%q,"status":"acknowledged"}`, m.ID)
		}
		return nil
	}
}

func TestRelayAcknowledgesOnlyDurableLocalCopies(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"ok", "ambiguous", "preflight-failed", "local-failed", "ack-error", "bad-ack", "transport-error", "malformed", "wrong-agent", "bad-message", "too-many"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			s := testStore(t)
			m := sampleMessage()
			calls := 0
			switch mode {
			case "ambiguous", "preflight-failed":
				s.Notify = func(context.Context, Message) (string, error) { return mode, errors.New("wake failed") }
			case "local-failed":
				s.Root = "relative"
			}
			ssh := SSH{Config: sessionmove.SSHConfig{Host: "vm", User: "agent", WBPath: "/usr/local/bin/wb"}, Run: relayRunner(t, m, mode, &calls)}
			e := ssh.Relay(context.Background(), s, m.To, true)
			if mode == "ok" || mode == "ambiguous" {
				if e != nil || calls != 2 {
					t.Fatal(calls, e)
				}
				messages, e := s.Receive(context.Background(), m.To, 0)
				if e != nil || len(messages) != 1 {
					t.Fatal(messages, e)
				}
			} else if e == nil {
				t.Fatal("failure lost")
			}
			if mode == "preflight-failed" || mode == "local-failed" {
				if calls != 1 {
					t.Fatal("remote acknowledged before safe local delivery")
				}
			}
		})
	}
}

func TestRelayReconnectsAndCancelsBackoff(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	m := sampleMessage()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	calls := 0
	reports := 0
	ssh := SSH{ReportRetry: func(err error) {
		reports++
		if err == nil {
			t.Fatal("missing retry reason")
		}
	}, Config: sessionmove.SSHConfig{Host: "vm"}, Run: func(_ context.Context, _ string, args []string, _ io.Reader, stdout, stderr io.Writer) error {
		calls++
		if calls == 1 {
			return errors.New("temporary offline")
		}
		cancel()
		_, _ = io.WriteString(stdout, "[]")
		return nil
	}}
	if e := ssh.Relay(ctx, s, m.To, false); !errors.Is(e, context.Canceled) || calls != 2 || reports != 1 {
		t.Fatal(calls, e, reports)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Millisecond)
	t.Cleanup(cancel)
	ssh.Run = func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error {
		return errors.New("offline")
	}
	if e := ssh.Relay(ctx, s, m.To, false); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
}

func TestRelayRejectsUnsafeAddressesAndCancelledNativeTransport(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	if e := (SSH{}).Relay(context.Background(), s, "mac", true); e == nil {
		t.Fatal("missing host accepted")
	}
	ssh := SSH{Config: sessionmove.SSHConfig{Host: "not-contacted"}}
	if e := ssh.Relay(context.Background(), s, "../bad", true); e == nil {
		t.Fatal("bad agent accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := ssh.Relay(ctx, s, "mac", true); e == nil {
		t.Fatal("cancel ignored")
	}
}
