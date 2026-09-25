// Package runnertest is task-8's test double for internal/runner.Runner: a
// scriptable Fake that matches an expected argv and returns canned output
// or an injected error, plus AllowRealProcess for the small number of tests
// that must start a real process. Every consumer of internal/runner depends
// on the runner.Runner interface, never on internal/runner.Real directly,
// so a unit test substitutes Fake here instead.
//
// Fake also has task-8's fail-call-N mode (decision 23): FailCall makes one
// chosen call fail on its own, standalone, and Fake implements
// internal/testsweep.Failer so internal/testsweep.Sweep can drive it once
// per call a happy-path body makes, to cover every error return a
// multi-call body reaches without a hand-written failure test per call. See
// FailCall's doc comment and internal/testsweep's package doc for usage.
package runnertest

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/testsweep"
)

// AllowRealProcess lets t start a real process through
// [github.com/sneat-dev/wb/internal/runner.Real] despite task-24's runtime
// guard. Only a file already on internal/quality/testdata/unit_tier.pending
// or unit_tier.allow may call it -- the static check in internal/quality
// fails an unreviewed new call the same way it fails any other pending-list
// violation.
//
// It works by setting the runner package's own allow-process environment
// variable through t.Setenv, so it is restored automatically at t's cleanup
// and (like every other t.Setenv call in this repository) refuses to
// coexist with t.Parallel().
func AllowRealProcess(t testing.TB) {
	t.Helper()
	t.Setenv("WB_RUNNER_ALLOW_REAL_PROCESS", "1")
}

// Call records one Run/RunWithInput/RunOpts/Start/Detach/Interactive/Stream
// invocation the Fake received.
type Call struct {
	Op   string // "Run", "RunWithInput", "RunOpts", "Start", "Detach", "Interactive" or "Stream"
	Dir  string
	Name string
	Args []string
	// Input is the stdin RunWithInput was given, or opts.Stdin for RunOpts.
	// Empty for every other Op.
	Input []byte
	// Opts is the RunOptions a RunOpts call was given. Zero value for every
	// other Op.
	Opts runner.RunOptions
	// StreamOpts is the StreamOptions a Stream call was given. Zero value
	// for every other Op.
	StreamOpts runner.StreamOptions
}

// Argv is Name followed by Args, the shape Expect's matcher predicates
// compare against.
func (c Call) Argv() []string {
	return append([]string{c.Name}, c.Args...)
}

// script is one configured response, matched in the order it was added via
// Expect; the first whose Match accepts a call answers it.
type script struct {
	match  func(Call) bool
	result runner.Result
	err    error
}

// Fake is a scriptable, in-memory [runner.Runner]. Configure expected calls
// with Expect before exercising the code under test, then assert on Calls.
// An unmatched call fails the test immediately through t, rather than
// silently succeeding: a fake that answers a call nobody scripted would
// hide the exact case task-8's contract tests exist to catch.
//
// Fake also has task-8's fail-call-N mode (decision 23): FailCall makes one
// chosen call number fail regardless of its scripted result, on its own or
// driven by internal/testsweep.Sweep across every call a happy path makes.
// See CallCount and FailCall, and the package example.
type Fake struct {
	t testing.TB

	mu      sync.Mutex
	scripts []script
	calls   []Call

	failAt  int // 1-indexed call number FailCall targets; 0 disables it.
	failErr error
}

var _ runner.Runner = (*Fake)(nil)
var _ testsweep.Failer = (*Fake)(nil)

// New returns a Fake that reports an unmatched or unexpected call through t.
func New(t testing.TB) *Fake {
	return &Fake{t: t}
}

// Expect adds one scripted response: the next call for which match returns
// true is answered with result and err (either may be zero). Scripts are
// tried in the order they were added and each is consumed on its first
// match, so a test that expects the same argv twice with different results
// calls Expect twice.
func (f *Fake) Expect(match func(Call) bool, result runner.Result, err error) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts = append(f.scripts, script{match: match, result: result, err: err})
	return f
}

// ExpectArgv is Expect's common case: match an exact argv (name followed by
// args).
func (f *Fake) ExpectArgv(argv []string, result runner.Result, err error) *Fake {
	return f.Expect(func(c Call) bool {
		return strings.Join(c.Argv(), "\x00") == strings.Join(argv, "\x00")
	}, result, err)
}

