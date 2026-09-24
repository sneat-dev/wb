package daemonv1connect

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"

	v1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
)

// The generated handler must remain a full implementation of the service
// interface; this also keeps UnimplementedDaemonServiceHandler honest.
var _ DaemonServiceHandler = (*dapbDaemonService)(nil)
var _ DaemonServiceHandler = UnimplementedDaemonServiceHandler{}

// dapbDaemonService is a hermetic in-process implementation of the generated
// DaemonServiceHandler. It records which RPC ran and echoes request fields into
// the response so tests can prove the wire round trip carried the payload.
type dapbDaemonService struct {
	mu    sync.Mutex
	calls map[string]int
}

func dapbNewDaemonService() *dapbDaemonService {
	return &dapbDaemonService{calls: make(map[string]int)}
}

func (s *dapbDaemonService) record(rpc string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[rpc]++
}

func (s *dapbDaemonService) callCount(rpc string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[rpc]
}

func (s *dapbDaemonService) GetDaemonInfo(context.Context, *connect.Request[v1.GetDaemonInfoRequest]) (*connect.Response[v1.GetDaemonInfoResponse], error) {
	s.record("GetDaemonInfo")
	return connect.NewResponse(&v1.GetDaemonInfoResponse{
		Build:               "dapb-build",
		ProtocolVersion:     3,
		QueueSchema:         4,
		SchedulerGeneration: "dapb-scheduler",
		State:               v1.DaemonState_DAEMON_STATE_READY,
		CpuBudget:           8,
	}), nil
}

func (s *dapbDaemonService) SubmitOperation(_ context.Context, req *connect.Request[v1.SubmitOperationRequest]) (*connect.Response[v1.Operation], error) {
	s.record("SubmitOperation")
	return connect.NewResponse(&v1.Operation{
		OperationId:    "op:" + req.Msg.GetIdempotencyKey(),
		CommandKind:    req.Msg.GetEnvironment()["kind"],
		ArgumentCount:  uint32(len(req.Msg.GetArgv())),
		CpuUnits:       req.Msg.GetCpuUnits(),
		TargetWorkerId: req.Msg.GetTargetWorkerId(),
		WorkerId:       req.Msg.GetWorkingDirectory(),
	}), nil
}

func (s *dapbDaemonService) GetOperation(_ context.Context, req *connect.Request[v1.GetOperationRequest]) (*connect.Response[v1.Operation], error) {
	s.record("GetOperation")
	switch req.Msg.GetOperationId() {
	case "":
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("dapb: operation_id is required"))
	case "dapb-missing":
		return nil, connect.NewError(connect.CodeNotFound, errors.New("dapb: operation not found"))
	}
	return connect.NewResponse(&v1.Operation{
		OperationId: req.Msg.GetOperationId(),
		Cursor:      "cursor:" + req.Msg.GetOperationId(),
	}), nil
}

func (s *dapbDaemonService) WaitOperation(_ context.Context, req *connect.Request[v1.WaitOperationRequest]) (*connect.Response[v1.Operation], error) {
	s.record("WaitOperation")
	return connect.NewResponse(&v1.Operation{
		OperationId:           req.Msg.GetOperationId(),
		Progress:              req.Msg.GetAfterCursor(),
		QueueWaitMilliseconds: int64(req.Msg.GetWaitMilliseconds()),
	}), nil
}

func (s *dapbDaemonService) CancelOperation(_ context.Context, req *connect.Request[v1.CancelOperationRequest]) (*connect.Response[v1.Operation], error) {
	s.record("CancelOperation")
	return connect.NewResponse(&v1.Operation{
		OperationId: req.Msg.GetOperationId(),
		State:       v1.OperationState_OPERATION_STATE_CANCELLED,
	}), nil
}

func (s *dapbDaemonService) RegisterWorker(_ context.Context, req *connect.Request[v1.RegisterWorkerRequest]) (*connect.Response[v1.RegisterWorkerResponse], error) {
	s.record("RegisterWorker")
	return connect.NewResponse(&v1.RegisterWorkerResponse{
		Registration: &v1.WorkerRegistration{
			WorkerId:              strings.Join([]string{req.Msg.GetWorkerId(), req.Msg.GetOs(), req.Msg.GetArch()}, "/"),
			WorkerGeneration:      req.Msg.GetBuild(),
			SchedulerGeneration:   req.Msg.GetPermittedRoots()[0],
			HeartbeatMilliseconds: req.Msg.GetProtocolVersion(),
			LeaseMilliseconds:     req.Msg.GetCpuCapacity(),
		},
	}), nil
}

