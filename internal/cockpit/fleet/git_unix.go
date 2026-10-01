//go:build !windows

package fleet

import (
	"os/exec"
	"syscall"
)

// killWithDescendants puts command in its own process group and makes
// cancelling it kill the whole group, so a descendant cannot outlive it.
func killWithDescendants(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
}
