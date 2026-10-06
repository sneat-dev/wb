package daemonruntime

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/testenv"
)

func TestDaemonRecoverDryRunThenAppliesOnlyProvenStaleLock(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.Alive = func(pid int) bool { return pid == 900 }
	state := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner", time.Now())
	state.MarkReady(900, time.Now())
	if err := (daemon.Store{Path: mustDaemonPath(t, StatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	lockPath := mustDaemonPath(t, daemonLifecycleLockPath, root)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, []byte("pid=800\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}

	dryRun, err := NewController(deps, root).RecoverLifecycleLock(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !dryRun.Eligible || dryRun.Applied || dryRun.OwnerPID != 800 || dryRun.OwnerAlive || dryRun.StateStatus != daemon.StatusReady || dryRun.Reason != "stale_owner_dead" {
		t.Fatalf("dry-run recovery = %#v", dryRun)
	}
	if contents, err := os.ReadFile(lockPath); err != nil || string(contents) != "pid=800\n" {
		t.Fatalf("dry run changed lock: contents=%q err=%v", contents, err)
	}

	applied, err := NewController(deps, root).RecoverLifecycleLock(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Eligible || !applied.Applied {
		t.Fatalf("applied recovery = %#v", applied)
	}
	after, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("recovery replaced the stable lock inode")
	}
	if contents, err := os.ReadFile(lockPath); err != nil || string(contents) != "pid=800\n" {
		t.Fatalf("recovery rewrote stable lock contents=%q err=%v", contents, err)
	}
	if contents, err := os.ReadFile(mustDaemonPath(t, daemonLifecycleOwnerPath, root)); err != nil || string(contents) != "pid=0\n" {
		t.Fatalf("applied owner contents=%q err=%v", contents, err)
	}
}

func TestDaemonRecoverRefusesActiveAndNonTerminalTransitions(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)
	release, err := controller.lifecycleLock()
	if err != nil {
		t.Fatal(err)
	}
	active, err := controller.RecoverLifecycleLock(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if active.Eligible || active.Reason != "active_transition" || !strings.Contains(active.Detail, "already in progress") {
		t.Fatalf("active recovery result = %#v", active)
	}
	release()

	if err := os.Remove(mustDaemonPath(t, daemonLifecycleOwnerPath, root)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mustDaemonPath(t, daemonLifecycleLockPath, root), []byte("pid=800\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	Provenance, err := controller.Provenance()
	if err != nil {
		t.Fatal(err)
	}
	state := daemonTestState(t, root, DefaultListen, Provenance, "owner", deps.Now().Add(-daemonReadyTimeout-time.Second))
	controller.deps.Health = func(context.Context, string) error { return errors.New("not reachable") }
	if err := (daemon.Store{Path: mustDaemonPath(t, StatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	result, err := controller.RecoverLifecycleLock(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Eligible || result.Reason != "interrupted_start" {
		t.Fatalf("non-terminal recovery result = %#v", result)
	}
	if _, err := controller.lifecycleLock(); err == nil || !strings.Contains(err.Error(), "recovery is unsafe") {
		t.Fatalf("non-terminal lifecycle acquisition error = %v", err)
	}
	if contents, err := os.ReadFile(mustDaemonPath(t, daemonLifecycleLockPath, root)); err != nil || string(contents) != "pid=800\n" {
		t.Fatalf("refused recovery changed lock: contents=%q err=%v", contents, err)
	}
	applied, err := controller.RecoverLifecycleLock(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || applied.Reason != "interrupted_start" {
		t.Fatalf("applied interrupted-start recovery = %#v", applied)
	}
	stored, found, err := (daemon.Store{Path: mustDaemonPath(t, StatePath, root)}).Load()
	if err != nil || !found || stored.Status != daemon.StatusStopped || stored.PID != 0 || stored.OwnerToken == "owner" {
		t.Fatalf("recovered startup state = %#v, found=%t, err=%v", stored, found, err)
	}
	release, err = controller.lifecycleLock()
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestDaemonLifecycleLockReclaimsDeadOwnerAndKeepsStableInode(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)
	state := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner", time.Now())
	state.MarkStopped(time.Now())
	if err := (daemon.Store{Path: mustDaemonPath(t, StatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	lockPath := mustDaemonPath(t, daemonLifecycleLockPath, root)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, []byte("pid=800\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	release, err := controller.lifecycleLock()
	if err != nil {
		t.Fatal(err)
	}
	release()
	after, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("lifecycle transition replaced the stable lock inode")
	}
	if contents, err := os.ReadFile(lockPath); err != nil || string(contents) != "pid=800\n" {
		t.Fatalf("lifecycle transition rewrote stable lock contents=%q err=%v", contents, err)
	}
	if contents, err := os.ReadFile(mustDaemonPath(t, daemonLifecycleOwnerPath, root)); err != nil || string(contents) != "pid=0\n" {
		t.Fatalf("released owner contents=%q err=%v", contents, err)
	}
}

func TestDaemonRecoverReportsIdleLockAsNoStaleOwner(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)
	release, err := controller.lifecycleLock()
	if err != nil {
		t.Fatal(err)
	}
	release()
	result, err := controller.RecoverLifecycleLock(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Eligible || result.Applied || result.Reason != "no_stale_owner" || !strings.Contains(result.Detail, "idle") {
		t.Fatalf("idle recovery result = %#v", result)
	}
}

func TestDaemonLifecycleOwnerRefusesPartialAtomicRecord(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)
	lockPath := mustDaemonPath(t, daemonLifecycleLockPath, root)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, []byte("pid=800\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mustDaemonPath(t, daemonLifecycleOwnerPath, root), []byte("pid="), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.lifecycleLock(); err == nil || !strings.Contains(err.Error(), "ambiguous ownership metadata") {
		t.Fatalf("partial owner acquisition error = %v", err)
	}
	result, err := controller.RecoverLifecycleLock(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Eligible || result.Reason != "ambiguous_owner" || !strings.Contains(result.Detail, "ambiguous ownership metadata") {
		t.Fatalf("partial owner recovery = %#v", result)
	}
}

func TestDaemonRecoverHandlesInterruptedInitializationBeforeState(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)
	lockPath := mustDaemonPath(t, daemonLifecycleLockPath, root)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := controller.lifecycleLock()
	if err != nil {
		t.Fatalf("empty first-creation remnant was not recoverable: %v", err)
	}
	release()
	if err := controller.writeLifecycleOwnerPID(800); err != nil {
		t.Fatal(err)
	}
	result, err := controller.RecoverLifecycleLock(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Eligible || !result.Applied || result.Reason != "interrupted_before_state" {
		t.Fatalf("pre-state recovery = %#v", result)
	}
}

func TestDaemonRecoverySerializesWithStartingChildState(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.Alive = func(pid int) bool { return pid == os.Getpid() }
	controller := NewController(deps, root)
	Provenance, err := controller.Provenance()
	if err != nil {
		t.Fatal(err)
	}
	state := daemonTestState(t, root, DefaultListen, Provenance, "owner", deps.Now().Add(-daemonReadyTimeout-time.Second))
	if err := controller.store.Save(state); err != nil {
		t.Fatal(err)
	}
	lockPath := mustDaemonPath(t, daemonLifecycleLockPath, root)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, []byte("pid=800\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	releaseState, err := controller.AcquireStateLock()
	if err != nil {
		t.Fatal(err)
	}
	resultCh := make(chan RecoveryResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := controller.RecoverLifecycleLock(context.Background(), true)
		resultCh <- result
		errCh <- err
	}()
	state.MarkStartingPID(os.Getpid(), deps.Now())
	if err := controller.store.Save(state); err != nil {
		releaseState()
		t.Fatal(err)
	}
	releaseState()
	result := <-resultCh
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if !result.Eligible || !result.Applied || result.Reason != "orphaned_healthy_start" {
		t.Fatalf("serialized recovery result = %#v", result)
	}
	stored, found, err := controller.store.Load()
	if err != nil || !found || stored.Status != daemon.StatusReady || stored.PID != os.Getpid() || stored.OwnerToken != "owner" {
		t.Fatalf("healthy orphaned child was not promoted = %#v, found=%t, err=%v", stored, found, err)
	}
}

func TestDaemonRecoveryRefusesUnverifiedLiveStartingProcess(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.Alive = func(pid int) bool { return pid == os.Getpid() }
	deps.OwnedHealth = func(context.Context, string, int, uint64) error {
		return errors.New("wrong scheduler generation")
	}
	controller := NewController(deps, root)
	Provenance, err := controller.Provenance()
	if err != nil {
		t.Fatal(err)
	}
	state := daemonTestState(t, root, DefaultListen, Provenance, "owner", deps.Now())
	state.MarkStartingPID(os.Getpid(), deps.Now())
	if err := controller.store.Save(state); err != nil {
		t.Fatal(err)
	}
	lockPath := mustDaemonPath(t, daemonLifecycleLockPath, root)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := controller.writeLifecycleOwnerPID(800); err != nil {
		t.Fatal(err)
	}

	result, err := controller.RecoverLifecycleLock(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Eligible || result.Applied || result.Reason != "startup_process_unverified" || !strings.Contains(result.Detail, "wrong scheduler generation") {
		t.Fatalf("unverified live startup recovery = %#v", result)
	}
	stored, found, err := controller.store.Load()
	if err != nil || !found || stored.Status != daemon.StatusStarting || stored.PID != os.Getpid() {
		t.Fatalf("unverified live startup state changed = %#v, found=%t, err=%v", stored, found, err)
	}
}

func TestDaemonOwnedHealthyRequiresExactPIDAndGeneration(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"status":"ready","daemon_pid":123,"scheduler_generation":45}`)
	}))
	t.Cleanup(server.Close)
	listen := strings.TrimPrefix(server.URL, "http://")
	if err := daemonOwnedHealthy(context.Background(), listen, 123, 45); err != nil {
		t.Fatalf("matching health identity rejected: %v", err)
	}
	if err := daemonOwnedHealthy(context.Background(), listen, 124, 45); err == nil {
		t.Fatal("wrong daemon pid accepted")
	}
	if err := daemonOwnedHealthy(context.Background(), listen, 123, 46); err == nil {
		t.Fatal("wrong scheduler generation accepted")
	}
}

func TestDaemonLaunchDoesNotPromoteChildThatExitsAfterHealthCheck(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	now := deps.Now()
	deps.Now = func() time.Time { return now }
	deps.Sleep = func(duration time.Duration) { now = now.Add(duration) }
	deps.Alive = func(pid int) bool { return pid > 0 }
	controller := NewController(deps, root)
	firstProbe := true
	deps.OwnedHealth = func(context.Context, string, int, uint64) error {
		if !firstProbe {
			return errors.New("daemon exited")
		}
		firstProbe = false
		state, found, err := controller.store.Load()
		if err != nil || !found {
			t.Fatalf("load starting state: found=%t err=%v", found, err)
		}
		if err := controller.MarkStoppedIfOwned(state.OwnerToken); err != nil {
			t.Fatalf("record child exit: %v", err)
		}
		return nil
	}
	controller.deps = deps
	Provenance, err := controller.Provenance()
	if err != nil {
		t.Fatal(err)
	}

	result, err := controller.launch(context.Background(), nil, DefaultListen, Provenance, "start", false)
	if err == nil || !strings.Contains(err.Error(), "did not become ready") {
		t.Fatalf("launch result = %#v, err=%v", result, err)
	}
	stored, found, err := controller.store.Load()
	if err != nil || !found || stored.Status != daemon.StatusStopped || stored.PID != 0 {
		t.Fatalf("exited child was promoted after health response: %#v, found=%t, err=%v", stored, found, err)
	}
}

func TestDaemonStatusMarksDeadReadyStateStopped(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	state := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "old", SHA256: "old", Version: "old"}, "owner", time.Now())
	state.MarkReady(900, time.Now())
	if err := (daemon.Store{Path: mustDaemonPath(t, StatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	result, err := NewController(deps, root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.State.Status != daemon.StatusStopped || result.State.PID != 0 || result.Reachable {
		t.Fatalf("dead ready daemon status = %#v", result)
	}
	stored, found, err := (daemon.Store{Path: mustDaemonPath(t, StatePath, root)}).Load()
	if err != nil || !found || stored.Status != daemon.StatusStopped {
		t.Fatalf("persisted stale daemon state = %#v, %t, %v", stored, found, err)
	}
}

func TestDaemonStatusReportsDirectTransportWithoutBridgeProbe(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	state := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "old", SHA256: "old", Version: "old"}, "owner", time.Now())
	state.MarkReady(902, time.Now())
	if err := (daemon.Store{Path: mustDaemonPath(t, StatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.Alive = func(pid int) bool { return pid == 902 }
	deps.Health = func(context.Context, string) error { return nil }
	deps.BridgeHealth = func(context.Context, string, string) error {
		t.Fatal("direct success unexpectedly probed the file bridge")
		return nil
	}

	result, err := NewController(deps, root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reachable || !result.DirectTransportReachable || result.ReachabilityTransport != "direct" || result.DirectTransportError != "" || result.ReachabilityError != "" {
		t.Fatalf("direct status = %#v", result)
	}
}

func TestDaemonStatusDoesNotBridgeDisallowedDirectFailure(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	state := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "old", SHA256: "old", Version: "old"}, "owner", time.Now())
	state.MarkReady(903, time.Now())
	if err := (daemon.Store{Path: mustDaemonPath(t, StatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.Alive = func(pid int) bool { return pid == 903 }
	deps.Health = func(context.Context, string) error { return errors.New("unexpected response identity") }
	deps.BridgeHealth = func(context.Context, string, string) error {
		t.Fatal("disallowed direct failure unexpectedly probed the file bridge")
		return nil
	}

	result, err := NewController(deps, root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Reachable || result.DirectTransportReachable || result.DirectTransportError != "unexpected response identity" || result.ReachabilityError != result.DirectTransportError {
		t.Fatalf("disallowed fallback status = %#v", result)
	}
}

func TestDaemonStartDoesNotRestartManagedProcessAfterFailedAPIProbe(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)
	first, err := controller.Start(context.Background(), DefaultListen)
	if err != nil {
		t.Fatal(err)
	}
	starts := 0
	originalStart := deps.Start
	deps.Start = func(executable string, args []string, logPath string) (int, error) {
		starts++
		return originalStart(executable, args, logPath)
	}
	deps.Health = func(context.Context, string) error { return errors.New("connect: operation not permitted") }

	result, err := NewController(deps, root).Start(context.Background(), DefaultListen)
	if err != nil {
		t.Fatal(err)
	}
	if !result.AlreadyRunning || !result.ProcessManagerRunning || result.Reachable || starts != 0 {
		t.Fatalf("start after blocked probe = %#v, starts=%d", result, starts)
	}
	if result.State.Queue.Generation != first.State.Queue.Generation {
		t.Fatalf("queue generation changed from %d to %d", first.State.Queue.Generation, result.State.Queue.Generation)
	}
}

func TestDaemonStartKeepsOwnerTokenOutOfProcessArguments(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	originalStart := deps.Start
	deps.Start = func(executable string, args []string, logPath string) (int, error) {
		for _, argument := range args {
			if strings.Contains(argument, strings.Repeat("a", 30)) {
				t.Fatalf("owner token leaked in daemon argv: %q", args)
			}
			if argument == "--owner-token" {
				t.Fatalf("owner-token flag leaked in daemon argv: %q", args)
			}
		}
		return originalStart(executable, args, logPath)
	}
	if _, err := NewController(deps, root).Start(context.Background(), DefaultListen); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonStopAndExplicitRestartPreserveQueueHandoff(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)
	first, err := controller.Start(context.Background(), DefaultListen)
	if err != nil {
		t.Fatal(err)
	}
	stopped, err := controller.Stop(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stopped.State.Status != daemon.StatusStopped || stopped.State.Queue.Generation != first.State.Queue.Generation {
		t.Fatalf("stop = %#v", stopped)
	}
	restarted, err := controller.RestartWithProgress(context.Background(), false, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.State.Queue.Generation != first.State.Queue.Generation+1 || restarted.State.Queue.HandoffFrom == nil {
		t.Fatalf("restart = %#v", restarted)
	}
}

func TestDaemonRestartProgressIsPhaseAwareAndBounded(t *testing.T) {
	t.Parallel()
	if daemonRestartProgressInterval >= 10*time.Second {
		t.Fatalf("restart progress interval = %s", daemonRestartProgressInterval)
	}
	ticks := make(chan time.Time, 1)
	stopped := false
	var observedInterval time.Duration
	controller := Controller{deps: Dependencies{RestartTicker: func(interval time.Duration) (<-chan time.Time, func()) {
		observedInterval = interval
		return ticks, func() { stopped = true }
	}}}
	messages := make(chan string, 2)
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		controller.restartPhase(func(message string) { messages <- message }, "draining daemon pid 900", func() { <-release })
		close(done)
	}()
	if message := <-messages; message != "draining daemon pid 900" {
		t.Fatalf("initial progress = %q", message)
	}
	ticks <- time.Now()
	if message := <-messages; message != "draining daemon pid 900 (still waiting)" {
		t.Fatalf("repeated progress = %q", message)
	}
	close(release)
	<-done
	if !stopped {
		t.Fatal("restart progress ticker was not stopped")
	}
	if observedInterval != daemonRestartProgressInterval {
		t.Fatalf("restart progress ticker interval = %s", observedInterval)
	}
}

func TestDaemonStartIsIdempotentAndHandoffsChangedInstalledBinary(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)
	first, err := controller.Start(context.Background(), DefaultListen)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Reachable || first.State.Queue.Generation != 1 {
		t.Fatalf("first start = %#v", first)
	}
	second, err := controller.Start(context.Background(), DefaultListen)
	if err != nil {
		t.Fatal(err)
	}
	if !second.AlreadyRunning {
		t.Fatalf("second start must be idempotent: %#v", second)
	}

	newExecutable := filepath.Join(root, "wb-new")
	if err := testenv.WriteExecutableFile(newExecutable, []byte("new installed binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	deps.Executable = func() (string, error) { return newExecutable, nil }
	handoff, err := NewController(deps, root).Start(context.Background(), DefaultListen)
	if err != nil {
		t.Fatal(err)
	}
	if !handoff.AutomaticVersionHandoff || handoff.State.Queue.Generation != 2 {
		t.Fatalf("version handoff = %#v", handoff)
	}
	if handoff.State.Queue.HandoffFrom == nil || handoff.State.Queue.HandoffFrom.Executable == newExecutable {
		t.Fatalf("queue handoff source = %#v", handoff.State.Queue.HandoffFrom)
	}
}

func TestWriteLifecycleOwnerPIDInjectedHonoursAnInjectedCreateFailure(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	controller := NewController(daemonTestDependencies(t, root), root)
	inj := &filewrite.Injector{Step: filewrite.StepOpenOrCreate, Err: errBoomForCmdWB}
	if err := controller.writeLifecycleOwnerPIDInjected(800, inj); err == nil || !strings.Contains(err.Error(), "create daemon lifecycle owner") {
		t.Fatalf("writeLifecycleOwnerPIDInjected error = %v", err)
	}
}

func TestWriteLifecycleOwnerPIDInjectedHonoursAnInjectedChmodFailure(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	controller := NewController(daemonTestDependencies(t, root), root)
	inj := &filewrite.Injector{Step: filewrite.StepChmod, Err: errBoomForCmdWB}
	if err := controller.writeLifecycleOwnerPIDInjected(800, inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("writeLifecycleOwnerPIDInjected error = %v", err)
	}
}

func TestWriteLifecycleOwnerPIDInjectedHonoursAnInjectedWriteFailure(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	controller := NewController(daemonTestDependencies(t, root), root)
	inj := &filewrite.Injector{Step: filewrite.StepWrite, Err: errBoomForCmdWB}
	if err := controller.writeLifecycleOwnerPIDInjected(800, inj); err == nil || !strings.Contains(err.Error(), "write daemon lifecycle owner") {
		t.Fatalf("writeLifecycleOwnerPIDInjected error = %v", err)
	}
}

func TestWriteLifecycleOwnerPIDInjectedHonoursAnInjectedSyncFailure(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	controller := NewController(daemonTestDependencies(t, root), root)
	inj := &filewrite.Injector{Step: filewrite.StepSync, Err: errBoomForCmdWB}
	if err := controller.writeLifecycleOwnerPIDInjected(800, inj); err == nil || !strings.Contains(err.Error(), "sync daemon lifecycle owner") {
		t.Fatalf("writeLifecycleOwnerPIDInjected error = %v", err)
	}
}

func TestWriteLifecycleOwnerPIDInjectedHonoursAnInjectedCloseFailure(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	controller := NewController(daemonTestDependencies(t, root), root)
	inj := &filewrite.Injector{Step: filewrite.StepClose, Err: errBoomForCmdWB}
	if err := controller.writeLifecycleOwnerPIDInjected(800, inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("writeLifecycleOwnerPIDInjected error = %v", err)
	}
	assertNoLeftoverDaemonLifecycleOwnerTempFile(t, root)
}

func TestWriteLifecycleOwnerPIDInjectedHonoursAnInjectedRenameFailure(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	controller := NewController(daemonTestDependencies(t, root), root)
	inj := &filewrite.Injector{Step: filewrite.StepRename, Err: errBoomForCmdWB}
	if err := controller.writeLifecycleOwnerPIDInjected(800, inj); err == nil || !strings.Contains(err.Error(), "replace daemon lifecycle owner") {
		t.Fatalf("writeLifecycleOwnerPIDInjected error = %v", err)
	}
	assertNoLeftoverDaemonLifecycleOwnerTempFile(t, root)
}
