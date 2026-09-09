//go:build !windows

package runqueue

import (
	"errors"
	"syscall"
)

// processAlive reports whether pid names a process that still exists. A bare
// os.FindProcess is not a liveness check on Unix — it always succeeds,
// process or not — so this sends the null signal instead: EPERM means the
// process exists but is owned by someone else (still alive, from our
// perspective), any other error means it is gone.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
