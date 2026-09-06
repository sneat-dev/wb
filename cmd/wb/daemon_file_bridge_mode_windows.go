//go:build windows

package main

import (
	"errors"
	"os"
)

func verifyBridgePathSecurity(string, os.FileInfo, os.FileMode) error {
	return errors.New("daemon file bridge is unavailable because this Windows build cannot verify owner-only ACLs")
}
