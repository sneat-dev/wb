package daemon

import "testing"

func detectWith(env map[string]string, pid, ppid int) (Supervisor, string, string) {
	return DetectSupervisor(func(name string) string { return env[name] }, pid, ppid)
}

func TestDetectSupervisorSystemd(t *testing.T) {
	kind, execPID, label := detectWith(map[string]string{"INVOCATION_ID": "abc123", "SYSTEMD_EXEC_PID": "4242"}, 4242, 1)
	if kind != SupervisorSystemd || execPID != "4242" || label != "" {
		t.Fatalf("detect = %s, %q, %q; want systemd, 4242, \"\"", kind, execPID, label)
	}
}

// Confirmed on a live host: a shell or agent process started inside a
// systemd-supervised session inherits INVOCATION_ID without SYSTEMD_EXEC_PID
// ever being set for it. That must not be mistaken for systemd supervision of
// THIS process (sneat-dev/wb#622 review item 3).
func TestDetectSupervisorInheritedInvocationIDWithoutExecPIDIsNone(t *testing.T) {
	kind, execPID, _ := detectWith(map[string]string{"INVOCATION_ID": "abc123"}, 4242, 1)
	if kind != SupervisorNone || execPID != "" {
		t.Fatalf("detect = %s, %q; want none, \"\"", kind, execPID)
	}
}

// A SYSTEMD_EXEC_PID that names a different process is the same inheritance
// shape as a missing one: the variable belongs to some other, unrelated unit.
func TestDetectSupervisorInheritedInvocationIDWithMismatchedExecPIDIsNone(t *testing.T) {
	kind, execPID, _ := detectWith(map[string]string{"INVOCATION_ID": "abc123", "SYSTEMD_EXEC_PID": "999"}, 4242, 1)
	if kind != SupervisorNone || execPID != "" {
		t.Fatalf("detect = %s, %q; want none, \"\"", kind, execPID)
	}
}

func TestDetectSupervisorLaunchd(t *testing.T) {
	kind, execPID, label := detectWith(map[string]string{"XPC_SERVICE_NAME": "dev.sneat.wb.daemon"}, 4242, 1)
	if kind != SupervisorLaunchd || execPID != "" || label != "dev.sneat.wb.daemon" {
		t.Fatalf("detect = %s, %q, %q; want launchd, \"\", dev.sneat.wb.daemon", kind, execPID, label)
	}
}

// launchd sets XPC_SERVICE_NAME to the literal "0" for a process it did not
// launch directly (a login shell, for example); that must not be mistaken for
// a managed job.
func TestDetectSupervisorLaunchdZeroIsNotSupervised(t *testing.T) {
	kind, _, _ := detectWith(map[string]string{"XPC_SERVICE_NAME": "0"}, 4242, 1)
	if kind != SupervisorNone {
		t.Fatalf("detect = %s; want none", kind)
	}
}

// A process whose parent is not launchd (ppid != 1) but whose environment
// still carries XPC_SERVICE_NAME is a child that inherited the variable, not
// one launchd itself started.
func TestDetectSupervisorLaunchdRequiresLaunchdParent(t *testing.T) {
	kind, _, label := detectWith(map[string]string{"XPC_SERVICE_NAME": "dev.sneat.wb.daemon"}, 4242, 4241)
	if kind != SupervisorNone || label != "" {
		t.Fatalf("detect = %s, %q; want none, \"\"", kind, label)
	}
}

// An "application."-prefixed label is macOS's own label for an ordinary
// foreground GUI application (an IDE, a terminal app), not a launch agent —
// treated as absent even with launchd as the direct parent (ppid 1), so a
// `daemon serve` run from an IDE's integrated terminal is never mistaken for
// one wb should try to kickstart or refuse under (sneat-dev/wb#622 review
// item 6, round-3 regression test for item M5).
func TestDetectSupervisorApplicationPrefixIsNotSupervisedEvenWithLaunchdAsParent(t *testing.T) {
	kind, execPID, label := detectWith(map[string]string{"XPC_SERVICE_NAME": "application.com.example.SomeIDE.12345"}, 4242, 1)
	if kind != SupervisorNone || execPID != "" || label != "" {
		t.Fatalf("detect = %s, %q, %q; want none, \"\", \"\"", kind, execPID, label)
	}
}

func TestDetectSupervisorNone(t *testing.T) {
	kind, execPID, label := detectWith(nil, 4242, 1)
	if kind != SupervisorNone || execPID != "" || label != "" {
		t.Fatalf("detect = %s, %q, %q; want none, \"\", \"\"", kind, execPID, label)
	}
}

func TestDetectSupervisorNilGetenv(t *testing.T) {
	kind, execPID, label := DetectSupervisor(nil, 4242, 1)
	if kind != SupervisorNone || execPID != "" || label != "" {
		t.Fatalf("detect = %s, %q, %q; want none, \"\", \"\"", kind, execPID, label)
	}
}

// systemd's INVOCATION_ID takes precedence when, implausibly, both are set —
// a process cannot be started by two supervisors, and systemd is the one that
// actually spawns the process, so its own evidence wins.
func TestDetectSupervisorPrefersSystemdWhenBothSet(t *testing.T) {
	kind, _, _ := detectWith(map[string]string{"INVOCATION_ID": "abc", "SYSTEMD_EXEC_PID": "4242", "XPC_SERVICE_NAME": "dev.sneat.wb.daemon"}, 4242, 1)
	if kind != SupervisorSystemd {
		t.Fatalf("detect = %s; want systemd", kind)
	}
}

func TestSupervisorValid(t *testing.T) {
	for _, kind := range []Supervisor{SupervisorNone, SupervisorSystemd, SupervisorLaunchd} {
		if !kind.Valid() {
			t.Fatalf("%s reported invalid", kind)
		}
	}
	if Supervisor("upstart").Valid() {
		t.Fatal("an unknown supervisor kind reported valid")
	}
}
