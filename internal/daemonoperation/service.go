package daemonoperation

import (
	"context"
	"fmt"
	"io"

	"connectrpc.com/connect"
	"github.com/sneat-dev/wb/internal/daemonruntime"
	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
	"github.com/sneat-dev/wb/internal/operationreceipt"
)

type Service struct {
	Client    func(context.Context, string, io.Writer) (daemonv1connect.DaemonServiceClient, error)
	RawPolicy func(string) (bool, string, error)
}
type SubmitRequest struct {
	ProjectsRoot, Cwd, IdempotencyKey string
	Argv                              []string
	CPUUnits                          uint32
	Wait                              bool
}

func (s Service) Submit(ctx context.Context, r SubmitRequest, progress io.Writer) (*daemonv1.Operation, error) {
	if err := s.RequireRawPolicy(r.ProjectsRoot); err != nil {
		return nil, err
	}
	client, err := s.Client(ctx, r.ProjectsRoot, progress)
	if err != nil {
		return nil, err
	}
	response, err := client.SubmitOperation(ctx, connect.NewRequest(&daemonv1.SubmitOperationRequest{IdempotencyKey: r.IdempotencyKey, WorkingDirectory: r.Cwd, Argv: r.Argv, CpuUnits: r.CPUUnits, LocalRawCommand: true}))
	if err != nil {
		return nil, err
	}
	if r.Wait {
		return waitForOperation(ctx, progress, client, response.Msg)
	}
	return response.Msg, nil
}
func (s Service) Get(ctx context.Context, root, id string, progress io.Writer) (*daemonv1.Operation, error) {
	client, err := s.Client(ctx, root, progress)
	if err != nil {
		return nil, err
	}
	response, err := client.GetOperation(ctx, connect.NewRequest(&daemonv1.GetOperationRequest{OperationId: id}))
	if err != nil {
		return nil, err
	}
	return response.Msg, nil
}
func (s Service) Cancel(ctx context.Context, root, id string, progress io.Writer) (*daemonv1.Operation, error) {
	client, err := s.Client(ctx, root, progress)
	if err != nil {
		return nil, err
	}
	response, err := client.CancelOperation(ctx, connect.NewRequest(&daemonv1.CancelOperationRequest{OperationId: id}))
	if err != nil {
		return nil, err
	}
	return response.Msg, nil
}
func (s Service) Wait(ctx context.Context, root, id, cursor string, progress io.Writer) (*daemonv1.Operation, error) {
	client, err := s.Client(ctx, root, progress)
	if err != nil {
		return nil, err
	}
	return waitForOperation(ctx, progress, client, &daemonv1.Operation{OperationId: id, Cursor: cursor})
}
func waitForOperation(ctx context.Context, progress io.Writer, client daemonv1connect.DaemonServiceClient, operation *daemonv1.Operation) (*daemonv1.Operation, error) {
	for !operationreceipt.Terminal(operation.State) {
		response, err := client.WaitOperation(ctx, connect.NewRequest(&daemonv1.WaitOperationRequest{
			OperationId: operation.OperationId, AfterCursor: operation.Cursor, WaitMilliseconds: 10_000,
		}))
		if err != nil {
			return nil, err
		}
		operation = response.Msg
		if !operationreceipt.Terminal(operation.State) {
			_, _ = fmt.Fprintf(progress, "wb: operation %s still %s\n", operation.OperationId, operationreceipt.StateName(operation.State))
		}
	}
	return operation, nil
}

func (service Service) RequireRawPolicy(root string) error {
	check := service.RawPolicy
	if check == nil {
		check = daemonruntime.DefaultDependencies(nil).RawPolicy
	}
	allowed, path, err := check(root)
	if err != nil {
		return fmt.Errorf("load daemon raw-execution policy: %w", err)
	}
	if allowed {
		return nil
	}
	return fmt.Errorf("raw daemon execution is disabled; an administrator must create %s with mode 0600 and contents {\"version\":1,\"allow_raw_daemon_execution\":true}", path)
}
