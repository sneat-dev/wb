//go:build !linux

package daemon

// ObservedParentSupervisor reports that this platform's actual-parent check is
// not implemented. On darwin the daemon is always started through launchd's
// own bootstrap/kickstart (cmd/wb's daemon_process_darwin.go), so its
// self-detected Supervisor already agrees with reality by construction; there
// is no supervised daemon path on Windows yet. `known=false` tells a caller to
// report this as "unknown" rather than as an observed absence of a supervisor.
func ObservedParentSupervisor(int) (Supervisor, bool) { return "", false }
