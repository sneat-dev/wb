package cmdmessage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/agentmessage"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

func fixture(t *testing.T) (shared.Runtime, Dependencies, agentmessage.Message) {
	t.Helper()
	path, err := os.MkdirTemp("", "wb-msg-cli-")
	if err != nil {
		t.Fatal(err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	root := filepath.Join(path, "messages")
	return shared.Runtime{Flags: func() shared.Flags { return shared.Flags{} }, ExitError: func(_ int, s string) error { return errors.New(s) }}, Dependencies{Store: func(string) (agentmessage.Store, error) { return agentmessage.Store{Root: root}, nil }, Send: func(_ context.Context, c sessionmove.SSHConfig, m agentmessage.Message) (agentmessage.Receipt, error) {
		return agentmessage.Receipt{ID: m.ID, Digest: m.Digest(), Status: "durable", Wake: "not-bound"}, nil
	}}, agentmessage.Message{Version: 1, ID: "0123456789abcdef0123456789abcdef", From: "vm", To: "mac", Body: "hello"}
}
func execute(t *testing.T, runtime shared.Runtime, deps Dependencies, input io.Reader, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := New(runtime, deps)
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetIn(input)
	cmd.SetArgs(args)
	e := cmd.Execute()
	return out.String(), e
}

func TestCLIProvidesBidirectionalSendReceiveAndExplicitAck(t *testing.T) {
	t.Parallel()
	runtime, deps, m := fixture(t)
	raw, _ := json.Marshal(m)
	out, e := execute(t, runtime, deps, strings.NewReader(string(raw)), "deliver")
	if e != nil || !strings.Contains(out, `"status":"durable"`) {
		t.Fatal(out, e)
	}
	out, e = execute(t, runtime, deps, nil, "receive", "--agent", "mac")
	if e != nil || !strings.Contains(out, `"body":"hello"`) {
		t.Fatal(out, e)
	}
	out, e = execute(t, runtime, deps, nil, "ack", m.ID, "--agent", "mac")
	if e != nil || !strings.Contains(out, "acknowledged") {
		t.Fatal(out, e)
	}
	out, e = execute(t, runtime, deps, nil, "receive", "--agent", "mac")
	if e != nil || strings.TrimSpace(out) != "[]" {
		t.Fatal(out, e)
	}
	for _, input := range []string{"inline", "stdin", "file"} {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			args := []string{"send", "--host", "mac", "--from", "vm", "--to", "mac"}
			switch input {
			case "inline":
				args = append(args, "--message", "hello")
			case "stdin":
				args = append(args, "--message-file", "-")
			case "file":
				p := filepath.Join(t.TempDir(), "message")
				if e := os.WriteFile(p, []byte("hello"), 0600); e != nil {
					t.Fatal(e)
				}
				args = append(args, "--message-file", p)
			}
			out, e := execute(t, runtime, deps, strings.NewReader("hello"), args...)
			if e != nil || !strings.Contains(out, "durable") {
				t.Fatal(out, e)
			}
		})
	}
}

func TestCLIRefusesInvalidInputsBeforeDispatch(t *testing.T) {
	t.Parallel()
	runtime, deps, m := fixture(t)
	for _, args := range [][]string{{"send"}, {"send", "--host", "mac", "--from", "vm", "--to", "mac", "--message", ""}, {"send", "--host", "bad;host", "--from", "vm", "--to", "mac", "--message", "hello"}, {"send", "--host", "mac", "--from", "vm", "--to", "mac", "--message-file", "/nonexistent"}, {"send", "--host", "mac", "--from", "vm", "--to", "mac", "--message-file", t.TempDir()}, {"receive"}, {"receive", "--agent", "mac", "--wait", "25h"}, {"receive", "--agent", "mac", "--wait", "-1s"}, {"ack", m.ID}, {"ack", "bad", "--agent", "mac"}, {"ack", m.ID, "--agent", "mac"}, {"deliver"}} {
		if _, e := execute(t, runtime, deps, strings.NewReader("invalid"), args...); e == nil {
			t.Fatal(args)
		}
	}
	deps.Send = func(context.Context, sessionmove.SSHConfig, agentmessage.Message) (agentmessage.Receipt, error) {
		return agentmessage.Receipt{}, errors.New("offline")
	}
	if _, e := execute(t, runtime, deps, strings.NewReader(""), "send", "--host", "mac", "--from", "vm", "--to", "mac", "--message", "hello", "--id", m.ID); e == nil {
		t.Fatal("transport error lost")
	}
}

func TestCLIReportsStoreAndIOFailures(t *testing.T) {
	t.Parallel()
	runtime, deps, m := fixture(t)
	raw, _ := json.Marshal(m)
	failStore := deps
	failStore.Store = func(string) (agentmessage.Store, error) { return agentmessage.Store{}, errors.New("state unavailable") }
	for _, args := range [][]string{{"deliver"}, {"receive", "--agent", "mac"}, {"ack", m.ID, "--agent", "mac"}} {
		if _, e := execute(t, runtime, failStore, bytes.NewReader(raw), args...); e == nil {
			t.Fatal(args)
		}
	}
	badStore := deps
	badStore.Store = func(string) (agentmessage.Store, error) { return agentmessage.Store{Root: "relative"}, nil }
	if _, e := execute(t, runtime, badStore, nil, "receive", "--agent", "mac"); e == nil {
		t.Fatal("read error lost")
	}
	if _, e := execute(t, runtime, deps, badReader{}, "send", "--host", "mac", "--from", "vm", "--to", "mac", "--message-file", "-"); e == nil {
		t.Fatal("input failure lost")
	}
	command := New(runtime, deps)
	command.SetArgs([]string{"send", "--host", "mac", "--from", "vm", "--to", "mac", "--message", "hello"})
	command.SetOut(badWriter{})
	command.SetErr(io.Discard)
	if e := command.Execute(); e == nil {
		t.Fatal("output failure lost")
	}
	store, _ := deps.Store("")
	store.Notify = func(context.Context, agentmessage.Message) (string, error) {
		return "ambiguous", errors.New("wake uncertain")
	}
	deps.Store = func(string) (agentmessage.Store, error) { return store, nil }
	out, e := execute(t, runtime, deps, bytes.NewReader(raw), "deliver")
	if e == nil || !strings.Contains(out, "durable") || !strings.Contains(out, "ambiguous") {
		t.Fatal(out, e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	t.Cleanup(cancel)
	command = New(runtime, deps)
	command.SetContext(ctx)
	command.SetArgs([]string{"receive", "--agent", "other", "--wait", "1h"})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	if e := command.Execute(); e == nil {
		t.Fatal("cancel lost")
	}
}

type badReader struct{}

func (badReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

type badWriter struct{}

func (badWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
