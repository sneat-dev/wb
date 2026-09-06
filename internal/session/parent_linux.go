//go:build linux

package session

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// parentPID reads a process's parent from /proc.
func parentPID(pid int) (int, bool) {
	content, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, false
	}
	// The comm field is parenthesised and may contain spaces, so fields are
	// counted from after the closing bracket rather than from the start.
	closing := strings.LastIndex(string(content), ")")
	if closing < 0 {
		return 0, false
	}
	fields := strings.Fields(string(content)[closing+1:])
	if len(fields) < 2 {
		return 0, false
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil || parent <= 0 {
		return 0, false
	}
	return parent, true
}

// processName reads a process's executable name from /proc. An unreadable or
// vanished process is simply unnamed: the caller treats that the same way it
// treats a name it does not recognise.
func processName(pid int) string {
	content, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "comm"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(content))
}
