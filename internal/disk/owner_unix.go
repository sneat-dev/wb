//go:build !windows

package disk

import (
	"os"
	"syscall"
)

// ownedByUser reports whether the invoking user owns path.
func ownedByUser(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}
