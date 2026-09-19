//go:build linux

package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemon"
)

// AC: recycled-pids-are-not-mistaken-for-a-live-daemon
//
// Linux is the platform where WB can observe a process start time, so this is
// where a recycled PID can be told apart from the process the record was
// written for.
func TestDaemonStatusTreatsARecycledPIDAsAnotherProcess(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	// The PID is this test's own, so it is definitely alive; the recorded start
	// time is an hour earlier, so it definitely belongs to a different process.
	deps.alive = func(pid int) bool { return pid == os.Getpid() }
	deps.health = func(context.Context, string) error { return nil }
	// daemonTestDependencies defaults processStartTime to an always-unknown
	// stub, so a hard-coded FAKE PID (the 900-series fixtures used
	// throughout this package's other tests) can never collide with a real,
	// unrelated process on the machine running the suite. That default
	// would defeat the entire point of THIS test, which deliberately uses
	// its own REAL, live PID (os.Getpid()) specifically to exercise the
	// real Linux implementation the AC above names — restore it
	// (sneat-dev/wb#622 review round 5: the stub default silently broke
	// this recycled-PID detection once assessIdentity started reading the
	// seam, reporting "identity=current, cannot observe a process start
	// time" instead of the recycled PID this test asserts).
	deps.processStartTime = daemon.ProcessStartTime
	controller := newDaemonController(deps, root)
	current, err := controller.provenance()
	if err != nil {
		t.Fatal(err)
	}
	state := daemonTestState(t, root, daemonDefaultListen, current, "owner", time.Now())
	state.MarkReadyWithProcess(os.Getpid(), time.Now().Add(-time.Hour), time.Now())
	if err := controller.store.Save(state); err != nil {
		t.Fatal(err)
	}

	result, err := controller.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Identity != identityProcessRecycled {
		t.Fatalf("identity = %q (%s), want %q", result.Identity, result.IdentityDetail, identityProcessRecycled)
	}
	if result.ReadyVerified || result.ReportedState != "unverified" || result.ProcessManagerRunning {
		t.Fatalf("a recycled PID was reported as this home's daemon: %#v", result)
	}
	if !result.Reachable {
		t.Fatal("something answering must stay reportable on its own")
	}
	stored, found, loadErr := controller.store.Load()
	if loadErr != nil || !found || stored.Status != daemon.StatusReady || stored.ProcessStartedAt.Equal(state.ProcessStartedAt) == false {
		t.Fatalf("the evidence was rewritten: %#v, %t, %v", stored, found, loadErr)
	}
}

// A recycled PID must not be signalled: stop is the one path where mistaking a
// stranger for the daemon does damage rather than merely misleading output.
func TestDaemonStopDoesNotSignalARecycledPID(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	signalled := false
	deps.stop = func(int, daemon.Supervisor, string) error { signalled = true; return nil }
	deps.alive = func(pid int) bool { return pid == os.Getpid() }
	// See TestDaemonStatusTreatsARecycledPIDAsAnotherProcess for why this
	// must restore the real implementation: without it, stop()'s own
	// recycled-PID early return (which is exactly what this test asserts)
	// is never taken, and stop() falls into its ordinary drain poll loop
	// instead — which spins forever here, since this PID (the live test
	// process itself) never goes "not alive" and daemonTestDependencies'
	// default clock/sleep never advance on their own. That hung Linux CI
	// for 35 minutes without naming a test (sneat-dev/wb#622 review round
	// 5): this file's `//go:build linux` tag meant it never ran during
	// this whole campaign's macOS-only local validation.
	deps.processStartTime = daemon.ProcessStartTime
	controller := newDaemonController(deps, root)
	current, err := controller.provenance()
	if err != nil {
		t.Fatal(err)
	}
	state := daemonTestState(t, root, daemonDefaultListen, current, "owner", time.Now())
	state.MarkReadyWithProcess(os.Getpid(), time.Now().Add(-time.Hour), time.Now())
	if err := controller.store.Save(state); err != nil {
		t.Fatal(err)
	}

	result, err := controller.stop(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if signalled {
		t.Fatal("stop signalled a process that is not this daemon")
	}
	if result.Identity != identityProcessRecycled || result.State.Status != daemon.StatusStopped {
		t.Fatalf("stop result = %#v", result)
	}
	stored, found, loadErr := controller.store.Load()
	if loadErr != nil || !found || stored.StoppedReason == "" {
		t.Fatalf("recycled stop was not recorded: %#v, %t, %v", stored, found, loadErr)
	}
}
