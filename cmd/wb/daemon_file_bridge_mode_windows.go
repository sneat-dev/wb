//go:build windows

package main

import (
	"os"

	"github.com/strongo/cli-helpers/daemonlifecycle"
)

func verifyBridgePathSecurity(path string, _ os.FileInfo, _ os.FileMode) error {
	return daemonlifecycle.ValidateOwnerOnly(path)
}
