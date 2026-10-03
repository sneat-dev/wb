package runexec

import (
	"bytes"
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/hostload"
	"github.com/sneat-dev/wb/internal/runlog"
	"github.com/sneat-dev/wb/internal/runqueue"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type testContextKey struct{}

func fakeExecuteOperations(t *testing.T) executeOperations {
	t.Helper()
	now := time.Unix(100, 0)
	return executeOperations{Getwd: func() (string, error) { return "/repo", nil }, Begin: func(string, []string, time.Time) (runlog.Recorder, error) {
		return runlog.Recorder{OperationID: "actual"}, nil
	}, Finish: func(*runlog.Recorder, int, time.Duration, time.Duration, time.Time) error { return nil }, ResolveLoad: func(string) (float64, string) { return 4, "" }, CheckLoad: func(hostload.Reader, float64, bool) error { return nil }, Interactive: func(_ io.Writer) bool { return false }, StartChild: func(context.Context, ExecuteRequest, []string, bool) (func() childResult, error) {
		return func() childResult { return childResult{} }, nil
	}, Environment: func([]string, string, int) []string { return []string{"WB_OPERATION_ID=actual"} }, Now: func() time.Time { now = now.Add(time.Second); return now }, PID: func() int { return 7 }, Ticker: realTicker, Admit: func(context.Context, string, []string, runqueue.Participant, func(QueueEvent), time.Duration) (runqueue.Admission, error) {
		return runqueue.Admission{Units: 2, Waited: time.Second}, nil
	}, Release: func(*runqueue.Lease) {}}
}
func TestExecuteFailureResultsTelemetryAndRelease(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"success", "cwd-fallback", "begin-warning", "finish-warning", "host-refusal", "admission-error", "start-error", "wait-error", "child-exit", "ungoverned", "nil-observer"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			ops := fakeExecuteOperations(t)
			sentinel := errors.New("failed effect")
			var stderr, stdout bytes.Buffer
			input := strings.NewReader("input")
			ctx := context.WithValue(context.Background(), testContextKey{}, true)
			args := []string{"go", "test", "./..."}
			if path == "ungoverned" {
				args = []string{"git", "status"}
			}
			request := ExecuteRequest{Argv: args, ProjectsRoot: "/root", ConfigPath: "config", AllowSaturatedHost: true, Stdin: input, Stdout: &stdout, Stderr: &stderr}
			var events []QueueEvent
			request.Observe = func(e QueueEvent) { events = append(events, e) }
			if path == "nil-observer" {
				request.Observe = nil
			}
			if path == "cwd-fallback" {
				ops.Getwd = func() (string, error) { return "", sentinel }
			}
			ops.Begin = func(cwd string, argv []string, at time.Time) (runlog.Recorder, error) {
				want := "/repo"
				if path == "cwd-fallback" {
					want = "."
				}
				if cwd != want || len(argv) != len(args) || at.IsZero() {
					t.Fatal("begin arguments")
				}
				if path == "begin-warning" {
					return runlog.Recorder{OperationID: "actual"}, sentinel
				}
				return runlog.Recorder{OperationID: "actual"}, nil
			}
			loadCalls, admitCalls, starts, finishes, releases := 0, 0, 0, 0, 0
			ops.ResolveLoad = func(config string) (float64, string) {
				loadCalls++
				if config != "config" {
					t.Fatal(config)
				}
				return 4, "floor skipped"
			}
			ops.CheckLoad = func(_ hostload.Reader, floor float64, allow bool) error {
				if floor != 4 || !allow {
					t.Fatal("load arguments")
				}
				if path == "host-refusal" {
					return sentinel
				}
				return nil
			}
			ops.Admit = func(got context.Context, root string, argv []string, self runqueue.Participant, observe func(QueueEvent), heartbeat time.Duration) (runqueue.Admission, error) {
				admitCalls++
				if got != ctx || root != "/root" || self.PID != 7 || heartbeat != 5*time.Millisecond {
					t.Fatal("admission arguments")
				}
				if path == "admission-error" {
					return runqueue.Admission{Waited: time.Second}, sentinel
				}
				units := 2
				if path == "ungoverned" {
					units = 0
				}
				observe(QueueEvent{Kind: ImmediatelyAdmitted})
				return runqueue.Admission{Units: units, Waited: time.Second}, nil
			}
			ops.Environment = func(argv []string, id string, units int) []string {
				if id != "actual" || units != map[bool]int{true: 0, false: 2}[path == "ungoverned"] {
					t.Fatal("environment arguments")
				}
				return []string{"WB_OPERATION_ID=actual"}
			}
			ops.StartChild = func(got context.Context, r ExecuteRequest, env []string, interactive bool) (func() childResult, error) {
				starts++
				if got != ctx || r.Stdin != input || r.Stdout != &stdout || r.Stderr != &stderr || env[0] != "WB_OPERATION_ID=actual" || interactive {
					t.Fatal("child arguments")
				}
				if path == "start-error" {
					return nil, sentinel
				}
				return func() childResult {
					if path == "wait-error" {
						return childResult{Err: sentinel, ExitCode: 1}
					}
					if path == "child-exit" {
						return childResult{Err: sentinel, ChildFailed: true, ExitCode: 7, UserCPU: 5 * time.Millisecond, SystemCPU: 2 * time.Millisecond}
					}
					return childResult{}
				}, nil
			}
			ops.Finish = func(_ *runlog.Recorder, code int, user, system time.Duration, _ time.Time) error {
				finishes++
				want := 0
				if path == "host-refusal" || path == "admission-error" || path == "start-error" || path == "wait-error" {
					want = 1
				}
				if path == "child-exit" {
					want = 7
					if user != 5*time.Millisecond || system != 2*time.Millisecond {
						t.Fatal("CPU times lost")
					}
				}
				if code != want {
					t.Fatalf("finish code%d want%d", code, want)
				}
				if path == "finish-warning" {
					return sentinel
				}
				return nil
			}
			ops.Release = func(*runqueue.Lease) {
				releases++
				if finishes != 1 {
					t.Fatal("release before finish")
				}
			}
			result, err := (Executor{QueueHeartbeat: 5 * time.Millisecond, ops: &ops}).Run(ctx, request)
			if finishes != 1 {
				t.Fatalf("finishes%d", finishes)
			}
			switch path {
			case "host-refusal", "admission-error", "start-error", "wait-error":
				if !errors.Is(err, sentinel) {
					t.Fatal(err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
			}
			if path == "child-exit" && (!result.ChildFailed || result.ExitCode != 7) {
				t.Fatal(result)
			}
			if path == "host-refusal" {
				if admitCalls != 0 || starts != 0 || releases != 0 {
					t.Fatal("work after refusal")
				}
			} else if path == "admission-error" {
				if starts != 0 || releases != 0 {
					t.Fatal("work after admission failure")
				}
			} else if releases != 1 || starts != 1 {
				t.Fatalf("starts%d releases%d", starts, releases)
			}
			if path == "ungoverned" {
				if loadCalls != 0 {
					t.Fatal("load check for ungoverned")
				}
				for _, event := range events {
					if event.Kind == Done {
						t.Fatal("done receipt for zero units")
					}
				}
			}
			if strings.HasSuffix(path, "warning") && !strings.Contains(stderr.String(), "warning: command telemetry") {
				t.Fatal(stderr.String())
			}
		})
	}
}
func TestExecuteJoinsCourtesyWriterBeforeFinishAndRelease(t *testing.T) {
	t.Parallel()
	ops := fakeExecuteOperations(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	observed, stopped := make(chan struct{}), make(chan struct{})
	ticks := make(chan time.Time, 1)
	ticks <- time.Now()
	var released atomic.Bool
	ops.Interactive = func(io.Writer) bool { return true }
	ops.Ticker = func(every time.Duration) (<-chan time.Time, func()) {
		if every != time.Millisecond {
			t.Fatal(every)
		}
		return ticks, func() { close(stopped) }
	}
	ops.StartChild = func(context.Context, ExecuteRequest, []string, bool) (func() childResult, error) {
		return func() childResult {
			select {
			case <-observed:
				return childResult{}
			case <-ctx.Done():
				return childResult{Err: ctx.Err()}
			}
		}, nil
	}
	ops.Finish = func(*runlog.Recorder, int, time.Duration, time.Duration, time.Time) error {
		select {
		case <-stopped:
		default:
			t.Error("courtesy goroutine not joined before finish")
		}
		return nil
	}
	ops.Release = func(*runqueue.Lease) {
		select {
		case <-stopped:
		default:
			t.Error("release before courtesy join")
		}
		released.Store(true)
	}
	var out bytes.Buffer
	result, err := (Executor{CourtesyHeartbeat: time.Millisecond, ops: &ops}).Run(ctx, ExecuteRequest{Argv: []string{"child", "arg"}, Stderr: &out, Observe: func(event QueueEvent) {
		if event.Kind == CourtesyRunning {
			if event.Summary != "child arg" {
				t.Error(event.Summary)
			}
			close(observed)
		}
	}})
	if err != nil || result.ChildFailed || !released.Load() {
		t.Fatalf("result=%+v err=%v released%v", result, err, released.Load())
	}
}
