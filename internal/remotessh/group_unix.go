//go:build !windows

package remotessh

import (
	"os"
	"os/exec"
	"syscall"
)

// ownGroup gives command a session of its own, of which it is the process group
// leader, and makes the end of its context kill that whole group. A new session
// has no controlling terminal: when the caller runs in a terminal, neither ssh
// nor a helper it starts (the inner ssh of a ProxyJump, an askpass program) can
// open /dev/tty and prompt there.
func ownGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
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

// inspectPath reads the mode and the owner of path, following symbolic links.
func inspectPath(path string) (pathFacts, error) {
	info, err := os.Stat(path)
	if err != nil {
		return pathFacts{}, err
	}
	// On these platforms Sys is always a *syscall.Stat_t.
	return pathFacts{mode: info.Mode(), owner: int(info.Sys().(*syscall.Stat_t).Uid)}, nil
}
