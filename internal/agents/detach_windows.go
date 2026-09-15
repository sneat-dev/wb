//go:build windows

package agents

import (
	"os"
	"os/exec"
	"syscall"
)

// configureDetached is a no-op on Windows, where WB has no process-group
// primitive; the owner still runs as an independent process.
func configureDetached(_ *exec.Cmd) {}

// terminateOwner terminates a detached owner's process on Windows, where there
// is no process group to scope the signal to.
func terminateOwner(pid int, _ syscall.Signal) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	if err := process.Kill(); err != nil && !os.IsProcessDone(err) {
		return err
	}
	return nil
}
