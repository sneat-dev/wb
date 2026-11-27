//go:build !windows

package diskprune

import (
	"fmt"
	"os"
	"syscall"
)

// ownedBy reports whether the invoking user owns the entry.
func ownedBy(info os.FileInfo, uid int) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == uid
}

// ownedByUserPath reports whether the invoking user owns path.
func ownedByUserPath(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && ownedBy(info, os.Getuid())
}

// traversable reports whether the invoking user may pass through path: a
// directory it cannot list but can enter may still hold its checkout.
func traversable(path string) bool { return syscall.Access(path, 0x1) == nil }

// dirKey identifies a directory by device and inode, so a symlink loop is
// walked once.
func dirKey(path string) (string, bool) {
	var stat syscall.Stat_t
	if err := syscall.Stat(path, &stat); err != nil {
		return "", false
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), true
}
