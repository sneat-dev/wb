// Package operationreceipt projects actual daemon operations for CLI receipts.
package operationreceipt

import (
	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"strings"
)

type Receipt struct {
	OperationID           string `json:"operation_id"`
	IdempotencyKey        string `json:"idempotency_key,omitempty"`
	State                 string `json:"state"`
	Cursor                string `json:"cursor"`
	CommandKind           string `json:"command_kind"`
	ArgsSHA256            string `json:"args_sha256"`
	ArgumentCount         uint32 `json:"argument_count"`
	CPUUnits              uint32 `json:"cpu_units"`
	ExitCode              int32  `json:"exit_code,omitempty"`
	SubmittedUnixMilli    int64  `json:"submitted_unix_milli"`
	StartedUnixMilli      int64  `json:"started_unix_milli,omitempty"`
	FinishedUnixMilli     int64  `json:"finished_unix_milli,omitempty"`
	QueueWaitMilliseconds int64  `json:"queue_wait_milliseconds,omitempty"`
	WallMilliseconds      int64  `json:"wall_milliseconds,omitempty"`
	Error                 string `json:"error,omitempty"`
	TargetWorkerID        string `json:"target_worker_id,omitempty"`
	StdoutTail            string `json:"stdout_tail,omitempty"`
	StderrTail            string `json:"stderr_tail,omitempty"`
}

func StateName(state daemonv1.OperationState) string {
	return strings.ToLower(strings.TrimPrefix(state.String(), "OPERATION_STATE_"))
}
func FromOperation(operation *daemonv1.Operation) Receipt {
	return Receipt{
		OperationID: operation.OperationId, IdempotencyKey: operation.IdempotencyKey,
		State: StateName(operation.State), Cursor: operation.Cursor,
		CommandKind: operation.CommandKind, ArgsSHA256: operation.ArgsSha256,
		ArgumentCount: operation.ArgumentCount, CPUUnits: operation.CpuUnits,
		ExitCode: operation.ExitCode, SubmittedUnixMilli: operation.SubmittedUnixMilli,
		StartedUnixMilli: operation.StartedUnixMilli, FinishedUnixMilli: operation.FinishedUnixMilli,
		QueueWaitMilliseconds: operation.QueueWaitMilliseconds, WallMilliseconds: operation.WallMilliseconds,
		Error: operation.Error, StdoutTail: string(operation.StdoutTail), StderrTail: string(operation.StderrTail),
		TargetWorkerID: operation.TargetWorkerId,
	}
}
