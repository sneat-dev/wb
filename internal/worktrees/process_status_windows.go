//go:build windows

package worktrees

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
func processIsDead(pid int) bool { return processStatus(pid) != nil }
