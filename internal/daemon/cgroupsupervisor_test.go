package daemon

import "testing"

func TestParseCgroupSupervisorServiceUnit(t *testing.T) {
	contents := "0::/user.slice/user-1000.slice/user@1000.service/app.slice/wb-daemon.service\n"
	kind, known := ParseCgroupSupervisor(contents)
	if !known || kind != SupervisorSystemd {
		t.Fatalf("cgroup supervisor = %s, %t; want systemd, true", kind, known)
	}
}

func TestParseCgroupSupervisorSystemUnit(t *testing.T) {
	contents := "0::/system.slice/wb-daemon.service\n"
	kind, known := ParseCgroupSupervisor(contents)
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
	kind, known := ParseCgroupSupervisor(contents)
	if !known || kind != SupervisorNone {
		t.Fatalf("cgroup supervisor = %s, %t; want none, true", kind, known)
	}
}

func TestParseCgroupSupervisorEmptyIsUnknown(t *testing.T) {
	if _, known := ParseCgroupSupervisor(""); known {
		t.Fatal("empty cgroup contents must be unknown, not a supervisor kind")
	}
	if _, known := ParseCgroupSupervisor("   \n  "); known {
		t.Fatal("whitespace-only cgroup contents must be unknown")
	}
}
