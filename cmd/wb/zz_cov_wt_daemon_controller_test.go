//go:build !windows

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemon"
)

func TestCwWtDaemonListenOrDefaultAndOwnedHealth(t *testing.T) {
	if got := daemonListenOrDefault(""); got != daemonDefaultListen {
		t.Fatalf("daemonListenOrDefault(\"\") = %q", got)
	}
	if got := daemonListenOrDefault("127.0.0.1:9"); got != "127.0.0.1:9" {
		t.Fatalf("daemonListenOrDefault(addr) = %q", got)
	}

	_, controller, _ := cwWtLockFixture(t)
	if err := controller.ownedHealth(context.Background(), "127.0.0.1:9", 0, 1); err == nil || !strings.Contains(err.Error(), "is not alive") {
		t.Fatalf("ownedHealth with pid 0 = %v", err)
	}
	controller.deps.alive = func(int) bool { return false }
	if err := controller.ownedHealth(context.Background(), "127.0.0.1:9", 42, 1); err == nil {
		t.Fatal("ownedHealth for a dead pid must fail")
	}
	// With no ownedHealth hook the plain health probe is used.
	controller.deps.alive = func(pid int) bool { return pid == 42 }
	controller.deps.health = func(context.Context, string) error { return errors.New("cwWt: unhealthy") }
	if err := controller.ownedHealth(context.Background(), "127.0.0.1:9", 42, 1); err == nil {
		t.Fatal("ownedHealth must fall back to the plain health probe")
	}
}

