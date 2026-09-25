//go:build darwin || linux

package process

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestCommandContextChildHasNoControllingTerminal pins Setsid's observable
// effect (replacing Setpgid, review round 3 R2/R3): the child leads a
// brand-new session as well as a new process group (its process group id is
// its own pid, exactly as Setpgid alone gave it), and -- unlike Setpgid alone
// -- it has no controlling terminal at all, so opening /dev/tty fails instead
// of the child being stopped by SIGTTIN/SIGTTOU for touching one it can no
// longer reach.
func TestCommandContextChildHasNoControllingTerminal(t *testing.T) {
	t.Parallel()
	resultPath := filepath.Join(t.TempDir(), "tty-result")
	command := CommandContext(context.Background(), os.Args[0], "-test.run=^TestProcessHelper$")
	command.Env = append(os.Environ(), "WB_PROCESS_HELPER=tty-probe", "WB_PROCESS_TTY_RESULT_PATH="+resultPath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("tty-probe helper: %v: %s", err, output)
	}
	raw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read tty probe result: %v", err)
	}
	got := string(raw)
	if got == "ok" {
		t.Fatalf("child opened /dev/tty successfully, want it to have no controlling terminal to open")
	}
}

// TestCmdStartRefusesAnUncancellableCommandAfterRefuseNewStarts pins the
// first-signal gate (review round 3 R3-B1): once RefuseNewStarts has been
// called, a command whose own context can never be cancelled -- one built
// directly on context.Background(), like most of this module's callers --
// is refused rather than started.
//
// This test mutates package-level state (the refusing flag) and must not run
// in parallel with any test that starts a real command through this
// package: see the t.Cleanup restoring it before this function returns.
func TestCmdStartRefusesAnUncancellableCommandAfterRefuseNewStarts(t *testing.T) {
	refusing.Store(false)
	t.Cleanup(func() { refusing.Store(false) })

	command := CommandContext(context.Background(), "/bin/echo", "should-not-run")
	RefuseNewStarts()
	err := command.Start()
	if err == nil {
		t.Fatal("Start after RefuseNewStarts = nil, want a refusal")
	}
	if !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("Start error = %v, want it to wrap ErrShuttingDown", err)
	}
}

// TestCmdStartAllowsABoundedRollbackContextAfterRefuseNewStarts is the other
// half of R3-B1: a command built on a context with its own deadline -- the
// exact shape worktrees' rollbackContext, orchestrate's pr_land_keep and
// agents' owner cleanup use, context.WithTimeout(context.WithoutCancel(ctx),
// ...) -- still starts after shutdown has begun, because its Done() is
// non-nil, so an interrupted "create" can still remove its half-made
// checkout and branch.
func TestCmdStartAllowsABoundedRollbackContextAfterRefuseNewStarts(t *testing.T) {
	refusing.Store(false)
	t.Cleanup(func() { refusing.Store(false) })

	rollback, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 30*time.Second)
	defer cancel()
	if rollback.Done() == nil {
		t.Fatal("rollback-shaped context has a nil Done(); the test no longer models R3-B1's exemption")
	}

	command := CommandContext(rollback, "/bin/echo", "rollback-cleanup")
	RefuseNewStarts()
	if err := command.Start(); err != nil {
		t.Fatalf("Start of a bounded rollback context after RefuseNewStarts = %v, want it to run", err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("Wait = %v", err)
	}
}

// TestCmdStartRefusalDoesNotBlockAnInteractiveOrDetachedCaller documents that
// the refusal only ever applies to a command actually driven through this
// package's Cmd.Start -- Real.Detach (internal/runner) builds its own plain
// *exec.Cmd and never reaches this gate at all, which is what "never
// register Detach" (review round 2/3) means in practice for refusal too.
func TestCmdStartRefusalDoesNotBlockAnInteractiveOrDetachedCaller(t *testing.T) {
	refusing.Store(false)
	t.Cleanup(func() { refusing.Store(false) })
	RefuseNewStarts()

	plain := exec.CommandContext(context.Background(), "/bin/echo", "detached-shaped")
	if err := plain.Run(); err != nil {
		t.Fatalf("a plain *exec.Cmd (Detach's shape) must never see this package's refusal: %v", err)
	}
}

// TestSignalLiveGroupsReachesARegisteredBackgroundContextChild is the
// registry's own reason to exist: a child started on context.Background()
// never sees its own context cancelled, so only the live-group registry --
// populated by Cmd.Start, independent of ctx -- can reach it.
func TestSignalLiveGroupsReachesARegisteredBackgroundContextChild(t *testing.T) {
	t.Parallel()
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	command := CommandContext(context.Background(), os.Args[0], "-test.run=^TestProcessHelper$")
	command.Env = append(os.Environ(), "WB_PROCESS_HELPER=block-until-signalled", "WB_PROCESS_CHILD_PID_PATH="+pidPath)
	if err := command.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	_ = readProcessID(t, pidPath) // confirms the child actually reached sleepUntilKilled.
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	t.Cleanup(func() {
		// Best-effort: if the assertion below already consumed waited, the
		// process is already dead and reaped, and this is a harmless no-op.
		_ = command.Process.Kill()
	})

	SignalLiveGroups(syscall.SIGKILL)

	// Wait, not a raw kill(pid, 0) poll: a killed-but-unreaped child is a
	// zombie whose pid still answers signal 0 until something reaps it, so
	// only the parent's own Wait returning proves the group was actually
	// reached.
	select {
	case err := <-waited:
		if err == nil {
			t.Fatal("Wait = nil, want an error reporting the child was killed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("child survived SignalLiveGroups(SIGKILL); it was never registered")
	}
}

// TestCmdWaitDeregistersSoALaterSignalLiveGroupsIsANoOp proves the other half
// of the registry's contract (Wait removes it, review round 2 point 3): once
// a Cmd has been waited on, a later SignalLiveGroups call has nothing left
// to reach for it -- exercised negatively, by asserting the call does not
// error or panic once the registry is provably empty of this entry.
func TestCmdWaitDeregistersSoALaterSignalLiveGroupsIsANoOp(t *testing.T) {
	t.Parallel()
	command := CommandContext(context.Background(), "/bin/echo", "quick")
	if err := command.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if command.tracked {
		t.Fatal("tracked = true after Wait, want Wait to have deregistered it")
	}
	// Never registered in the first place, so nothing to signal -- this must
	// not panic or block.
	SignalLiveGroups(syscall.SIGKILL)
}

// TestCommandContextInteractiveIsNeverTrackable pins the "never register ...
// Interactive" rule (review round 2/3): an interactive command shares wb's
// own foreground process group and terminal, so the registry must not carry
// it at all, regardless of shutdown state.
func TestCommandContextInteractiveIsNeverTrackable(t *testing.T) {
	t.Parallel()
	command := CommandContextInteractive(context.Background(), true, "/bin/echo", "foreground")
	if command.trackable {
		t.Fatal("trackable = true for an interactive command, want false")
	}
}
