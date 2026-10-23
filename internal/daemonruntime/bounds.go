package daemonruntime

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

type LifecycleBounds struct{ Ready, Stop, SupervisorRestart, SupervisorPoll time.Duration }

func DefaultLifecycleBounds() LifecycleBounds {
	return LifecycleBounds{5 * time.Second, 5 * time.Second, 30 * time.Second, 200 * time.Millisecond}
}
func (controller Controller) bounds() LifecycleBounds {
	if controller.deps.Bounds != nil {
		return controller.deps.Bounds()
	}
	return DefaultLifecycleBounds()
}
func newRuntimeTicker(interval time.Duration) (<-chan time.Time, func()) {
	ticker := time.NewTicker(interval)
	return ticker.C, ticker.Stop
}

type nativeCommandBounds struct{ Systemctl, Launchctl time.Duration }

func defaultNativeCommandBounds() nativeCommandBounds {
	return nativeCommandBounds{3 * time.Second, 30 * time.Second}
}

type nativeOperations struct {
	runSystemctl, runLaunchctl func(...string) ([]byte, error)
	commandBounds              func() nativeCommandBounds
	lifecycleBounds            func() LifecycleBounds
}

func defaultNativeOperations() *nativeOperations {
	native := &nativeOperations{commandBounds: defaultNativeCommandBounds, lifecycleBounds: DefaultLifecycleBounds}
	native.runSystemctl = func(args ...string) ([]byte, error) {
		return runBoundedNativeCommand("systemctl", native.commandBounds().Systemctl, args...)
	}
	native.runLaunchctl = func(args ...string) ([]byte, error) {
		return runNativeLaunchctl(native.commandBounds().Launchctl, args...)
	}
	return native
}
func runBoundedNativeCommand(name string, bound time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), bound)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...) //nolint:gosec // fixed native executable and argv, no shell.
	command.WaitDelay = 2 * time.Second
	return command.CombinedOutput()
}
func supervisorEnvironmentVariable(name string) bool {
	switch name {
	case "INVOCATION_ID", "SYSTEMD_EXEC_PID", "JOURNAL_STREAM", "XPC_SERVICE_NAME":
		return true
	}
	return false
}
func systemRunningState(state string) bool {
	switch strings.TrimSpace(state) {
	case "running", "degraded", "starting", "initializing", "maintenance", "stopping":
		return true
	}
	return false
}

// ProcessAlive observes native process liveness directly without constructing a controller or starting a daemon.
func ProcessAlive(pid int) bool { return defaultNativeOperations().processAlive(pid) }
