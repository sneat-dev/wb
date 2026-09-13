//go:build windows

package main

import (
	"os"

	"github.com/strongo/cli-helpers/daemonlifecycle"
)

func validateDaemonLifecycleFilePermissions(path string, _ uint32) error {
	return daemonlifecycle.ValidateOwnerOnly(path)
}

func tryLockDaemonFile(file *os.File) (bool, error) {
	return daemonlifecycle.TryLock(file)
}

func unlockDaemonFile(file *os.File) error {
	return daemonlifecycle.Unlock(file)
}