func (s *dapbDaemonService) LeaseOperation(_ context.Context, req *connect.Request[v1.LeaseOperationRequest]) (*connect.Response[v1.LeaseOperationResponse], error) {
	s.record("LeaseOperation")
	return connect.NewResponse(&v1.LeaseOperationResponse{
		Assignment: &v1.WorkerAssignment{
			WorkerId:              req.Msg.GetWorkerId(),
			WorkerGeneration:      req.Msg.GetWorkerGeneration(),
			SchedulerGeneration:   "dapb-scheduler",
			LeaseId:               "dapb-lease",
			LeaseExpiresUnixMilli: 4242,
			OperationId:           "dapb-op",
			WorkingDirectory:      "/dapb/work",
			Argv:                  []string{"dapb-cmd", "dapb-arg"},
			CpuUnits:              req.Msg.GetWaitMilliseconds(),
		},
	}), nil
}

func (s *dapbDaemonService) HeartbeatOperation(_ context.Context, req *connect.Request[v1.HeartbeatOperationRequest]) (*connect.Response[v1.HeartbeatOperationResponse], error) {
	s.record("HeartbeatOperation")
	return connect.NewResponse(&v1.HeartbeatOperationResponse{
		Operation: &v1.Operation{
			OperationId:           req.Msg.GetOperationId(),
			WorkerId:              req.Msg.GetWorkerId(),
			Progress:              req.Msg.GetProgress(),
			LeaseExpiresUnixMilli: 99,
		},
	}), nil
}

func (s *dapbDaemonService) CompleteOperation(_ context.Context, req *connect.Request[v1.CompleteOperationRequest]) (*connect.Response[v1.CompleteOperationResponse], error) {
	s.record("CompleteOperation")
	return connect.NewResponse(&v1.CompleteOperationResponse{
		Operation: &v1.Operation{
			OperationId: req.Msg.GetOperationId(),
			WorkerId:    req.Msg.GetWorkerId(),
			ExitCode:    req.Msg.GetExitCode(),
			StdoutTail:  req.Msg.GetStdoutTail(),
			StderrTail:  req.Msg.GetStderrTail(),
			Error:       req.Msg.GetError(),
		},
	}), nil
}

func (s *dapbDaemonService) DisconnectWorker(_ context.Context, req *connect.Request[v1.DisconnectWorkerRequest]) (*connect.Response[v1.DisconnectWorkerResponse], error) {
	s.record("DisconnectWorker")
	return connect.NewResponse(&v1.DisconnectWorkerResponse{
		RecoveryRequiredOperations: uint32(len(req.Msg.GetWorkerGeneration())),
	}), nil
}

// dapbMountHandler mounts the generated handler on a loopback test server and
// returns the server plus the mount path.
func dapbMountHandler(t *testing.T, svc DaemonServiceHandler) *httptest.Server {
	t.Helper()
	path, handler := NewDaemonServiceHandler(svc)
	if path != "/wb.daemon.v1.DaemonService/" {
		t.Fatalf("handler mount path = %q, want %q", path, "/wb.daemon.v1.DaemonService/")
	}
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func dapbWant[T comparable](t *testing.T, rpc, field string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s: %s = %v, want %v", rpc, field, got, want)
	}
}

