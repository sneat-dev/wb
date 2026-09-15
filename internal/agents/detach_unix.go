//go:build darwin || linux

package agents

import (
	"os/exec"
	"syscall"
)

// configureDetached puts the run owner in its own session, exactly like WB's
// existing detached lifecycle-hook worker. It is what lets the dispatching CLI
// exit without taking the worker down with it, and what keeps a later
// process-group signal scoped to this run alone.
func configureDetached(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// terminateOwner signals one detached owner's process group.
func terminateOwner(pid int, signal syscall.Signal) error {
	err := syscall.Kill(-pid, signal)
	if err == nil || isNoSuchProcess(err) {
		return nil
	}
	return err
}
