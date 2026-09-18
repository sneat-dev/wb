package daemon

import "strings"

// Supervisor names what owns a running daemon process's lifecycle: whichever
// process manager exec'd it and will restart it if it exits.
//
// Recording this at `daemon serve` startup, rather than inferring it later, is
// what lets a restart hand the process back to its owner instead of starting a
// detached replacement behind its back (sneat-dev/wb#617): the process that
// knows it is systemd's or launchd's child is the process being asked to
// restart, and no later reader can observe that as reliably as it can.
type Supervisor string

const (
	// SupervisorNone: nothing is recorded as having supervised this process's
	// start. A restart of a daemon in this state has nothing to hand off to,
	// so it must start its own replacement.
	SupervisorNone Supervisor = "none"
	// SupervisorSystemd: a systemd (user or system) unit started this process.
	SupervisorSystemd Supervisor = "systemd"
	// SupervisorLaunchd: a launchd agent or daemon started this process.
	SupervisorLaunchd Supervisor = "launchd"
)

// Valid reports whether kind is one of the three reportable values. It exists
// so a record read back from disk — including one written by a future build
// with a supervisor kind this build does not know — is never silently treated
// as SupervisorNone.
func (kind Supervisor) Valid() bool {
	switch kind {
	case SupervisorNone, SupervisorSystemd, SupervisorLaunchd:
		return true
	default:
		return false
	}
}

// DetectSupervisor observes the environment variables systemd and launchd are
// documented to set on a process they exec directly, through an injectable
// seam so no test needs a real systemd or launchd:
//
//   - systemd sets INVOCATION_ID (systemd.exec(5)) on every unit it starts.
//     When the unit also names SYSTEMD_EXEC_PID (systemd >= 246), that PID is
//     returned alongside so a reader can name it without shelling out.
//   - launchd sets XPC_SERVICE_NAME to the job label for a job it manages, and
//     to the literal string "0" for a process it did not launch directly (for
//     example a login shell) — "0" is therefore treated as absent, not launchd.
//
// Neither variable survives an intermediate detached-start hop (wb's own
// exec.Command launch clears neither, but does not set them either), so a
// daemon started by `wb daemon start`/`restart` rather than by a supervisor
// correctly detects SupervisorNone.
func DetectSupervisor(getenv func(string) string) (kind Supervisor, execPID string) {
	if getenv == nil {
		return SupervisorNone, ""
	}
	if strings.TrimSpace(getenv("INVOCATION_ID")) != "" {
		return SupervisorSystemd, strings.TrimSpace(getenv("SYSTEMD_EXEC_PID"))
	}
	if name := strings.TrimSpace(getenv("XPC_SERVICE_NAME")); name != "" && name != "0" {
		return SupervisorLaunchd, ""
	}
	return SupervisorNone, ""
}