// TestDapbConnectRoundTrip drives all ten generated client methods against the
// generated handler over the default Connect protocol and asserts that request
// payloads reach the service and responses come back intact.
func TestDapbConnectRoundTrip(t *testing.T) {
	t.Parallel()
	svc := dapbNewDaemonService()
	srv := dapbMountHandler(t, svc)

	// The ten subtests below are deliberately NOT t.Parallel(): the
	// assertions after the loop (service call counts, the interceptor
	// count) read state the subtests populate, and that only works because
	// each t.Run call blocks until its subtest returns. Making a subtest
	// parallel makes t.Run return as soon as it calls t.Parallel(), before
	// its body has actually run -- the trailing assertions would then race
	// the subtests themselves and, under -count=1, always read zeros (this
	// exact failure mode shipped once, from an earlier, purely
	// thread-safety-focused pass over this file, and was caught by a full
	// `go test ./...` run, not by -race). intercepted is still atomic
	// because that costs nothing and stays correct if this test is ever
	// restructured to make the subtests independent.
	var intercepted atomic.Int64
	interceptor := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			intercepted.Add(1)
			return next(ctx, req)
		}
	})
	// The trailing slash proves the base URL is normalised, and the options are
	// threaded through connect.WithClientOptions.
	client := NewDaemonServiceClient(srv.Client(), srv.URL+"/", connect.WithInterceptors(interceptor), connect.WithSendGzip())
	if client == nil {
		t.Fatal("NewDaemonServiceClient returned nil")
	}
	ctx := context.Background()

	t.Run("GetDaemonInfo", func(t *testing.T) {
		resp, err := client.GetDaemonInfo(ctx, connect.NewRequest(&v1.GetDaemonInfoRequest{}))
		if err != nil {
			t.Fatalf("GetDaemonInfo error: %v", err)
		}
		dapbWant(t, "GetDaemonInfo", "Build", resp.Msg.GetBuild(), "dapb-build")
		dapbWant(t, "GetDaemonInfo", "ProtocolVersion", resp.Msg.GetProtocolVersion(), uint32(3))
		dapbWant(t, "GetDaemonInfo", "QueueSchema", resp.Msg.GetQueueSchema(), uint32(4))
		dapbWant(t, "GetDaemonInfo", "SchedulerGeneration", resp.Msg.GetSchedulerGeneration(), "dapb-scheduler")
		dapbWant(t, "GetDaemonInfo", "State", resp.Msg.GetState(), v1.DaemonState_DAEMON_STATE_READY)
		dapbWant(t, "GetDaemonInfo", "CpuBudget", resp.Msg.GetCpuBudget(), uint32(8))
	})

	t.Run("SubmitOperation", func(t *testing.T) {
		resp, err := client.SubmitOperation(ctx, connect.NewRequest(&v1.SubmitOperationRequest{
			IdempotencyKey:   "dapb-idem",
			WorkingDirectory: "/dapb/dir",
			Argv:             []string{"dapb-run", "--x"},
			Environment:      map[string]string{"kind": "dapb-kind"},
			CpuUnits:         6,
			LocalRawCommand:  true,
			TargetWorkerId:   "dapb-worker",
		}))
		if err != nil {
			t.Fatalf("SubmitOperation error: %v", err)
		}
		dapbWant(t, "SubmitOperation", "OperationId", resp.Msg.GetOperationId(), "op:dapb-idem")
		dapbWant(t, "SubmitOperation", "CommandKind", resp.Msg.GetCommandKind(), "dapb-kind")
		dapbWant(t, "SubmitOperation", "ArgumentCount", resp.Msg.GetArgumentCount(), uint32(2))
		dapbWant(t, "SubmitOperation", "CpuUnits", resp.Msg.GetCpuUnits(), uint32(6))
		dapbWant(t, "SubmitOperation", "TargetWorkerId", resp.Msg.GetTargetWorkerId(), "dapb-worker")
		dapbWant(t, "SubmitOperation", "WorkerId", resp.Msg.GetWorkerId(), "/dapb/dir")
	})

	t.Run("GetOperation", func(t *testing.T) {
		resp, err := client.GetOperation(ctx, connect.NewRequest(&v1.GetOperationRequest{OperationId: "dapb-op-1"}))
		if err != nil {
			t.Fatalf("GetOperation error: %v", err)
		}
		dapbWant(t, "GetOperation", "OperationId", resp.Msg.GetOperationId(), "dapb-op-1")
		dapbWant(t, "GetOperation", "Cursor", resp.Msg.GetCursor(), "cursor:dapb-op-1")
	})

	t.Run("WaitOperation", func(t *testing.T) {
		resp, err := client.WaitOperation(ctx, connect.NewRequest(&v1.WaitOperationRequest{
			OperationId:      "dapb-op-2",
			AfterCursor:      "dapb-cursor",
			WaitMilliseconds: 1500,
		}))
		if err != nil {
			t.Fatalf("WaitOperation error: %v", err)
		}
		dapbWant(t, "WaitOperation", "OperationId", resp.Msg.GetOperationId(), "dapb-op-2")
		dapbWant(t, "WaitOperation", "Progress", resp.Msg.GetProgress(), "dapb-cursor")
		dapbWant(t, "WaitOperation", "QueueWaitMilliseconds", resp.Msg.GetQueueWaitMilliseconds(), int64(1500))
	})

	t.Run("CancelOperation", func(t *testing.T) {
		resp, err := client.CancelOperation(ctx, connect.NewRequest(&v1.CancelOperationRequest{OperationId: "dapb-op-3"}))
		if err != nil {
			t.Fatalf("CancelOperation error: %v", err)
		}
		dapbWant(t, "CancelOperation", "OperationId", resp.Msg.GetOperationId(), "dapb-op-3")
		dapbWant(t, "CancelOperation", "State", resp.Msg.GetState(), v1.OperationState_OPERATION_STATE_CANCELLED)
	})

	t.Run("RegisterWorker", func(t *testing.T) {
		resp, err := client.RegisterWorker(ctx, connect.NewRequest(&v1.RegisterWorkerRequest{
			WorkerId:        "dapb-w",
			Build:           "dapb-build-2",
			ProtocolVersion: 7,
			Os:              "linux",
			Arch:            "amd64",
			CpuCapacity:     12,
			PermittedRoots:  []string{"/dapb/root"},
		}))
		if err != nil {
			t.Fatalf("RegisterWorker error: %v", err)
		}
		reg := resp.Msg.GetRegistration()
		if reg == nil {
			t.Fatal("RegisterWorker returned nil registration")
		}
		dapbWant(t, "RegisterWorker", "WorkerId", reg.GetWorkerId(), "dapb-w/linux/amd64")
		dapbWant(t, "RegisterWorker", "WorkerGeneration", reg.GetWorkerGeneration(), "dapb-build-2")
		dapbWant(t, "RegisterWorker", "SchedulerGeneration", reg.GetSchedulerGeneration(), "/dapb/root")
		dapbWant(t, "RegisterWorker", "HeartbeatMilliseconds", reg.GetHeartbeatMilliseconds(), uint32(7))
		dapbWant(t, "RegisterWorker", "LeaseMilliseconds", reg.GetLeaseMilliseconds(), uint32(12))
	})

	t.Run("LeaseOperation", func(t *testing.T) {
		resp, err := client.LeaseOperation(ctx, connect.NewRequest(&v1.LeaseOperationRequest{
			WorkerId:         "dapb-w2",
			WorkerGeneration: "dapb-wg",
			WaitMilliseconds: 3,
		}))
		if err != nil {
			t.Fatalf("LeaseOperation error: %v", err)
		}
		assignment := resp.Msg.GetAssignment()
		if assignment == nil {
			t.Fatal("LeaseOperation returned nil assignment")
		}
		dapbWant(t, "LeaseOperation", "WorkerId", assignment.GetWorkerId(), "dapb-w2")
		dapbWant(t, "LeaseOperation", "WorkerGeneration", assignment.GetWorkerGeneration(), "dapb-wg")
		dapbWant(t, "LeaseOperation", "SchedulerGeneration", assignment.GetSchedulerGeneration(), "dapb-scheduler")
		dapbWant(t, "LeaseOperation", "LeaseId", assignment.GetLeaseId(), "dapb-lease")
		dapbWant(t, "LeaseOperation", "LeaseExpiresUnixMilli", assignment.GetLeaseExpiresUnixMilli(), int64(4242))
		dapbWant(t, "LeaseOperation", "OperationId", assignment.GetOperationId(), "dapb-op")
		dapbWant(t, "LeaseOperation", "WorkingDirectory", assignment.GetWorkingDirectory(), "/dapb/work")
		dapbWant(t, "LeaseOperation", "CpuUnits", assignment.GetCpuUnits(), uint32(3))
		if got := strings.Join(assignment.GetArgv(), ","); got != "dapb-cmd,dapb-arg" {
			t.Errorf("LeaseOperation: Argv = %q, want %q", got, "dapb-cmd,dapb-arg")
		}
	})

	t.Run("HeartbeatOperation", func(t *testing.T) {
		resp, err := client.HeartbeatOperation(ctx, connect.NewRequest(&v1.HeartbeatOperationRequest{
			WorkerId:         "dapb-w3",
			WorkerGeneration: "dapb-wg3",
			OperationId:      "dapb-op-4",
			LeaseId:          "dapb-lease-4",
			Progress:         "50%",
		}))
		if err != nil {
			t.Fatalf("HeartbeatOperation error: %v", err)
		}
		op := resp.Msg.GetOperation()
		if op == nil {
			t.Fatal("HeartbeatOperation returned nil operation")
		}
		dapbWant(t, "HeartbeatOperation", "OperationId", op.GetOperationId(), "dapb-op-4")
		dapbWant(t, "HeartbeatOperation", "WorkerId", op.GetWorkerId(), "dapb-w3")
		dapbWant(t, "HeartbeatOperation", "Progress", op.GetProgress(), "50%")
		dapbWant(t, "HeartbeatOperation", "LeaseExpiresUnixMilli", op.GetLeaseExpiresUnixMilli(), int64(99))
	})

	t.Run("CompleteOperation", func(t *testing.T) {
		resp, err := client.CompleteOperation(ctx, connect.NewRequest(&v1.CompleteOperationRequest{
			WorkerId:         "dapb-w4",
			WorkerGeneration: "dapb-wg4",
			OperationId:      "dapb-op-5",
			LeaseId:          "dapb-lease-5",
			ExitCode:         3,
			StdoutTail:       []byte("dapb-out"),
			StderrTail:       []byte("dapb-err"),
			Error:            "dapb-failure",
		}))
		if err != nil {
			t.Fatalf("CompleteOperation error: %v", err)
		}
		op := resp.Msg.GetOperation()
		if op == nil {
			t.Fatal("CompleteOperation returned nil operation")
		}
		dapbWant(t, "CompleteOperation", "OperationId", op.GetOperationId(), "dapb-op-5")
		dapbWant(t, "CompleteOperation", "WorkerId", op.GetWorkerId(), "dapb-w4")
		dapbWant(t, "CompleteOperation", "ExitCode", op.GetExitCode(), int32(3))
		dapbWant(t, "CompleteOperation", "Error", op.GetError(), "dapb-failure")
		dapbWant(t, "CompleteOperation", "StdoutTail", string(op.GetStdoutTail()), "dapb-out")
		dapbWant(t, "CompleteOperation", "StderrTail", string(op.GetStderrTail()), "dapb-err")
	})

	t.Run("DisconnectWorker", func(t *testing.T) {
		resp, err := client.DisconnectWorker(ctx, connect.NewRequest(&v1.DisconnectWorkerRequest{
			WorkerId:         "dapb-w5",
			WorkerGeneration: "dapb-gen",
		}))
		if err != nil {
			t.Fatalf("DisconnectWorker error: %v", err)
		}
		dapbWant(t, "DisconnectWorker", "RecoveryRequiredOperations", resp.Msg.GetRecoveryRequiredOperations(), uint32(len("dapb-gen")))
	})

	for _, rpc := range []string{
		"GetDaemonInfo", "SubmitOperation", "GetOperation", "WaitOperation",
		"CancelOperation", "RegisterWorker", "LeaseOperation",
		"HeartbeatOperation", "CompleteOperation", "DisconnectWorker",
	} {
		if got := svc.callCount(rpc); got != 1 {
			t.Errorf("service call count for %s = %d, want 1", rpc, got)
		}
	}
	if got := intercepted.Load(); got != 10 {
		t.Errorf("unary interceptor ran %d times, want 10", got)
	}
}

