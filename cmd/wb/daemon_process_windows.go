//go:build windows

package main

import (
	"context"
	"os"

	"golang.org/x/sys/windows"
)

const windowsStillActive = 259

// The MVP runs as a child process on Windows. Queue ownership has already been
// checkpointed before this termination path is used. A future per-user Windows
// service adapter replaces this with Service Control Manager draining.
func daemonProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var code uint32
	return windows.GetExitCodeProcess(handle, &code) == nil && code == windowsStillActive
}

func stopDaemonProcess(pid int) error {
	if pid <= 0 {
		return nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}

func signalDaemonContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(parent)
}
