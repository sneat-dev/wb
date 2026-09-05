//go:build darwin || linux

package process

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// cancellationGrace gives a cooperative child a brief chance to stop before
// escalation. It is deliberately bounded: Command.Wait cannot return while a
// descendant that inherited CombinedOutput's pipes remains alive.
var cancellationGrace = 250 * time.Millisecond

func commandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return commandContextInteractive(ctx, false, name, args...)
}

func commandContextInteractive(ctx context.Context, interactive bool, name string, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, name, args...)
	if interactive {
		return command
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		return terminateProcessGroup(command.Process.Pid)
	}
	// A descendant that unexpectedly retains an output pipe must not leave the
	// caller blocked indefinitely after cancellation. Normal cancellation kills
	// the whole group before this delay is needed.
	command.WaitDelay = cancellationGrace
	return command
}

func terminateProcessGroup(pid int) error {
	if err := signalProcessGroup(pid, syscall.SIGTERM); err != nil {
		return err
	}

	timer := time.NewTimer(cancellationGrace)
	defer timer.Stop()
	<-timer.C
	return signalProcessGroup(pid, syscall.SIGKILL)
}

func signalProcessGroup(pid int, signal syscall.Signal) error {
	// Setpgid makes the direct child the process-group leader. Negative PID is
	// therefore scoped to precisely that child and descendants which inherited
	// its group; it never scans or signals unrelated system processes.
	err := syscall.Kill(-pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
