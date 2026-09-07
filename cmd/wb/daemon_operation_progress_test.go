package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
)

type progressWaitClient struct {
	daemonv1connect.DaemonServiceClient
	calls int
}

func (c *progressWaitClient) WaitOperation(_ context.Context, req *connect.Request[daemonv1.WaitOperationRequest]) (*connect.Response[daemonv1.Operation], error) {
	c.calls++
	state := daemonv1.OperationState_OPERATION_STATE_RUNNING
	if c.calls == 3 {
		state = daemonv1.OperationState_OPERATION_STATE_FAILED
	}
	return connect.NewResponse(&daemonv1.Operation{OperationId: req.Msg.OperationId, State: state, Error: "test failed", StderrTail: []byte("assertion context")}), nil
}

func TestDaemonOperationHumanProgressDoesNotEnterAgentStreams(t *testing.T) {
	var agent bytes.Buffer
	path := filepath.Join(t.TempDir(), "human.log")
	writer, closeWriter, err := daemonOperationProgressWriter(&agent, true, path)
	if err != nil {
		t.Fatal(err)
	}
	client := &progressWaitClient{}
	operation, err := waitForDaemonOperation(context.Background(), writer, client, &daemonv1.Operation{OperationId: "test"})
	closeWriter()
	if err != nil {
		t.Fatal(err)
	}
	if agent.Len() != 0 {
		t.Fatalf("intermediate agent output: %s", agent.String())
	}
	human, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(human, []byte("still running")) != 2 {
		t.Fatalf("human progress: %s", human)
	}
	if err := writeDaemonOperation(&agent, "json", operation); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(agent.Bytes(), []byte("assertion context")) || !bytes.Contains(agent.Bytes(), []byte(`"state": "failed"`)) {
		t.Fatalf("terminal error context missing: %s", agent.String())
	}
}

func TestDaemonOperationQuietProgressAndInvalidDestination(t *testing.T) {
	writer, closeWriter, err := daemonOperationProgressWriter(io.Discard, false, "")
	if err != nil {
		t.Fatal(err)
	}
	closeWriter()
	if writer != io.Discard {
		t.Fatal("quiet mode does not discard progress")
	}
	if _, _, err := daemonOperationProgressWriter(io.Discard, false, "human.log"); err == nil {
		t.Fatal("conflicting flags accepted")
	}
	if _, _, err := daemonOperationProgressWriter(io.Discard, true, filepath.Join(t.TempDir(), "absent", "log")); err == nil {
		t.Fatal("invalid destination silently accepted")
	}
}
