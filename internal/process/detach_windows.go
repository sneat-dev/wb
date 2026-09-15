//go:build windows

package process

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// ConfigureDetached starts a child in its own process group with no console and
// no window, so it survives the process that started it.
func ConfigureDetached(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
		HideWindow:    true,
	}
}
