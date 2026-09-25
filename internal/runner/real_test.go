package runner_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
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
	if _, err := fmt.Fprint(os.Stdout, os.Getenv("WB_RUNNER_STDOUT")); err != nil { //nolint:forbidigo // helper-process fixture
		os.Exit(9)
	}
	if _, err := fmt.Fprint(os.Stderr, os.Getenv("WB_RUNNER_STDERR")); err != nil { //nolint:forbidigo // helper-process fixture
		os.Exit(9)
	}
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

// TestRunnerHelperProcessLeavesAGrandchildHoldingItsStdio is the child half
// of TestRealRunReturnsSuccessWhenOnlyAGrandchildKeepsThePipeOpen and its
// non-zero-exit sibling below: it starts a grandchild that inherits its own
// stdout/stderr -- the same fds runner.Real's Run captured via a pipe --
// and lets that grandchild sleep well past WaitDelay, then this process
// itself exits immediately with WB_RUNNER_EXIT. This reproduces an ssh
// ControlPersist master (started by `git ls-remote`/`fetch` over ssh) or a
// credential helper that outlives a successful child while still holding
// its inherited stdout/stderr open.
func TestRunnerHelperProcessLeavesAGrandchildHoldingItsStdio(t *testing.T) {
	t.Parallel()
	sleepMS := os.Getenv("WB_RUNNER_GRANDCHILD_SLEEP_MS")
	if sleepMS == "" {
		return
	}
	grandchild := exec.Command(os.Args[0], "-test.run=^TestRunnerHelperProcessSleepsInheritingStdio$") //nolint:gosec // helper-process fixture re-running this same test binary
	grandchild.Env = append(os.Environ(), "WB_RUNNER_SLEEP_MS="+sleepMS)
	grandchild.Stdout = os.Stdout
	grandchild.Stderr = os.Stderr
	if err := grandchild.Start(); err != nil {
		os.Exit(9)
	}
	// Deliberately not waited on: the grandchild outliving this process,
	// while still holding its inherited stdout/stderr open, is exactly the
	// scenario under test.
	code, _ := strconv.Atoi(os.Getenv("WB_RUNNER_EXIT"))
	os.Exit(code)
}

// TestRunnerHelperProcessSleepsInheritingStdio is the grandchild half of the
// same scenario: it holds whatever stdout/stderr fds it inherited open for
// WB_RUNNER_SLEEP_MS -- long enough to outlast WaitDelay -- then exits on
// its own. No parent test ever waits on it; it is bounded by its own sleep
// only, never used as synchronization.
func TestRunnerHelperProcessSleepsInheritingStdio(t *testing.T) {
	t.Parallel()
	ms, err := strconv.Atoi(os.Getenv("WB_RUNNER_SLEEP_MS"))
	if err != nil {
		return
	}
	time.Sleep(time.Duration(ms) * time.Millisecond)
}

// TestRealRunReturnsSuccessWhenOnlyAGrandchildKeepsThePipeOpen covers
// runner/real.go's withoutSpuriousWaitDelay: internal/process sets
// WaitDelay (250ms) on every non-interactive child (command_unix.go), so
// when the direct child exits 0 but a grandchild it started keeps stdout
// open past that delay, Cmd.Wait forces the pipe closed and returns the
// bare exec.ErrWaitDelay sentinel even though the child's own captured
// output is already complete. Before the fix, Run propagated that as a
// failure for a child that actually succeeded; the assertion on elapsed
// time proves the call returns around WaitDelay rather than blocking for
// the grandchild's full (much longer) sleep.
func TestRealRunReturnsSuccessWhenOnlyAGrandchildKeepsThePipeOpen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("WaitDelay is only set on darwin/linux (internal/process/command_unix.go); Windows has no forced pipe-close to race")
	}
	runnertest.AllowRealProcess(t)
	// The grandchild's sleep is deliberately much longer than WaitDelay
	// (250ms): the bound below only needs it to still be sleeping when Run
	// returns, with headroom for -race's slower subprocess startup, not to
	// double as a synchronization primitive.
	t.Setenv("WB_RUNNER_GRANDCHILD_SLEEP_MS", "4000")
	t.Setenv("WB_RUNNER_EXIT", "0")

	start := time.Now()
	result, err := runner.New().Run(context.Background(), t.TempDir(), os.Args[0], "-test.run=^TestRunnerHelperProcessLeavesAGrandchildHoldingItsStdio$")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Run: %v, want nil -- the child itself exited 0 and only a grandchild kept stdout open", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", result.ExitCode)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("Run took %s, want it to return well before the grandchild's 4s sleep (bounded by WaitDelay plus subprocess startup, not the full sleep)", elapsed)
	}
}

