package cmdmessage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/agentmessage"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

func TestSendDefaultsToLocalDurabilityWithoutNetworkDispatch(t *testing.T) {
	t.Parallel()
	runtime, deps, message := fixture(t)
	deps.Send = func(context.Context, sessionmove.SSHConfig, agentmessage.Message) (agentmessage.Receipt, error) {
		t.Fatal("ordinary agent send invoked remote transport")
		return agentmessage.Receipt{}, nil
	}
	out, err := execute(t, runtime, deps, strings.NewReader(message.Body), "send", "--from", message.From, "--to", message.To, "--id", message.ID, "--message-file", "-")
	var receipt agentmessage.Receipt
	if err != nil || json.Unmarshal([]byte(out), &receipt) != nil || receipt.ID != message.ID || receipt.Digest != message.Digest() || receipt.Status != "durable" {
		t.Fatalf("receipt=%s error=%v", out, err)
	}
	store, _ := deps.Store("")
	messages, err := store.Receive(context.Background(), message.To, 0)
	if err != nil || len(messages) != 1 || messages[0] != message {
		t.Fatalf("inbox=%+v error=%v", messages, err)
	}
	for _, flag := range []string{"--user", "--remote-wb"} {
		if _, err := execute(t, runtime, deps, nil, "send", "--from", message.From, "--to", message.To, "--message", "x", flag, "value"); err == nil {
			t.Fatalf("ignored operator flag %s without host", flag)
		}
	}
}

func TestForwardDispatchPreservesRouteAndReportsOnlyBoundedDiagnostics(t *testing.T) {
	t.Parallel()
	runtime, deps, _ := fixture(t)
	sentinel := errors.New("forward incomplete")
	ctx := context.WithValue(context.Background(), routeContextKey{}, "route-context")
	deps.Forward = func(gotCtx context.Context, config sessionmove.SSHConfig, store agentmessage.Store, from, to string, once bool, report func(error)) error {
		if gotCtx.Value(routeContextKey{}) != "route-context" || config.Host != "vm" || config.User != "worker" || config.WBPath != "/opt/wb" || store.Root == "" || from != "mac" || to != "vm" || !once {
			t.Fatal("forward dispatch lost configuration or context")
		}
		report(errors.New("offline"))
		report(errors.New("offline\n" + strings.Repeat("x", 1000)))
		return sentinel
	}
	command := New(runtime, deps)
	var stdout, stderr bytes.Buffer
	command.SetContext(ctx)
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SilenceErrors, command.SilenceUsage = true, true
	command.SetArgs([]string{"forward", "--host", "vm", "--user", "worker", "--remote-wb", "/opt/wb", "--from", "mac", "--to", "vm", "--once"})
	if err := command.Execute(); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if stdout.Len() != 0 || stderr.Len() > 700 || strings.Count(stderr.String(), "\n") != 2 || !strings.Contains(stderr.String(), "offline\\n") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

type routeContextKey struct{}

func TestForwardRefusesInvalidRoutesAndUnavailableStoreBeforeTransport(t *testing.T) {
	t.Parallel()
	runtime, deps, _ := fixture(t)
	runtime.ExitError = func(code int, message string) error {
		if code != shared.ExitUsage {
			t.Fatalf("usage exit=%d", code)
		}
		return errors.New(message)
	}
	deps.Forward = func(context.Context, sessionmove.SSHConfig, agentmessage.Store, string, string, bool, func(error)) error {
		t.Fatal("invalid route dispatched")
		return nil
	}
	for _, args := range [][]string{
		{"forward", "--from", "mac", "--to", "vm"},
		{"forward", "--host", "vm", "--to", "vm"},
		{"forward", "--host", "vm", "--from", "mac"},
		{"forward", "--host", "vm", "--from", "mac", "--to", "mac"},
	} {
		if _, err := execute(t, runtime, deps, nil, args...); err == nil {
			t.Fatal(args)
		}
	}
	sentinel := errors.New("store unavailable")
	deps.Store = func(string) (agentmessage.Store, error) { return agentmessage.Store{}, sentinel }
	if _, err := execute(t, runtime, deps, nil, "forward", "--host", "vm", "--from", "mac", "--to", "vm"); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}
