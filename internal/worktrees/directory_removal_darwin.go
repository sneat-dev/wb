//go:build darwin

package worktrees

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

func directoryDescriptorWasRemovedAt(directory *os.File, expectedPath string, _ uint64) bool {
	if directory == nil {
		return false
	}
	var path [4096]byte
	var pinner runtime.Pinner
	pinner.Pin(&path[0])
	defer pinner.Unpin()
	_, err := unix.FcntlInt(directory.Fd(), unix.F_GETPATH, int(uintptr(unsafe.Pointer(&path[0]))))
	runtime.KeepAlive(&path)
	if err != nil {
		return false
	}
	end := bytes.IndexByte(path[:], 0)
	if end < 0 || filepath.Clean(string(path[:end])) != filepath.Clean(expectedPath) {
		return false
	}
	_, err = os.Lstat(expectedPath)
	return os.IsNotExist(err)
}
