//go:build !windows

package sessionlaunch

import "syscall"

func processStatus(pid int) error { return syscall.Kill(pid, 0) }
