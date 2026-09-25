package runner

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"

	"github.com/sneat-dev/wb/internal/console"
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
//
// Run is documented as the operation "most git and gh calls" use, so its
// child always carries console.Env(): the non-interactive settings every
// direct exec.Command git/gh call site in this repository has set by hand
// (nonInteractiveChildEnv, GIT_SSH_COMMAND), so a consumer that migrates
// onto Run keeps the same "never hangs on a prompt" guarantee without
// reproducing that env-building itself.
func (Real) Run(ctx context.Context, dir, name string, args ...string) (Result, error) {
	if err := guardRealProcess(); err != nil {
		return Result{}, err
	}
	command := process.CommandContext(ctx, name, args...)
	command.Dir = dir
	command.Env = console.Env()
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	runErr := command.Run()
	result := Result{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCodeOf(runErr)}
	return result, runErr
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
	return Result{Stdout: h.stdout.String(), Stderr: h.stderr.String(), ExitCode: exitCodeOf(waitErr)}, waitErr
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