// TestDapbConnectErrorPropagation asserts service-side Connect errors survive the
// generated client round trip with their codes intact.
func TestDapbConnectErrorPropagation(t *testing.T) {
	t.Parallel()
	srv := dapbMountHandler(t, dapbNewDaemonService())
	client := NewDaemonServiceClient(srv.Client(), srv.URL)
	ctx := context.Background()

	resp, err := client.GetOperation(ctx, connect.NewRequest(&v1.GetOperationRequest{}))
	if err == nil {
		t.Fatalf("expected an error for empty operation id, got response %v", resp)
	}
	if resp != nil {
		t.Errorf("response = %v, want nil on error", resp)
	}
	dapbWant(t, "GetOperation", "error code", connect.CodeOf(err), connect.CodeInvalidArgument)

	_, err = client.GetOperation(ctx, connect.NewRequest(&v1.GetOperationRequest{OperationId: "dapb-missing"}))
	dapbWant(t, "GetOperation", "error code", connect.CodeOf(err), connect.CodeNotFound)
	if !strings.Contains(err.Error(), "operation not found") {
		t.Errorf("GetOperation error = %v, want it to mention 'operation not found'", err)
	}
}

// TestDapbConnectGRPCWebClientOption asserts the generated client works with the
// gRPC-Web protocol selected through connect.WithGRPCWeb.
func TestDapbConnectGRPCWebClientOption(t *testing.T) {
	t.Parallel()
	svc := dapbNewDaemonService()
	srv := dapbMountHandler(t, svc)
	client := NewDaemonServiceClient(srv.Client(), srv.URL, connect.WithGRPCWeb())

	resp, err := client.GetDaemonInfo(context.Background(), connect.NewRequest(&v1.GetDaemonInfoRequest{}))
	if err != nil {
		t.Fatalf("gRPC-Web GetDaemonInfo error: %v", err)
	}
	dapbWant(t, "gRPC-Web GetDaemonInfo", "Build", resp.Msg.GetBuild(), "dapb-build")
	dapbWant(t, "GetDaemonInfo call count", "calls", svc.callCount("GetDaemonInfo"), 1)
}

