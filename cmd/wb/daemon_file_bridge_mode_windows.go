//go:build windows

package main

import (
	"os"

	"github.com/strongo/cli-helpers/daemonlifecycle"
)

func verifyBridgePathSecurity(path string, _ os.FileInfo, _ os.FileMode, private bool) error {
	if !private {
		return nil
	}
	return daemonlifecycle.ValidateOwnerOnly(path)
}
