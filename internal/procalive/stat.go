package procalive

import (
	"strconv"
	"strings"
)

// procStatNumThreadsIndex is the index of num_threads (stat field 20) among
// the fields that follow the comm field's closing ')', where index 0 is the
// state (field 3).
const procStatNumThreadsIndex = 17

// procStatFields returns the whitespace-separated fields of a Linux
// /proc/<pid>/stat record that follow the comm field. The comm field is wrapped
// in parentheses and may contain spaces and parentheses, so fields are parsed
// after the last ')'.
func procStatFields(contents string) []string {
	closeIdx := strings.LastIndexByte(contents, ')')
	if closeIdx < 0 {
		return nil
	}
	return strings.Fields(contents[closeIdx+1:])
}

// parseProcStatState extracts the single-character process state (field 3)
// from a Linux /proc/<pid>/stat record.
func parseProcStatState(contents string) (byte, bool) {
	fields := procStatFields(contents)
	if len(fields) == 0 || len(fields[0]) != 1 {
		return 0, false
	}
	return fields[0][0], true
}

// parseProcStatNumThreads extracts num_threads (field 20) from a Linux
// /proc/<pid>/stat record.
func parseProcStatNumThreads(contents string) (int, bool) {
	fields := procStatFields(contents)
	if len(fields) <= procStatNumThreadsIndex {
		return 0, false
	}
	n, err := strconv.Atoi(fields[procStatNumThreadsIndex])
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// isDeadProcStat reports whether a Linux /proc/<pid>/stat payload proves the
// whole process has exited: state zombie ('Z') or dead ('X') AND at most one
// thread. A zombie main thread whose worker threads still run (the main thread
// called pthread_exit) reports 'Z' with num_threads > 1 and is still live. Any
// record that cannot be fully parsed is not proof of exit.
func isDeadProcStat(contents []byte) bool {
	s := string(contents)
	state, ok := parseProcStatState(s)
	if !ok || (state != 'Z' && state != 'X') {
		return false
	}
	threads, ok := parseProcStatNumThreads(s)
	return ok && threads <= 1
}
