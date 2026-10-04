package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/daemonruntime"
	"github.com/sneat-dev/wb/internal/wbhome"
)

//nolint:paralleltest // actual default home resolution deliberately uses a process-wide WB_HOME refusal fixture.
func TestDashboardDefaultLocalCallbackRefusesAnInvalidPrivateHome(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("private home blocker"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(wbhome.EnvOverride, blocker)
	t.Setenv(wbhome.EnvMigrationCompat, "")
	operations := defaultDashboardCommandDependencies()
	address, warning, err := operations.LocalURL(context.Background(), t.TempDir())
	if err == nil || address != "" || warning != "" {
		t.Fatalf("address=%q warning=%q err=%v", address, warning, err)
	}
}

//nolint:paralleltest // existing private daemon fixture pins the process-wide WB_HOME; actual Controller and Store are exercised with controlled process/health dependencies.
func TestDashboardNativeControllerReturnsExistingPrivateDaemonAndWarning(t *testing.T) {
	root := daemonTestRoot(t)
	dependencies := daemonTestDependencies(t, root)
	current, err := newDaemonController(dependencies, root).Provenance()
	if err != nil {
		t.Fatal(err)
	}
	state := daemonTestState(t, root, daemonruntime.DefaultListen, current, "private-owner", dependencies.Now())
	state.MarkReadyWithProcess(901, dependencies.Now(), dependencies.Now())
	store := daemon.Store{Path: mustDaemonPath(t, daemonruntime.StatePath, root)}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	dependencies.Alive = func(pid int) bool { return pid == 901 }
	address, warning, err := dashboardLocalURL(context.Background(), root, dependencies)
	if err != nil || address != "http://"+daemonruntime.DefaultListen+"/" || warning != "" {
		t.Fatalf("address=%q warning=%q err=%v", address, warning, err)
	}
	state.Provenance.Executable = filepath.Join(root, "different-private-binary")
	state.Supervisor = daemon.SupervisorSystemd
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	address, warning, err = dashboardLocalURL(context.Background(), root, dependencies)
	if err != nil || address != "http://"+daemonruntime.DefaultListen+"/" || !strings.Contains(warning, "does not match this invocation's own binary") {
		t.Fatalf("address=%q warning=%q err=%v", address, warning, err)
	}
	after, found, err := store.Load()
	if err != nil || !found || after.Provenance.Executable != state.Provenance.Executable || after.PID != 901 {
		t.Fatalf("existing state changed=%+v found=%t err=%v", after, found, err)
	}
}

func TestDashboardStartedAddressKeepsDefaultFallbackAndWarning(t *testing.T) {
	t.Parallel()
	for _, listen := range []string{"", "localhost:8123"} {
		result := daemonruntime.Result{State: daemonruntime.PublicState{Listen: listen}, Warning: "native warning"}
		address, warning := dashboardStartedURL(result)
		expected := listen
		if expected == "" {
			expected = daemonruntime.DefaultListen
		}
		if address != "http://"+expected+"/" || warning != result.Warning {
			t.Fatalf("address=%q warning=%q", address, warning)
		}
	}
}
