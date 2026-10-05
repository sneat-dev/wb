package daemonoperation

import (
	"bytes"
	"testing"

	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
)

func TestCwCovWaitForDaemonOperationStopsOnTerminalState(t *testing.T) {
	t.Parallel()
	// A terminal operation is returned unchanged without any client call.
	operation := &daemonv1.Operation{OperationId: "wbo-3", State: daemonv1.OperationState_OPERATION_STATE_SUCCEEDED}
	var progress bytes.Buffer
	result, err := waitForOperation(t.Context(), &progress, nil, operation)
	if err != nil {
		t.Fatal(err)
	}
	if result != operation || progress.Len() != 0 {
		t.Fatalf("terminal wait = (%+v, %q)", result, progress.String())
	}
}
