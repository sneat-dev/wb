// Package runner is spec/plans/coverage-to-100/README.md task-8's command
// runner: the one seam every caller uses to start an external program,
// built on internal/process rather than beside it
// (rule:reuse-existing-sneat-code). Production code depends on the Runner
// interface, never on os/exec directly, so a unit test substitutes
// runnertest's scriptable fake instead of starting a real process.
//
// Runner has four operations, exactly as the plan defines them, plus two
// gaps the plan's own text names and invites filling when a real site needs
// them (see task-8's "If the runner cannot express something a site needs"
// note): RunWithInput and RunOpts.
//
//   - Run captures stdout, stderr and the exit status -- most git and gh
//     calls.
//   - RunWithInput is Run with the child's stdin supplied by the caller,
//     for the rare site that must pass a request body a remote or local
//     child reads from stdin instead of argv (added for remotessh's SSH
//     boundary).
//   - RunOpts is Run with a RunOptions value -- a per-call environment
//     and/or a WaitDelay -- for the several sites that build a filtered or
//     augmented child environment (internal/console.Env()/CommandEnv(), a
//     GOWORK=off override, an extra credential) or that must bound how
//     long a child's inherited pipes are allowed to stay open after the
//     child itself has exited (os/exec.Cmd.WaitDelay). RunOptions carries
//     an optional Stdin too, so a caller needing both input and a custom
//     environment does not have to choose between RunWithInput and RunOpts.
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
	"io"
	"os"
	"time"
)

// Result is one Run or Start/Wait call's captured output.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// RunOptions customizes a RunOpts call beyond dir/argv.
type RunOptions struct {
	// Env overrides the child's environment. Nil inherits the calling
	// process's own environment, matching os/exec.Cmd's own default when
	// Env is left nil -- the same default Run and RunWithInput use.
	Env []string
	// Stdin is written to the child's stdin before its output is read, like
	// RunWithInput's input. Nil/empty gives the child no stdin (the same as
	// Run).
	Stdin []byte
	// WaitDelay bounds how long RunOpts waits for the child's I/O pipes to
	// drain after the process itself has exited (os/exec.Cmd.WaitDelay).
	// Zero uses the underlying implementation's own default -- Real inherits
	// internal/process's, not zero/unbounded -- so a caller that genuinely
	// needs a specific bound (a package-manager launcher that hands off to a
	// grandchild and exits early) sets one explicitly rather than relying on
	// whatever internal/process happens to default to today.
	WaitDelay time.Duration
}

// StreamOptions customizes a Stream call: a per-call environment and/or
// explicit stdio streams, for a caller that must pass a restricted or
// augmented environment through to a child whose stdio is not captured but
// streamed directly to/from the caller's own (possibly non-os.Std{in,out,err})
// readers and writers -- added for internal/hooks' template runner, which
// both sanitizes the child's environment (stripping WB_AGENT_* and
// git-generated GIT_* variables) and must honor a caller-supplied
// Stdin/Stdout/Stderr (a hook harness under test, or cobra's
// InOrStdin/OutOrStdout/ErrOrStderr) rather than the process's own. Unlike
// Interactive, Stream never adjusts the child's foreground terminal process
// group.
type StreamOptions struct {
	// Env overrides the child's environment, exactly like RunOptions.Env.
	// Nil inherits the calling process's own environment.
	Env []string
	// Stdin, Stdout and Stderr are the child's stdio streams. A nil field
	// falls back to the calling process's own os.Stdin/os.Stdout/os.Stderr,
	// matching Interactive's behavior for a caller that only needs to
	// override some of the three.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
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
	// RunWithInput is Run with input written to the child's stdin before its
	// output is read. See the package doc's note on why this exists
	// alongside Run rather than folding input into it.
	RunWithInput(ctx context.Context, dir string, input []byte, name string, args ...string) (Result, error)
	// RunOpts is Run with a RunOptions value: a per-call environment,
	// stdin, and/or WaitDelay. See the package doc's note on RunOpts.
	RunOpts(ctx context.Context, dir string, opts RunOptions, name string, args ...string) (Result, error)
	// Start begins name with args in dir and returns a Handle without
	// waiting for it to exit.
	Start(ctx context.Context, dir, name string, args ...string) (Handle, error)
	// Detach starts name with args in dir as a process that outlives the
	// caller, and reports its process id.
	Detach(dir, name string, args ...string) (pid int, err error)
	// Interactive starts name with args in dir with stdio passed through to
	// the caller's own, and waits for it to exit.
	Interactive(ctx context.Context, dir, name string, args ...string) error
	// Stream starts name with args in dir, streaming opts.Stdin/Stdout/Stderr
	// directly rather than capturing them, with opts.Env as its environment,
	// and waits for it to exit. The returned Result's ExitCode reflects the
	// exit status exactly like Run/RunOpts; Stdout/Stderr are always empty
	// since output was streamed rather than captured. See StreamOptions.
	Stream(ctx context.Context, dir string, opts StreamOptions, name string, args ...string) (Result, error)
}
