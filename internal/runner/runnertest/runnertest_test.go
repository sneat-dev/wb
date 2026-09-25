package runnertest

import (
	"context"
	"os"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
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
