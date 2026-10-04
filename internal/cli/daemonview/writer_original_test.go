package daemonview

import (
	"errors"
	"testing"

	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
)

type originalFailWriter struct{ Allow int }

func (w *originalFailWriter) Write(p []byte) (int, error) {
	if w.Allow == 0 {
		return 0, errors.New("write refused")
	}
	w.Allow--
	return len(p), nil
}
func TestCwWtDaemonOperationProgressFailurePropagation(t *testing.T) {
	operation := &daemonv1.Operation{
		OperationId: "wbo-cwWt", State: daemonv1.OperationState_OPERATION_STATE_SUCCEEDED,
		Cursor: "c", CpuUnits: 1, FinishedUnixMilli: 1,
		TargetWorkerId: "worker-1", StdoutTail: []byte("out\n"), StderrTail: []byte("err\n"),
	}
	for allow := 0; allow < 5; allow++ {
		if err := Operation(&originalFailWriter{Allow: allow}, "text", operation); err == nil {
			t.Fatalf("Operation with %d writes allowed returned nil", allow)
		}
	}
}
