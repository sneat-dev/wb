//go:build linux

package worktrees

import (
	"os"

	"golang.org/x/sys/unix"
)

func directoryDescriptorWasRemovedAt(directory *os.File, _ string, previousLinks uint64) bool {
	if directory == nil {
		return false
	}
	var removed unix.Stat_t
	return unix.Fstat(int(directory.Fd()), &removed) == nil && uint64(removed.Nlink) < previousLinks
}
