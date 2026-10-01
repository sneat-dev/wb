//go:build !windows

package remotessh

import (
	"os"
	"os/exec"
	"syscall"
)

// ownGroup puts command in a process group of its own and makes the end of its
// context kill that whole group.
func ownGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return killGroup(syscall.Kill, command.Process.Pid) }
}

// killGroup kills, with kill, the process group whose leader is pid. A group
// that is gone is the process being done, which exec then does not report as a
// failure of its own.
func killGroup(kill func(pid int, signal syscall.Signal) error, pid int) error {
	if err := kill(-pid, syscall.SIGKILL); err != nil {
		return os.ErrProcessDone
	}
	return nil
}
