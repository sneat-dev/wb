package daemonruntime

import (
	"errors"
	"strings"
	"testing"
)

func TestDaemonLaunchStopsBeforeStateAndProcessWhenTheCheckRefuses(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	starts := 0
	deps.Start = func(string, []string, string) (int, error) { starts++; return 1, nil }
	deps.CheckOtherRoot = func(string, bool) error { return errors.New("registered for another root") }
	controller := NewController(deps, root)

	if _, err := controller.Start(t.Context(), DefaultListen); err == nil || !strings.Contains(err.Error(), "another root") {
		t.Fatalf("Start = %v", err)
	}
	if _, found, err := controller.store.Load(); err != nil || found || starts != 0 {
		t.Fatalf("a refused launch wrote state (found=%t, err=%v) or started a process (%d)", found, err, starts)
	}
}
