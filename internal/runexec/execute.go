package runexec

import (
	"context"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/hostload"
	"github.com/sneat-dev/wb/internal/process"
	"github.com/sneat-dev/wb/internal/runenv"
	"github.com/sneat-dev/wb/internal/runlog"
	"github.com/sneat-dev/wb/internal/runqueue"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

type childResult struct {
	Err                error
	ChildFailed        bool
	ExitCode           int
	UserCPU, SystemCPU time.Duration
}
type executeOperations struct {
	Release     func(*runqueue.Lease)
	Getwd       func() (string, error)
	Begin       func(string, []string, time.Time) (runlog.Recorder, error)
	Finish      func(*runlog.Recorder, int, time.Duration, time.Duration, time.Time) error
	ResolveLoad func(string) (float64, string)
	CheckLoad   func(hostload.Reader, float64, bool) error
	System      hostload.Reader
	Interactive func(io.Writer) bool
	StartChild  func(context.Context, ExecuteRequest, []string, bool) (func() childResult, error)
	Environment func([]string, string, int) []string
	Now         func() time.Time
	PID         func() int
	Ticker      func(time.Duration) (<-chan time.Time, func())
	Admit       func(context.Context, string, []string, runqueue.Participant, func(QueueEvent), time.Duration) (runqueue.Admission, error)
}

// Executor's durations configure this invocation; zero uses production defaults.
// There are no process-global test overrides.
type Executor struct {
	QueueHeartbeat, CourtesyHeartbeat time.Duration
	ops                               *executeOperations
}

func Execute(ctx context.Context, request ExecuteRequest) (ExecuteResult, error) {
	return (Executor{}).Run(ctx, request)
}
func defaultExecuteOperations() executeOperations {
	admission := defaultAdmissionOperations()
	return executeOperations{
		Release: func(lease *runqueue.Lease) { lease.Release() },
		Getwd:   os.Getwd, Begin: runlog.Begin, Finish: func(r *runlog.Recorder, code int, user, system time.Duration, at time.Time) error {
			return r.Finish(code, user, system, at)
		},
		ResolveLoad: hostload.Resolve, CheckLoad: hostload.Check, System: hostload.System,
		Interactive: func(out io.Writer) bool { return console.Interactive(out, false) }, StartChild: startChild,
		Environment: func(argv []string, id string, units int) []string {
			flags := ""
			if units > 0 {
				flags = runqueue.EffectiveGOFLAGS()
			}
			return runenv.Synchronous(os.Environ(), argv, id, units, flags)
		}, Now: time.Now, PID: os.Getpid, Ticker: realTicker, Admit: admission.run,
	}
}
func startChild(ctx context.Context, request ExecuteRequest, environment []string, interactive bool) (func() childResult, error) {
	child := process.CommandContextInteractive(ctx, interactive, request.Argv[0], request.Argv[1:]...)
	child.Dir = request.directory
	child.Stdin, child.Stdout, child.Stderr, child.Env = request.Stdin, request.Stdout, request.Stderr, environment
	if err := child.Start(); err != nil {
		return nil, err
	}
	return func() childResult {
		result := childResult{Err: child.Wait()}
		if result.Err != nil {
			result.ExitCode = 1
			var exited *exec.ExitError
			if errors.As(result.Err, &exited) {
				result.ChildFailed = true
				result.ExitCode = exited.ExitCode()
			}
		}
		if child.ProcessState != nil {
			result.UserCPU = child.ProcessState.UserTime()
			result.SystemCPU = child.ProcessState.SystemTime()
		}
		return result
	}, nil
}
func (executor Executor) Run(ctx context.Context, request ExecuteRequest) (ExecuteResult, error) {
	ops := defaultExecuteOperations()
	if executor.ops != nil {
		ops = *executor.ops
	}
	queueHeartbeat := executor.QueueHeartbeat
	if queueHeartbeat <= 0 {
		queueHeartbeat = Heartbeat
	}
	courtesyHeartbeat := executor.CourtesyHeartbeat
	if courtesyHeartbeat <= 0 {
		courtesyHeartbeat = Heartbeat
	}
	observe := request.Observe
	if observe == nil {
		observe = func(QueueEvent) {}
	}
	started := ops.Now()
	cwd, err := ops.Getwd()
	if err != nil {
		cwd = "."
	}
	recorder, telemetryErr := ops.Begin(cwd, request.Argv, started)
	if telemetryErr != nil {
		_, _ = fmt.Fprintf(request.Stderr, "warning: command telemetry start failed: %v\n", telemetryErr)
	}
	if runqueue.Classify(request.Argv) != runqueue.KindNone {
		floor, reason := ops.ResolveLoad(request.ConfigPath)
		if err := ops.CheckLoad(ops.System, floor, request.AllowSaturatedHost); err != nil {
			_ = ops.Finish(&recorder, 1, 0, 0, ops.Now())
			return ExecuteResult{}, fmt.Errorf("wb: %w", err)
		}
		if reason != "" {
			recorder.RecordLoadFloorSkipped(reason)
		}
		if request.AllowSaturatedHost {
			recorder.RecordLoadOverride(true)
		}
	}
	admission, err := ops.Admit(ctx, request.ProjectsRoot, request.Argv, runqueue.Participant{PID: ops.PID(), Summary: runqueue.Summary(request.Argv), Worktree: cwd}, observe, queueHeartbeat)
	admittedAt := ops.Now()
	recorder.RecordAdmission(admission.Units, admission.Waited)
	if err != nil {
		_ = ops.Finish(&recorder, 1, 0, 0, ops.Now())
		return ExecuteResult{}, fmt.Errorf("wait for WB CPU capacity: %w", err)
	}
	recorder.RecordQueueAdmittedAt(admittedAt)
	defer ops.Release(admission.Lease)
	request.directory = cwd
	interactive := ops.Interactive(request.Stderr)
	wait, startErr := ops.StartChild(ctx, request, ops.Environment(request.Argv, recorder.OperationID, admission.Units), interactive)
	result := childResult{Err: startErr}
	if startErr != nil {
		result.ExitCode = 1
	} else {
		done, joined := make(chan struct{}), make(chan struct{})
		if interactive {
			go func() {
				defer close(joined)
				ticks, stop := ops.Ticker(courtesyHeartbeat)
				defer stop()
				for {
					select {
					case <-done:
						return
					case <-ticks:
						observe(QueueEvent{Kind: CourtesyRunning, Summary: strings.Join(request.Argv, " ")})
					}
				}
			}()
		} else {
			close(joined)
		}
		result = wait()
		close(done)
		<-joined
	}
	if admission.Units > 0 {
		observe(QueueEvent{Kind: Done, Elapsed: ops.Now().Sub(started), ExitCode: result.ExitCode})
	}
	if err := ops.Finish(&recorder, result.ExitCode, result.UserCPU, result.SystemCPU, ops.Now()); err != nil {
		_, _ = fmt.Fprintf(request.Stderr, "warning: command telemetry finish failed: %v\n", err)
	}
	if result.ChildFailed {
		return ExecuteResult{ExitCode: result.ExitCode, ChildFailed: true}, nil
	}
	if result.Err != nil {
		return ExecuteResult{}, fmt.Errorf("execute %s: %w", request.Argv[0], result.Err)
	}
	return ExecuteResult{}, nil
}
