package daemon

import (
	"strconv"
	"strings"
)

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
// seam so no test needs a real systemd or launchd. pid and ppid are this
// process's own — normally os.Getpid() and os.Getppid(), overridden by tests —
// because a bare environment variable is not enough evidence on its own: both
// variables are observed to survive into children that inherit their parent's
// environment without being started by the supervisor at all.
//
//   - systemd sets INVOCATION_ID (systemd.exec(5)) on every unit it starts, and
//     since systemd 246 also sets SYSTEMD_EXEC_PID to the exact PID it exec'd.
//     Confirmed on a live host: a shell or an agent process started *inside* a
//     systemd-supervised session inherits INVOCATION_ID from its parent without
//     SYSTEMD_EXEC_PID ever being set for it. Detection therefore requires
//     SYSTEMD_EXEC_PID to be present *and* equal to this process's own pid;
//     INVOCATION_ID alone, or a non-matching SYSTEMD_EXEC_PID, reports None.
//   - launchd sets XPC_SERVICE_NAME to the job label for a job it manages, and
//     to the literal string "0" for a process it did not launch directly (for
//     example a login shell) — "0" is therefore treated as absent, not launchd.
//     launchd is additionally required to be this process's *direct* parent
//     (ppid == 1, which is launchd on every macOS version this targets) as the
//     equivalent inheritance guard: a child of a launchd-managed process can
//     otherwise inherit XPC_SERVICE_NAME from its parent's environment without
//     having been launched by launchd itself.
//
// label is the launchd job label from XPC_SERVICE_NAME (empty for systemd and
// for None): callers use it to tell wb's own self-managed launchd job from a
// foreign one, which needs different handoff handling (see cmd/wb's
// daemonLaunchdLabel).
func DetectSupervisor(getenv func(string) string, pid, ppid int) (kind Supervisor, execPID string, label string) {
	if getenv == nil {
		return SupervisorNone, "", ""
	}
	if strings.TrimSpace(getenv("INVOCATION_ID")) != "" {
		if execPIDText := strings.TrimSpace(getenv("SYSTEMD_EXEC_PID")); execPIDText != "" {
			if parsed, err := strconv.Atoi(execPIDText); err == nil && parsed > 0 && parsed == pid {
				return SupervisorSystemd, execPIDText, ""
			}
		}
		return SupervisorNone, "", ""
	}
	if name := strings.TrimSpace(getenv("XPC_SERVICE_NAME")); name != "" && name != "0" {
		if ppid == 1 {
			return SupervisorLaunchd, "", name
		}
		return SupervisorNone, "", ""
	}
	return SupervisorNone, "", ""
}
