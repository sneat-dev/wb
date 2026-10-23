//go:build linux

package session

import (
	"os"
	"path/filepath"
	"strconv"
)

// parentPID reads a process's parent from /proc.
func parentPID(pid int) (int, bool) {
	content, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, false
	}
	return parentPIDFromStat(content)
}
