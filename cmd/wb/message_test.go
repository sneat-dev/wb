package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/sneat-dev/wb/internal/agentmessage"
)

func TestMessageCommandRoundTripUsesOneLocalInboxAndExplicitAcknowledgment(t *testing.T) {
	t.Parallel()
	inv := &invocation{projectsRoot: t.TempDir()}
	run := func(args ...string) []byte {
		t.Helper()
		command := newMessageCmd(inv)
		var out bytes.Buffer
		command.SetOut(&out)
		command.SetErr(&bytes.Buffer{})
		command.SetArgs(args)
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	var receipt agentmessage.Receipt
	if err := json.Unmarshal(run("send", "--local", "--from", "vm", "--to", "mac", "--message", "round trip"), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "durable" || receipt.Wake != "not-bound" {
		t.Fatalf("receipt: %+v", receipt)
	}
	var inbox []agentmessage.Message
	if err := json.Unmarshal(run("receive", "--agent", "mac"), &inbox); err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].ID != receipt.ID || inbox[0].Body != "round trip" {
		t.Fatalf("inbox: %+v", inbox)
	}
	run("ack", receipt.ID, "--agent", "mac")
	if err := json.Unmarshal(run("receive", "--agent", "mac"), &inbox); err != nil || len(inbox) != 0 {
		t.Fatalf("acknowledged inbox: %+v %v", inbox, err)
	}
}

func TestMessageCommandCancelledTransportsNeverRequireNetwork(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"send", "--host", "unreachable.invalid", "--from", "vm", "--to", "mac", "--message", "cancelled"},
		{"relay", "--host", "unreachable.invalid", "--agent", "mac", "--once"},
	} {
		t.Run(args[0], func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			command := newMessageCmd(&invocation{projectsRoot: t.TempDir()})
			command.SetContext(ctx)
			command.SetArgs(args)
			command.SetOut(&bytes.Buffer{})
			command.SetErr(&bytes.Buffer{})
			command.SilenceErrors = true
			command.SilenceUsage = true
			if err := command.Execute(); err == nil {
				t.Fatal("cancelled transport succeeded")
			}
		})
	}
}
