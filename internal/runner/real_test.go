package runner_test

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

// TestRunnerHelperProcess is the child half of every real_test.go case
// below: it is inert unless WB_RUNNER_HELPER is set, so an ordinary suite
// run treats it as a passing no-op, exactly like internal/process's own
// TestProcessHelper. WB_RUNNER_SLEEP keeps it alive long enough for a
// parent test to signal it before it would otherwise exit on its own.
func TestRunnerHelperProcess(t *testing.T) {
	t.Parallel()
	if os.Getenv("WB_RUNNER_HELPER") != "1" {
		return
	}
	fmt.Fprint(os.Stdout, os.Getenv("WB_RUNNER_STDOUT")) //nolint:forbidigo,errcheck // helper-process fixture
	fmt.Fprint(os.Stderr, os.Getenv("WB_RUNNER_STDERR")) //nolint:forbidigo,errcheck // helper-process fixture
	if os.Getenv("WB_RUNNER_SLEEP") == "1" {
		time.Sleep(10 * time.Second)
	}
	code, _ := strconv.Atoi(os.Getenv("WB_RUNNER_EXIT"))
	os.Exit(code)
}

// helperArgs is the argv real_test.go's cases pass to re-run this test
// binary as TestRunnerHelperProcess.
func helperArgs() []string {
	return []string{"-test.run=^TestRunnerHelperProcess$"}
}

