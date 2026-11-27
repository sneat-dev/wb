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
