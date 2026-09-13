//go:build darwin || linux

package lifecyclehooks

import (
	"os/exec"
	"syscall"
)

func configureDetached(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
