package session

import (
	"path/filepath"
	"strconv"
	"strings"
)

// parentPIDFromStat keeps the parenthesised proc comm field separate from numeric columns.
func parentPIDFromStat(content []byte) (int, bool) {
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

// processEvidenceFromProc keeps each proc observation local to one lookup.
func processEvidenceFromProc(pid int, readLink func(string) (string, error), readFile func(string) ([]byte, error)) (ProcessEvidence, bool) {
	if pid <= 0 {
		return ProcessEvidence{}, false
	}
	executable, err := readLink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
	if err != nil || strings.TrimSpace(executable) == "" {
		return ProcessEvidence{}, false
	}
	raw, err := readFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return ProcessEvidence{}, false
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return ProcessEvidence{Executable: executable, Args: parts}, true
}