// TestDapbConnectGRPCClientOption asserts the generated client works with the
// gRPC protocol selected through connect.WithGRPC over HTTP/2.
func TestDapbConnectGRPCClientOption(t *testing.T) {
	t.Parallel()
	svc := dapbNewDaemonService()
	path, handler := NewDaemonServiceHandler(svc)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)

	client := NewDaemonServiceClient(srv.Client(), srv.URL, connect.WithGRPC())
	resp, err := client.GetDaemonInfo(context.Background(), connect.NewRequest(&v1.GetDaemonInfoRequest{}))
	if err != nil {
		t.Fatalf("gRPC GetDaemonInfo error: %v", err)
	}
	dapbWant(t, "gRPC GetDaemonInfo", "ProtocolVersion", resp.Msg.GetProtocolVersion(), uint32(3))
	dapbWant(t, "GetDaemonInfo call count", "calls", svc.callCount("GetDaemonInfo"), 1)
}

// TestDapbConnectHandlerUnknownPath asserts the generated handler 404s for paths
// under the service prefix that are not one of its RPCs.
func TestDapbConnectHandlerUnknownPath(t *testing.T) {
	t.Parallel()
	_, handler := NewDaemonServiceHandler(dapbNewDaemonService())

	req := httptest.NewRequest(http.MethodPost, "/wb.daemon.v1.DaemonService/NoSuchRPC", strings.NewReader(""))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	dapbWant(t, "unknown path", "status", rec.Code, http.StatusNotFound)
	if !strings.Contains(rec.Body.String(), "404 page not found") {
		t.Errorf("unknown path body = %q, want a 404 page", rec.Body.String())
	}
}

