//go:build !windows

package agents

import (
	"os"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

// fileLockingSupported reports whether an exclusive advisory lock exists and is
// inherited by a child process on this platform.
const fileLockingSupported = true

func lockFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func unlockFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}

func isLockBusy(err error) bool {
	return err == unix.EWOULDBLOCK
}
