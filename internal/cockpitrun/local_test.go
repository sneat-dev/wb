package cockpitrun

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/daemonruntime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func daemonTestRoot(t *testing.T) string { t.Helper(); return t.TempDir() }
func mustDaemonPath(t *testing.T, f func(string) (string, error), root string) string {
	t.Helper()
	path, err := f(root)
	if err != nil {
		t.Fatal(err)
	}
	return path
}
func daemonTestDependencies(t *testing.T, root string) daemonruntime.Dependencies {
	t.Helper()
	executable := filepath.Join(root, "wb")
	if err := os.WriteFile(executable, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	alive := map[int]bool{}
	pid := 900
	return daemonruntime.Dependencies{Now: func() time.Time { return time.Date(2026, 9, 5, 7, 0, 0, 0, time.UTC) }, LockNow: time.Now, Executable: func() (string, error) { return executable, nil }, Start: func(string, []string, string) (int, error) { pid++; alive[pid] = true; return pid, nil }, Alive: func(p int) bool { return alive[p] }, Stop: func(p int, _ daemon.Supervisor, _ string) error { alive[p] = false; return nil }, Sleep: func(time.Duration) {}, Version: func() buildinfo.Report { return buildinfo.Report{Version: "test", Revision: "test-revision"} }, Token: func() (string, error) { return strings.Repeat("a", 32), nil }, Health: func(context.Context, string) error { return nil }, ProcessStartTime: func(int) (time.Time, bool) { return time.Time{}, false }, SupervisorPresent: func(daemon.Supervisor, string) (bool, string) { return true, "" }}
}
func TestCockpitLocalFromDaemonStartsWithoutMintingForJSON(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.LocalClient = func(string, string) (*http.Client, error) { return nil, errors.New("must not connect") }
	session, err := NewLocalService(deps, nil).Local(context.Background(), LocalRequest{Root: root, Listen: daemonruntime.DefaultListen, Mint: false})
	if err != nil || session.Listen == "" || session.Code != "" {
		t.Fatalf("session = %+v, err = %v", session, err)
	}
}

func TestCockpitOwnerClientUsesTheDefaultLocalClient(t *testing.T) {
	deps := daemonTestDependencies(t, daemonTestRoot(t))
	deps.LocalClient = nil
	ready := func() (daemon.State, bool, error) { return daemon.State{Status: daemon.StatusReady}, true, nil }
	// Whether the platform has a unix-socket client or not, the call must
	// return rather than panic, and never dial anything here.
	_, _ = cockpitOwnerClient(daemonruntime.Result{ProcessManagerRunning: true}, ready, deps, t.TempDir())
}

func TestCockpitOwnerClientRefusesAnUnreadyDaemon(t *testing.T) {
	deps := daemonTestDependencies(t, daemonTestRoot(t))
	load := func(state daemon.State, found bool, err error) func() (daemon.State, bool, error) {
		return func() (daemon.State, bool, error) { return state, found, err }
	}
	for name, test := range map[string]struct {
		result daemonruntime.Result
		load   func() (daemon.State, bool, error)
	}{
		"load error":      {daemonruntime.Result{ProcessManagerRunning: true}, load(daemon.State{}, false, errors.New("unreadable"))},
		"no state":        {daemonruntime.Result{ProcessManagerRunning: true}, load(daemon.State{}, false, nil)},
		"not ready":       {daemonruntime.Result{ProcessManagerRunning: true}, load(daemon.State{}, true, nil)},
		"no process held": {daemonruntime.Result{}, load(daemon.State{Status: daemon.StatusReady}, true, nil)},
	} {
		if client, err := cockpitOwnerClient(test.result, test.load, deps, "/root"); err == nil || client != nil {
			t.Fatalf("%s: client = %v, err = %v", name, client, err)
		}
	}
}

func TestCockpitLocalFromDaemonReportsStartFailures(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.Executable = func() (string, error) { return "", errors.New("no executable") }
	if _, err := NewLocalService(deps, nil).Local(context.Background(), LocalRequest{Root: root, Listen: daemonruntime.DefaultListen, Mint: true}); err == nil || !strings.Contains(err.Error(), "start local daemon") {
		t.Fatalf("err = %v", err)
	}
}

func TestCockpitLocalFromDaemonStartsOnTheRequestedListen(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	session, err := NewLocalService(deps, nil).Local(context.Background(), LocalRequest{Root: root, Listen: "127.0.0.1:43999", Mint: false})
	if err != nil || session.Listen != "127.0.0.1:43999" {
		t.Fatalf("session = %+v, err = %v", session, err)
	}
}

func startedDaemon(t *testing.T, listen string) (daemonruntime.Dependencies, string, *int) {
	t.Helper()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	if _, err := daemonruntime.NewController(deps, root).Start(context.Background(), listen); err != nil {
		t.Fatal(err)
	}
	starts := 0
	inner := deps.Start
	deps.Start = func(executable string, args []string, log string) (int, error) {
		starts++
		return inner(executable, args, log)
	}
	return deps, root, &starts
}

func TestCockpitNeverReplacesARunningDaemonOnAnotherAddress(t *testing.T) {
	deps, root, starts := startedDaemon(t, "127.0.0.1:43001")
	_, err := NewLocalService(deps, nil).Local(context.Background(), LocalRequest{Root: root, Listen: "127.0.0.1:43002", Mint: false})
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1:43001") || !strings.Contains(err.Error(), "--listen 127.0.0.1:43001") || *starts != 0 {
		t.Fatalf("err = %v, starts = %d", err, *starts)
	}
	if state, _, _ := daemonruntime.NewController(deps, root).LoadState(); state.Listen != "127.0.0.1:43001" {
		t.Fatalf("the running daemon's record changed: %+v", state)
	}
}

