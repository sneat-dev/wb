//go:build darwin || linux

package process

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// tailCovProcessGroupOf reports one process group that this process is not
// allowed to signal. Signalling it therefore produces EPERM without delivering
// anything, which is the only way to reach the escalation failure path without
// actually disturbing a live daemon.
//
// A candidate is only returned after signal 0 has confirmed that the kernel
// would refuse a real signal: signal 0 runs the same permission check but
// delivers nothing. Without that probe this helper could hand back the CI
// runner's own process group and the caller would really terminate it, which
// killed the coverage job on GitHub Actions.
func tailCovProcessGroupOf(t *testing.T) int {
	t.Helper()
	ps, err := exec.LookPath("ps")
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	raw, err := exec.Command(ps, "-axo", "uid=,pgid=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pgid, err := strconv.Atoi(fields[1])
		if err != nil || pgid <= 1 {
			// kill(2) treats -1 as "every process the caller may signal" and 0
			// as the caller's own group, so neither may be probed or signalled.
			continue
		}
		// Signal 0 is the permission probe: it delivers nothing, and an EPERM
		// here proves the real SIGTERM below would be refused too.
		if err := syscall.Kill(-pgid, 0); errors.Is(err, syscall.EPERM) {
			return pgid
		}
	}
	return 0
}

// TestTailCovCommandContextOwnsTheProcessTree pins the configuration that makes
// cancellation own descendant processes: the direct child leads a new process
// group, cancellation signals that group, and a descendant that keeps an output
// pipe alive cannot block the caller past the grace period.
func TestTailCovCommandContextOwnsTheProcessTree(t *testing.T) {
	command := CommandContext(context.Background(), "/bin/echo", "owned")
	if command.Path != "/bin/echo" {
		t.Fatalf("Path = %q, want /bin/echo", command.Path)
	}
	if command.SysProcAttr == nil || !command.SysProcAttr.Setpgid {
		t.Fatalf("SysProcAttr = %#v, want Setpgid so cancellation is scoped to the child's group", command.SysProcAttr)
	}
	if command.Cancel == nil {
		t.Fatal("Cancel = nil, want a group-scoped cancellation")
	}
	if command.WaitDelay != cancellationGrace {
		t.Fatalf("WaitDelay = %v, want %v", command.WaitDelay, cancellationGrace)
	}

	output, err := command.Output()
	if err != nil {
		t.Fatalf("configured command did not run: %v", err)
	}
	if got := strings.TrimSpace(string(output)); got != "owned" {
		t.Fatalf("output = %q, want the command's own stdout", got)
	}

	// The child leads its own process group, which is what makes the negative
	// PID in signalProcessGroup resolve to the child and its descendants rather
	// than to the test process's group.
	sleeper, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatalf("sleep: %v", err)
	}
	long := CommandContext(context.Background(), sleeper, "30")
	if err := long.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_ = long.Process.Kill()
		_ = long.Wait()
	})
	group, err := syscall.Getpgid(long.Process.Pid)
	if err != nil {
		t.Fatalf("getpgid(%d): %v", long.Process.Pid, err)
	}
	if group != long.Process.Pid {
		t.Fatalf("child process group = %d, want its own pid %d", group, long.Process.Pid)
	}
	if mine, err := syscall.Getpgid(0); err == nil && mine == group {
		t.Fatalf("child shares the caller's process group %d, want its own", mine)
	}
}

// TestTailCovCommandContextInteractivePreservesTheForegroundGroup is the other
// half of the contract: an interactive command must keep the caller's terminal
// process group, so Ctrl-C reaches it through the terminal instead of being
// intercepted by group ownership.
func TestTailCovCommandContextInteractivePreservesTheForegroundGroup(t *testing.T) {
	command := CommandContextInteractive(context.Background(), true, "/bin/echo", "foreground")
	if command.SysProcAttr != nil {
		t.Fatalf("SysProcAttr = %#v, want nil so the caller's process group is preserved", command.SysProcAttr)
	}
	if command.Cancel == nil {
		t.Fatal("Cancel = nil, want exec's own cancellation retained for an interactive command")
	}
	if command.WaitDelay != 0 {
		t.Fatalf("WaitDelay = %v, want 0 for an interactive command", command.WaitDelay)
	}

	output, err := command.Output()
	if err != nil {
		t.Fatalf("interactive command did not run: %v", err)
	}
	if got := strings.TrimSpace(string(output)); got != "foreground" {
		t.Fatalf("output = %q, want the command's own stdout", got)
	}
}

// TestTailCovCommandContextNonInteractiveHasNoGroupBeforeStart covers the guard
// at the top of the cancellation hook: a command cancelled before it is started
// has no process to signal, so cancellation is a no-op rather than a nil
// dereference.
func TestTailCovCommandContextNonInteractiveHasNoGroupBeforeStart(t *testing.T) {
	command := CommandContext(context.Background(), "/bin/sleep", "30")
	if err := command.Cancel(); err != nil {
		t.Fatalf("Cancel before Start = %v, want nil", err)
	}
	if command.Process != nil {
		t.Fatalf("Process = %v, want nil before Start", command.Process)
	}
}

// TestTailCovTerminateProcessGroupReportsASignalFailure proves a process group
// that cannot be signalled is reported rather than swallowed: only ESRCH ("the
// group is already gone") is a success.
func TestTailCovTerminateProcessGroupReportsASignalFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		// Root may signal every process group, so EPERM is unreachable and any
		// group this test picked would really receive SIGTERM.
		t.Skip("running as root: the EPERM branch of terminateProcessGroup is unreachable")
	}
	pgid := tailCovProcessGroupOf(t)
	if pgid == 0 {
		t.Fatal("no process group this process may not signal was found; cannot exercise the signal-refusal path")
	}

	start := time.Now()
	err := terminateProcessGroup(pgid)
	if !errors.Is(err, syscall.EPERM) {
		t.Fatalf("terminateProcessGroup(%d) = %v, want EPERM", pgid, err)
	}
	// The failure is returned straight away: escalation to SIGKILL must not
	// happen for a group that never received SIGTERM.
	if elapsed := time.Since(start); elapsed >= cancellationGrace {
		t.Fatalf("terminateProcessGroup waited %v before failing, want an immediate return", elapsed)
	}
}

// TestTailCovConfigureDetachedStartsANewSession asserts the observable effect
// of detaching rather than only the field it sets: the child becomes the leader
// of a brand-new session, so a later process-group signal aimed at the parent
// cannot reach it.
func TestTailCovConfigureDetachedStartsANewSession(t *testing.T) {
	sleeper, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatalf("sleep: %v", err)
	}
	command := exec.Command(sleeper, "30")
	ConfigureDetached(command)
	if command.SysProcAttr == nil || !command.SysProcAttr.Setsid {
		t.Fatalf("SysProcAttr = %#v, want Setsid", command.SysProcAttr)
	}
	if err := command.Start(); err != nil {
		t.Fatalf("start detached command: %v", err)
	}
	pid := command.Process.Pid
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})

	// syscall.Getsid exists on darwin but not on linux; a brand-new session is
	// equally observable as a new process group whose id is the child's pid.
	group, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatalf("getpgid(%d): %v", pid, err)
	}
	if group != pid {
		t.Fatalf("detached child process group = %d, want its own pid %d", group, pid)
	}
	if ours, err := syscall.Getpgid(0); err == nil && ours == group {
		t.Fatalf("detached child shares the caller's process group %d, want a new one", ours)
	}
}
