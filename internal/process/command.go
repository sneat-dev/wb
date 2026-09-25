// Package process starts bounded subprocesses with a lifecycle that owns their
// descendants as well as their direct child.
package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"
)

// ErrShuttingDown is wrapped by the error Start returns once RefuseNewStarts
// has been called and the command's own context can never be cancelled (see
// Cmd.Start).
var ErrShuttingDown = errors.New("process: wb is shutting down")

// refusing is set once, by the root signal handler's first signal, and never
// cleared: the process is on its way out for the rest of its life.
var refusing atomic.Bool

// RefuseNewStarts makes every future Cmd.Start reject a command whose own
// context can never be cancelled (ctx.Done() == nil, e.g. one built directly
// on context.Background()). A command whose context carries its own deadline
// -- a bounded rollback such as worktrees' rollbackContext, orchestrate's
// pr_land_keep, or agents' owner cleanup -- is deliberately exempt: it must
// still run so an interrupted "create" can undo its half-made state, and its
// own deadline is what keeps it from outliving shutdown indefinitely.
//
// It is safe to call more than once and from any goroutine; only the first
// call has an effect.
func RefuseNewStarts() {
	refusing.Store(true)
}

// Cmd wraps *exec.Cmd so Start, Wait and Run can register the child's process
// group in the live-group registry for exactly the span it can be signalled,
// and so Start can refuse a new, uncancellable child once shutdown has begun
// -- without requiring every caller across the module to change how it
// drives the command. Every field (Dir, Env, Stdout, ...) and every method
// this type does not redeclare (Output, CombinedOutput, ...) is the embedded
// *exec.Cmd's own, unchanged.
//
// Output and CombinedOutput are NOT overridden: the standard library starts
// and waits for the process entirely inside those calls, with no seam this
// type can intercept short of reimplementing them, so a child driven that way
// is not reachable through SignalLiveGroups and is not subject to
// RefuseNewStarts. The two current call sites (internal/quality/verify.go)
// are CI verification subprocesses, not part of an interactive wb
// invocation's signal surface; ctx cancellation (Cancel, set by
// commandContext) still reaches them exactly as it does today.
type Cmd struct {
	*exec.Cmd

	ctx       context.Context
	trackable bool
	token     uint64
	tracked   bool
}

// CommandContext returns a command whose cancellation owns the process tree
// on the platforms where WB supports process-tree cancellation, and whose
// process group is registered with the live-group registry for the duration
// it is running (see SignalLiveGroups).
func CommandContext(ctx context.Context, name string, args ...string) *Cmd {
	return &Cmd{Cmd: commandContext(ctx, name, args...), ctx: ctx, trackable: true}
}

// CommandContextInteractive preserves the caller's foreground terminal
// process group when interactive is true. Such a command already shares wb's
// own process group and terminal, so a terminal signal reaches it directly:
// it is never registered with the live-group registry. Noninteractive
// commands behave exactly like CommandContext.
func CommandContextInteractive(ctx context.Context, interactive bool, name string, args ...string) *Cmd {
	return &Cmd{
		Cmd:       commandContextInteractive(ctx, interactive, name, args...),
		ctx:       ctx,
		trackable: !interactive,
	}
}

// Start starts the command. If RefuseNewStarts has been called and this
// command's own context can never be cancelled, Start refuses to run it
// instead, so shutdown cannot be extended indefinitely by new, unbounded
// work; a command built on a context with its own deadline (a bounded
// rollback) still starts normally. A trackable command that does start is
// registered with the live-group registry immediately, so SignalLiveGroups
// can reach it even if its own context is never cancelled.
func (c *Cmd) Start() error {
	if refusing.Load() && c.ctx != nil && c.ctx.Done() == nil {
		return fmt.Errorf("%w: refusing to start %s", ErrShuttingDown, c.Cmd.Path)
	}
	if err := c.Cmd.Start(); err != nil {
		return err
	}
	c.track()
	return nil
}

// Wait waits for the command to exit and removes it from the live-group
// registry on every return path, matching Start's registration.
func (c *Cmd) Wait() error {
	err := c.Cmd.Wait()
	c.untrack()
	return err
}

// Run starts the command and waits for it to exit, exactly like exec.Cmd's
// own Run, through this type's own Start and Wait so registration and the
// shutdown refusal both still apply.
func (c *Cmd) Run() error {
	if err := c.Start(); err != nil {
		return err
	}
	return c.Wait()
}

func (c *Cmd) track() {
	if !c.trackable || c.Cmd.Process == nil {
		return
	}
	c.token = trackStart(c.Cmd.Process.Pid)
	c.tracked = true
}

func (c *Cmd) untrack() {
	if !c.tracked {
		return
	}
	trackStop(c.token)
	c.tracked = false
}

// SignalLiveGroups sends sig to the process group of every Cmd currently
// registered between a successful Start and its Wait, tolerating a group
// that has already exited. It is the mechanism that reaches a child whose own
// context was never cancelled (SignalLiveGroups(syscall.SIGINT), stage one of
// wb's interrupt handling) and the one that finishes the job on a second
// signal (SignalLiveGroups(syscall.SIGKILL)). It is a no-op on platforms with
// no process-group support (see command_other.go).
//
// Known limitation: there is a small window between a child's process
// exiting (removing it from the registry, in Wait) and the kernel reusing its
// pid for an unrelated process, during which a concurrent SignalLiveGroups
// call could -- in principle -- signal that unrelated process's group
// instead. This is the same pid-reuse exposure inherent to any pid-based
// signalling design (os/signal and process supervisors carry the same risk);
// it is not specific to this registry and is accepted rather than solved
// here.
func SignalLiveGroups(sig os.Signal) {
	signalLiveGroups(sig)
}
