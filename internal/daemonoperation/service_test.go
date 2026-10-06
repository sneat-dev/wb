package daemonoperation

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"
	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
)

type fakeClient struct {
	daemonv1connect.DaemonServiceClient
	submit func(context.Context, *daemonv1.SubmitOperationRequest) (*daemonv1.Operation, error)
	get    func(context.Context, string) (*daemonv1.Operation, error)
	cancel func(context.Context, string) (*daemonv1.Operation, error)
	wait   func(context.Context, *daemonv1.WaitOperationRequest) (*daemonv1.Operation, error)
}

func (c fakeClient) SubmitOperation(ctx context.Context, r *connect.Request[daemonv1.SubmitOperationRequest]) (*connect.Response[daemonv1.Operation], error) {
	v, err := c.submit(ctx, r.Msg)
	return connect.NewResponse(v), err
}
func (c fakeClient) GetOperation(ctx context.Context, r *connect.Request[daemonv1.GetOperationRequest]) (*connect.Response[daemonv1.Operation], error) {
	v, err := c.get(ctx, r.Msg.OperationId)
	return connect.NewResponse(v), err
}
func (c fakeClient) CancelOperation(ctx context.Context, r *connect.Request[daemonv1.CancelOperationRequest]) (*connect.Response[daemonv1.Operation], error) {
	v, err := c.cancel(ctx, r.Msg.OperationId)
	return connect.NewResponse(v), err
}
func (c fakeClient) WaitOperation(ctx context.Context, r *connect.Request[daemonv1.WaitOperationRequest]) (*connect.Response[daemonv1.Operation], error) {
	v, err := c.wait(ctx, r.Msg)
	return connect.NewResponse(v), err
}
func TestSubmitPreservesRequestAndPolicyClientRPCWaitOrder(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	var order []string
	var progress bytes.Buffer
	client := fakeClient{submit: func(got context.Context, r *daemonv1.SubmitOperationRequest) (*daemonv1.Operation, error) {
		order = append(order, "submit")
		if got != ctx || r.IdempotencyKey != "key" || r.WorkingDirectory != "/cwd" || !reflect.DeepEqual(r.Argv, []string{"echo", "a"}) || r.CpuUnits != 3 || !r.LocalRawCommand {
			t.Fatalf("request=%+v", r)
		}
		return &daemonv1.Operation{OperationId: "id", Cursor: "first", State: daemonv1.OperationState_OPERATION_STATE_RUNNING}, nil
	}}
	count := 0
	client.wait = func(got context.Context, r *daemonv1.WaitOperationRequest) (*daemonv1.Operation, error) {
		order = append(order, "wait")
		if got != ctx || r.OperationId != "id" || r.WaitMilliseconds != 10000 {
			t.Fatalf("wait=%+v", r)
		}
		count++
		if count == 1 {
			if r.AfterCursor != "first" {
				t.Fatal(r)
			}
			return &daemonv1.Operation{OperationId: "id", Cursor: "second", State: daemonv1.OperationState_OPERATION_STATE_RUNNING}, nil
		}
		if r.AfterCursor != "second" {
			t.Fatal(r)
		}
		return &daemonv1.Operation{OperationId: "id", State: daemonv1.OperationState_OPERATION_STATE_FAILED, StderrTail: []byte("failed")}, nil
	}
	service := Service{RawPolicy: func(root string) (bool, string, error) {
		order = append(order, "policy")
		if root != "root" {
			t.Fatal(root)
		}
		return true, "policy", nil
	}, Client: func(got context.Context, root string, out io.Writer) (daemonv1connect.DaemonServiceClient, error) {
		order = append(order, "client")
		if got != ctx || root != "root" || out != &progress {
			t.Fatal("client inputs")
		}
		return client, nil
	}}
	request := SubmitRequest{ProjectsRoot: "root", Cwd: "/cwd", IdempotencyKey: "key", Argv: []string{"echo", "a"}, CPUUnits: 3, Wait: true}
	result, err := service.Submit(ctx, request, &progress)
	if err != nil || string(result.StderrTail) != "failed" || !reflect.DeepEqual(order, []string{"policy", "client", "submit", "wait", "wait"}) || progress.String() != "wb: operation id still running\n" {
		t.Fatalf("%+v %v %v %q", result, err, order, progress.String())
	}
	request.Wait = false
	result, err = service.Submit(ctx, request, &progress)
	if err != nil || result.Cursor != "first" {
		t.Fatalf("no-wait=%+v %v", result, err)
	}
}
func TestOperationEffectErrorsAreExactAndPolicyRefusesBeforeBootstrap(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("stage")
	ctx := t.Context()
	client := fakeClient{submit: func(context.Context, *daemonv1.SubmitOperationRequest) (*daemonv1.Operation, error) {
		return nil, sentinel
	}, get: func(context.Context, string) (*daemonv1.Operation, error) { return nil, sentinel }, cancel: func(context.Context, string) (*daemonv1.Operation, error) { return nil, sentinel }, wait: func(context.Context, *daemonv1.WaitOperationRequest) (*daemonv1.Operation, error) {
		return nil, sentinel
	}}
	service := Service{RawPolicy: func(string) (bool, string, error) { return true, "policy", nil }, Client: func(context.Context, string, io.Writer) (daemonv1connect.DaemonServiceClient, error) {
		return client, nil
	}}
	calls := []func() error{func() error { _, err := service.Submit(ctx, SubmitRequest{}, io.Discard); return err }, func() error { _, err := service.Get(ctx, "root", "id", io.Discard); return err }, func() error { _, err := service.Cancel(ctx, "root", "id", io.Discard); return err }, func() error { _, err := service.Wait(ctx, "root", "id", "cursor", io.Discard); return err }}
	for _, call := range calls {
		if err := call(); err != sentinel {
			t.Fatal(err)
		}
	}
	service.Client = func(context.Context, string, io.Writer) (daemonv1connect.DaemonServiceClient, error) {
		return nil, sentinel
	}
	for _, call := range calls {
		if err := call(); err != sentinel {
			t.Fatal(err)
		}
	}
	service.Client = func(context.Context, string, io.Writer) (daemonv1connect.DaemonServiceClient, error) {
		t.Fatal("policy denial reached bootstrap")
		return nil, nil
	}
	service.RawPolicy = func(string) (bool, string, error) { return false, "policy", nil }
	if _, err := service.Submit(ctx, SubmitRequest{}, io.Discard); err == nil || !strings.Contains(err.Error(), "mode 0600") {
		t.Fatal(err)
	}
}
func TestGetCancelAndWaitPreserveContextIdentityAndReceipt(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	want := &daemonv1.Operation{OperationId: "id", State: daemonv1.OperationState_OPERATION_STATE_CANCELLED}
	called := 0
	check := func(got context.Context, id string) {
		called++
		if got != ctx || id != "id" {
			t.Fatal("request identity")
		}
	}
	client := fakeClient{get: func(got context.Context, id string) (*daemonv1.Operation, error) { check(got, id); return want, nil }, cancel: func(got context.Context, id string) (*daemonv1.Operation, error) { check(got, id); return want, nil }, wait: func(got context.Context, r *daemonv1.WaitOperationRequest) (*daemonv1.Operation, error) {
		check(got, r.OperationId)
		if r.AfterCursor != "cursor" {
			t.Fatal(r)
		}
		return want, nil
	}}
	service := Service{Client: func(got context.Context, root string, p io.Writer) (daemonv1connect.DaemonServiceClient, error) {
		if got != ctx || root != "root" || p != io.Discard {
			t.Fatal("bootstrap inputs")
		}
		return client, nil
	}}
	a, err := service.Get(ctx, "root", "id", io.Discard)
	if err != nil || a != want {
		t.Fatal(a, err)
	}
	b, err := service.Cancel(ctx, "root", "id", io.Discard)
	if err != nil || b != want {
		t.Fatal(b, err)
	}
	c, err := service.Wait(ctx, "root", "id", "cursor", io.Discard)
	if err != nil || c != want || called != 3 {
		t.Fatal(c, err, called)
	}
}
