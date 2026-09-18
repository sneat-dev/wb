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
