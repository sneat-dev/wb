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
	if pid <= 0 {
		return "", false
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return "", false
	}
	return ParseCgroupSupervisor(string(data), expectedUnit)
}
