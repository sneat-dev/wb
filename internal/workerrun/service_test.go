package workerrun

import (
	"bytes"
	"context"
	"errors"
	"io"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/daemon"
	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
	"github.com/sneat-dev/wb/internal/runqueue"
)

type workerClient struct {
	daemonv1connect.DaemonServiceClient
	register   func(context.Context, *connect.Request[daemonv1.RegisterWorkerRequest]) (*connect.Response[daemonv1.RegisterWorkerResponse], error)
	lease      func(context.Context, *connect.Request[daemonv1.LeaseOperationRequest]) (*connect.Response[daemonv1.LeaseOperationResponse], error)
	complete   func(context.Context, *connect.Request[daemonv1.CompleteOperationRequest]) (*connect.Response[daemonv1.CompleteOperationResponse], error)
	heartbeat  func(context.Context, *connect.Request[daemonv1.HeartbeatOperationRequest]) (*connect.Response[daemonv1.HeartbeatOperationResponse], error)
	disconnect func(context.Context, *connect.Request[daemonv1.DisconnectWorkerRequest]) (*connect.Response[daemonv1.DisconnectWorkerResponse], error)
}

func (c *workerClient) RegisterWorker(ctx context.Context, r *connect.Request[daemonv1.RegisterWorkerRequest]) (*connect.Response[daemonv1.RegisterWorkerResponse], error) {
	return c.register(ctx, r)
}
func (c *workerClient) LeaseOperation(ctx context.Context, r *connect.Request[daemonv1.LeaseOperationRequest]) (*connect.Response[daemonv1.LeaseOperationResponse], error) {
	return c.lease(ctx, r)
}
func (c *workerClient) CompleteOperation(ctx context.Context, r *connect.Request[daemonv1.CompleteOperationRequest]) (*connect.Response[daemonv1.CompleteOperationResponse], error) {
	return c.complete(ctx, r)
}
func (c *workerClient) HeartbeatOperation(ctx context.Context, r *connect.Request[daemonv1.HeartbeatOperationRequest]) (*connect.Response[daemonv1.HeartbeatOperationResponse], error) {
	return c.heartbeat(ctx, r)
}
func (c *workerClient) DisconnectWorker(ctx context.Context, r *connect.Request[daemonv1.DisconnectWorkerRequest]) (*connect.Response[daemonv1.DisconnectWorkerResponse], error) {
	return c.disconnect(ctx, r)
}
func testService() Service {
	return New(Dependencies{Snapshot: func() buildinfo.Report { return buildinfo.Report{Version: "v"} }})
}
func testRegistration() *daemonv1.WorkerRegistration {
	return &daemonv1.WorkerRegistration{WorkerId: "id", WorkerGeneration: "wg", SchedulerGeneration: "sg", HeartbeatMilliseconds: 1}
}
func testAssignment() *daemonv1.WorkerAssignment {
	return &daemonv1.WorkerAssignment{WorkerId: "id", WorkerGeneration: "wg", SchedulerGeneration: "sg", OperationId: "op", LeaseId: "lease", WorkingDirectory: "/allowed", Argv: []string{"child"}}
}
func stopHeartbeat(service *Service) {
	service.ops.Ticker = func(time.Duration) (<-chan time.Time, func()) { return make(chan time.Time), func() {} }
}
func TestConnectionBuildMetadataRegistrationAndAnnouncementFailureOrdering(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"register-error", "empty-registration", "announcement-error", "empty-lease", "lease-error", "assignment-error", "waiting"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			sentinel := errors.New("stage refused")
			service := testService()
			service.deps.Snapshot = func() buildinfo.Report { return buildinfo.Report{Version: "v", Revision: "revision", Modified: true} }
			calls, announcements, disconnects, leases := 0, 0, 0, 0
			client := &workerClient{}
			client.register = func(ctx context.Context, r *connect.Request[daemonv1.RegisterWorkerRequest]) (*connect.Response[daemonv1.RegisterWorkerResponse], error) {
				calls++
				if r.Msg.WorkerId != "id" || r.Msg.Build != "v@revision+modified" || r.Msg.ProtocolVersion != daemon.ProtocolVersion || r.Msg.Os != runtime.GOOS || r.Msg.Arch != runtime.GOARCH || r.Msg.CpuCapacity != 3 || r.Msg.PermittedRoots[0] != "/allowed" {
					t.Fatalf("registration=%+v", r.Msg)
				}
				if mode == "register-error" {
					return nil, sentinel
				}
				if mode == "empty-registration" {
					return connect.NewResponse(&daemonv1.RegisterWorkerResponse{}), nil
				}
				return connect.NewResponse(&daemonv1.RegisterWorkerResponse{Registration: testRegistration()}), nil
			}
			client.lease = func(_ context.Context, r *connect.Request[daemonv1.LeaseOperationRequest]) (*connect.Response[daemonv1.LeaseOperationResponse], error) {
				leases++
				if r.Msg.WaitMilliseconds != 8000 || r.Msg.WorkerId != "id" || r.Msg.WorkerGeneration != "wg" {
					t.Fatalf("lease=%+v", r.Msg)
				}
				if mode == "empty-lease" {
					return connect.NewResponse(&daemonv1.LeaseOperationResponse{}), nil
				}
				if mode == "assignment-error" {
					a := testAssignment()
					a.WorkerGeneration = "wrong"
					return connect.NewResponse(&daemonv1.LeaseOperationResponse{Assignment: a}), nil
				}
				if mode == "waiting" && leases == 1 {
					return connect.NewResponse(&daemonv1.LeaseOperationResponse{Assignment: &daemonv1.WorkerAssignment{}}), nil
				}
				return nil, sentinel
			}
			client.disconnect = func(ctx context.Context, r *connect.Request[daemonv1.DisconnectWorkerRequest]) (*connect.Response[daemonv1.DisconnectWorkerResponse], error) {
				disconnects++
				deadline, ok := ctx.Deadline()
				if ctx.Err() != nil || !ok || time.Until(deadline) > time.Second || r.Msg.WorkerGeneration != "wg" || r.Msg.WorkerId != "id" {
					t.Fatalf("disconnect identity/deadline=%+v %v", r.Msg, deadline)
				}
				return nil, sentinel
			}
			var diagnostics bytes.Buffer
			request := ConnectRequest{WorkerID: "id", Roots: []string{"/allowed"}, CPUCapacity: 3}
			err := service.connection(t.Context(), request, client, func(result ConnectResult) error {
				announcements++
				if result.Build != "v@revision+modified" || result.WorkerGeneration != "wg" || result.SchedulerGeneration != "sg" || result.HeartbeatMilliseconds != 1 || result.OS != runtime.GOOS || result.Arch != runtime.GOARCH {
					t.Fatalf("result=%+v", result)
				}
				result.PermittedRoots[0] = "mutated"
				if request.Roots[0] != "/allowed" {
					t.Fatal("result aliases caller roots")
				}
				if mode == "announcement-error" {
					return sentinel
				}
				return nil
			}, &diagnostics, true)
			if err == nil {
				t.Fatal("stage error lost")
			}
			if mode == "register-error" || mode == "announcement-error" || mode == "lease-error" || mode == "waiting" {
				if !errors.Is(err, sentinel) {
					t.Fatalf("identity=%v", err)
				}
			}
			expectedDisconnect := 1
			if mode == "register-error" || mode == "empty-registration" || mode == "announcement-error" {
				expectedDisconnect = 0
			}
			if disconnects != expectedDisconnect || calls != 1 {
				t.Fatalf("calls/disconnect=%d/%d", calls, disconnects)
			}
			if mode == "waiting" && !strings.Contains(diagnostics.String(), "wb: worker id heartbeat: waiting\n") {
				t.Fatalf("waiting diagnostic=%q", diagnostics.String())
			}
			if (mode == "register-error" || mode == "empty-registration") && announcements != 0 {
				t.Fatal("announced before registration")
			}
		})
	}
}
func TestConnectCancellationReconnectAndSingleAnnouncementPolicy(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"already-canceled", "client-error", "connection-retry", "cancel-during-backoff"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			service := testService()
			calls, announcements, waits := 0, 0, 0
			sentinel := errors.New("disconnect stage")
			client := &workerClient{register: func(context.Context, *connect.Request[daemonv1.RegisterWorkerRequest]) (*connect.Response[daemonv1.RegisterWorkerResponse], error) {
				return connect.NewResponse(&daemonv1.RegisterWorkerResponse{Registration: testRegistration()}), nil
			}, lease: func(context.Context, *connect.Request[daemonv1.LeaseOperationRequest]) (*connect.Response[daemonv1.LeaseOperationResponse], error) {
				return nil, sentinel
			}, disconnect: func(context.Context, *connect.Request[daemonv1.DisconnectWorkerRequest]) (*connect.Response[daemonv1.DisconnectWorkerResponse], error) {
				return nil, nil
			}}
			var diagnostics bytes.Buffer
			service.deps.Client = func(got context.Context, root string, out io.Writer) (daemonv1connect.DaemonServiceClient, error) {
				calls++
				if got != ctx || root != "root" || out != &diagnostics {
					t.Fatal("current request was lost")
				}
				if calls == 3 {
					cancel()
					return nil, sentinel
				}
				if mode == "client-error" {
					return nil, sentinel
				}
				return client, nil
			}
			service.ops.After = func(d time.Duration) <-chan time.Time {
				waits++
				if d != 2*time.Second {
					t.Fatalf("backoff=%v", d)
				}
				ch := make(chan time.Time, 1)
				if mode == "cancel-during-backoff" {
					cancel()
				} else {
					ch <- time.Time{}
				}
				return ch
			}
			if mode == "already-canceled" {
				cancel()
			}
			if err := service.Connect(ctx, ConnectRequest{ProjectsRoot: "root", WorkerID: "id"}, func(ConnectResult) error { announcements++; return nil }, &diagnostics); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "already-canceled":
				if calls != 0 || waits != 0 {
					t.Fatal("canceled request bootstrapped")
				}
			case "client-error":
				if announcements != 0 || calls != 3 || waits != 2 {
					t.Fatalf("calls/waits/announce=%d/%d/%d", calls, waits, announcements)
				}
			case "connection-retry":
				if announcements != 1 || waits != 2 {
					t.Fatalf("first announcement mutated: %d/%d", announcements, waits)
				}
			case "cancel-during-backoff":
				if calls != 1 || waits != 1 || announcements != 1 {
					t.Fatal("backoff cancellation not honored")
				}
			}
			if waits > 0 && !strings.Contains(diagnostics.String(), "reconnecting after: disconnect stage") {
				t.Fatalf("diagnostic=%q", diagnostics.String())
			}
		})
	}
}
func TestHeartbeatControlledIntervalsRefusalsAndShutdown(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"zero", "too-long", "normal", "rpc-error", "empty", "terminal", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			service := testService()
			registration := testRegistration()
			if mode == "zero" {
				registration.HeartbeatMilliseconds = 0
			}
			if mode == "too-long" {
				registration.HeartbeatMilliseconds = 10001
			}
			ticks := make(chan time.Time, 1)
			stops := 0
			service.ops.Ticker = func(interval time.Duration) (<-chan time.Time, func()) {
				expected := time.Millisecond
				if mode == "zero" || mode == "too-long" {
					expected = 5 * time.Second
				}
				if interval != expected {
					t.Fatalf("interval=%v", interval)
				}
				return ticks, func() { stops++ }
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var progress atomic.Value
			progress.Store("running")
			errOut := make(chan error, 1)
			done := make(chan struct{})
			sentinel := errors.New("heartbeat denied")
			client := &workerClient{heartbeat: func(got context.Context, r *connect.Request[daemonv1.HeartbeatOperationRequest]) (*connect.Response[daemonv1.HeartbeatOperationResponse], error) {
				if got != ctx || r.Msg.OperationId != "op" || r.Msg.LeaseId != "lease" || r.Msg.WorkerGeneration != "wg" || r.Msg.Progress != "running" {
					t.Fatalf("heartbeat=%+v", r.Msg)
				}
				switch mode {
				case "rpc-error":
					return nil, sentinel
				case "empty":
					return connect.NewResponse(&daemonv1.HeartbeatOperationResponse{}), nil
				case "terminal":
					return connect.NewResponse(&daemonv1.HeartbeatOperationResponse{Operation: &daemonv1.Operation{State: daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED}}), nil
				}
				cancel()
				return connect.NewResponse(&daemonv1.HeartbeatOperationResponse{Operation: &daemonv1.Operation{State: daemonv1.OperationState_OPERATION_STATE_RUNNING}}), nil
			}}
			var out bytes.Buffer
			if mode == "cancel" {
				cancel()
			} else {
				ticks <- time.Time{}
			}
			service.heartbeat(ctx, &out, client, registration, testAssignment(), &progress, cancel, errOut, done)
			select {
			case <-done:
			default:
				t.Fatal("heartbeat not joined")
			}
			if stops != 1 || ctx.Err() == nil {
				t.Fatalf("stop/cancel=%d/%v", stops, ctx.Err())
			}
			select {
			case err := <-errOut:
				switch mode {
				case "rpc-error":
					if !errors.Is(err, sentinel) {
						t.Fatalf("identity=%v", err)
					}
				case "empty":
					if !strings.Contains(err.Error(), "empty heartbeat") {
						t.Fatal(err)
					}
				case "terminal":
					if !strings.Contains(err.Error(), "became terminal") {
						t.Fatal(err)
					}
				default:
					t.Fatalf("unexpected error=%v", err)
				}
			default:
				if mode == "rpc-error" || mode == "empty" || mode == "terminal" {
					t.Fatal("refusal missing")
				}
			}
			if mode == "normal" && out.String() != "wb: worker id operation op: running\n" {
				t.Fatalf("running=%q", out.String())
			}
		})
	}
}
func TestDefaultTimerBindingsProduceRealEvents(t *testing.T) {
	t.Parallel()
	ops := defaultOperations()
	select {
	case <-ops.After(time.Nanosecond):
	case <-time.After(time.Second):
		t.Fatal("real reconnect timer missing")
	}
	ticks, stop := ops.Ticker(time.Nanosecond)
	t.Cleanup(stop)
	select {
	case <-ticks:
	case <-time.After(time.Second):
		t.Fatal("real heartbeat timer missing")
	}
}

