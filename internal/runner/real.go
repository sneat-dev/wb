package runner

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"

	"github.com/sneat-dev/wb/internal/process"
)

// Real is the production Runner, built on internal/process. Every operation
// checks guardRealProcess first.
type Real struct{}

var _ Runner = Real{}

// New returns the production Runner.
func New() Real { return Real{} }

// Run starts name with args in dir, waits for it to exit, and returns its
// captured stdout, stderr and exit status.
func (Real) Run(ctx context.Context, dir, name string, args ...string) (Result, error) {
	if err := guardRealProcess(); err != nil {
		return Result{}, err
	}
	command := process.CommandContext(ctx, name, args...)
	command.Dir = dir
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	runErr := command.Run()
	result := Result{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCodeOf(runErr)}
	return withoutSpuriousWaitDelay(command, result, runErr)
}

// RunWithInput is Run with input written to the child's stdin.
func (Real) RunWithInput(ctx context.Context, dir string, input []byte, name string, args ...string) (Result, error) {
	if err := guardRealProcess(); err != nil {
		return Result{}, err
	}
	command := process.CommandContext(ctx, name, args...)
	command.Dir = dir
	command.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	runErr := command.Run()
	result := Result{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCodeOf(runErr)}
	return withoutSpuriousWaitDelay(command, result, runErr)
}

// RunOpts is Run with a RunOptions value: a per-call environment, stdin,
// and/or WaitDelay.
func (Real) RunOpts(ctx context.Context, dir string, opts RunOptions, name string, args ...string) (Result, error) {
	if err := guardRealProcess(); err != nil {
		return Result{}, err
	}
	command := process.CommandContext(ctx, name, args...)
	command.Dir = dir
	if opts.Env != nil {
		command.Env = opts.Env
	}
	if len(opts.Stdin) > 0 {
		command.Stdin = bytes.NewReader(opts.Stdin)
	}
	if opts.WaitDelay > 0 {
		command.WaitDelay = opts.WaitDelay
	}
	capture := configureOutputCapture(command, opts.CaptureCombined)
	runErr := command.Run()
	result := Result{Stdout: capture.stdout.String(), Stderr: capture.stderr.String(), CombinedOutput: capture.combined.String(), ExitCode: exitCodeOf(runErr)}
	return withoutSpuriousWaitDelay(command, result, runErr)
}

// outputCapture owns the writers attached to one child. Keeping it alive
// through command.Run also keeps every captured buffer alive through Wait.
type outputCapture struct {
	stdout   bytes.Buffer
	stderr   bytes.Buffer
	combined bytes.Buffer
}

func configureOutputCapture(command *exec.Cmd, combined bool) *outputCapture {
	capture := &outputCapture{}
	if combined {
		// The same writer makes os/exec use one pipe, matching CombinedOutput.
		command.Stdout = &capture.combined
		command.Stderr = &capture.combined
	} else {
		command.Stdout = &capture.stdout
		command.Stderr = &capture.stderr
	}
	return capture
}

// Start begins name with args in dir and returns a Handle without waiting
// for it to exit.
func (Real) Start(ctx context.Context, dir, name string, args ...string) (Handle, error) {
	if err := guardRealProcess(); err != nil {
		return nil, err
	}
	command := process.CommandContext(ctx, name, args...)
	command.Dir = dir
	handle := &realHandle{command: command}
	command.Stdout = &handle.stdout
	command.Stderr = &handle.stderr
	if err := command.Start(); err != nil {
		return nil, err
	}
	return handle, nil
}

// Detach starts name with args in dir as a process that outlives the
// caller, and reports its process id.
func (Real) Detach(dir, name string, args ...string) (int, error) {
	if err := guardRealProcess(); err != nil {
		return 0, err
	}
	command := exec.Command(name, args...)
	command.Dir = dir
	process.ConfigureDetached(command)
	if err := command.Start(); err != nil {
		return 0, err
	}
	pid := command.Process.Pid
	// A detached process is deliberately not waited on: it outlives this
	// call. Releasing it here avoids leaking this process's own resources
	// for tracking a child it will never Wait on.
	_ = command.Process.Release()
	return pid, nil
}

// Interactive starts name with args in dir with stdio passed through to the
// caller's own, and waits for it to exit.
func (Real) Interactive(ctx context.Context, dir, name string, args ...string) error {
	if err := guardRealProcess(); err != nil {
		return err
	}
	command := process.CommandContextInteractive(ctx, true, name, args...)
	command.Dir = dir
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

// realHandle is Real's Handle: a started *exec.Cmd whose output accumulates
// in buffers Wait reads back.
type realHandle struct {
	command *exec.Cmd
	stdout  bytes.Buffer
	stderr  bytes.Buffer
}

func (h *realHandle) Wait() (Result, error) {
	waitErr := h.command.Wait()
	result := Result{Stdout: h.stdout.String(), Stderr: h.stderr.String(), ExitCode: exitCodeOf(waitErr)}
	return withoutSpuriousWaitDelay(h.command, result, waitErr)
}

// Signal and Pid are only ever called on a Handle Start returned, and Start
// never returns one until command.Start() has succeeded, so command.Process
// is always non-nil here.

func (h *realHandle) Signal(signal os.Signal) error {
	return h.command.Process.Signal(signal)
}

func (h *realHandle) Pid() int {
	return h.command.Process.Pid
}

// withoutSpuriousWaitDelay clears runErr when Cmd.Wait returned the bare
// exec.ErrWaitDelay sentinel: a grandchild that inherited the child's
// stdout/stderr (an ssh ControlPersist master from `git ls-remote`/`fetch`
// over ssh, a credential helper) kept one of those pipes open past
// command.WaitDelay after the child itself had already exited successfully.
// The child's own captured output is complete by then -- only the
// lingering descendant's pipe was force-closed -- so callers must see
// success rather than a spurious failure the child never had.
//
// Per os/exec's Cmd.Wait (awaitGoroutines), that bare, unwrapped
// exec.ErrWaitDelay is returned only on the path where the process's own
// exit was already successful: a non-zero exit becomes *exec.ExitError
// before the I/O goroutines are ever consulted, so it never reaches this
// function and keeps its error untouched. command.ProcessState.Success()
// is checked all the same, defensively, so a future stdlib change that
// widened when ErrWaitDelay can appear would not silently swallow a real
// failure.
func withoutSpuriousWaitDelay(command *exec.Cmd, result Result, runErr error) (Result, error) {
	if errors.Is(runErr, exec.ErrWaitDelay) && command.ProcessState != nil && command.ProcessState.Success() {
		return result, nil
	}
	return result, runErr
}

// exitCodeOf reports err's process exit code, or 0 for a nil error (success)
// or an error that never reached a process exit (a start failure, a
// context cancellation before exec) -- exec.Cmd itself reports -1 in that
// case, and a caller that never checks err would otherwise misread that as
// a real exit status.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 0
}
