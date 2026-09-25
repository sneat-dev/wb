// Package runnertest is task-8's test double for internal/runner.Runner: a
// scriptable Fake that matches an expected argv and returns canned output
// or an injected error, plus AllowRealProcess for the small number of tests
// that must start a real process. Every consumer of internal/runner depends
// on the runner.Runner interface, never on internal/runner.Real directly,
// so a unit test substitutes Fake here instead.
package runnertest

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
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

// Call records one Run/Start/Detach/Interactive invocation the Fake
// received.
type Call struct {
	Op   string // "Run", "Start", "Detach" or "Interactive"
	Dir  string
	Name string
	Args []string
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
type Fake struct {
	t testing.TB

	mu      sync.Mutex
	scripts []script
	calls   []Call
}

var _ runner.Runner = (*Fake)(nil)

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

// answer records call and returns the first unconsumed script matching it,
// failing the test if none match.
func (f *Fake) answer(call Call) (runner.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	for i, s := range f.scripts {
		if s.match(call) {
			f.scripts = append(f.scripts[:i], f.scripts[i+1:]...)
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
