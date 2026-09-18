//go:build linux

package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ObservedParentSupervisor independently observes what actually parents a
// running process, so `wb daemon status` can compare it against what that
// process recorded about its own start (State.Supervisor). The two are
// expected to agree; a live daemon whose recorded supervisor is
// SupervisorNone while its actual parent is a systemd instance is evidence of
// the shape of sneat-dev/wb#617 — something restarted it outside of whatever
// it detected at its own startup.
//
// This is deliberately narrower than asking systemd whether a *specific* unit
// for this home exists and is failing, which sneat-dev/wb#617 also asks for:
// naming and querying that unit portably (which unit, on which bus, under
// which user) is its own project. This answers a smaller, always-available
// question instead — whose process is actually the parent, from /proc alone —
// and that limit is intentional and documented rather than papered over: see
// the Feature and the PR that introduced this function.
func ObservedParentSupervisor(pid int) (Supervisor, bool) {
	if pid <= 0 {
		return "", false
	}
	status, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	if err != nil {
		return "", false
	}
	ppid, ok := ParseProcStatusPPid(string(status))
	if !ok {
		return "", false
	}
	comm, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(ppid), "comm"))
	if err != nil {
		return "", false
	}
	if strings.TrimSpace(string(comm)) == "systemd" {
		return SupervisorSystemd, true
	}
	return SupervisorNone, true
}