func TestRealRunCapturesStdoutStderrAndExitStatus(t *testing.T) {
	runnertest.AllowRealProcess(t)
	t.Setenv("WB_RUNNER_HELPER", "1")
	t.Setenv("WB_RUNNER_STDOUT", "hello-stdout")
	t.Setenv("WB_RUNNER_STDERR", "hello-stderr")
	t.Setenv("WB_RUNNER_EXIT", "0")

	result, err := runner.New().Run(context.Background(), t.TempDir(), os.Args[0], helperArgs()...)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Stdout != "hello-stdout" || result.Stderr != "hello-stderr" || result.ExitCode != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestRealRunReportsNonZeroExitStatus(t *testing.T) {
	runnertest.AllowRealProcess(t)
	t.Setenv("WB_RUNNER_HELPER", "1")
	t.Setenv("WB_RUNNER_EXIT", "7")

	result, err := runner.New().Run(context.Background(), t.TempDir(), os.Args[0], helperArgs()...)
	if err == nil {
		t.Fatal("want an error for a non-zero exit")
	}
	if result.ExitCode != 7 {
		t.Fatalf("ExitCode = %d, want 7", result.ExitCode)
	}
}

func TestRealRunReportsAStartFailureWithZeroExitCode(t *testing.T) {
	runnertest.AllowRealProcess(t)
	result, err := runner.New().Run(context.Background(), t.TempDir(), "wb-runner-does-not-exist-anywhere")
	if err == nil {
		t.Fatal("want an error for a program that does not exist")
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 for a start failure (never reached a process exit)", result.ExitCode)
	}
}

func TestRealStartAndHandleWaitReportOutputAndExitStatus(t *testing.T) {
	runnertest.AllowRealProcess(t)
	t.Setenv("WB_RUNNER_HELPER", "1")
	t.Setenv("WB_RUNNER_STDOUT", "started-then-waited")
	t.Setenv("WB_RUNNER_EXIT", "0")

	handle, err := runner.New().Start(context.Background(), t.TempDir(), os.Args[0], helperArgs()...)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if handle.Pid() == 0 {
		t.Fatal("Pid() = 0, want the started process's real pid")
	}
	result, waitErr := handle.Wait()
	if waitErr != nil {
		t.Fatalf("Wait: %v", waitErr)
	}
	if result.Stdout != "started-then-waited" {
		t.Fatalf("result.Stdout = %q", result.Stdout)
	}
}

func TestRealStartReturnsAnErrorForAProgramThatDoesNotExist(t *testing.T) {
	runnertest.AllowRealProcess(t)
	if _, err := runner.New().Start(context.Background(), t.TempDir(), "wb-runner-does-not-exist-anywhere"); err == nil {
		t.Fatal("want an error for a program that does not exist")
	}
}

func TestRealHandleSignalDeliversToTheProcess(t *testing.T) {
	runnertest.AllowRealProcess(t)
	t.Setenv("WB_RUNNER_HELPER", "1")
	t.Setenv("WB_RUNNER_SLEEP", "1")

	handle, err := runner.New().Start(context.Background(), t.TempDir(), os.Args[0], helperArgs()...)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := handle.Signal(os.Kill); err != nil {
		t.Fatalf("Signal: %v", err)
	}
	if _, err := handle.Wait(); err == nil {
		t.Fatal("want a non-nil error after the process was killed")
	}
}

func TestRealDetachStartsAProcessAndReportsItsPid(t *testing.T) {
	runnertest.AllowRealProcess(t)
	t.Setenv("WB_RUNNER_HELPER", "1")
	t.Setenv("WB_RUNNER_EXIT", "0")

	pid, err := runner.New().Detach(t.TempDir(), os.Args[0], helperArgs()...)
	if err != nil {
		t.Fatalf("Detach: %v", err)
	}
	if pid == 0 {
		t.Fatal("Detach returned pid 0")
	}
}

func TestRealDetachReturnsAnErrorForAProgramThatDoesNotExist(t *testing.T) {
	runnertest.AllowRealProcess(t)
	if _, err := runner.New().Detach(t.TempDir(), "wb-runner-does-not-exist-anywhere"); err == nil {
		t.Fatal("want an error for a program that does not exist")
	}
}

func TestRealInteractiveRunsToCompletion(t *testing.T) {
	runnertest.AllowRealProcess(t)
	t.Setenv("WB_RUNNER_HELPER", "1")
	t.Setenv("WB_RUNNER_EXIT", "0")

	if err := runner.New().Interactive(context.Background(), t.TempDir(), os.Args[0], helperArgs()...); err != nil {
		t.Fatalf("Interactive: %v", err)
	}
}

func TestRealInteractiveReportsNonZeroExitStatus(t *testing.T) {
	runnertest.AllowRealProcess(t)
	t.Setenv("WB_RUNNER_HELPER", "1")
	t.Setenv("WB_RUNNER_EXIT", "3")

	if err := runner.New().Interactive(context.Background(), t.TempDir(), os.Args[0], helperArgs()...); err == nil {
		t.Fatal("want an error for a non-zero exit")
	}
}

func TestRealRunBlockedByTheRuntimeGuardReturnsErrRealProcessBlocked(t *testing.T) {
	if _, err := runner.New().Run(context.Background(), t.TempDir(), os.Args[0], helperArgs()...); err != runner.ErrRealProcessBlocked {
		t.Fatalf("Run() err = %v, want runner.ErrRealProcessBlocked", err)
	}
}

func TestRealStartBlockedByTheRuntimeGuardReturnsErrRealProcessBlocked(t *testing.T) {
	if _, err := runner.New().Start(context.Background(), t.TempDir(), os.Args[0], helperArgs()...); err != runner.ErrRealProcessBlocked {
		t.Fatalf("Start() err = %v, want runner.ErrRealProcessBlocked", err)
	}
}

func TestRealDetachBlockedByTheRuntimeGuardReturnsErrRealProcessBlocked(t *testing.T) {
	if _, err := runner.New().Detach(t.TempDir(), os.Args[0], helperArgs()...); err != runner.ErrRealProcessBlocked {
		t.Fatalf("Detach() err = %v, want runner.ErrRealProcessBlocked", err)
	}
}

func TestRealInteractiveBlockedByTheRuntimeGuardReturnsErrRealProcessBlocked(t *testing.T) {
	if err := runner.New().Interactive(context.Background(), t.TempDir(), os.Args[0], helperArgs()...); err != runner.ErrRealProcessBlocked {
		t.Fatalf("Interactive() err = %v, want runner.ErrRealProcessBlocked", err)
	}
}

func TestRealHandleSignalAfterWaitReportsAnErrorRatherThanPanicking(t *testing.T) {
	runnertest.AllowRealProcess(t)
	t.Setenv("WB_RUNNER_HELPER", "1")
	t.Setenv("WB_RUNNER_EXIT", "0")

	handle, err := runner.New().Start(context.Background(), t.TempDir(), os.Args[0], helperArgs()...)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := handle.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if err := handle.Signal(os.Interrupt); err == nil {
		t.Fatal("want an error signalling an already-waited-on process, not a panic or a silent success")
	}
}
