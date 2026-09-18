//go:build linux

package daemon

import (
	"os"
	"path/filepath"
	"strconv"
)

// ObservedCgroupSupervisor independently observes whether pid is currently
// running inside a systemd service unit's cgroup, so `wb daemon status` can
// compare it against what that process recorded about its own start
// (State.Supervisor). See ParseCgroupSupervisor for why cgroup membership,
// not parentage, is what is checked.
func ObservedCgroupSupervisor(pid int) (Supervisor, bool) {
	if pid <= 0 {
		return "", false
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return "", false
	}
	return ParseCgroupSupervisor(string(data))
}
