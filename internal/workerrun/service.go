package workerrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/sneat-dev/wb/internal/daemon"
	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
	"github.com/sneat-dev/wb/internal/operationreceipt"
	"github.com/sneat-dev/wb/internal/runqueue"
)

const reconnectInterval = 2 * time.Second

type ConnectResult struct {
	WorkerID              string   `json:"worker_id"`
	WorkerGeneration      string   `json:"worker_generation"`
	SchedulerGeneration   string   `json:"scheduler_generation"`
	Build                 string   `json:"build"`
	ProtocolVersion       uint32   `json:"protocol_version"`
	OS                    string   `json:"os"`
	Arch                  string   `json:"arch"`
	CPUCapacity           uint32   `json:"cpu_capacity"`
	PermittedRoots        []string `json:"permitted_roots"`
	HeartbeatMilliseconds uint32   `json:"heartbeat_milliseconds"`
}

func (service Service) Connect(ctx context.Context, request ConnectRequest, announce func(ConnectResult) error, diagnostics io.Writer) error {
	first := true
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		client, err := service.deps.Client(ctx, request.ProjectsRoot, diagnostics)
		if err == nil {
			err = service.connection(ctx, request, client, announce, diagnostics, first)
			first = false
		}
		if ctx.Err() != nil {
			return nil
		}
		_, _ = fmt.Fprintf(diagnostics, "wb: worker %s reconnecting after: %v\n", request.WorkerID, err)
		select {
		case <-ctx.Done():
			return nil
		case <-service.ops.After(reconnectInterval):
		}
	}
}
func (service Service) connection(ctx context.Context, request ConnectRequest, client daemonv1connect.DaemonServiceClient, announce func(ConnectResult) error, diagnostics io.Writer, first bool) error {
	version := service.deps.Snapshot()
	build := version.Version
	if version.Revision != "" {
		build += "@" + version.Revision
	}
	if version.Modified {
		build += "+modified"
	}
	registered, err := client.RegisterWorker(ctx, connect.NewRequest(&daemonv1.RegisterWorkerRequest{
		WorkerId: request.WorkerID, Build: build, ProtocolVersion: daemon.ProtocolVersion,
		Os: runtime.GOOS, Arch: runtime.GOARCH, CpuCapacity: request.CPUCapacity, PermittedRoots: request.Roots,
	}))
	if err != nil {
		return err
	}
	registration := registered.Msg.Registration
	if registration == nil {
		return errors.New("daemon returned an empty worker registration")
	}
	if first {
		result := ConnectResult{
			WorkerID: request.WorkerID, WorkerGeneration: registration.WorkerGeneration, SchedulerGeneration: registration.SchedulerGeneration,
			Build: build, ProtocolVersion: daemon.ProtocolVersion, OS: runtime.GOOS, Arch: runtime.GOARCH,
			CPUCapacity: request.CPUCapacity, PermittedRoots: append([]string(nil), request.Roots...), HeartbeatMilliseconds: registration.HeartbeatMilliseconds,
		}
		if err := announce(result); err != nil {
			return err
		}
	}
	defer service.disconnect(client, registration)
	for {
		response, err := client.LeaseOperation(ctx, connect.NewRequest(&daemonv1.LeaseOperationRequest{
			WorkerId: request.WorkerID, WorkerGeneration: registration.WorkerGeneration, WaitMilliseconds: 8_000,
		}))
		if err != nil {
			return err
		}
		assignment := response.Msg.Assignment
		if assignment == nil {
			return errors.New("daemon returned an empty worker lease response")
		}
		if assignment.OperationId == "" {
			_, _ = fmt.Fprintf(diagnostics, "wb: worker %s heartbeat: waiting\n", request.WorkerID)
			continue
		}
		if err := service.assignment(ctx, request.ProjectsRoot, diagnostics, client, registration, request.Roots, assignment); err != nil {
			return err
		}
	}
}
func (service Service) disconnect(client daemonv1connect.DaemonServiceClient, registration *daemonv1.WorkerRegistration) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _ = client.DisconnectWorker(ctx, connect.NewRequest(&daemonv1.DisconnectWorkerRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration,
	}))
}
func (service Service) assignment(parent context.Context, projectsRoot string, diagnostics io.Writer, client daemonv1connect.DaemonServiceClient, registration *daemonv1.WorkerRegistration, roots []string, assignment *daemonv1.WorkerAssignment) error {
	if assignment.WorkerId != registration.WorkerId || assignment.WorkerGeneration != registration.WorkerGeneration || assignment.SchedulerGeneration != registration.SchedulerGeneration {
		return errors.New("daemon returned an assignment for a different worker or scheduler generation")
	}
	permitted, permissionErr := service.ops.PermitsDirectory(roots, assignment.WorkingDirectory)
	if permissionErr != nil || !permitted {
		reason := fmt.Sprintf("worker refused assigned cwd outside permitted roots: %s", assignment.WorkingDirectory)
		if permissionErr != nil {
			reason = "worker refused assigned cwd: " + permissionErr.Error()
		}
		_, err := client.CompleteOperation(parent, connect.NewRequest(&daemonv1.CompleteOperationRequest{
			WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration,
			OperationId: assignment.OperationId, LeaseId: assignment.LeaseId, ExitCode: 1, Error: reason,
		}))
		return err
	}
	if len(assignment.Argv) == 0 || strings.TrimSpace(assignment.Argv[0]) == "" {
		return errors.New("daemon returned an assignment without a command")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var progress atomic.Value
	progress.Store("waiting for CPU capacity")
	heartbeatErrors := make(chan error, 1)
	heartbeatDone := make(chan struct{})
	go service.heartbeat(ctx, diagnostics, client, registration, assignment, &progress, cancel, heartbeatErrors, heartbeatDone)

	self := runqueue.Participant{PID: service.ops.PID(), Summary: runqueue.Summary(assignment.Argv), Worktree: assignment.WorkingDirectory}
	// Review finding (PR #628, M4): the daemon may hand a worker an
	// explicit, caller-declared CpuUnits (the trusted raw-execution
	// fallback); honor it directly via the plain budget-sum pool, the same
	// way internal/daemon/service.go's own executor does, instead of
	// reclassifying argv adaptively.
	var admission runqueue.Admission
	var err error
	if explicit := int(assignment.CpuUnits); explicit != 0 {
		admission, err = service.ops.AdmitExplicit(ctx, projectsRoot, explicit, self)
	} else {
		admission, err = service.ops.Admit(ctx, projectsRoot, assignment.Argv, self, nil)
	}
	units := admission.Units
	if err == nil {
		progress.Store("running")
	}
	var stdout, stderr workerTailBuffer
	exitCode := 1
	if err == nil {
		err = service.ops.Run(ctx, assignment, units, &stdout, &stderr)
		service.ops.Release(admission.Lease)
		exitCode = 0
		if err != nil {
			exitCode = 1
			var exitError interface{ ExitCode() int }
			if errors.As(err, &exitError) {
				exitCode = exitError.ExitCode()
			}
		}
	}
	cancel()
	<-heartbeatDone
	select {
	case heartbeatErr := <-heartbeatErrors:
		if heartbeatErr != nil {
			return heartbeatErr
		}
	default:
	}
	errorText := ""
	if err != nil {
		errorText = err.Error()
	}
	_, completeErr := client.CompleteOperation(parent, connect.NewRequest(&daemonv1.CompleteOperationRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration,
		OperationId: assignment.OperationId, LeaseId: assignment.LeaseId, ExitCode: int32(exitCode),
		StdoutTail: stdout.Bytes(), StderrTail: stderr.Bytes(), Error: errorText,
	}))
	return completeErr
}
func (service Service) heartbeat(ctx context.Context, out io.Writer, client daemonv1connect.DaemonServiceClient, registration *daemonv1.WorkerRegistration, assignment *daemonv1.WorkerAssignment, progress *atomic.Value, cancel context.CancelFunc, errorsOut chan<- error, done chan<- struct{}) {
	defer close(done)
	interval := time.Duration(registration.HeartbeatMilliseconds) * time.Millisecond
	if interval <= 0 || interval > 10*time.Second {
		interval = 5 * time.Second
	}
	ticks, stop := service.ops.Ticker(interval)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			status, _ := progress.Load().(string)
			response, err := client.HeartbeatOperation(ctx, connect.NewRequest(&daemonv1.HeartbeatOperationRequest{
				WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration,
				OperationId: assignment.OperationId, LeaseId: assignment.LeaseId, Progress: status,
			}))
			if err != nil {
				errorsOut <- err
				cancel()
				return
			}
			if response.Msg.Operation == nil {
				errorsOut <- errors.New("daemon returned an empty heartbeat response")
				cancel()
				return
			}
			if operationreceipt.Terminal(response.Msg.Operation.State) {
				errorsOut <- errors.New("operation became terminal while the worker was executing it")
				cancel()
				return
			}
			_, _ = fmt.Fprintf(out, "wb: worker %s operation %s: %s\n", registration.WorkerId, assignment.OperationId, status)
		}
	}
}
func CanonicalRoots(input []string) ([]string, error) {
	if len(input) == 0 {
		return nil, errors.New("at least one --root is required")
	}
	seen := map[string]bool{}
	result := make([]string, 0, len(input))
	for _, value := range input {
		if !filepath.IsAbs(value) {
			return nil, fmt.Errorf("--root must be absolute: %s", value)
		}
		root, err := filepath.EvalSymlinks(filepath.Clean(value))
		if err != nil {
			return nil, fmt.Errorf("resolve --root %s: %w", value, err)
		}
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("--root is not a directory: %s", value)
		}
		if !seen[root] {
			seen[root] = true
			result = append(result, root)
		}
	}
	return result, nil
}
func workerPermitsDirectory(roots []string, cwd string) (bool, error) {
	if !filepath.IsAbs(cwd) {
		return false, errors.New("assigned cwd is not absolute")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(cwd))
	if err != nil {
		return false, err
	}
	for _, root := range roots {
		relative, err := filepath.Rel(root, resolved)
		if err == nil && (relative == "." || (relative != ".." && !filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))) {
			return true, nil
		}
	}
	return false, nil
}

type workerTailBuffer struct{ bytes.Buffer }

func (buffer *workerTailBuffer) Write(contents []byte) (int, error) {
	written := len(contents)
	_, _ = buffer.Buffer.Write(contents)
	const limit = 64 << 10
	if buffer.Len() > limit {
		kept := append([]byte(nil), buffer.Bytes()[buffer.Len()-limit:]...)
		buffer.Reset()
		_, _ = buffer.Buffer.Write(kept)
	}
	return written, nil
}