// TestRealRunKeepsTheErrorWhenTheChildExitsNonZeroWhileAGrandchildKeepsThePipeOpen
// is TestRealRunReturnsSuccessWhenOnlyAGrandchildKeepsThePipeOpen's
// non-zero-exit sibling: os/exec's Cmd.Wait only surfaces the bare
// exec.ErrWaitDelay sentinel when the process's own exit already succeeded
// (a non-zero exit becomes *exec.ExitError before the I/O goroutines are
// ever consulted), so this must keep reporting the child's real failure
// even though the same grandchild is still holding the pipe open.
func TestRealRunKeepsTheErrorWhenTheChildExitsNonZeroWhileAGrandchildKeepsThePipeOpen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("WaitDelay is only set on darwin/linux (internal/process/command_unix.go); Windows has no forced pipe-close to race")
	}
	runnertest.AllowRealProcess(t)
	t.Setenv("WB_RUNNER_GRANDCHILD_SLEEP_MS", "4000")
	t.Setenv("WB_RUNNER_EXIT", "7")

	start := time.Now()
	result, err := runner.New().Run(context.Background(), t.TempDir(), os.Args[0], "-test.run=^TestRunnerHelperProcessLeavesAGrandchildHoldingItsStdio$")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("want an error for a non-zero exit even though a grandchild kept the pipe open")
	}
	if result.ExitCode != 7 {
		t.Fatalf("ExitCode = %d, want 7", result.ExitCode)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("Run took %s, want it to return well before the grandchild's 4s sleep", elapsed)
	}
}

// TestRunnerHelperProcessEchoesStdin is the child half of
// TestRealRunWithInputWritesToTheChildsStdin: it copies its stdin to
// stdout, so the parent can assert the exact bytes RunWithInput wrote
// reached the child.
func TestRunnerHelperProcessEchoesStdin(t *testing.T) {
	t.Parallel()
	if os.Getenv("WB_RUNNER_ECHO_STDIN") != "1" {
		return
	}
	if _, err := io.Copy(os.Stdout, os.Stdin); err != nil { //nolint:forbidigo // helper-process fixture
		os.Exit(9)
	}
	os.Exit(0)
}

func TestRealRunWithInputWritesToTheChildsStdin(t *testing.T) {
	runnertest.AllowRealProcess(t)
	t.Setenv("WB_RUNNER_ECHO_STDIN", "1")

	result, err := runner.New().RunWithInput(context.Background(), t.TempDir(), []byte("hello-stdin"), os.Args[0], "-test.run=^TestRunnerHelperProcessEchoesStdin$")
	if err != nil {
		t.Fatalf("RunWithInput: %v", err)
	}
	if result.Stdout != "hello-stdin" {
		t.Fatalf("result.Stdout = %q, want the echoed stdin %q", result.Stdout, "hello-stdin")
	}
}

func TestRealRunWithInputReportsNonZeroExitStatus(t *testing.T) {
	runnertest.AllowRealProcess(t)
	t.Setenv("WB_RUNNER_HELPER", "1")
	t.Setenv("WB_RUNNER_EXIT", "7")

	result, err := runner.New().RunWithInput(context.Background(), t.TempDir(), nil, os.Args[0], helperArgs()...)
	if err == nil {
		t.Fatal("want an error for a non-zero exit")
	}
	if result.ExitCode != 7 {
		t.Fatalf("ExitCode = %d, want 7", result.ExitCode)
	}
}

