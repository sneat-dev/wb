package daemon

import "testing"

func TestDetectSupervisorSystemd(t *testing.T) {
	env := map[string]string{"INVOCATION_ID": "abc123", "SYSTEMD_EXEC_PID": "4242"}
	kind, execPID := DetectSupervisor(func(name string) string { return env[name] })
	if kind != SupervisorSystemd || execPID != "4242" {
		t.Fatalf("detect = %s, %q; want systemd, 4242", kind, execPID)
	}
}

func TestDetectSupervisorSystemdWithoutExecPID(t *testing.T) {
	env := map[string]string{"INVOCATION_ID": "abc123"}
	kind, execPID := DetectSupervisor(func(name string) string { return env[name] })
	if kind != SupervisorSystemd || execPID != "" {
		t.Fatalf("detect = %s, %q; want systemd, \"\"", kind, execPID)
	}
}

func TestDetectSupervisorLaunchd(t *testing.T) {
	env := map[string]string{"XPC_SERVICE_NAME": "dev.sneat.wb.daemon"}
	kind, execPID := DetectSupervisor(func(name string) string { return env[name] })
	if kind != SupervisorLaunchd || execPID != "" {
		t.Fatalf("detect = %s, %q; want launchd, \"\"", kind, execPID)
	}
}

// launchd sets XPC_SERVICE_NAME to the literal "0" for a process it did not
// launch directly (a login shell, for example); that must not be mistaken for
// a managed job.
func TestDetectSupervisorLaunchdZeroIsNotSupervised(t *testing.T) {
	env := map[string]string{"XPC_SERVICE_NAME": "0"}
	kind, _ := DetectSupervisor(func(name string) string { return env[name] })
	if kind != SupervisorNone {
		t.Fatalf("detect = %s; want none", kind)
	}
}

func TestDetectSupervisorNone(t *testing.T) {
	kind, execPID := DetectSupervisor(func(string) string { return "" })
	if kind != SupervisorNone || execPID != "" {
		t.Fatalf("detect = %s, %q; want none, \"\"", kind, execPID)
	}
}

func TestDetectSupervisorNilGetenv(t *testing.T) {
	kind, execPID := DetectSupervisor(nil)
	if kind != SupervisorNone || execPID != "" {
		t.Fatalf("detect = %s, %q; want none, \"\"", kind, execPID)
	}
}

// systemd's INVOCATION_ID takes precedence when, implausibly, both are set —
// a process cannot be started by two supervisors, and systemd is the one that
// actually spawns the process, so its own evidence wins.
func TestDetectSupervisorPrefersSystemdWhenBothSet(t *testing.T) {
	env := map[string]string{"INVOCATION_ID": "abc", "XPC_SERVICE_NAME": "dev.sneat.wb.daemon"}
	kind, _ := DetectSupervisor(func(name string) string { return env[name] })
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