func TestCockpitReusesARunningDaemonWhereverItListens(t *testing.T) {
	deps, root, starts := startedDaemon(t, "127.0.0.1:43001")
	session, err := NewLocalService(deps, nil).Local(context.Background(), LocalRequest{Root: root, Listen: "", Mint: false})
	if err != nil || session.Listen != "127.0.0.1:43001" || *starts != 0 {
		t.Fatalf("session = %+v, err = %v, starts = %d", session, err, *starts)
	}
	session, err = NewLocalService(deps, nil).Local(context.Background(), LocalRequest{Root: root, Listen: "127.0.0.1:43001", Mint: false})
	if err != nil || session.Listen != "127.0.0.1:43001" || *starts != 0 {
		t.Fatalf("same address: session = %+v, err = %v, starts = %d", session, err, *starts)
	}
}

func TestCockpitStartsOnTheRequestedOrDefaultAddressWhenNothingRuns(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	session, err := NewLocalService(deps, nil).Local(context.Background(), LocalRequest{Root: root, Listen: "", Mint: false})
	if err != nil || session.Listen != daemonruntime.DefaultListen {
		t.Fatalf("default: session = %+v, err = %v", session, err)
	}
	// The recorded daemon is no longer alive: a stale record does not block another address.
	state, _, err := daemonruntime.NewController(deps, root).LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if err := deps.Stop(state.PID, daemon.SupervisorNone, ""); err != nil {
		t.Fatal(err)
	}
	session, err = NewLocalService(deps, nil).Local(context.Background(), LocalRequest{Root: root, Listen: "127.0.0.1:43003", Mint: false})
	if err != nil || session.Listen != "127.0.0.1:43003" {
		t.Fatalf("stale record: session = %+v, err = %v", session, err)
	}
}

func TestCockpitReportsAnUnreadableDaemonRecord(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	path := mustDaemonPath(t, daemonruntime.StatePath, root)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocalService(deps, nil).Local(context.Background(), LocalRequest{Root: root, Listen: "", Mint: false}); err == nil || !strings.Contains(err.Error(), "daemon record") {
		t.Fatalf("err = %v", err)
	}
}
