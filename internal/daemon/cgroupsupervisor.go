package daemon

import "strings"

// ParseCgroupSupervisor is the pure half of ObservedCgroupSupervisor: it
// classifies the contents of a Linux `/proc/<pid>/cgroup` file.
//
// A process systemd started — as a system unit, or inside a `--user` unit —
// runs inside a cgroup whose path contains a `.service` component (for
// example `.../user@1000.service/app.slice/wb-daemon.service`, or
// `/system.slice/wb-daemon.service` for a system unit). A process that is not
// running under any systemd unit — including one merely reparented to PID 1
// after its original parent exited — runs inside a session scope
// (`session-N.scope`) or some other non-`.service` cgroup instead.
//
// This is deliberately not a parent-PID check: PID 1 *is* systemd on every
// host this matters for, so an orphaned, wholly unsupervised process that
// gets reparented to it would make a parent-PID check report systemd for
// every such process (sneat-dev/wb#622 review item 7). Cgroup membership does
// not change on reparenting, so it does not share that false-positive.
func ParseCgroupSupervisor(contents string) (Supervisor, bool) {
	trimmed := strings.TrimSpace(contents)
	if trimmed == "" {
		return "", false
	}
	for _, line := range strings.Split(trimmed, "\n") {
		if strings.Contains(line, ".service") {
			return SupervisorSystemd, true
		}
	}
	return SupervisorNone, true
}
