package agents

import (
	"errors"
	"syscall"
)

// terminationSignal is the graceful signal a stop sends before escalating.
func terminationSignal() syscall.Signal { return syscall.SIGTERM }

// killSignal is what a run that ignores the graceful signal gets.
func killSignal() syscall.Signal { return syscall.SIGKILL }

func isNoSuchProcess(err error) bool { return errors.Is(err, syscall.ESRCH) }

// terminateOwner signals one detached owner's process group.
func terminateOwner(pid int, signal syscall.Signal) error {
	err := signalProcessGroup(pid, signal)
	if err == nil || isNoSuchProcess(err) {
		return nil
	}
	return err
}
