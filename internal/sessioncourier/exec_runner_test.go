package sessioncourier

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

// TestExecCommandRunnerRunWritesInputAndCapturedOutput covers
// execCommandRunner.Run's happy path against a scripted runner.Runner,
// asserting the input it forwards to RunWithInput and that both stdout and
// stderr reach the caller's writers.
func TestExecCommandRunnerRunWritesInputAndCapturedOutput(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.Expect(func(c runnertest.Call) bool {
		return c.Op == "RunWithInput" && c.Name == "ssh" && string(c.Input) == "request"
	}, runner.Result{Stdout: "out", Stderr: "warn"}, nil)

	execRunner := execCommandRunner{Runner: fake}
	var stdout, stderr boundedBuffer
	stdout.limit, stderr.limit = 1024, 1024
	if err := execRunner.Run(context.Background(), "ssh", []string{"host"}, []byte("request"), &stdout, &stderr); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(stdout.Bytes()) != "out" || string(stderr.Bytes()) != "warn" {
		t.Fatalf("stdout/stderr = %q/%q", stdout.Bytes(), stderr.Bytes())
	}
}

// TestExecCommandRunnerRunDefaultsToProductionRunner proves the zero-value
// execCommandRunner{} every production call site constructs resolves a nil
// Runner to the production runner.Runner. Under `go test`, runner.Real
// refuses to start a real process (task-24's guard), so this observes the
// guard error instead of shelling out.
func TestExecCommandRunnerRunDefaultsToProductionRunner(t *testing.T) {
	t.Parallel()
	var stdout, stderr boundedBuffer
	stdout.limit, stderr.limit = 1024, 1024
	err := execCommandRunner{}.Run(context.Background(), "ssh", []string{"host"}, nil, &stdout, &stderr)
	if !errors.Is(err, runner.ErrRealProcessBlocked) {
		t.Fatalf("execCommandRunner{} with no injected runner = %v, want it to reach the production runner.Runner", err)
	}
}

// TestExecCommandRunnerRunSurfacesAWriteFailure proves a caller writer's own
// failure is not swallowed just because the child exited zero, covering
// both the stdout and stderr write-error branches independently.
func TestExecCommandRunnerRunSurfacesAWriteFailure(t *testing.T) {
	t.Parallel()
	t.Run("stdout", func(t *testing.T) {
		t.Parallel()
		fake := runnertest.New(t)
		fake.ExpectArgv([]string{"ssh", "host"}, runner.Result{Stdout: "out"}, nil)
		var stderr boundedBuffer
		stderr.limit = 1024
		err := execCommandRunner{Runner: fake}.Run(context.Background(), "ssh", []string{"host"}, nil, failingExecWriter{err: errors.New("disk full")}, &stderr)
		if err == nil || !strings.Contains(err.Error(), "disk full") {
			t.Fatalf("Run() err = %v, want the stdout writer's own failure", err)
		}
	})
	t.Run("stderr", func(t *testing.T) {
		t.Parallel()
		fake := runnertest.New(t)
		fake.ExpectArgv([]string{"ssh", "host"}, runner.Result{Stderr: "warn"}, nil)
		var stdout boundedBuffer
		stdout.limit = 1024
		err := execCommandRunner{Runner: fake}.Run(context.Background(), "ssh", []string{"host"}, nil, &stdout, failingExecWriter{err: errors.New("pipe closed")})
		if err == nil || !strings.Contains(err.Error(), "pipe closed") {
			t.Fatalf("Run() err = %v, want the stderr writer's own failure", err)
		}
	})
}

// failingExecWriter always reports err, so a test can exercise the
// write-error branch no real caller's boundedBuffer ever reaches.
type failingExecWriter struct{ err error }

func (w failingExecWriter) Write([]byte) (int, error) { return 0, w.err }
