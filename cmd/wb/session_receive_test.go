package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionreceive"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestSessionReceiveCommandUsesConfiguredMachineAndExactStdinBytes(t *testing.T) {
	raw := []byte("{\n  \"exact\": \"courier bytes\"\n}\n")
	var captured sessionreceive.Options
	deps := sessionReceiveDependencies{
		localMachine: func() (string, error) { return "target-vm", nil },
		store:        func(string) (sessionmove.Store, error) { return sessionmove.NewStore("/target/handoffs"), nil },
		receive: func(_ context.Context, options sessionreceive.Options) (sessionreceive.Result, error) {
			captured = options
			return sessionreceive.Result{
				Request: sessionmove.Request{HandoffID: "handoff-123"},
				Digest:  sessionmove.DigestBytes(raw), Phase: sessionmove.PhaseWorktreeReady,
				Worktree: &structSessionReceiveResult,
			}, nil
		},
	}
	command := newSessionReceiveCmdWithDeps(deps)
	command.SetArgs([]string{"--format", "json"})
	command.SetIn(bytes.NewReader(raw))
	var output bytes.Buffer
	command.SetOut(&output)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if captured.LocalMachine != "target-vm" || captured.Store.Root != "/target/handoffs" || !bytes.Equal(captured.RawRequest, raw) {
		t.Fatalf("receive options = %#v", captured)
	}
	if strings.Contains(output.String(), "receipt") || !strings.Contains(output.String(), `"phase": "worktree_ready"`) {
		t.Fatalf("output = %s", output.String())
	}
	if command.Flags().Lookup("digest") != nil || command.Flags().Lookup("machine") != nil || command.Flags().Lookup("config") != nil {
		t.Fatal("fixed receive boundary exposed request-authority flags")
	}
}

func TestSessionReceiveCommandBoundsInputBeforeTargetExecution(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  string
	}{
		{name: "empty", want: "request must not be empty"},
		{name: "oversized", input: bytes.Repeat([]byte("x"), maxSessionReceiveBytes+1), want: "exceeds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			deps := sessionReceiveDependencies{
				localMachine: func() (string, error) { return "target-vm", nil },
				store:        func(string) (sessionmove.Store, error) { return sessionmove.NewStore(t.TempDir()), nil },
				receive: func(context.Context, sessionreceive.Options) (sessionreceive.Result, error) {
					called = true
					return sessionreceive.Result{}, errors.New("must not run")
				},
			}
			command := newSessionReceiveCmdWithDeps(deps)
			command.SetIn(bytes.NewReader(test.input))
			if err := command.Execute(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
			if called {
				t.Fatal("target receive ran after input refusal")
			}
		})
	}
}

var structSessionReceiveResult = func() (result worktrees.SessionReceiveResult) {
	result.Repository = "acme/app"
	result.WorktreeDir = "/target/worktree"
	result.Commit = strings.Repeat("a", 40)
	return
}()
