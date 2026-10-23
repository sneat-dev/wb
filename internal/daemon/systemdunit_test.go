package daemon

import "testing"

func TestParseSystemctlShowParsesKnownFields(t *testing.T) {
	output := "ActiveState=failed\nResult=exit-code\nNRestarts=4468\n"
	state, known := ParseSystemctlShow(output)
	if !known {
		t.Fatal("known = false, want true")
	}
	if state.ActiveState != "failed" || state.Result != "exit-code" || state.NRestarts != 4468 {
		t.Fatalf("state = %#v", state)
	}
}

func TestParseSystemctlShowIgnoresUnknownFieldsAndBlankLines(t *testing.T) {
	output := "ActiveState=active\n\nMainPID=12345\nResult=success\n"
	state, known := ParseSystemctlShow(output)
	if !known {
		t.Fatal("known = false, want true")
	}
	if state.ActiveState != "active" || state.Result != "success" || state.NRestarts != 0 {
		t.Fatalf("state = %#v", state)
	}
}

func TestParseSystemctlShowEmptyIsUnknown(t *testing.T) {
	if _, known := ParseSystemctlShow(""); known {
		t.Fatal("empty output — systemctl absent, or its user manager unreachable — must be unknown")
	}
	if _, known := ParseSystemctlShow("   \n  "); known {
		t.Fatal("whitespace-only output must be unknown")
	}
}

func TestParseSystemctlShowToleratesAMalformedNRestarts(t *testing.T) {
	state, known := ParseSystemctlShow("ActiveState=failed\nNRestarts=not-a-number\n")
	if !known {
		t.Fatal("known = false, want true")
	}
	if state.ActiveState != "failed" || state.NRestarts != 0 {
		t.Fatalf("state = %#v, want NRestarts left at zero rather than a parse crash", state)
	}
}

func TestSystemdUnitLooksOrphanedOnFailed(t *testing.T) {
	if !SystemdUnitLooksOrphaned(SystemdUnitState{ActiveState: "failed", NRestarts: 4468}) {
		t.Fatal("a failed unit must be reported as orphaned")
	}
}

func TestSystemdUnitLooksOrphanedOnRestartingAfterAFailure(t *testing.T) {
	if !SystemdUnitLooksOrphaned(SystemdUnitState{ActiveState: "activating", NRestarts: 1}) {
		t.Fatal("an activating unit with at least one restart must be reported as orphaned")
	}
}

func TestSystemdUnitLooksOrphanedNotOnFirstEverActivation(t *testing.T) {
	if SystemdUnitLooksOrphaned(SystemdUnitState{ActiveState: "activating", NRestarts: 0}) {
		t.Fatal("a unit activating for the first time, with no restarts yet, must not be reported as orphaned")
	}
}

func TestSystemdUnitLooksOrphanedNotOnHealthyStates(t *testing.T) {
	for _, activeState := range []string{"active", "inactive", "reloading", "deactivating"} {
		if SystemdUnitLooksOrphaned(SystemdUnitState{ActiveState: activeState, NRestarts: 999}) {
			t.Fatalf("ActiveState=%s must not be reported as orphaned regardless of NRestarts", activeState)
		}
	}
}