// Calls returns every call the Fake has received so far, in order.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// CallCount reports how many calls the Fake has answered so far, across
// Run, Start, Detach and Interactive combined, in the order it answered
// them. It implements internal/testsweep.Failer.
func (f *Fake) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// FailCall arranges for the callNum'th call (1-indexed, counting Run,
// Start, Detach and Interactive together in the order the Fake answers
// them) to fail with failErr instead of returning its scripted result;
// every other call keeps returning its own script unchanged, so FailCall
// fails exactly call N and passes the rest. The call FailCall targets must
// still be scripted via Expect/ExpectArgv first -- FailCall replaces that
// call's result, not the argv match that catches an unexpected call.
//
// It works standalone, or driven once per call number by
// internal/testsweep.Sweep, which is why Fake implements
// internal/testsweep.Failer. A callNum of 0 disables the override; calling
// FailCall again replaces the previous target rather than adding a second
// one.
func (f *Fake) FailCall(callNum int, failErr error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failAt = callNum
	f.failErr = failErr
}

// answer records call and returns the first unconsumed script matching it,
// failing the test if none match. When call is the call number FailCall
// last targeted, it returns failErr instead of that script's own result.
func (f *Fake) answer(call Call) (runner.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	n := len(f.calls)
	for i, s := range f.scripts {
		if s.match(call) {
			f.scripts = append(f.scripts[:i], f.scripts[i+1:]...)
			if f.failAt != 0 && n == f.failAt {
				return runner.Result{}, f.failErr
			}
			return s.result, s.err
		}
	}
	f.t.Helper()
	f.t.Fatalf("runnertest.Fake: unexpected call %s %v in %s (no script matched; call Expect/ExpectArgv first)", call.Op, call.Argv(), call.Dir)
	return runner.Result{}, fmt.Errorf("runnertest.Fake: unexpected call %s %v", call.Op, call.Argv())
}

// Run implements runner.Runner.
func (f *Fake) Run(_ context.Context, dir, name string, args ...string) (runner.Result, error) {
	return f.answer(Call{Op: "Run", Dir: dir, Name: name, Args: args})
}

// RunWithInput implements runner.Runner. The input the caller passed is
// recorded on Call.Input, so a test can assert on it via Calls/ExpectArgv's
// match function alongside the argv.
func (f *Fake) RunWithInput(_ context.Context, dir string, input []byte, name string, args ...string) (runner.Result, error) {
	return f.answer(Call{Op: "RunWithInput", Dir: dir, Name: name, Args: args, Input: input})
}

// RunOpts implements runner.Runner. opts is recorded on Call.Opts (and its
// Stdin duplicated onto Call.Input alongside RunWithInput's), so a test can
// assert on the environment/stdin/WaitDelay a call was given.
func (f *Fake) RunOpts(_ context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	return f.answer(Call{Op: "RunOpts", Dir: dir, Name: name, Args: args, Input: opts.Stdin, Opts: opts})
}

// Start implements runner.Runner. The returned Handle's Wait replays the
// scripted result immediately; Start never actually runs a process.
func (f *Fake) Start(_ context.Context, dir, name string, args ...string) (runner.Handle, error) {
	result, err := f.answer(Call{Op: "Start", Dir: dir, Name: name, Args: args})
	if err != nil {
		return nil, err
	}
	return &fakeHandle{result: result}, nil
}

// Detach implements runner.Runner.
func (f *Fake) Detach(dir, name string, args ...string) (int, error) {
	result, err := f.answer(Call{Op: "Detach", Dir: dir, Name: name, Args: args})
	return result.ExitCode, err
}

// Interactive implements runner.Runner.
func (f *Fake) Interactive(_ context.Context, dir, name string, args ...string) error {
	_, err := f.answer(Call{Op: "Interactive", Dir: dir, Name: name, Args: args})
	return err
}

// Stream implements runner.Runner. opts is recorded on Call.StreamOpts, so a
// test can assert on the environment/stdio a call was given without the Fake
// itself reading from or writing to them.
func (f *Fake) Stream(_ context.Context, dir string, opts runner.StreamOptions, name string, args ...string) (runner.Result, error) {
	return f.answer(Call{Op: "Stream", Dir: dir, Name: name, Args: args, StreamOpts: opts})
}

// fakeHandle is Start's returned Handle: it replays the script's result once
// Wait is called.
type fakeHandle struct {
	result runner.Result
}

func (h *fakeHandle) Wait() (runner.Result, error) {
	if h.result.ExitCode != 0 {
		return h.result, fmt.Errorf("exit status %d", h.result.ExitCode)
	}
	return h.result, nil
}

func (h *fakeHandle) Signal(os.Signal) error { return nil }

func (h *fakeHandle) Pid() int { return 0 }
