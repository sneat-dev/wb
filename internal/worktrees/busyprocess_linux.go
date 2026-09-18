//go:build linux

package worktrees

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// selfAndAncestorPIDs returns the current process's own pid together with
// every ancestor pid up to (but not including) pid 1, by following /proc's
// stat ppid field. It is used to recognise when a busy-process refusal is
// caused by the very process running this command's own working directory
// (or its parent shell's) rather than by some unrelated agent or user
// session, since the remedy for those two cases is completely different: an
// unrelated process must finish or be asked to leave, but this process (or
// its shell) just needs to `cd` out of the checkout before re-running the
// same command.
func selfAndAncestorPIDs() map[int]bool {
	pids := map[int]bool{}
	pid := os.Getpid()
	for i := 0; i < 64 && pid > 1 && !pids[pid]; i++ {
		pids[pid] = true
		ppid, ok := readPPID(pid)
		if !ok {
			break
		}
		pid = ppid
	}
	return pids
}

// readPPID reads pid's parent pid from /proc/<pid>/stat. The comm field
// (2nd, in parentheses) may itself contain spaces or parentheses, so the
// remaining fields are parsed starting just after the stat line's LAST ')',
// where ppid is always the first of them regardless of comm's contents.
func readPPID(pid int) (int, bool) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, false
	}
	content := string(data)
	closing := strings.LastIndexByte(content, ')')
	if closing < 0 || closing+2 >= len(content) {
		return 0, false
	}
	fields := strings.Fields(content[closing+2:])
	if len(fields) < 2 {
		return 0, false
	}
	ppid, convErr := strconv.Atoi(fields[1])
	if convErr != nil {
		return 0, false
	}
	return ppid, true
}

// BusyProcessCheckSupported reports whether BusyProcessReason can actually
// inspect live process working directories on this OS. Linux exposes this
// through /proc; other kernels have no equivalent WB relies on here.
const BusyProcessCheckSupported = true

// BusyProcessReason reports, naming the PID and command, whether any live
// process's current working directory is inside one of paths (a clone,
// checkout, or one of its linked worktrees) — a shell sitting in a directory
// Git itself has no record of, which neither a Git-operation-in-progress
// check nor a Work Log claim would ever see. Only a readable
// /proc/<pid>/cwd counts as evidence: a permission error reading another
// user's process is expected on a shared machine and is silently skipped,
// not treated as a refusal or a failure.
func BusyProcessReason(paths []string) string {
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
	selfAndAncestors := selfAndAncestorPIDs()
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
			if selfAndAncestors[pid] {
				return fmt.Sprintf("this command's own process tree (pid %d, %s) has its working directory inside %s; cd out of the checkout and re-run", pid, comm, path)
			}
			return fmt.Sprintf("process %d (%s) has its working directory inside %s", pid, comm, path)
		}
	}
	return ""
}
