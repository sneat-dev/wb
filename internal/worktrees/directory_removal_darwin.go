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
	// golang.org/x/sys/unix only wraps fcntl for an int argument (FcntlInt);
	// F_GETPATH takes an output buffer pointer, which has no wrapped
	// equivalent there, so the raw syscall is the only way to make this call.
	//nolint:staticcheck // SA1019: no libSystem wrapper exists for a pointer-arg fcntl(F_GETPATH) in x/sys/unix
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