func TestRealRunWithInputBlockedByTheRuntimeGuardReturnsErrRealProcessBlocked(t *testing.T) {
	if _, err := runner.New().RunWithInput(context.Background(), t.TempDir(), nil, os.Args[0], helperArgs()...); err != runner.ErrRealProcessBlocked {
		t.Fatalf("RunWithInput() err = %v, want runner.ErrRealProcessBlocked", err)
	}
}

// TestRunnerHelperProcessEchoesAnEnvironmentVariable is the child half of
// TestRealRunOptsAppliesACustomEnvironment: it prints WB_RUNNER_PROBE, which
// exists in its environment only if RunOpts' Env override actually replaced
// the inherited one (the parent process never sets it).
func TestRunnerHelperProcessEchoesAnEnvironmentVariable(t *testing.T) {
	t.Parallel()
	if os.Getenv("WB_RUNNER_ECHO_ENV") != "1" {
		return
	}
	if _, err := fmt.Fprint(os.Stdout, os.Getenv("WB_RUNNER_PROBE")); err != nil { //nolint:forbidigo // helper-process fixture
		os.Exit(9)
	}
	os.Exit(0)
}

func TestRealRunOptsAppliesACustomEnvironment(t *testing.T) {
	runnertest.AllowRealProcess(t)
	t.Setenv("WB_RUNNER_ECHO_ENV", "1")

	opts := runner.RunOptions{Env: append(os.Environ(), "WB_RUNNER_ECHO_ENV=1", "WB_RUNNER_PROBE=from-runopts")}
	result, err := runner.New().RunOpts(context.Background(), t.TempDir(), opts, os.Args[0], "-test.run=^TestRunnerHelperProcessEchoesAnEnvironmentVariable$")
	if err != nil {
		t.Fatalf("RunOpts: %v", err)
	}
	if result.Stdout != "from-runopts" {
		t.Fatalf("result.Stdout = %q, want the RunOpts-supplied WB_RUNNER_PROBE", result.Stdout)
	}
}

func TestRealRunOptsWithoutEnvInheritsTheParentEnvironmentLikeRun(t *testing.T) {
	runnertest.AllowRealProcess(t)
	t.Setenv("WB_RUNNER_HELPER", "1")
	t.Setenv("WB_RUNNER_STDOUT", "opts-inherited")
	t.Setenv("WB_RUNNER_EXIT", "0")

	result, err := runner.New().RunOpts(context.Background(), t.TempDir(), runner.RunOptions{}, os.Args[0], helperArgs()...)
	if err != nil {
		t.Fatalf("RunOpts: %v", err)
	}
	if result.Stdout != "opts-inherited" {
		t.Fatalf("result.Stdout = %q, want the parent's inherited WB_RUNNER_STDOUT", result.Stdout)
	}
}

func TestRealRunOptsWritesStdinAndHonorsWaitDelay(t *testing.T) {
	runnertest.AllowRealProcess(t)
	t.Setenv("WB_RUNNER_ECHO_STDIN", "1")

	opts := runner.RunOptions{Stdin: []byte("hello-opts-stdin"), WaitDelay: time.Second}
	result, err := runner.New().RunOpts(context.Background(), t.TempDir(), opts, os.Args[0], "-test.run=^TestRunnerHelperProcessEchoesStdin$")
	if err != nil {
		t.Fatalf("RunOpts: %v", err)
	}
	if result.Stdout != "hello-opts-stdin" {
		t.Fatalf("result.Stdout = %q, want the echoed stdin", result.Stdout)
	}
}

func TestRealRunOptsBlockedByTheRuntimeGuardReturnsErrRealProcessBlocked(t *testing.T) {
	if _, err := runner.New().RunOpts(context.Background(), t.TempDir(), runner.RunOptions{}, os.Args[0], helperArgs()...); err != runner.ErrRealProcessBlocked {
		t.Fatalf("RunOpts() err = %v, want runner.ErrRealProcessBlocked", err)
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
