//go:build windows

package session

import (
	"errors"

	"golang.org/x/sys/windows"
)

const windowsStillActive = 259

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		// Only an invalid PID proves absence. Other failures, including access
		// denial, leave a possibly live owner protected.
		return !errors.Is(err, windows.ERROR_INVALID_PARAMETER)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return true
	}
	return exitCode == windowsStillActive
}
