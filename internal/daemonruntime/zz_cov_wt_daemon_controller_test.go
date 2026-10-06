//go:build !windows

package daemonruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/daemon"
)

func TestCwWtDaemonListenOrDefaultAndOwnedHealth(t *testing.T) {
	t.Parallel()
	if got := daemonListenOrDefault(""); got != DefaultListen {
		t.Fatalf("daemonListenOrDefault(\"\") = %q", got)
	}
	if got := daemonListenOrDefault("127.0.0.1:9"); got != "127.0.0.1:9" {
		t.Fatalf("daemonListenOrDefault(addr) = %q", got)
	}

	_, controller, _ := cwWtLockFixture(t)
	if err := controller.ownedHealth(context.Background(), "127.0.0.1:9", 0, 1); err == nil || !strings.Contains(err.Error(), "is not alive") {
		t.Fatalf("ownedHealth with pid 0 = %v", err)
	}
	controller.deps.Alive = func(int) bool { return false }
	if err := controller.ownedHealth(context.Background(), "127.0.0.1:9", 42, 1); err == nil {
		t.Fatal("ownedHealth for a dead pid must fail")
	}
	// With no ownedHealth hook the plain health probe is used.
	controller.deps.Alive = func(pid int) bool { return pid == 42 }
	controller.deps.Health = func(context.Context, string) error { return errors.New("cwWt: unhealthy") }
	if err := controller.ownedHealth(context.Background(), "127.0.0.1:9", 42, 1); err == nil {
		t.Fatal("ownedHealth must fall back to the plain health probe")
	}
}

