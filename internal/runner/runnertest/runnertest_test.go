package runnertest

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/testsweep"
)

func TestAllowRealProcessSetsTheRunnerAllowEnvironmentVariable(t *testing.T) {
	AllowRealProcess(t)
	if os.Getenv("WB_RUNNER_ALLOW_REAL_PROCESS") != "1" {
		t.Fatal("AllowRealProcess did not set WB_RUNNER_ALLOW_REAL_PROCESS=1")
	}
}

func TestFakeRunReturnsTheScriptedResult(t *testing.T) {
	t.Parallel()
	fake := New(t)
	fake.ExpectArgv([]string{"git", "status"}, runner.Result{Stdout: "clean"}, nil)

	result, err := fake.Run(context.Background(), "/repo", "git", "status")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Stdout != "clean" {
		t.Fatalf("result = %+v", result)
	}
}

func TestFakeRunReturnsTheScriptedError(t *testing.T) {
	t.Parallel()
	fake := New(t)
	wantErr := os.ErrPermission
	fake.ExpectArgv([]string{"git", "push"}, runner.Result{}, wantErr)

	_, err := fake.Run(context.Background(), "/repo", "git", "push")
	if err != wantErr {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

func TestFakeRunWithInputRecordsInputAndReturnsTheScriptedResult(t *testing.T) {
	t.Parallel()
	fake := New(t)
	fake.ExpectArgv([]string{"ssh", "host"}, runner.Result{Stdout: "remote-out"}, nil)

	result, err := fake.RunWithInput(context.Background(), "/repo", []byte("request-body"), "ssh", "host")
	if err != nil {
		t.Fatalf("RunWithInput: %v", err)
	}
	if result.Stdout != "remote-out" {
		t.Fatalf("result = %+v", result)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Op != "RunWithInput" || string(calls[0].Input) != "request-body" {
		t.Fatalf("Calls() = %+v, want one RunWithInput call carrying the input", calls)
	}
}

func TestFakeRunOptsRecordsOptsAndReturnsTheScriptedResult(t *testing.T) {
	t.Parallel()
	fake := New(t)
	fake.ExpectArgv([]string{"go", "env", "GOFLAGS"}, runner.Result{Stdout: "-race"}, nil)

	opts := runner.RunOptions{Env: []string{"GOWORK=off"}, Stdin: []byte("in"), WaitDelay: time.Second}
	result, err := fake.RunOpts(context.Background(), "/repo", opts, "go", "env", "GOFLAGS")
	if err != nil {
		t.Fatalf("RunOpts: %v", err)
	}
	if result.Stdout != "-race" {
		t.Fatalf("result = %+v", result)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Op != "RunOpts" {
		t.Fatalf("Calls() = %+v, want one RunOpts call", calls)
	}
	if string(calls[0].Input) != "in" || calls[0].Opts.WaitDelay != time.Second || len(calls[0].Opts.Env) != 1 {
		t.Fatalf("Calls()[0] = %+v, want it to carry opts", calls[0])
	}
}

func TestFakeExpectMatchesByPredicateNotJustExactArgv(t *testing.T) {
	t.Parallel()
	fake := New(t)
	fake.Expect(func(c Call) bool {
		return c.Name == "git" && len(c.Args) > 0 && c.Args[0] == "fetch"
	}, runner.Result{Stdout: "fetched"}, nil)

	result, err := fake.Run(context.Background(), "/repo", "git", "fetch", "origin")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Stdout != "fetched" {
		t.Fatalf("result = %+v", result)
	}
}

func TestFakeConsumesEachScriptOnlyOnce(t *testing.T) {
	t.Parallel()
	fake := New(t)
	fake.ExpectArgv([]string{"git", "status"}, runner.Result{Stdout: "first"}, nil)
	fake.ExpectArgv([]string{"git", "status"}, runner.Result{Stdout: "second"}, nil)

	first, _ := fake.Run(context.Background(), "/repo", "git", "status")
	second, _ := fake.Run(context.Background(), "/repo", "git", "status")
	if first.Stdout != "first" || second.Stdout != "second" {
		t.Fatalf("first=%+v second=%+v, want scripts consumed in order", first, second)
	}
}

func TestFakeCallsRecordsEveryCallInOrder(t *testing.T) {
	t.Parallel()
	fake := New(t)
	fake.ExpectArgv([]string{"git", "status"}, runner.Result{}, nil)
	fake.ExpectArgv([]string{"git", "fetch"}, runner.Result{}, nil)

	_, _ = fake.Run(context.Background(), "/repo", "git", "status")
	_, _ = fake.Run(context.Background(), "/repo", "git", "fetch")

	calls := fake.Calls()
	if len(calls) != 2 || calls[0].Name != "git" || calls[0].Args[0] != "status" || calls[1].Args[0] != "fetch" {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestFakeAnswerFailsTheTestOnAnUnmatchedCall(t *testing.T) {
	t.Parallel()
	spy := &fatalSpy{}
	fake := New(spy)

	_, _ = fake.Run(context.Background(), "/repo", "git", "status")

	if !spy.fatalCalled {
		t.Fatal("want Fatalf called for an unscripted call")
	}
}

func TestFakeStartReturnsAHandleWhoseWaitReplaysTheScriptedResult(t *testing.T) {
	t.Parallel()
	fake := New(t)
	fake.ExpectArgv([]string{"tmux", "new-session"}, runner.Result{Stdout: "started", ExitCode: 0}, nil)

	handle, err := fake.Start(context.Background(), "/repo", "tmux", "new-session")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if handle.Pid() != 0 {
		t.Fatalf("Pid() = %d, want 0 for a fake handle", handle.Pid())
	}
	if err := handle.Signal(os.Interrupt); err != nil {
		t.Fatalf("Signal: %v", err)
	}
	result, waitErr := handle.Wait()
	if waitErr != nil {
		t.Fatalf("Wait: %v", waitErr)
	}
	if result.Stdout != "started" {
		t.Fatalf("result = %+v", result)
	}
}

func TestFakeStartReturnsAnErrorWithoutAHandle(t *testing.T) {
	t.Parallel()
	fake := New(t)
	wantErr := os.ErrNotExist
	fake.ExpectArgv([]string{"missing"}, runner.Result{}, wantErr)

	handle, err := fake.Start(context.Background(), "/repo", "missing")
	if err != wantErr || handle != nil {
		t.Fatalf("Start() = (%v, %v), want (nil, %v)", handle, err, wantErr)
	}
}

func TestFakeStartHandleWaitReportsANonZeroExitAsAnError(t *testing.T) {
	t.Parallel()
	fake := New(t)
	fake.ExpectArgv([]string{"false"}, runner.Result{ExitCode: 1}, nil)

	handle, err := fake.Start(context.Background(), "/repo", "false")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, waitErr := handle.Wait(); waitErr == nil {
		t.Fatal("want Wait to report an error for a non-zero exit code")
	}
}

func TestFakeDetachReturnsTheScriptedExitCodeAndError(t *testing.T) {
	t.Parallel()
	fake := New(t)
	fake.ExpectArgv([]string{"wb-daemon"}, runner.Result{ExitCode: 0}, nil)

	pid, err := fake.Detach("/repo", "wb-daemon")
	if err != nil || pid != 0 {
		t.Fatalf("Detach() = (%d, %v)", pid, err)
	}
}

func TestFakeInteractiveReturnsTheScriptedError(t *testing.T) {
	t.Parallel()
	fake := New(t)
	wantErr := os.ErrClosed
	fake.ExpectArgv([]string{"vim"}, runner.Result{}, wantErr)

	err := fake.Interactive(context.Background(), "/repo", "vim")
	if err != wantErr {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

func TestFakeStreamRecordsOptsAndReturnsTheScriptedResult(t *testing.T) {
	t.Parallel()
	fake := New(t)
	fake.ExpectArgv([]string{"/bin/sh", "hook.sh"}, runner.Result{ExitCode: 3}, os.ErrPermission)

	var stdin, stdout, stderr bytes.Buffer
	opts := runner.StreamOptions{Env: []string{"WB_HOOK=pre-commit"}, Stdin: &stdin, Stdout: &stdout, Stderr: &stderr}
	result, err := fake.Stream(context.Background(), "/repo", opts, "/bin/sh", "hook.sh")
	if err != os.ErrPermission {
		t.Fatalf("err = %v, want %v", err, os.ErrPermission)
	}
	if result.ExitCode != 3 {
		t.Fatalf("result = %+v, want ExitCode 3", result)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Op != "Stream" {
		t.Fatalf("Calls() = %+v, want one Stream call", calls)
	}
	if len(calls[0].StreamOpts.Env) != 1 || calls[0].StreamOpts.Env[0] != "WB_HOOK=pre-commit" {
		t.Fatalf("Calls()[0].StreamOpts = %+v, want it to carry Env", calls[0].StreamOpts)
	}
	if calls[0].StreamOpts.Stdin != &stdin || calls[0].StreamOpts.Stdout != &stdout || calls[0].StreamOpts.Stderr != &stderr {
		t.Fatalf("Calls()[0].StreamOpts did not carry the caller's stdio streams")
	}
}

func TestFakeCallCountReportsCallsAnsweredSoFar(t *testing.T) {
	t.Parallel()
	fake := New(t)
	fake.ExpectArgv([]string{"git", "status"}, runner.Result{}, nil)
	fake.ExpectArgv([]string{"git", "fetch"}, runner.Result{}, nil)

	if got := fake.CallCount(); got != 0 {
		t.Fatalf("CallCount() = %d before any call, want 0", got)
	}
	_, _ = fake.Run(context.Background(), "/repo", "git", "status")
	if got := fake.CallCount(); got != 1 {
		t.Fatalf("CallCount() = %d after one call, want 1", got)
	}
	_, _ = fake.Run(context.Background(), "/repo", "git", "fetch")
	if got := fake.CallCount(); got != 2 {
		t.Fatalf("CallCount() = %d after two calls, want 2", got)
	}
}

func TestFakeFailCallFailsExactlyOneCallAndPassesTheRest(t *testing.T) {
	t.Parallel()
	fake := New(t)
	errBoom := errors.New("boom")
	fake.ExpectArgv([]string{"git", "status"}, runner.Result{Stdout: "first"}, nil)
	fake.ExpectArgv([]string{"git", "fetch"}, runner.Result{Stdout: "second"}, nil)
	fake.ExpectArgv([]string{"git", "push"}, runner.Result{Stdout: "third"}, nil)
	fake.FailCall(2, errBoom)

	first, err := fake.Run(context.Background(), "/repo", "git", "status")
	if err != nil || first.Stdout != "first" {
		t.Fatalf("call 1 = (%+v, %v), want (\"first\", nil)", first, err)
	}
	second, err := fake.Run(context.Background(), "/repo", "git", "fetch")
	if !errors.Is(err, errBoom) {
		t.Fatalf("call 2 err = %v, want errBoom", err)
	}
	if second != (runner.Result{}) {
		t.Fatalf("call 2 result = %+v, want the zero Result when FailCall overrides it", second)
	}
	third, err := fake.Run(context.Background(), "/repo", "git", "push")
	if err != nil || third.Stdout != "third" {
		t.Fatalf("call 3 = (%+v, %v), want (\"third\", nil): FailCall must pass every call but the one it targets", third, err)
	}
}

func TestFakeFailCallOfZeroDisablesTheOverride(t *testing.T) {
	t.Parallel()
	fake := New(t)
	fake.ExpectArgv([]string{"git", "status"}, runner.Result{Stdout: "ok"}, nil)
	fake.FailCall(1, errors.New("boom"))
	fake.FailCall(0, nil)

	result, err := fake.Run(context.Background(), "/repo", "git", "status")
	if err != nil || result.Stdout != "ok" {
		t.Fatalf("Run() = (%+v, %v), want (\"ok\", nil): FailCall(0, ...) should disable the override", result, err)
	}
}

func TestFakeFailCallReplacesItsPreviousTarget(t *testing.T) {
	t.Parallel()
	fake := New(t)
	errBoom := errors.New("boom")
	fake.ExpectArgv([]string{"git", "status"}, runner.Result{Stdout: "ok"}, nil)
	fake.ExpectArgv([]string{"git", "fetch"}, runner.Result{Stdout: "ok"}, nil)
	fake.FailCall(1, errors.New("stale target"))
	fake.FailCall(2, errBoom)

	_, err := fake.Run(context.Background(), "/repo", "git", "status")
	if err != nil {
		t.Fatalf("call 1 err = %v, want nil: the later FailCall(2, ...) must replace the FailCall(1, ...) target", err)
	}
	_, err = fake.Run(context.Background(), "/repo", "git", "fetch")
	if !errors.Is(err, errBoom) {
		t.Fatalf("call 2 err = %v, want errBoom", err)
	}
}

// TestFakeWorksAsATestsweepFailer is task-8's proof that Fake plugs into
// internal/testsweep.Sweep: a happy-path body making three real Runner
// calls gets swept end to end, one call number at a time.
func TestFakeWorksAsATestsweepFailer(t *testing.T) {
	t.Parallel()
	errBoom := errors.New("boom")

	body := func(fake *Fake) error {
		fake.ExpectArgv([]string{"git", "fetch"}, runner.Result{}, nil)
		fake.ExpectArgv([]string{"git", "merge"}, runner.Result{}, nil)
		fake.ExpectArgv([]string{"git", "push"}, runner.Result{}, nil)

		if _, err := fake.Run(context.Background(), "/repo", "git", "fetch"); err != nil {
			return err
		}
		if _, err := fake.Run(context.Background(), "/repo", "git", "merge"); err != nil {
			return err
		}
		_, err := fake.Run(context.Background(), "/repo", "git", "push")
		return err
	}

	sawCallNums := map[int]bool{}
	testsweep.Sweep(t, func() *Fake { return New(t) }, errBoom, body,
		func(t testing.TB, callNum, total int, err error) {
			if total != 3 {
				t.Fatalf("total = %d, want 3", total)
			}
			if !errors.Is(err, errBoom) {
				t.Fatalf("call %d err = %v, want it to wrap errBoom", callNum, err)
			}
			sawCallNums[callNum] = true
		})

	for _, want := range []int{1, 2, 3} {
		if !sawCallNums[want] {
			t.Fatalf("Sweep never checked call %d", want)
		}
	}
}

func TestCallArgvIsNameFollowedByArgs(t *testing.T) {
	t.Parallel()
	call := Call{Name: "git", Args: []string{"status", "--porcelain"}}
	got := call.Argv()
	want := []string{"git", "status", "--porcelain"}
	if len(got) != len(want) {
		t.Fatalf("Argv() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Argv() = %v, want %v", got, want)
		}
	}
}

// fatalSpy is a minimal testing.TB double: it records whether Fatalf was
// called instead of aborting the real test, so
// TestFakeAnswerFailsTheTestOnAnUnmatchedCall can assert the Fake's own
// failure behaviour without actually failing itself.
type fatalSpy struct {
	testing.TB
	fatalCalled bool
}

func (s *fatalSpy) Helper() {}

func (s *fatalSpy) Fatalf(string, ...any) {
	s.fatalCalled = true
}
