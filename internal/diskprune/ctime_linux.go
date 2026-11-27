//go:build linux

package diskprune

import (
	"os"
	"syscall"
	"time"
)

// changeTime is the later of a file's modification and status-change times:
// restoring an archive preserves the first but not the second.
func changeTime(info os.FileInfo) time.Time {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return latest(info.ModTime(), time.Unix(stat.Ctim.Unix()))
	}
	return info.ModTime()
}
