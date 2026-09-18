//go:build !linux

package daemon

// ObservedCgroupSupervisor reports that this platform's cgroup check is not
// implemented. On darwin the daemon is always started through launchd's own
// bootstrap/kickstart (cmd/wb's daemon_process_darwin.go), so its
// self-detected Supervisor already agrees with reality by construction; there
// is no cgroup concept, or a supervised daemon path, on Windows.
// `known=false` tells a caller to report this as "unknown" rather than as an
// observed absence of a supervisor.
func ObservedCgroupSupervisor(int, string) (Supervisor, bool) { return "", false }
