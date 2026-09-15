//go:build darwin || linux

package process

import (
	"os/exec"
	"syscall"
)

// ConfigureDetached puts a child in its own session, so it survives the process
// that started it and a later process-group signal stays scoped to that child
// and its descendants. It is the one spelling of "detached worker" WB uses,
// shared by the lifecycle-hook dispatcher and the agent run owner.
func ConfigureDetached(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
