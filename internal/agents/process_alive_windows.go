//go:build windows

package agents

import (
	"os"
	"syscall"
)

// processAlive reports whether a recorded process identity still exists.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}
