package main

import (
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/daemon"
)

func TestDaemonSupervisorPresenceRejectsMissingLaunchdLabel(t *testing.T) {
	t.Parallel()
	if present, label := daemonSupervisorPresent(daemon.SupervisorLaunchd, "  "); present || label != "" {
		t.Fatalf("blank launchd label: present=%t label=%q", present, label)
	}
	if present, label := daemonSupervisorPresent(daemon.Supervisor("none"), "wb"); present || label != "" {
		t.Fatalf("unknown supervisor: present=%t label=%q", present, label)
	}
}

func TestDaemonSupervisorPresenceReturnsMissingLaunchdJobIdentity(t *testing.T) {
	t.Parallel()
	label := "com.sneat.wb.unknown-job-never-installed"
	present, observed := daemonSupervisorPresent(daemon.SupervisorLaunchd, label)
	if present || observed != label || !strings.Contains(observed, "wb") {
		t.Fatalf("missing launchd job: present=%t label=%q", present, observed)
	}
}
