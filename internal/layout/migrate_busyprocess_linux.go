//go:build linux

package layout

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// busyProcessCheckSupported reports whether busyProcessReason can actually
// inspect live process working directories on this OS. Linux exposes this
// through /proc; other kernels have no equivalent WB relies on here.
const busyProcessCheckSupported = true

// busyProcessReason reports, naming the PID and command, whether any live
// process's current working directory is inside one of paths (a clone or one
// of its linked worktrees) — a shell sitting in a directory Git itself has no
// record of, which neither a Git-operation-in-progress check nor a Work Log
// claim would ever see. Only a readable /proc/<pid>/cwd counts as evidence: a
// permission error reading another user's process is expected on a shared
// machine and is silently skipped, not treated as a refusal or a failure.
func busyProcessReason(paths []string) string {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return ""
	}
	cleaned := make([]string, 0, len(paths))
	for _, path := range paths {
		if path != "" {
			cleaned = append(cleaned, filepath.Clean(path))
		}
	}
	if len(cleaned) == 0 {
		return ""
	}
	for _, entry := range entries {
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil || pid <= 0 {
			continue
		}
		cwd, readErr := os.Readlink(filepath.Join("/proc", entry.Name(), "cwd"))
		if readErr != nil {
			// Permission denied (another user's process) or the process has
			// already exited between ReadDir and here; neither is evidence.
			continue
		}
		cwd = filepath.Clean(cwd)
		for _, path := range cleaned {
			if cwd != path && !strings.HasPrefix(cwd, path+string(filepath.Separator)) {
				continue
			}
			comm := "unknown"
			if raw, commErr := os.ReadFile(filepath.Join("/proc", entry.Name(), "comm")); commErr == nil {
				comm = strings.TrimSpace(string(raw))
			}
			return fmt.Sprintf("process %d (%s) has its working directory inside %s", pid, comm, path)
		}
	}
	return ""
}
