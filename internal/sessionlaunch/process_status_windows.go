//go:build windows

package sessionlaunch

import (
	"os"
	"syscall"
)

func processStatus(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(syscall.Signal(0))
}
