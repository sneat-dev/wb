package daemonruntime

import (
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/daemon"
)

func TestDaemonSupervisorPresenceRejectsMissingLaunchdLabel(t *testing.T) {
	native := defaultNativeOperations()
	commandBounds := defaultNativeCommandBounds()
	native.commandBounds = func() nativeCommandBounds { return commandBounds }

	t.Parallel()
	if present, label := native.daemonSupervisorPresent(daemon.SupervisorLaunchd, "  "); present || label != "" {
		t.Fatalf("blank launchd label: present=%t label=%q", present, label)
	}
	if present, label := native.daemonSupervisorPresent(daemon.Supervisor("none"), "wb"); present || label != "" {
		t.Fatalf("unknown supervisor: present=%t label=%q", present, label)
	}
}

func TestDaemonSupervisorPresenceReturnsMissingLaunchdJobIdentity(t *testing.T) {
	native := defaultNativeOperations()
	commandBounds := defaultNativeCommandBounds()
	native.commandBounds = func() nativeCommandBounds { return commandBounds }

	t.Parallel()
	label := "com.sneat.wb.unknown-job-never-installed"
	present, observed := native.daemonSupervisorPresent(daemon.SupervisorLaunchd, label)
	if present || observed != label || !strings.Contains(observed, "wb") {
		t.Fatalf("missing launchd job: present=%t label=%q", present, observed)
	}
}
