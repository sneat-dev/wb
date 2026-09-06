package main

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
	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/daemon"
	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
	"github.com/sneat-dev/wb/internal/process"
	"github.com/sneat-dev/wb/internal/runqueue"
)

const workerReconnectInterval = 2 * time.Second

type workerConnectResult struct {
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

func newWorkerCmd(deps daemonDependencies) *cobra.Command {
	command := &cobra.Command{Use: "worker", Short: "Run sandbox-inherited workers for durable daemon jobs"}
	command.AddCommand(newWorkerConnectCmd(deps))
	return command
}

func newWorkerConnectCmd(deps daemonDependencies) *cobra.Command {
	var workerID, format string
	var roots []string
	var cpuCapacity uint32
	var jsonOut bool
	command := &cobra.Command{
		Use:   "connect",
		Short: "Connect a sandboxed worker to the local WB scheduler",
		Long: `Connect one long-lived worker from inside the caller or harness sandbox.

The daemon only schedules and journals normal jobs. This process independently
checks every assigned working directory against --root, executes with its own
inherited sandbox and environment, heartbeats every five seconds, and returns a
bounded terminal receipt. Repeat --root to permit more canonical roots.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			selected, err := daemonOutputFormat(format, jsonOut)
			if err != nil {
				return usageError(err.Error())
			}
			workerID = strings.TrimSpace(workerID)
			if workerID == "" {
				return usageError("--id is required and must stay stable across reconnects")
			}
			permitted, err := canonicalWorkerRoots(roots)
			if err != nil {
				return usageError(err.Error())
			}
			if cpuCapacity == 0 {
				cpuCapacity = uint32(runqueue.Budget())
			}
			return connectWorker(command, deps, workerID, permitted, cpuCapacity, selected)
		},
	}
	command.Flags().StringVar(&workerID, "id", "", "stable worker ID (required; reuse it after reconnect)")
	command.Flags().StringArrayVar(&roots, "root", nil, "canonical root this worker may execute within (repeatable, required)")
	command.Flags().Uint32Var(&cpuCapacity, "cpu-capacity", 0, "maximum CPU units this worker accepts (default: WB machine budget)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	return command
}

func connectWorker(command *cobra.Command, deps daemonDependencies, workerID string, roots []string, cpuCapacity uint32, format string) error {
	first := true
	for {
		if err := command.Context().Err(); err != nil {
			return nil
		}
		client, err := daemonOperationClient(command.Context(), deps, projectsRoot)
		if err == nil {
			err = runWorkerConnection(command, client, workerID, roots, cpuCapacity, format, first)
			first = false
		}
		if command.Context().Err() != nil {
			return nil
		}
		_, _ = fmt.Fprintf(command.ErrOrStderr(), "wb: worker %s reconnecting after: %v\n", workerID, err)
		select {
		case <-command.Context().Done():
			return nil
		case <-time.After(workerReconnectInterval):
		}
	}
}

func runWorkerConnection(command *cobra.Command, client daemonv1connect.DaemonServiceClient, workerID string, roots []string, cpuCapacity uint32, format string, announce bool) error {
	version := collectVersion()
	build := version.Version
	if version.Revision != "" {
		build += "@" + version.Revision
	}
	if version.Modified {
		build += "+modified"
	}
	registered, err := client.RegisterWorker(command.Context(), connect.NewRequest(&daemonv1.RegisterWorkerRequest{
		WorkerId: workerID, Build: build, ProtocolVersion: daemon.ProtocolVersion,
		Os: runtime.GOOS, Arch: runtime.GOARCH, CpuCapacity: cpuCapacity, PermittedRoots: roots,
	}))
	if err != nil {
		return err
	}
	registration := registered.Msg.Registration
	if registration == nil {
		return errors.New("daemon returned an empty worker registration")
	}
	if announce {
		result := workerConnectResult{
			WorkerID: workerID, WorkerGeneration: registration.WorkerGeneration, SchedulerGeneration: registration.SchedulerGeneration,
			Build: build, ProtocolVersion: daemon.ProtocolVersion, OS: runtime.GOOS, Arch: runtime.GOARCH,
			CPUCapacity: cpuCapacity, PermittedRoots: append([]string(nil), roots...), HeartbeatMilliseconds: registration.HeartbeatMilliseconds,
		}
		if format == "json" {
			if err := writeJSONTo(command.OutOrStdout(), result); err != nil {
				return err
			}
		} else if _, err := fmt.Fprintf(command.OutOrStdout(), "worker %s connected: generation=%s scheduler=%s cpu=%d roots=%d\n", workerID, registration.WorkerGeneration, registration.SchedulerGeneration, cpuCapacity, len(roots)); err != nil {
			return err
		}
	}
	defer disconnectWorker(client, registration)
	for {
		response, err := client.LeaseOperation(command.Context(), connect.NewRequest(&daemonv1.LeaseOperationRequest{
			WorkerId: workerID, WorkerGeneration: registration.WorkerGeneration, WaitMilliseconds: 8_000,
		}))
		if err != nil {
			return err
		}
		assignment := response.Msg.Assignment
		if assignment == nil {
			return errors.New("daemon returned an empty worker lease response")
		}
		if assignment.OperationId == "" {
			_, _ = fmt.Fprintf(command.ErrOrStderr(), "wb: worker %s heartbeat: waiting\n", workerID)
			continue
		}
		if err := executeWorkerAssignment(command, client, registration, roots, assignment); err != nil {
			return err
		}
	}
}

func disconnectWorker(client daemonv1connect.DaemonServiceClient, registration *daemonv1.WorkerRegistration) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _ = client.DisconnectWorker(ctx, connect.NewRequest(&daemonv1.DisconnectWorkerRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration,
	}))
}

func executeWorkerAssignment(command *cobra.Command, client daemonv1connect.DaemonServiceClient, registration *daemonv1.WorkerRegistration, roots []string, assignment *daemonv1.WorkerAssignment) error {
	if assignment.WorkerId != registration.WorkerId || assignment.WorkerGeneration != registration.WorkerGeneration || assignment.SchedulerGeneration != registration.SchedulerGeneration {
		return errors.New("daemon returned an assignment for a different worker or scheduler generation")
	}
	permitted, permissionErr := workerPermitsDirectory(roots, assignment.WorkingDirectory)
	if permissionErr != nil || !permitted {
		reason := fmt.Sprintf("worker refused assigned cwd outside permitted roots: %s", assignment.WorkingDirectory)
		if permissionErr != nil {
			reason = "worker refused assigned cwd: " + permissionErr.Error()
		}
		_, err := client.CompleteOperation(command.Context(), connect.NewRequest(&daemonv1.CompleteOperationRequest{
			WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration,
			OperationId: assignment.OperationId, LeaseId: assignment.LeaseId, ExitCode: 1, Error: reason,
		}))
		return err
	}
	if len(assignment.Argv) == 0 || strings.TrimSpace(assignment.Argv[0]) == "" {
		return errors.New("daemon returned an assignment without a command")
	}
	ctx, cancel := context.WithCancel(command.Context())
	defer cancel()
	var progress atomic.Value
	progress.Store("waiting for CPU capacity")
	heartbeatErrors := make(chan error, 1)
	heartbeatDone := make(chan struct{})
	go workerHeartbeatLoop(ctx, command.ErrOrStderr(), client, registration, assignment, &progress, cancel, heartbeatErrors, heartbeatDone)

	units := int(assignment.CpuUnits)
	lease, _, err := runqueue.Acquire(ctx, projectsRoot, units, runqueue.Budget())
	if err == nil {
		progress.Store("running")
	}
	var stdout, stderr workerTailBuffer
	exitCode := 1
	if err == nil {
		child := process.CommandContext(ctx, assignment.Argv[0], assignment.Argv[1:]...)
		child.Dir = assignment.WorkingDirectory
		child.Env = workerChildEnvironment(os.Environ(), assignment.OperationId, units)
		child.Stdout, child.Stderr = &stdout, &stderr
		err = child.Run()
		lease.Release()
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
	_, completeErr := client.CompleteOperation(command.Context(), connect.NewRequest(&daemonv1.CompleteOperationRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration,
		OperationId: assignment.OperationId, LeaseId: assignment.LeaseId, ExitCode: int32(exitCode),
		StdoutTail: stdout.Bytes(), StderrTail: stderr.Bytes(), Error: errorText,
	}))
	return completeErr
}

func workerHeartbeatLoop(ctx context.Context, out io.Writer, client daemonv1connect.DaemonServiceClient, registration *daemonv1.WorkerRegistration, assignment *daemonv1.WorkerAssignment, progress *atomic.Value, cancel context.CancelFunc, errorsOut chan<- error, done chan<- struct{}) {
	defer close(done)
	interval := time.Duration(registration.HeartbeatMilliseconds) * time.Millisecond
	if interval <= 0 || interval > 10*time.Second {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
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
			if daemonOperationTerminal(response.Msg.Operation.State) {
				errorsOut <- errors.New("operation became terminal while the worker was executing it")
				cancel()
				return
			}
			_, _ = fmt.Fprintf(out, "wb: worker %s operation %s: %s\n", registration.WorkerId, assignment.OperationId, status)
		}
	}
}

func canonicalWorkerRoots(input []string) ([]string, error) {
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

func workerChildEnvironment(base []string, operationID string, units int) []string {
	return mergeWorkerEnvironment(base, map[string]string{
		"GOMAXPROCS": fmt.Sprint(units), "NX_PARALLEL": fmt.Sprint(units),
		"WB_CPU_UNITS": fmt.Sprint(units), "WB_OPERATION_ID": operationID,
	})
}

func mergeWorkerEnvironment(base []string, additions map[string]string) []string {
	result := append([]string(nil), base...)
	for key, value := range additions {
		prefix := key + "="
		filtered := result[:0]
		for _, entry := range result {
			if !strings.HasPrefix(entry, prefix) {
				filtered = append(filtered, entry)
			}
		}
		result = append(filtered, prefix+value)
	}
	return result
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
