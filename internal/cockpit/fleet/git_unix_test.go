//go:build !windows

package fleet

import "syscall"

// processAlive reports whether a process with pid still exists.
func processAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }
