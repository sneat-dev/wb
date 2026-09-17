//go:build windows

package agents

import (
	"os"
	"syscall"
)

// signalProcessGroup terminates a worker on Windows, where there is no process
// group to scope a signal to.
func signalProcessGroup(pid int, _ syscall.Signal) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	return process.Kill()
}
