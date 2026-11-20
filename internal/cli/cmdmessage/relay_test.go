package cmdmessage

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/agentmessage"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

func TestLocalSendAndOutboundRelayUseLocalMailbox(t *testing.T) {
	t.Parallel()
	runtime, deps, _ := fixture(t)
	out, e := execute(t, runtime, deps, nil, "send", "--local", "--from", "vm", "--to", "mac", "--message", "hello")
	if e != nil || !strings.Contains(out, "durable") {
		t.Fatal(out, e)
	}
	deps.Relay = func(_ context.Context, c sessionmove.SSHConfig, s agentmessage.Store, agent string, once bool, report func(error)) error {
		report(errors.New(strings.Repeat("x", 600)))
		if c.Host != "vm" || agent != "mac" || !once || s.Root == "" {
			t.Fatal(c, agent, once)
		}
		return nil
	}
	if _, e := execute(t, runtime, deps, nil, "relay", "--host", "vm", "--agent", "mac", "--once"); e != nil {
		t.Fatal(e)
	}
	for _, args := range [][]string{{"send", "--host", "vm", "--local", "--from", "vm", "--to", "mac", "--message", "x"}, {"send", "--local", "--user", "user", "--from", "vm", "--to", "mac", "--message", "x"}, {"relay", "--agent", "mac"}, {"relay", "--host", "vm"}} {
		if _, e := execute(t, runtime, deps, nil, args...); e == nil {
			t.Fatal(args)
		}
	}
	deps.Store = func(string) (agentmessage.Store, error) {
		return agentmessage.Store{}, errors.New("storage unavailable")
	}
	for _, args := range [][]string{{"send", "--local", "--from", "vm", "--to", "mac", "--message", "x"}, {"relay", "--host", "vm", "--agent", "mac"}} {
		if _, e := execute(t, runtime, deps, nil, args...); e == nil {
			t.Fatal(args)
		}
	}
}

func TestRemoteSendRejectsLocalProjectsRootSelection(t *testing.T) {
	t.Parallel()
	runtime, deps, _ := fixture(t)
	cmd := New(runtime, deps)
	cmd.PersistentFlags().String("projects-root", "", "test root selector")
	cmd.SetArgs([]string{"send", "--host", "vm", "--from", "vm", "--to", "mac", "--message", "x", "--projects-root", "/tmp/local"})
	if e := cmd.Execute(); e == nil {
		t.Fatal("remote sender silently ignored local projects root")
	}
}

func TestRelayRetryDiagnosticsStayBoundedAndOffStdout(t *testing.T) {
	t.Parallel()
	runtime, deps, _ := fixture(t)
	deps.Relay = func(_ context.Context, _ sessionmove.SSHConfig, _ agentmessage.Store, _ string, _ bool, report func(error)) error {
		report(errors.New("offline\n" + strings.Repeat("x", 1000)))
		return nil
	}
	command := New(runtime, deps)
	var stdout, stderr strings.Builder
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"relay", "--host", "vm", "--agent", "mac"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 || stderr.Len() > 600 || strings.Count(stderr.String(), "\n") != 1 || !strings.Contains(stderr.String(), "offline\\n") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
