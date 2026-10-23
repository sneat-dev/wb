package runexec

import (
	"context"
	"github.com/sneat-dev/wb/internal/operationreceipt"
	"os"
)

// SubmissionOperations binds the actual authenticated transport at composition.
type SubmissionOperations struct {
	Getwd func() (string, error)
	Send  func(context.Context, SubmitRequest, Submission) (operationreceipt.Receipt, error)
}

func NewSubmission(send func(context.Context, SubmitRequest, Submission) (operationreceipt.Receipt, error)) SubmissionOperations {
	return SubmissionOperations{Getwd: os.Getwd, Send: send}
}
func (ops SubmissionOperations) Run(ctx context.Context, request SubmitRequest) (operationreceipt.Receipt, error) {
	cwd, err := ops.Getwd()
	if err != nil {
		return operationreceipt.Receipt{}, err
	}
	return ops.Send(ctx, request, Submission{WorkingDirectory: cwd, Argv: append([]string(nil), request.Argv...), WorkerID: request.WorkerID, IdempotencyKey: request.IdempotencyKey})
}
