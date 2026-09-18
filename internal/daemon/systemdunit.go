package daemon

import (
	"strconv"
	"strings"
)

// SystemdUnitState is the subset of `systemctl show` fields relevant to
// telling a genuinely-owned, healthy systemd unit from one that exists but is
// failing to keep the daemon up. This is the sneat-dev/wb#617 detector's
// central evidence: a live host was confirmed running an orphaned,
// unsupervised `daemon serve` (PPID 1, a session scope — not the unit's own
// cgroup, so ObservedCgroupSupervisor cannot see it) answering the port,
// while its own systemd unit sat ActiveState=failed with NRestarts=4468 —
// systemd itself was crash-looping trying and failing to keep the real
// daemon up, and the orphan was serving the port instead
// (sneat-dev/wb#622 review item 2).
type SystemdUnitState struct {
	ActiveState string
	Result      string
	NRestarts   int
}

// ParseSystemctlShow parses `systemctl show -p ActiveState,Result,NRestarts
// <unit>` output (newline-separated Key=Value pairs) into a SystemdUnitState.
// found=false for empty input — systemctl being entirely absent, or its user
// manager unreachable, both look like this from the caller's side, and
// neither is evidence that the unit is (or is not) healthy.
func ParseSystemctlShow(output string) (SystemdUnitState, bool) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return SystemdUnitState{}, false
	}
	var state SystemdUnitState
	for _, line := range strings.Split(trimmed, "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		switch strings.TrimSpace(key) {
		case "ActiveState":
			state.ActiveState = strings.TrimSpace(value)
		case "Result":
			state.Result = strings.TrimSpace(value)
		case "NRestarts":
			if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
				state.NRestarts = n
			}
		}
	}
	return state, true
}

// SystemdUnitLooksOrphaned reports whether a queried unit's state is evidence
// that it still exists and is fighting an unsupervised process for
// ownership of the daemon it is meant to run: failed outright, or stuck
// restarting after at least one failure. A unit that is simply "inactive"
// (never started, or cleanly stopped) or "active" (running normally — the
// unit itself, not necessarily the process this build is inspecting) is not
// evidence of anything wrong.
func SystemdUnitLooksOrphaned(state SystemdUnitState) bool {
	switch state.ActiveState {
	case "failed":
		return true
	case "activating":
		return state.NRestarts > 0
	default:
		return false
	}
}