// TestDapbUnimplementedHandler asserts every generated method of the
// Unimplemented helper fails with CodeUnimplemented and a helpful message.
func TestDapbUnimplementedHandler(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc := UnimplementedDaemonServiceHandler{}

	type dapbUnimplementedCall struct {
		rpc string
		run func() error
	}
	calls := []dapbUnimplementedCall{
		{"GetDaemonInfo", func() error {
			resp, err := svc.GetDaemonInfo(ctx, connect.NewRequest(&v1.GetDaemonInfoRequest{}))
			if resp != nil {
				t.Errorf("GetDaemonInfo response = %v, want nil", resp)
			}
			return err
		}},
		{"SubmitOperation", func() error {
			resp, err := svc.SubmitOperation(ctx, connect.NewRequest(&v1.SubmitOperationRequest{}))
			if resp != nil {
				t.Errorf("SubmitOperation response = %v, want nil", resp)
			}
			return err
		}},
		{"GetOperation", func() error {
			resp, err := svc.GetOperation(ctx, connect.NewRequest(&v1.GetOperationRequest{}))
			if resp != nil {
				t.Errorf("GetOperation response = %v, want nil", resp)
			}
			return err
		}},
		{"WaitOperation", func() error {
			resp, err := svc.WaitOperation(ctx, connect.NewRequest(&v1.WaitOperationRequest{}))
			if resp != nil {
				t.Errorf("WaitOperation response = %v, want nil", resp)
			}
			return err
		}},
		{"CancelOperation", func() error {
			resp, err := svc.CancelOperation(ctx, connect.NewRequest(&v1.CancelOperationRequest{}))
			if resp != nil {
				t.Errorf("CancelOperation response = %v, want nil", resp)
			}
			return err
		}},
		{"RegisterWorker", func() error {
			resp, err := svc.RegisterWorker(ctx, connect.NewRequest(&v1.RegisterWorkerRequest{}))
			if resp != nil {
				t.Errorf("RegisterWorker response = %v, want nil", resp)
			}
			return err
		}},
		{"LeaseOperation", func() error {
			resp, err := svc.LeaseOperation(ctx, connect.NewRequest(&v1.LeaseOperationRequest{}))
			if resp != nil {
				t.Errorf("LeaseOperation response = %v, want nil", resp)
			}
			return err
		}},
		{"HeartbeatOperation", func() error {
			resp, err := svc.HeartbeatOperation(ctx, connect.NewRequest(&v1.HeartbeatOperationRequest{}))
			if resp != nil {
				t.Errorf("HeartbeatOperation response = %v, want nil", resp)
			}
			return err
		}},
		{"CompleteOperation", func() error {
			resp, err := svc.CompleteOperation(ctx, connect.NewRequest(&v1.CompleteOperationRequest{}))
			if resp != nil {
				t.Errorf("CompleteOperation response = %v, want nil", resp)
			}
			return err
		}},
		{"DisconnectWorker", func() error {
			resp, err := svc.DisconnectWorker(ctx, connect.NewRequest(&v1.DisconnectWorkerRequest{}))
			if resp != nil {
				t.Errorf("DisconnectWorker response = %v, want nil", resp)
			}
			return err
		}},
	}

	for _, call := range calls {
		err := call.run()
		if err == nil {
			t.Fatalf("%s: expected error, got nil", call.rpc)
		}
		if got := connect.CodeOf(err); got != connect.CodeUnimplemented {
			t.Errorf("%s: code = %v, want %v", call.rpc, got, connect.CodeUnimplemented)
		}
		want := "wb.daemon.v1.DaemonService." + call.rpc + " is not implemented"
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error = %q, want it to contain %q", call.rpc, err.Error(), want)
		}
	}
}