// Fake exit statuses are execution-stage contracts, separate from native child proofs.
type exitStatus struct{ code int }

func (e exitStatus) Error() string { return "child failed" }
func (e exitStatus) ExitCode() int { return e.code }
func TestAssignmentStageFailuresCompletionAndLeaseRelease(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"permission-error", "outside", "admission-error", "run-error", "exit-status", "complete-error", "success"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			service := testService()
			stopHeartbeat(&service)
			a := testAssignment()
			sentinel := errors.New("stage failed")
			releases, runs, completes := 0, 0, 0
			service.ops.PermitsDirectory = func([]string, string) (bool, error) {
				if mode == "permission-error" {
					return false, sentinel
				}
				return mode != "outside", nil
			}
			service.ops.PID = func() int { return 123 }
			service.ops.Admit = func(ctx context.Context, root string, argv []string, self runqueue.Participant, _ *runqueue.Ticket) (runqueue.Admission, error) {
				if root != "root" || self.PID != 123 || self.Worktree != "/allowed" {
					t.Fatalf("admission=%s/%+v", root, self)
				}
				if mode == "admission-error" {
					return runqueue.Admission{}, sentinel
				}
				return runqueue.Admission{Units: 2}, nil
			}
			service.ops.Release = func(*runqueue.Lease) { releases++ }
			service.ops.Run = func(ctx context.Context, got *daemonv1.WorkerAssignment, units int, out, errOut io.Writer) error {
				runs++
				if got != a || units != 2 {
					t.Fatal("execution request changed")
				}
				_, _ = io.WriteString(out, "stdout")
				_, _ = io.WriteString(errOut, "stderr")
				if mode == "run-error" {
					return sentinel
				}
				if mode == "exit-status" {
					return exitStatus{23}
				}
				return nil
			}
			client := &workerClient{complete: func(got context.Context, r *connect.Request[daemonv1.CompleteOperationRequest]) (*connect.Response[daemonv1.CompleteOperationResponse], error) {
				completes++
				if got != t.Context() || got.Err() != nil || r.Msg.OperationId != "op" || r.Msg.LeaseId != "lease" {
					t.Fatalf("completion context/receipt=%v/%+v", got, r.Msg)
				}
				expected := int32(0)
				if mode == "permission-error" || mode == "outside" || mode == "admission-error" || mode == "run-error" {
					expected = 1
				}
				if mode == "exit-status" {
					expected = 23
				}
				if r.Msg.ExitCode != expected {
					t.Fatalf("status=%d", r.Msg.ExitCode)
				}
				if mode == "permission-error" && !strings.Contains(r.Msg.Error, "worker refused assigned cwd: stage failed") {
					t.Fatal(r.Msg.Error)
				}
				if mode == "outside" && !strings.Contains(r.Msg.Error, "outside permitted roots") {
					t.Fatal(r.Msg.Error)
				}
				if runs > 0 && (string(r.Msg.StdoutTail) != "stdout" || string(r.Msg.StderrTail) != "stderr") {
					t.Fatal("tail streams changed")
				}
				if mode == "complete-error" {
					return nil, sentinel
				}
				return connect.NewResponse(&daemonv1.CompleteOperationResponse{}), nil
			}}
			err := service.assignment(t.Context(), "root", io.Discard, client, testRegistration(), []string{"/allowed"}, a)
			if mode == "complete-error" {
				if !errors.Is(err, sentinel) {
					t.Fatalf("completion identity=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if completes != 1 || runs != releases {
				t.Fatalf("complete/run/release=%d/%d/%d", completes, runs, releases)
			}
		})
	}
}
func TestAssignmentHeartbeatFailurePrecedesCompletionAndJoinsStop(t *testing.T) {
	t.Parallel()
	service := testService()
	a := testAssignment()
	sentinel := errors.New("lease revoked")
	ticks := make(chan time.Time, 1)
	returned := make(chan struct{})
	stopped := make(chan struct{})
	service.ops.Ticker = func(time.Duration) (<-chan time.Time, func()) { return ticks, func() { close(stopped) } }
	service.ops.PermitsDirectory = func([]string, string) (bool, error) { return true, nil }
	service.ops.Admit = func(context.Context, string, []string, runqueue.Participant, *runqueue.Ticket) (runqueue.Admission, error) {
		return runqueue.Admission{}, nil
	}
	service.ops.Run = func(ctx context.Context, _ *daemonv1.WorkerAssignment, _ int, _, _ io.Writer) error {
		ticks <- time.Time{}
		<-ctx.Done()
		close(returned)
		return ctx.Err()
	}
	service.ops.Release = func(*runqueue.Lease) {}
	client := &workerClient{heartbeat: func(context.Context, *connect.Request[daemonv1.HeartbeatOperationRequest]) (*connect.Response[daemonv1.HeartbeatOperationResponse], error) {
		return nil, sentinel
	}, complete: func(context.Context, *connect.Request[daemonv1.CompleteOperationRequest]) (*connect.Response[daemonv1.CompleteOperationResponse], error) {
		t.Fatal("completed after heartbeat authority refusal")
		return nil, nil
	}}
	if err := service.assignment(t.Context(), "root", io.Discard, client, testRegistration(), nil, a); !errors.Is(err, sentinel) {
		t.Fatalf("heartbeat identity=%v", err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("ticker writer outlived assignment")
	}
	select {
	case <-returned:
	default:
		t.Fatal("child stage unjoined")
	}
}
