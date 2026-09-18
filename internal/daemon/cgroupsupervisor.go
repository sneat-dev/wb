package daemon

import (
	"regexp"
	"strings"
)

// systemdUserManagerUnit matches the per-user systemd MANAGER's own unit
// (user@1000.service), which wraps every process in a login session —
// including ones with no matching systemd unit at all. It is never "ours",
// regardless of what unit name is configured or defaulted.
var systemdUserManagerUnit = regexp.MustCompile(`^user@[0-9]+\.service$`)

// ParseCgroupSupervisor is the pure half of ObservedCgroupSupervisor: it
// classifies the contents of a Linux `/proc/<pid>/cgroup` file against a
// specific expected systemd unit name (see cmd/wb's daemonSystemdUnitName).
//
// Only the unified (cgroup v2) "0::" hierarchy line is considered — a
// v1/hybrid host's legacy per-controller lines are not authoritative for
// which service actually owns the process. The unit is identified by the
// LAST path component of that line: a system unit's cgroup path is
// `.../system.slice/wb-daemon.service`, and a user unit's is
// `.../user@1000.service/app.slice/wb-daemon.service`.
//
// A plain substring match on ".service" anywhere in the path — this
// function's first version — flagged ANY process running under the user's
// systemd session, including one under a completely unrelated foreign unit,
// as "supervised" (sneat-dev/wb#622 review item 4). This version instead:
//
//   - excludes the per-user manager's own unit (user@<uid>.service), which
//     wraps every process in the session and confirms nothing about any
//     specific unit;
//   - reports SupervisorNone for a component that is not a service at all
//     (an app.slice, a *.scope — for example a session scope, which is what
//     a process merely reparented to PID 1 after its original parent exited
//     runs inside, never a `.service`: this is exactly the false positive a
//     parent-PID check could not avoid, sneat-dev/wb#622 review item 7 from
//     the previous round);
//   - reports SupervisorNone for a service unit that IS a service but is not
//     the expected one (a foreign unit such as openclaw-gateway.service) —
//     it is not evidence that OUR unit is managing this process, so it is
//     treated the same as no service membership at all.
func ParseCgroupSupervisor(contents string, expectedUnit string) (Supervisor, bool) {
	line, ok := unifiedCgroupLine(contents)
	if !ok {
		return "", false
	}
	component := lastPathComponent(line)
	if component == "" || !strings.HasSuffix(component, ".service") {
		return SupervisorNone, true
	}
	if systemdUserManagerUnit.MatchString(component) {
		return SupervisorNone, true
	}
	if component != strings.TrimSpace(expectedUnit) {
		return SupervisorNone, true
	}
	return SupervisorSystemd, true
}

// unifiedCgroupLine returns the "0::" (cgroup v2 unified hierarchy) line's
// path, without the "0::" prefix. found=false when the input is empty or
// carries no such line at all.
func unifiedCgroupLine(contents string) (string, bool) {
	trimmed := strings.TrimSpace(contents)
	if trimmed == "" {
		return "", false
	}
	for _, line := range strings.Split(trimmed, "\n") {
		if rest, found := strings.CutPrefix(line, "0::"); found {
			return rest, true
		}
	}
	return "", false
}

// lastPathComponent returns the last "/"-separated component of path, or ""
// for an empty or root-only path.
func lastPathComponent(path string) string {
	trimmed := strings.TrimRight(path, "/")
	if trimmed == "" {
		return ""
	}
	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
		return trimmed[idx+1:]
	}
	return trimmed
}
