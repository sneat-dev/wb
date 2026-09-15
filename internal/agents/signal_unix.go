//go:build darwin || linux

package agents

import "syscall"

// signalProcessGroup signals the process group whose leader is pid. Negative PID
// scopes the signal to exactly that child and the descendants that inherited
// its group, never to unrelated system processes.
func signalProcessGroup(pid int, signal syscall.Signal) error {
	return syscall.Kill(-pid, signal)
}
