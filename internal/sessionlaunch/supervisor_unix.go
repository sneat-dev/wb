//go:build darwin || linux

package sessionlaunch

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/sneat-dev/wb/internal/console"
	"golang.org/x/sys/unix"
)

func prepareOwnedTerminal(command *exec.Cmd, stdin *os.File) (func() error, error) {
	if !console.IsTerminal(stdin) {
		return func() error { return nil }, nil
	}
	fd := int(stdin.Fd())
	original, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
	if err != nil {
		return nil, fmt.Errorf("observe owned terminal foreground: %w", err)
	}
	if original != syscall.Getpgrp() {
		return nil, fmt.Errorf("private supervisor is not its owned terminal foreground group")
	}
	// Foreground uses the controlling descriptor in the parent (Go syscall's
	// documented contract), retaining Herdr's terminal input/process observation.
	command.SysProcAttr.Foreground = true
	command.SysProcAttr.Ctty = fd
	ignored := signal.Ignored(syscall.SIGTTOU)
	signal.Ignore(syscall.SIGTTOU)
	return func() error {
		if !ignored {
			defer signal.Reset(syscall.SIGTTOU)
		}
		current, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
		if err != nil {
			return fmt.Errorf("observe terminal before foreground restore: %w", err)
		}
		if current == original {
			return nil
		}
		if command.Process == nil || current != command.Process.Pid {
			return fmt.Errorf("owned terminal foreground changed to an unrelated group; refusing restoration")
		}
		if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, original); err != nil {
			return fmt.Errorf("restore owned terminal foreground: %w", err)
		}
		return nil
	}, nil
}

func cleanupOwnedChild(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	// CommandContext created this exact group. Never signal the supervisor's
	// inherited group, and never scan arbitrary machine processes by name.
	if command.SysProcAttr == nil || !command.SysProcAttr.Setpgid || command.SysProcAttr.Pgid != 0 || command.Cancel == nil {
		return fmt.Errorf("private child lacks owned process-group cleanup")
	}
	if err := syscall.Kill(-command.Process.Pid, 0); errors.Is(err, syscall.ESRCH) {
		return nil
	} else if err != nil {
		return err
	}
	return command.Cancel()
}
