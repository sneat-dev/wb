//go:build darwin

package worktrees

import (
	"bytes"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/unix"
)

func directoryDescriptorWasRemovedAt(directory *os.File, expectedPath string, _ uint64) bool {
	if directory == nil {
		return false
	}
	var path [4096]byte
	_, _, errno := unix.Syscall(unix.SYS_FCNTL, directory.Fd(), uintptr(unix.F_GETPATH), uintptr(unsafe.Pointer(&path[0])))
	if errno != 0 {
		return false
	}
	end := bytes.IndexByte(path[:], 0)
	if end < 0 || filepath.Clean(string(path[:end])) != filepath.Clean(expectedPath) {
		return false
	}
	_, err := os.Lstat(expectedPath)
	return os.IsNotExist(err)
}
