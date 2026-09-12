//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

func validateDaemonLifecycleFilePermissions(mode uint32) error {
	if mode&0o077 != 0 {
		return fmt.Errorf("file mode %#o is not owner-only", mode&0o777)
	}
	return nil
}

func tryLockDaemonFile(file *os.File) (bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	return err == nil, err
}

func unlockDaemonFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