func TestCwWtDaemonControllerStartAndStopErrors(t *testing.T) {
	root, controller, deps := cwWtLockFixture(t)
	ctx := context.Background()

	// A lifecycle lock that cannot be taken stops Start before anything else.
	blocked := cwWtDaemonRoot(t)
	blockedDeps := daemonTestDependencies(t, blocked)
	if err := os.WriteFile(filepath.Join(blocked, ".wb"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newDaemonController(blockedDeps, blocked).Start(ctx, daemonDefaultListen); err == nil || !strings.Contains(err.Error(), "secure daemon runtime") {
		t.Fatalf("Start with a blocked runtime = %v", err)
	}

	// A non-loopback listener is a usage error.
	if _, err := controller.Start(ctx, "0.0.0.0:1234"); err == nil {
		t.Fatal("Start on a public listener must fail")
	}

	// A provenance failure is reported.
	noExec := deps
	noExec.executable = func() (string, error) { return "", errors.New("cwWt: no executable") }
	if _, err := newDaemonController(noExec, root).Start(ctx, daemonDefaultListen); err == nil {
		t.Fatal("Start without an executable must fail")
	}
	if _, err := (daemonController{deps: noExec, root: root}).provenance(); err == nil {
		t.Fatal("provenance without an executable must fail")
	}

	// Stop with no durable state reports an unmanaged result.
	if _, err := controller.Stop(ctx); err != nil {
		t.Fatalf("Stop with no state: %v", err)
	}

	// Stop over a dead recorded process reconciles the state to stopped.
	deadDeps := deps
	deadDeps.alive = func(int) bool { return false }
	dead := daemon.NewStarting(nil, daemonDefaultListen, daemon.Provenance{}, "t", deps.now())
	dead.MarkReady(4242, deps.now())
	if err := (daemon.Store{Path: daemonStatePath(root)}).Save(dead); err != nil {
		t.Fatal(err)
	}
	deadController := newDaemonController(deadDeps, root)
	result, err := deadController.Stop(ctx)
	if err != nil {
		t.Fatalf("Stop over a dead process: %v", err)
	}
	if result.Action != "stop" || !result.Managed || result.State.Status != daemon.StatusStopped {
		t.Fatalf("stop result = %+v", result)
	}

	// Stop over an already-stopped state returns the stored state.
	stopped := dead
	stopped.MarkStopped(deps.now())
	if err := (daemon.Store{Path: daemonStatePath(root)}).Save(stopped); err != nil {
		t.Fatal(err)
	}
	if _, err := deadController.Stop(ctx); err != nil {
		t.Fatalf("Stop over a stopped state: %v", err)
	}

	// A process that refuses to drain is reported.
	refuseDeps := deps
	refuseDeps.alive = func(pid int) bool { return pid == 4242 }
	refuseDeps.stop = func(int) error { return errors.New("cwTt: cannot signal") }
	refused := daemon.NewStarting(nil, daemonDefaultListen, daemon.Provenance{}, "t", deps.now())
	refused.MarkReady(4242, deps.now())
	if err := (daemon.Store{Path: daemonStatePath(root)}).Save(refused); err != nil {
		t.Fatal(err)
	}
	if _, err := newDaemonController(refuseDeps, root).Stop(ctx); err == nil || !strings.Contains(err.Error(), "request daemon drain") {
		t.Fatalf("Stop with a refusing process = %v", err)
	}
}

func TestCwWtDaemonControllerStartHandsOffDifferentBinary(t *testing.T) {
	root, _, deps := cwWtLockFixture(t)
	// A ready daemon from a different binary is drained and replaced.
	foreign := daemon.NewStarting(nil, daemonDefaultListen, daemon.Provenance{Executable: "/somewhere/else/wb", Version: "old"}, "t", deps.now())
	foreign.MarkReady(4242, deps.now())
	if err := (daemon.Store{Path: daemonStatePath(root)}).Save(foreign); err != nil {
		t.Fatal(err)
	}
	// The fixture's own liveness map is authoritative for every pid it starts;
	// only the hand-crafted foreign pid is overridden here.
	live := deps
	originalAlive, originalStop := deps.alive, deps.stop
	overridden := map[int]bool{4242: false}
	live.alive = func(pid int) bool {
		if value, ok := overridden[pid]; ok {
			return value
		}
		return originalAlive(pid)
	}
	live.stop = func(pid int) error { overridden[pid] = false; return originalStop(pid) }

	result, err := newDaemonController(live, root).Start(context.Background(), daemonDefaultListen)
	if err != nil {
		t.Fatalf("Start handing off a foreign daemon: %v", err)
	}
	if result.Action != "start" {
		t.Fatalf("handoff start result = %+v", result)
	}
	state, found, err := (daemon.Store{Path: daemonStatePath(root)}).Load()
	if err != nil || !found {
		t.Fatalf("state after handoff: found=%t err=%v", found, err)
	}
	if state.Queue.HandoffFrom == nil {
		t.Fatal("handoff did not record the previous owner")
	}
}

func TestCwWtDaemonControllerLaunchAndRestartErrors(t *testing.T) {
	root, _, deps := cwWtLockFixture(t)
	ctx := context.Background()

	// A failing token generator stops launch before any state is written.
	noToken := deps
	noToken.token = func() (string, error) { return "", errors.New("cwWt: no token") }
	if _, err := newDaemonController(noToken, root).Start(ctx, daemonDefaultListen); err == nil || !strings.Contains(err.Error(), "cwWt: no token") {
		t.Fatalf("Start with a failing token = %v", err)
	}

	// A failing process starter is reported.
	noStart := deps
	noStart.start = func(string, []string, string) (int, error) { return 0, errors.New("cwWt: cannot spawn") }
	if _, err := newDaemonController(noStart, root).Start(ctx, daemonDefaultListen); err == nil {
		t.Fatal("Start with a failing spawn must fail")
	}

	// Restart with no managed daemon and --if-running succeeds without starting.
	if result, err := newDaemonController(deps, root).RestartWithProgress(ctx, true, nil); err != nil || result.Action != "restart" {
		t.Fatalf("Restart --if-running with no daemon = (%+v, %v)", result, err)
	}

	// Restart without --if-running starts a replacement.
	if _, err := newDaemonController(deps, root).RestartWithProgress(ctx, false, nil); err != nil {
		t.Fatalf("Restart with no daemon: %v", err)
	}

	// The phase callback is exercised when a replacement is started.
	phases := []string{}
	if _, err := newDaemonController(deps, root).RestartWithProgress(ctx, false, func(phase string) { phases = append(phases, phase) }); err != nil {
		t.Fatalf("Restart with progress: %v", err)
	}
	if len(phases) == 0 {
		t.Fatal("restart progress reported no phase")
	}

	// A provenance failure during restart is reported.
	noExec := deps
	noExec.alive = func(int) bool { return false }
	noExec.executable = func() (string, error) { return "", errors.New("cwWt: no executable") }
	if _, err := newDaemonController(noExec, root).RestartWithProgress(ctx, false, nil); err == nil {
		t.Fatal("Restart without an executable must fail")
	}
}

func TestCwWtDaemonManagedServeLifecycle(t *testing.T) {
	root := cwWtDaemonRoot(t)
	t.Setenv("WB_HOME", filepath.Join(root, "wb-home"))
	deps := daemonTestDependencies(t, root)
	previousRoot := projectsRoot
	projectsRoot = root
	defer func() { projectsRoot = previousRoot }()

	if err := secureDaemonRuntime(root); err != nil {
		t.Fatal(err)
	}
	statePath := daemonStatePath(root)
	starting := daemon.NewStarting(nil, "127.0.0.1:0", daemon.Provenance{}, "cw-wt-token", deps.now())
	if err := (daemon.Store{Path: statePath}).Save(starting); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	// Sample the durable state while the managed daemon is serving, so the
	// recorded startup pid is observable before it is reconciled to stopped.
	pidCh := make(chan int, 1)
	go func() {
		time.Sleep(250 * time.Millisecond)
		sampled, _, err := (daemon.Store{Path: statePath}).Load()
		if err != nil {
			pidCh <- -1
			return
		}
		pidCh <- sampled.PID
	}()
	go func() {
		time.Sleep(700 * time.Millisecond)
		cancel()
	}()
	command := newDaemonServeCmd(deps)
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetContext(ctx)
	var out, errOut strings.Builder
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SetArgs([]string{"--listen", "127.0.0.1:0", "--lifecycle-state", statePath})
	if err := command.Execute(); err != nil {
		t.Fatalf("managed serve: %v (stderr=%s)", err, errOut.String())
	}

	select {
	case pid := <-pidCh:
		if pid != os.Getpid() {
			t.Fatalf("serving managed state pid = %d, want %d", pid, os.Getpid())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the managed serve never published a state sample")
	}

	// The managed startup is reconciled to stopped when the context ends.
	state, found, err := (daemon.Store{Path: statePath}).Load()
	if err != nil || !found {
		t.Fatalf("managed state after serve: found=%t err=%v", found, err)
	}
	if state.Status != daemon.StatusStopped {
		t.Fatalf("managed state after serve = %s, want stopped", state.Status)
	}
	if state.OwnerToken != "cw-wt-token" {
		t.Fatalf("managed state owner token = %q", state.OwnerToken)
	}
}

func TestCwWtDaemonManagedServeRefusesSupersededOwnership(t *testing.T) {
	root := cwWtDaemonRoot(t)
	t.Setenv("WB_HOME", filepath.Join(root, "wb-home"))
	deps := daemonTestDependencies(t, root)
	previousRoot := projectsRoot
	projectsRoot = root
	defer func() { projectsRoot = previousRoot }()
	if err := secureDaemonRuntime(root); err != nil {
		t.Fatal(err)
	}
	statePath := daemonStatePath(root)
	// A ready state is not a starting state, so the managed start is refused
	// before the listener is even considered.
	ready := daemon.NewStarting(nil, "127.0.0.1:0", daemon.Provenance{}, "cw-wt-token", deps.now())
	ready.MarkReady(1, deps.now())
	if err := (daemon.Store{Path: statePath}).Save(ready); err != nil {
		t.Fatal(err)
	}
	command := newDaemonServeCmd(deps)
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetContext(context.Background())
	var out, errOut strings.Builder
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SetArgs([]string{"--listen", "127.0.0.1:0", "--lifecycle-state", statePath})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "no longer owns a starting lifecycle state") {
		t.Fatalf("managed serve with a ready state = %v", err)
	}
}
