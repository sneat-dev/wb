//go:build !windows

package session

import (
	"errors"
	"syscall"
)

func processAlive(pid int) bool {
	return processAliveWith(pid, func(pid int) error { return syscall.Kill(pid, 0) })
}

func processAliveWith(pid int, signal func(int) error) bool {
	if pid <= 0 {
		return false
	}
	// Only ESRCH proves absence. Permission and unexpected failures must not
	// authorize pruning or an ownership takeover.
	return !errors.Is(signal(pid), syscall.ESRCH)
}