// TestDapbConnectProcedureConstants pins the generated procedure names to the
// paths the handler routes.
func TestDapbConnectProcedureConstants(t *testing.T) {
	t.Parallel()
	dapbWant(t, "DaemonServiceName", "name", DaemonServiceName, "wb.daemon.v1.DaemonService")
	want := map[string]string{
		"GetDaemonInfo":      DaemonServiceGetDaemonInfoProcedure,
		"SubmitOperation":    DaemonServiceSubmitOperationProcedure,
		"GetOperation":       DaemonServiceGetOperationProcedure,
		"WaitOperation":      DaemonServiceWaitOperationProcedure,
		"CancelOperation":    DaemonServiceCancelOperationProcedure,
		"RegisterWorker":     DaemonServiceRegisterWorkerProcedure,
		"LeaseOperation":     DaemonServiceLeaseOperationProcedure,
		"HeartbeatOperation": DaemonServiceHeartbeatOperationProcedure,
		"CompleteOperation":  DaemonServiceCompleteOperationProcedure,
		"DisconnectWorker":   DaemonServiceDisconnectWorkerProcedure,
	}
	if len(want) != 10 {
		t.Fatalf("expected 10 generated procedures, got %d", len(want))
	}
	for rpc, procedure := range want {
		dapbWant(t, rpc, "procedure", procedure, "/wb.daemon.v1.DaemonService/"+rpc)
	}
}
