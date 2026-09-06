//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

func verifyBridgePathSecurity(path string, info os.FileInfo, want os.FileMode) error {
	if info.Mode().Perm() != want {
		return fmt.Errorf("daemon file bridge path %s has mode %o; require %o", path, info.Mode().Perm(), want)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("daemon file bridge path %s is not owned by the current user", path)
	}
	return nil
}
