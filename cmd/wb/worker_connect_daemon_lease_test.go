//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
)

// wb worker connect's real reconnect loop must actually reach the local
// daemon, register, lease a genuinely queued operation, and execute it -
// not merely be wired to an already-canceled context (that is proven
// separately by TestWorkerConnectCLIWiresIntoConnectWorker). This drives
// the whole path through Execute() against a real in-process daemon: submit
// a real (non-local-raw) operation so it can only complete via a worker
// lease, connect a worker that leases and runs it, and confirm the
// operation's own durable receipt shows it actually completed.
func TestWorkerConnectLeasesAndExecutesARealQueuedOperation(t *testing.T) {
	root, deps := cwWtDaemonOpFixture(t)

	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}

	client, err := daemonOperationClient(context.Background(), deps, root, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("daemon operation client: %v", err)
	}
	// LocalRawCommand is deliberately left false: that path executes inside
	// the daemon itself and would never reach a worker lease at all, which
	// is exactly the path this test must not take.
	submitResponse, err := client.SubmitOperation(context.Background(), connect.NewRequest(&daemonv1.SubmitOperationRequest{
		WorkingDirectory: work, Argv: []string{"true"}, CpuUnits: 1, TargetWorkerId: "cw-connect-lease-worker",
	}))
	if err != nil {
		t.Fatalf("submit operation: %v", err)
	}
	submitted := daemonOperationResult{OperationID: submitResponse.Msg.OperationId}
	if submitted.OperationID == "" {
		t.Fatalf("submit response = %#v", submitResponse.Msg)
	}

	fetchOperation := func() (daemonOperationResult, error) {
		var out bytes.Buffer
		get := newDaemonOperationGetCmd(&invocation{projectsRoot: root}, deps)
		get.SilenceUsage, get.SilenceErrors = true, true
		get.SetOut(&out)
		get.SetErr(&bytes.Buffer{})
		get.SetArgs([]string{"--json", submitted.OperationID})
		if err := get.Execute(); err != nil {
			return daemonOperationResult{}, err
		}
		var fetched daemonOperationResult
		err := json.Unmarshal(out.Bytes(), &fetched)
		return fetched, err
	}

	// The worker connect loop only stops on context cancellation or a lease
	// error, so a background watcher cancels its context the moment the
	// operation it must have executed shows a terminal state.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopWatching := make(chan struct{})
	defer close(stopWatching)
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopWatching:
				return
			case <-ticker.C:
				fetched, err := fetchOperation()
				if err == nil && (fetched.State == "succeeded" || fetched.State == "failed") {
					cancel()
					return
				}
			}
		}
	}()

	command := newWorkerConnectCmd(&invocation{projectsRoot: root}, deps)
	command.SilenceUsage, command.SilenceErrors = true, true
	command.SetContext(ctx)
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"--id", "cw-connect-lease-worker", "--root", root, "--cpu-capacity", "1"})
	if err := command.Execute(); err != nil {
		t.Fatalf("worker connect: %v (stderr=%s)", err, stderr.String())
	}

	fetched, err := fetchOperation()
	if err != nil {
		t.Fatalf("operation get after connect: %v", err)
	}
	if fetched.State != "succeeded" {
		t.Fatalf("operation state = %q (error %q), want succeeded; worker stderr=%s", fetched.State, fetched.Error, stderr.String())
	}
}
