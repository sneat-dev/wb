//go:build !windows

package diskprune

import (
	"os"
	"syscall"
)

// ownedBy reports whether the invoking user owns the entry.
func ownedBy(info os.FileInfo, uid int) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == uid
}
