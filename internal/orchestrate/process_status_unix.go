//go:build !windows

package orchestrate

import (
	"errors"
	"syscall"
)

func operationProcessMayBeLive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM) || !errors.Is(err, syscall.ESRCH)
}
