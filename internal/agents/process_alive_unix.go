//go:build !windows

package agents

import (
	"errors"
	"syscall"
)

// processAlive reports whether a recorded process identity still exists. A PID
// is only ever a liveness coordinate — never an identity — and a permission
// error still proves the process exists.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
