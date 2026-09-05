//go:build windows

package orchestrate

import (
	"os"
	"syscall"
)

func operationProcessMayBeLive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	return process.Signal(syscall.Signal(0)) == nil
}
