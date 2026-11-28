//go:build !windows

package diskprune

import (
	"os"
	"syscall"
)

// ownedBy reports whether uid owns the entry.
func ownedBy(info os.FileInfo, uid int) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == uid
}

// deviceOf is the filesystem an entry lives on; a directory on another one
// is a mount.
func deviceOf(info os.FileInfo) uint64 {
	// On Unix every FileInfo from os.Lstat or DirEntry.Info carries one.
	return uint64(info.Sys().(*syscall.Stat_t).Dev)
}

// chmodDirNoFollow makes a directory writable through a descriptor opened
// without following symlinks, so a symlink swapped in is never followed.
func chmodDirNoFollow(path string) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return
	}
	_ = syscall.Fchmod(fd, 0o700)
	_ = syscall.Close(fd)
}
