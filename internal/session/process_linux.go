//go:build linux

package session

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func processEvidence(pid int) (ProcessEvidence, bool) {
	if pid <= 0 {
		return ProcessEvidence{}, false
	}
	executable, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
	if err != nil || strings.TrimSpace(executable) == "" {
		return ProcessEvidence{}, false
	}
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return ProcessEvidence{}, false
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return ProcessEvidence{Executable: executable, Args: parts}, true
}
