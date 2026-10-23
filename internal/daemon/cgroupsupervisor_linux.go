//go:build linux

package daemon

import (
	"os"
	"path/filepath"
	"strconv"
)

// ObservedCgroupSupervisor independently observes whether pid is currently
// running inside expectedUnit's own cgroup, so `wb daemon status` can compare
// it against what that process recorded about its own start
// (State.Supervisor). See ParseCgroupSupervisor for why cgroup membership,
// not parentage, is what is checked, and why expectedUnit — not a bare
// ".service" substring match — is required.
func ObservedCgroupSupervisor(pid int, expectedUnit string) (Supervisor, bool) {
	contents, ok := readCgroupFromProcRoot("/proc", pid)
	if !ok {
		return "", false
	}
	return ParseCgroupSupervisor(contents, expectedUnit)
}

// ObservedCgroupUnit independently observes the raw systemd unit name (if
// any) pid is currently running inside, regardless of what unit a caller
// expects. It is what lets a daemon record its OWN actual unit at `daemon
// serve` startup (sneat-dev/wb#622 review round 3, item M3), rather than a
// later, separate `wb daemon status` invocation guessing from its own
// environment.
func ObservedCgroupUnit(pid int) (string, bool) {
	contents, ok := readCgroupFromProcRoot("/proc", pid)
	if !ok {
		return "", false
	}
	return ParseCgroupUnit(contents)
}

// readCgroupFromProcRoot shares the process validation and cgroup-file read
// used by both observations. Parsing decides whether readable data is known.
func readCgroupFromProcRoot(procRoot string, pid int) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return "", false
	}
	return string(data), true
}
