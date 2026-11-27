//go:build !windows

package diskprune

import (
	"os"
	"syscall"
)

// diskBytes is what unlinking the file's last link gives back: its blocks.
func diskBytes(info os.FileInfo) int64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return int64(stat.Blocks) * 512
	}
	return info.Size()
}

// unlinkBytes is what unlinking this name frees: the blocks when it is the
// last link, nothing when another link keeps the file.
func unlinkBytes(info os.FileInfo) int64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink > 1 {
		return 0
	}
	return diskBytes(info)
}

// linkCount is the file's hard-link count.
func linkCount(info os.FileInfo) uint64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(stat.Nlink)
	}
	return 1
}
