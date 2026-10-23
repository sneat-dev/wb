package operationreceipt

import (
	"encoding/json"
	"reflect"
	"testing"

	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
)

func TestReceiptPreservesActualOperationAndStableJSONFields(t *testing.T) {
	t.Parallel()
	operation := &daemonv1.Operation{OperationId: "op", IdempotencyKey: "key", State: daemonv1.OperationState_OPERATION_STATE_RUNNING, Cursor: "cursor", CommandKind: "go test", ArgsSha256: "digest", ArgumentCount: 3, CpuUnits: 2, ExitCode: 7, SubmittedUnixMilli: 10, StartedUnixMilli: 20, FinishedUnixMilli: 30, QueueWaitMilliseconds: 4, WallMilliseconds: 5, Error: "failed", TargetWorkerId: "worker", StdoutTail: []byte("out"), StderrTail: []byte("err")}
	receipt := FromOperation(operation)
	want := Receipt{OperationID: "op", IdempotencyKey: "key", State: "running", Cursor: "cursor", CommandKind: "go test", ArgsSHA256: "digest", ArgumentCount: 3, CPUUnits: 2, ExitCode: 7, SubmittedUnixMilli: 10, StartedUnixMilli: 20, FinishedUnixMilli: 30, QueueWaitMilliseconds: 4, WallMilliseconds: 5, Error: "failed", TargetWorkerID: "worker", StdoutTail: "out", StderrTail: "err"}
	if !reflect.DeepEqual(receipt, want) {
		t.Fatalf("receipt=%+v", receipt)
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]any{"operation_id": "op", "idempotency_key": "key", "state": "running", "cursor": "cursor", "command_kind": "go test", "args_sha256": "digest", "argument_count": float64(3), "cpu_units": float64(2), "exit_code": float64(7), "submitted_unix_milli": float64(10), "started_unix_milli": float64(20), "finished_unix_milli": float64(30), "queue_wait_milliseconds": float64(4), "wall_milliseconds": float64(5), "error": "failed", "target_worker_id": "worker", "stdout_tail": "out", "stderr_tail": "err"} {
		if fields[key] != value {
			t.Errorf("field%s=%v want%v", key, fields[key], value)
		}
	}
	operation.StdoutTail[0] = 'x'
	if receipt.StdoutTail != "out" {
		t.Fatal("tail alias")
	}
}
