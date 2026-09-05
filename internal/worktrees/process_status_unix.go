//go:build !windows

package worktrees

import (
	"errors"
	"syscall"
)

func processStatus(pid int) error { return syscall.Kill(pid, 0) }
func processIsDead(pid int) bool  { return errors.Is(processStatus(pid), syscall.ESRCH) }
