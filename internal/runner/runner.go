// Package runner is spec/plans/coverage-to-100/README.md task-8's command
// runner: the one seam every caller uses to start an external program,
// built on internal/process rather than beside it
// (rule:reuse-existing-sneat-code). Production code depends on the Runner
// interface, never on os/exec directly, so a unit test substitutes
// runnertest's scriptable fake instead of starting a real process.
//
// Runner has four operations, exactly as the plan defines them:
//
//   - Run captures stdout, stderr and the exit status -- most git and gh
//     calls.
//   - Start returns a Handle with Wait and Signal, for a long-running child
//     wb supervises.
//   - Detach starts a process that outlives wb -- daemon launch,
//     browser.go, lifecycle hooks.
//   - Interactive passes stdio through -- `wb run -- …`, tmux, agent
//     harnesses.
//
// The real implementation ([Real]) carries task-24's runtime guard: it
// refuses to start a process while testing.Testing() is true, unless the
// binary is built with the e2e tag, the calling test named itself on
// runnertest's allow/pending list by calling AllowRealProcess, or the
// process is the Go helper-process re-exec of the test binary itself. See
// guard.go.
package runner

import (
	"context"
	"os"
)

// Result is one Run or Start/Wait call's captured output.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Handle is a process started by Start: callers wait for it or signal it
// without depending on os/exec directly.
type Handle interface {
	// Wait blocks until the process exits and returns its captured output.
	Wait() (Result, error)
	// Signal delivers signal to the process. Once the process has already
	// been waited on, it reports an error (never a panic) rather than
	// signalling an unrelated, possibly-reused pid.
	Signal(signal os.Signal) error
	// Pid reports the started process's process id.
	Pid() int
}

// Runner is task-8's command-runner port.
type Runner interface {
	// Run starts name with args in dir, waits for it to exit, and returns
	// its captured stdout, stderr and exit status.
	Run(ctx context.Context, dir, name string, args ...string) (Result, error)
	// RunEnv is Run, but the child's environment is env exactly, not
	// inherited from the caller -- the same replace-not-merge semantics
	// os/exec.Cmd.Env documents. A nil env is Run's own default: "inherit
	// the caller's ambient environment unchanged". It exists for a call
	// site that must run a program with a deliberately restricted or
	// rebuilt environment (a git invocation that must not read a user's
	// global/system gitconfig, for example), which Run's own signature has
	// no way to express.
	RunEnv(ctx context.Context, dir string, env []string, name string, args ...string) (Result, error)
	// Start begins name with args in dir and returns a Handle without
	// waiting for it to exit.
	Start(ctx context.Context, dir, name string, args ...string) (Handle, error)
	// Detach starts name with args in dir as a process that outlives the
	// caller, and reports its process id.
	Detach(dir, name string, args ...string) (pid int, err error)
	// Interactive starts name with args in dir with stdio passed through to
	// the caller's own, and waits for it to exit.
	Interactive(ctx context.Context, dir, name string, args ...string) error
}
