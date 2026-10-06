package daemonview

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
)

func TestCwCovWriteDaemonOperationTextAndJSON(t *testing.T) {
	t.Parallel()
	operation := &daemonv1.Operation{
		OperationId: "wbo-1", IdempotencyKey: "key-1",
		State: daemonv1.OperationState_OPERATION_STATE_SUCCEEDED, Cursor: "c-1",
		CommandKind: "raw", ArgsSha256: "abc", ArgumentCount: 2, CpuUnits: 4,
		ExitCode: 0, SubmittedUnixMilli: 1, StartedUnixMilli: 2, FinishedUnixMilli: 3,
		QueueWaitMilliseconds: 1, WallMilliseconds: 1,
		StdoutTail: []byte("hello\n"), StderrTail: []byte("warning\n"),
		TargetWorkerId: "worker-1",
	}
	var out bytes.Buffer
	if err := Operation(&out, "text", operation); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"operation wbo-1: state=succeeded, cursor=c-1, cpu_units=4",
		", target_worker=worker-1", ", exit_code=0",
		"hello", "stderr:", "warning",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("daemon operation text missing %q:\n%s", want, text)
		}
	}

	// An unfinished operation with no worker and no tails prints one short line.
	out.Reset()
	if err := Operation(&out, "text", &daemonv1.Operation{
		OperationId: "wbo-2", State: daemonv1.OperationState_OPERATION_STATE_QUEUED, Cursor: "c-2", CpuUnits: 1,
	}); err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(out.String())
	if line != "operation wbo-2: state=queued, cursor=c-2, cpu_units=1" {
		t.Fatalf("queued operation line = %q", line)
	}

	out.Reset()
	if err := Operation(&out, "json", operation); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		OperationID    string `json:"operation_id"`
		State          string `json:"state"`
		TargetWorkerID string `json:"target_worker_id"`
		StdoutTail     string `json:"stdout_tail"`
		StderrTail     string `json:"stderr_tail"`
		ArgumentCount  int    `json:"argument_count"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("daemon operation JSON: %v\n%s", err, out.String())
	}
	if payload.OperationID != "wbo-1" || payload.State != "succeeded" || payload.TargetWorkerID != "worker-1" {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.StdoutTail != "hello\n" || payload.StderrTail != "warning\n" || payload.ArgumentCount != 2 {
		t.Fatalf("payload tails = %+v", payload)
	}
}