func TestCwWtDaemonControllerStartAndStopErrors(t *testing.T) {
	t.Parallel()
	root, controller, deps := cwWtLockFixture(t)
	ctx := context.Background()

	// A lifecycle lock that cannot be taken stops Start before anything else.
	blocked := cwWtDaemonRoot(t)
	blockedDeps := daemonTestDependencies(t, blocked)
	if err := os.WriteFile(filepath.Join(blocked, ".wb"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewController(blockedDeps, blocked).Start(ctx, DefaultListen); err == nil || !strings.Contains(err.Error(), "secure daemon runtime") {
		t.Fatalf("Start with a blocked runtime = %v", err)
	}

	// A non-loopback listener is a usage error.
	if _, err := controller.Start(ctx, "0.0.0.0:1234"); err == nil {
		t.Fatal("Start on a public listener must fail")
	}

	// A provenance failure is reported.
	noExec := deps
	noExec.Executable = func() (string, error) { return "", errors.New("cwWt: no executable") }
	if _, err := NewController(noExec, root).Start(ctx, DefaultListen); err == nil {
		t.Fatal("Start without an executable must fail")
	}
	if _, err := (Controller{deps: noExec, root: root}).Provenance(); err == nil {
		t.Fatal("provenance without an executable must fail")
	}

	// Stop with no durable state reports an unmanaged result.
	if _, err := controller.Stop(ctx); err != nil {
		t.Fatalf("Stop with no state: %v", err)
	}

	// Stop over a dead recorded process reconciles the state to stopped.
	deadDeps := deps
	deadDeps.Alive = func(int) bool { return false }
	dead := daemon.NewStartingAt(nil, DefaultListen, daemon.Provenance{}, "t", "", "", deps.Now())
	dead.MarkReady(4242, deps.Now())
	if err := (daemon.Store{Path: mustDaemonPath(t, StatePath, root)}).Save(dead); err != nil {
		t.Fatal(err)
	}
	deadController := NewController(deadDeps, root)
	result, err := deadController.Stop(ctx)
	if err != nil {
		t.Fatalf("Stop over a dead process: %v", err)
	}
	if result.Action != "stop" || !result.Managed || result.State.Status != daemon.StatusStopped {
		t.Fatalf("stop result = %+v", result)
	}

	// Stop over an already-stopped state returns the stored state.
	stopped := dead
	stopped.MarkStopped(deps.Now())
	if err := (daemon.Store{Path: mustDaemonPath(t, StatePath, root)}).Save(stopped); err != nil {
		t.Fatal(err)
	}
	if _, err := deadController.Stop(ctx); err != nil {
		t.Fatalf("Stop over a stopped state: %v", err)
	}

	// A process that refuses to drain is reported.
	refuseDeps := deps
	refuseDeps.Alive = func(pid int) bool { return pid == 4242 }
	refuseDeps.Stop = func(int, daemon.Supervisor, string) error { return errors.New("cwTt: cannot signal") }
	refused := daemon.NewStartingAt(nil, DefaultListen, daemon.Provenance{}, "t", "", "", deps.Now())
	refused.MarkReady(4242, deps.Now())
	if err := (daemon.Store{Path: mustDaemonPath(t, StatePath, root)}).Save(refused); err != nil {
		t.Fatal(err)
	}
	if _, err := NewController(refuseDeps, root).Stop(ctx); err == nil || !strings.Contains(err.Error(), "request daemon drain") {
		t.Fatalf("Stop with a refusing process = %v", err)
	}
}

func TestCwWtDaemonControllerStartHandsOffDifferentBinary(t *testing.T) {
	t.Parallel()
	root, _, deps := cwWtLockFixture(t)
	// A ready daemon from a different binary is drained and replaced.
	foreign := daemon.NewStartingAt(nil, DefaultListen, daemon.Provenance{Executable: "/somewhere/else/wb", Version: "old"}, "t", "", "", deps.Now())
	foreign.MarkReady(4242, deps.Now())
	if err := (daemon.Store{Path: mustDaemonPath(t, StatePath, root)}).Save(foreign); err != nil {
		t.Fatal(err)
	}
	// The fixture's own liveness map is authoritative for every pid it starts;
	// only the hand-crafted foreign pid is overridden here.
	live := deps
	originalAlive, originalStop := deps.Alive, deps.Stop
	overridden := map[int]bool{4242: false}
	live.Alive = func(pid int) bool {
		if value, ok := overridden[pid]; ok {
			return value
		}
		return originalAlive(pid)
	}
	live.Stop = func(pid int, supervisor daemon.Supervisor, label string) error {
		overridden[pid] = false
		return originalStop(pid, supervisor, label)
	}

	result, err := NewController(live, root).Start(context.Background(), DefaultListen)
	if err != nil {
		t.Fatalf("Start handing off a foreign daemon: %v", err)
	}
	if result.Action != "start" {
		t.Fatalf("handoff start result = %+v", result)
	}
	state, found, err := (daemon.Store{Path: mustDaemonPath(t, StatePath, root)}).Load()
	if err != nil || !found {
		t.Fatalf("state after handoff: found=%t err=%v", found, err)
	}
	if state.Queue.HandoffFrom == nil {
		t.Fatal("handoff did not record the previous owner")
	}
}

func TestCwWtDaemonControllerLaunchAndRestartErrors(t *testing.T) {
	t.Parallel()
	root, _, deps := cwWtLockFixture(t)
	ctx := context.Background()

	// A failing token generator stops launch before any state is written.
	noToken := deps
	noToken.Token = func() (string, error) { return "", errors.New("cwWt: no token") }
	if _, err := NewController(noToken, root).Start(ctx, DefaultListen); err == nil || !strings.Contains(err.Error(), "cwWt: no token") {
		t.Fatalf("Start with a failing token = %v", err)
	}

	// A failing process starter is reported.
	noStart := deps
	noStart.Start = func(string, []string, string) (int, error) { return 0, errors.New("cwWt: cannot spawn") }
	if _, err := NewController(noStart, root).Start(ctx, DefaultListen); err == nil {
		t.Fatal("Start with a failing spawn must fail")
	}

	// Restart with no managed daemon and --if-running succeeds without starting.
	if result, err := NewController(deps, root).RestartWithProgress(ctx, true, nil, false); err != nil || result.Action != "restart" {
		t.Fatalf("Restart --if-running with no daemon = (%+v, %v)", result, err)
	}

	// Restart without --if-running starts a replacement.
	if _, err := NewController(deps, root).RestartWithProgress(ctx, false, nil, false); err != nil {
		t.Fatalf("Restart with no daemon: %v", err)
	}

	// The phase callback is exercised when a replacement is started.
	phases := []string{}
	if _, err := NewController(deps, root).RestartWithProgress(ctx, false, func(phase string) { phases = append(phases, phase) }, false); err != nil {
		t.Fatalf("Restart with progress: %v", err)
	}
	if len(phases) == 0 {
		t.Fatal("restart progress reported no phase")
	}

	// A provenance failure during restart is reported.
	noExec := deps
	noExec.Alive = func(int) bool { return false }
	noExec.Executable = func() (string, error) { return "", errors.New("cwWt: no executable") }
	if _, err := NewController(noExec, root).RestartWithProgress(ctx, false, nil, false); err == nil {
		t.Fatal("Restart without an executable must fail")
	}
}
