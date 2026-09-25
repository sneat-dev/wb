package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/daemon"
	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
)

// serveDashboard wires its worker queue's raw-execution authorization to the
// real, machine-wide administrator opt-in file (daemon.RequireRawExecutionPolicy
// against daemon.RawExecutionPolicyPath()), not to any test-injectable
// dependency - that authorization closure is otherwise only ever
// constructed, never invoked, unless a real LocalRawCommand submission
// reaches a really-running daemon built by serveDashboard itself. This
// drives that exact path: a real serveDashboard instance, submitting a raw
// command directly against its local RPC socket, and asserts the daemon
// refuses it with the real disabled-by-default policy message (this
// machine's account has no ~/.config/wb/daemon-raw-exec.json).
func TestServeDashboardRefusesARawCommandWithoutAnAdministratorOptIn(t *testing.T) {
	if _, err := daemon.RawExecutionPolicyPath(); err != nil {
		t.Skip("cannot resolve this account's raw-execution policy path")
	}
	root, err := os.MkdirTemp("/tmp", "wb-rawpolicy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	pinDaemonHome(t, root)

	configPath := memoryHubConfig(t)
	deps := daemonTestDependencies(t, root)
	deps.hubConfigPath = func() string { return configPath }

	address := freeLoopbackAddress(t)
	command := &cobra.Command{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command.SetContext(ctx)
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	served := make(chan error, 1)
	go func() {
		served <- serveDashboard(&invocation{projectsRoot: root}, command, deps, address, daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}, "owner-token", true, false)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("serveDashboard: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("serveDashboard did not return after its context was cancelled")
		}
	})
	waitForHealth(t, address)

	state, found, err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}).Load()
	if err != nil || !found {
		t.Fatalf("load serveDashboard's own lifecycle state: found=%t err=%v", found, err)
	}
	localClient := deps.localClient
	if localClient == nil {
		localClient = daemonLocalHTTPClient
	}
	httpClient, err := localClient(root, state.OwnerToken)
	if err != nil {
		t.Fatalf("build local daemon RPC client: %v", err)
	}
	client := daemonv1connect.NewDaemonServiceClient(httpClient, daemonRPCBaseURL)

	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SubmitOperation(ctx, connect.NewRequest(&daemonv1.SubmitOperationRequest{
		WorkingDirectory: work, Argv: []string{"true"}, LocalRawCommand: true,
	})); err == nil || !strings.Contains(err.Error(), "raw daemon execution") {
		t.Fatalf("submit a local raw command against a real serveDashboard queue = %v, want a raw-execution-policy refusal", err)
	}
}
