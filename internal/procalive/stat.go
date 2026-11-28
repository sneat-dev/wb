package procalive

import "strings"

// parseProcStatState extracts the single-character process state (field 3)
// from a Linux /proc/<pid>/stat record. The comm field is wrapped in
// parentheses and may contain spaces and parentheses, so fields are parsed
// after the last ')'.
func parseProcStatState(contents string) (byte, bool) {
	closeIdx := strings.LastIndexByte(contents, ')')
	if closeIdx < 0 || closeIdx+1 >= len(contents) {
		return 0, false
	}
	fields := strings.Fields(contents[closeIdx+1:])
	if len(fields) == 0 || len(fields[0]) != 1 {
		return 0, false
	}
	return fields[0][0], true
}

// isDeadProcStat reports whether a Linux /proc/<pid>/stat payload describes a
// zombie ('Z') or dead ('X') process.
func isDeadProcStat(contents []byte) bool {
	state, ok := parseProcStatState(string(contents))
	return ok && (state == 'Z' || state == 'X')
}
