//go:build !windows

package agents

import (
	"os"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

// lockFile is the non-blocking exclusive claim. It is a var so a test can pin
// the non-busy error branch deterministically.
var lockFile = func(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func unlockFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}

func isLockBusy(err error) bool {
	return err == unix.EWOULDBLOCK
}
