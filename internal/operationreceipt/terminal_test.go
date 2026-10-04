package operationreceipt

import (
	"testing"

	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
)

func TestCwCovDaemonOperationTerminalAndState(t *testing.T) {
	for state, want := range map[daemonv1.OperationState]bool{
		daemonv1.OperationState_OPERATION_STATE_SUCCEEDED:         true,
		daemonv1.OperationState_OPERATION_STATE_FAILED:            true,
		daemonv1.OperationState_OPERATION_STATE_CANCELLED:         true,
		daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED: true,
		daemonv1.OperationState_OPERATION_STATE_QUEUED:            false,
		daemonv1.OperationState_OPERATION_STATE_RUNNING:           false,
	} {
		if got := Terminal(state); got != want {
			t.Errorf("Terminal(%v) = %t, want %t", state, got, want)
		}
	}
	if got := StateName(daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED); got != "recovery_required" {
		t.Errorf("StateName = %q", got)
	}
	if got := StateName(daemonv1.OperationState_OPERATION_STATE_QUEUED); got != "queued" {
		t.Errorf("StateName(queued) = %q", got)
	}
}
