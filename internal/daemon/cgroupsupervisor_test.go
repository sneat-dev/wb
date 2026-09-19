package daemon

import "testing"

const testWBUnit = "wb-daemon.service"

func TestParseCgroupSupervisorServiceUnit(t *testing.T) {
	contents := "0::/user.slice/user-1000.slice/user@1000.service/app.slice/wb-daemon.service\n"
	kind, known := ParseCgroupSupervisor(contents, testWBUnit)
	if !known || kind != SupervisorSystemd {
		t.Fatalf("cgroup supervisor = %s, %t; want systemd, true", kind, known)
	}
}

func TestParseCgroupSupervisorSystemUnit(t *testing.T) {
	contents := "0::/system.slice/wb-daemon.service\n"
	kind, known := ParseCgroupSupervisor(contents, testWBUnit)
	if !known || kind != SupervisorSystemd {
		t.Fatalf("cgroup supervisor = %s, %t; want systemd, true", kind, known)
	}
}

// A process merely reparented to PID 1 (systemd, on every host this matters
// for) after its original parent exited is NOT itself a systemd unit: its own
// cgroup is a login/session scope, never a `.service`. This is exactly the
// false positive a parent-PID check could not avoid (sneat-dev/wb#622 review
// item 7).
func TestParseCgroupSupervisorSessionScopeIsNotSystemd(t *testing.T) {
	contents := "0::/user.slice/user-1000.slice/session-3.scope\n"
	kind, known := ParseCgroupSupervisor(contents, testWBUnit)
	if !known || kind != SupervisorNone {
		t.Fatalf("cgroup supervisor = %s, %t; want none, true", kind, known)
	}
}

func TestParseCgroupSupervisorEmptyIsUnknown(t *testing.T) {
	if _, known := ParseCgroupSupervisor("", testWBUnit); known {
		t.Fatal("empty cgroup contents must be unknown, not a supervisor kind")
	}
	if _, known := ParseCgroupSupervisor("   \n  ", testWBUnit); known {
		t.Fatal("whitespace-only cgroup contents must be unknown")
	}
}

// A component under an app.slice that is a *.scope, not a *.service, is not
// a systemd unit at all — this is the shape a plain, unmanaged application
// process (or a user session's own transient scope) runs inside
// (sneat-dev/wb#622 review item 4).
func TestParseCgroupSupervisorAppSliceScopeIsNotAService(t *testing.T) {
	contents := "0::/user.slice/user-1000.slice/user@1000.service/app.slice/app-glib-12345.scope\n"
	kind, known := ParseCgroupSupervisor(contents, testWBUnit)
	if !known || kind != SupervisorNone {
		t.Fatalf("cgroup supervisor = %s, %t; want none, true", kind, known)
	}
}

func TestParseCgroupSupervisorInitScopeIsNotAService(t *testing.T) {
	contents := "0::/init.scope\n"
	kind, known := ParseCgroupSupervisor(contents, testWBUnit)
	if !known || kind != SupervisorNone {
		t.Fatalf("cgroup supervisor = %s, %t; want none, true", kind, known)
	}
}

// The per-user systemd MANAGER's own unit wraps every process in the login
// session, including ones with no matching systemd unit of their own at all;
// it must never itself be reported as "supervised".
func TestParseCgroupSupervisorUserManagerUnitIsExcluded(t *testing.T) {
	contents := "0::/user.slice/user-1000.slice/user@1000.service\n"
	kind, known := ParseCgroupSupervisor(contents, testWBUnit)
	if !known || kind != SupervisorNone {
		t.Fatalf("cgroup supervisor = %s, %t; want none, true", kind, known)
	}
	// A configured/default unit literally named like a user manager unit
	// (never a real shape, but the exclusion must not depend on expectedUnit)
	// still excludes it.
	kind, known = ParseCgroupSupervisor(contents, "user@1000.service")
	if !known || kind != SupervisorNone {
		t.Fatalf("cgroup supervisor = %s, %t; want none, true even when configured", kind, known)
	}
}

// A foreign unit — one wb did not install and does not expect — must not be
// flagged as "ours" merely because it is SOME systemd service
// (sneat-dev/wb#622 review item 4's named case: openclaw-gateway.service).
func TestParseCgroupSupervisorForeignUnitIsNotOurs(t *testing.T) {
	contents := "0::/user.slice/user-1000.slice/user@1000.service/app.slice/openclaw-gateway.service\n"
	kind, known := ParseCgroupSupervisor(contents, testWBUnit)
	if !known || kind != SupervisorNone {
		t.Fatalf("cgroup supervisor = %s, %t; want none, true (a foreign unit is not ours)", kind, known)
	}
}

// The very same foreign-looking unit IS reported as systemd when it is
// actually the configured or default unit name — "foreign" is relative to
// what this build expects, not a fixed denylist.
func TestParseCgroupSupervisorMatchesWhenItIsTheConfiguredUnit(t *testing.T) {
	contents := "0::/user.slice/user-1000.slice/user@1000.service/app.slice/openclaw-gateway.service\n"
	kind, known := ParseCgroupSupervisor(contents, "openclaw-gateway.service")
	if !known || kind != SupervisorSystemd {
		t.Fatalf("cgroup supervisor = %s, %t; want systemd, true when it is the configured unit", kind, known)
	}
}

// A legacy (cgroup v1 / hybrid) per-controller line is not authoritative: only
// the unified "0::" line is read.
func TestParseCgroupSupervisorIgnoresLegacyHierarchyLines(t *testing.T) {
	contents := "1:name=systemd:/user.slice/user-1000.slice/user@1000.service/app.slice/wb-daemon.service\n0::/user.slice/user-1000.slice/session-3.scope\n"
	kind, known := ParseCgroupSupervisor(contents, testWBUnit)
	if !known || kind != SupervisorNone {
		t.Fatalf("cgroup supervisor = %s, %t; want none, true (the 0:: line governs, not the legacy line)", kind, known)
	}
}
