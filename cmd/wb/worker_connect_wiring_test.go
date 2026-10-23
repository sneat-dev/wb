package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/spf13/cobra"
)

// The actual root family delegates a canceled request before daemon bootstrap;
// the separate lease journey proves the live native transport and execution.
func TestWorkerConnectCLIWiresIntoConnectWorker(t *testing.T) {
	root := t.TempDir()
	command := workerConnectForTest(&invocation{projectsRoot: root}, defaultDaemonDependencies())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	command.SetContext(ctx)
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	command.SetArgs([]string{"--id", "cw-worker-wiring", "--root", root})
	if err := command.Execute(); err != nil {
		t.Fatalf("worker connect with an already-canceled context: %v", err)
	}
}

// Select the genuine production-registered child; no operation/error proxy.
func workerConnectForTest(inv *invocation, deps daemonDependencies) *cobra.Command {
	root := newWorkerCmd(inv, deps)
	child, _, err := root.Find([]string{"connect"})
	if err != nil {
		panic(err)
	}
	root.RemoveCommand(child)
	return child
}
