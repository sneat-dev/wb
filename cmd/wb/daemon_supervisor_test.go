package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/daemon"
)

// AC: supervisor-is-detected-and-reported (a directly-invoked, non-managed
// serve records what its own environment says started it).
func TestServeDashboardRecordsSupervisorFromEnvironment(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "wb-supervisor-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	pinDaemonHome(t, root)
	previousRoot := projectsRoot
	projectsRoot = root
	t.Cleanup(func() { projectsRoot = previousRoot })

	deps := daemonTestDependencies(t, root)
	env := map[string]string{"INVOCATION_ID": "abc123", "SYSTEMD_EXEC_PID": "4242"}
	deps.getenv = func(name string) string { return env[name] }

	address := freeLoopbackAddress(t)
	command := &cobra.Command{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command.SetContext(ctx)
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)

	store := daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}
	served := make(chan error, 1)
	go func() { served <- serveDashboard(command, deps, address, store, "owner-token", true, false) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("serveDashboard: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("serveDashboard did not return after its context was cancelled")
		}
	})
	waitForHealth(t, address)

	state, found, err := store.Load()
	if err != nil || !found {
		t.Fatalf("state after serve: found=%t err=%v", found, err)
	}
	if state.Supervisor != daemon.SupervisorSystemd || state.SupervisorExecPID != "4242" {
		t.Fatalf("recorded supervisor = %#v", state)
	}
}

// A launchd-started daemon records launchd, and reports no exec PID: launchd
// does not name one the way SYSTEMD_EXEC_PID does.
func TestServeDashboardRecordsLaunchdSupervisor(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "wb-supervisor-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	pinDaemonHome(t, root)
	previousRoot := projectsRoot
	projectsRoot = root
	t.Cleanup(func() { projectsRoot = previousRoot })

	deps := daemonTestDependencies(t, root)
	env := map[string]string{"XPC_SERVICE_NAME": "dev.sneat.wb.daemon"}
	deps.getenv = func(name string) string { return env[name] }

	address := freeLoopbackAddress(t)
	command := &cobra.Command{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command.SetContext(ctx)
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)

	store := daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}
	served := make(chan error, 1)
	go func() { served <- serveDashboard(command, deps, address, store, "owner-token", true, false) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("serveDashboard: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("serveDashboard did not return after its context was cancelled")
		}
	})
	waitForHealth(t, address)

	state, found, err := store.Load()
	if err != nil || !found {
		t.Fatalf("state after serve: found=%t err=%v", found, err)
	}
	if state.Supervisor != daemon.SupervisorLaunchd || state.SupervisorExecPID != "" {
		t.Fatalf("recorded supervisor = %#v", state)
	}
}

// AC: supervisor-is-detected-and-reported (the managed-start path — the shape
// a supervisor unit passing --managed-start would exercise — also records it,
// and the record survives being reconciled to stopped).
func TestManagedServeRecordsSupervisorAndSurvivesStop(t *testing.T) {
	root := cwWtDaemonRoot(t)
	t.Setenv("WB_HOME", filepath.Join(root, "wb-home"))
	deps := daemonTestDependencies(t, root)
	deps.getenv = func(name string) string {
		if name == "INVOCATION_ID" {
			return "managed-invocation"
		}
		return ""
	}
	previousRoot := projectsRoot
	projectsRoot = root
	defer func() { projectsRoot = previousRoot }()
	if err := secureDaemonRuntime(root); err != nil {
		t.Fatal(err)
	}
	statePath := mustDaemonPath(t, daemonStatePath, root)
	starting := daemon.NewStarting(nil, "127.0.0.1:0", daemon.Provenance{}, "cw-wt-token", deps.now())
	if err := (daemon.Store{Path: statePath}).Save(starting); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(250 * time.Millisecond)
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

	state, found, err := (daemon.Store{Path: statePath}).Load()
	if err != nil || !found {
		t.Fatalf("managed state after serve: found=%t err=%v", found, err)
	}
	if state.Status != daemon.StatusStopped {
		t.Fatalf("managed state after serve = %s, want stopped", state.Status)
	}
	if state.Supervisor != daemon.SupervisorSystemd {
		t.Fatalf("managed state supervisor = %q, want systemd", state.Supervisor)
	}
}

// AC: supervisor-is-detected-and-reported (a record predating this field
// reports none, never a blank value).
func TestDaemonStatusReportsNoneForARecordPredatingSupervisorField(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	state := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner", time.Now())
	state.MarkReadyWithProcess(os.Getpid(), daemonProcessStartedAt(os.Getpid()), time.Now())
	// Supervisor is left at its zero value, exactly as an unmarshalled record
	// written before this field existed would be.
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.alive = func(pid int) bool { return pid == os.Getpid() }

	result, err := newDaemonController(deps, root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.State.Supervisor != daemon.SupervisorNone {
		t.Fatalf("supervisor = %q, want none", result.State.Supervisor)
	}

	var buffer bytes.Buffer
	if err := writeDaemonResult(&buffer, "text", result); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buffer.String(), "supervisor=none") {
		t.Fatalf("text status = %q", buffer.String())
	}
}

// AC: a-supervised-restart-hands-off-not-doubles (the successful handoff).
func TestRestartOfASupervisedDaemonWaitsForTheSupervisorInsteadOfLaunching(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	now := deps.now()
	deps.now = func() time.Time { return now }
	controller := newDaemonController(deps, root)

	old := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "old", SHA256: "old", Version: "old"}, "owner-token", now)
	old.Supervisor = daemon.SupervisorSystemd
	old.MarkReadyWithProcess(901, now, now)
	if err := controller.store.Save(old); err != nil {
		t.Fatal(err)
	}
	alive := map[int]bool{901: true}
	deps.alive = func(pid int) bool { return alive[pid] }
	deps.stop = func(pid int) error {
		if pid != 901 {
			t.Fatalf("stop signalled unexpected pid %d", pid)
		}
		alive[901] = false
		return nil
	}
	// stop() sees the old process dead on its very first check (deps.stop set
	// that synchronously above) and writes its own MarkStopped record before
	// this ever runs, so the poll loop's *first* sleep — after it has already
	// observed "not ready yet" once — is where the supervisor's replacement
	// appears. Nothing here writes it any earlier: that would race stop()'s
	// own final write the way a synchronous test double otherwise would,
	// which a real supervisor (always slower than one poll tick) never does.
	replaced := false
	deps.sleep = func(d time.Duration) {
		now = now.Add(d)
		if replaced {
			return
		}
		replaced = true
		replacement, _, loadErr := controller.store.Load()
		if loadErr != nil {
			t.Errorf("load before replacement: %v", loadErr)
			return
		}
		current, provErr := controller.provenance()
		if provErr != nil {
			t.Errorf("provenance: %v", provErr)
			return
		}
		replacement.Provenance = current
		replacement.Supervisor = daemon.SupervisorSystemd
		replacement.MarkReadyWithProcess(902, now, now)
		alive[902] = true
		if saveErr := controller.store.Save(replacement); saveErr != nil {
			t.Errorf("save replacement: %v", saveErr)
		}
	}
	deps.start = func(string, []string, string) (int, error) {
		t.Fatal("a supervised restart must not launch a detached daemon")
		return 0, nil
	}
	deps.ownedHealth = func(context.Context, string, int, uint64) error { return nil }
	controller.deps = deps

	result, err := controller.RestartWithProgress(context.Background(), false, nil)
	if err != nil {
		t.Fatalf("supervised restart: %v", err)
	}
	if !result.ReadyVerified || result.State.PID != 902 || !result.AutomaticVersionHandoff {
		t.Fatalf("supervised restart result = %#v", result)
	}
}

// AC: a-supervised-restart-hands-off-not-doubles (the timeout).
func TestRestartOfASupervisedDaemonTimesOutWithoutLaunchingADetachedReplacement(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	now := deps.now()
	deps.now = func() time.Time { return now }
	deps.sleep = func(d time.Duration) { now = now.Add(d) }
	controller := newDaemonController(deps, root)

	old := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "old", SHA256: "old", Version: "old"}, "owner-token", now)
	old.Supervisor = daemon.SupervisorSystemd
	old.MarkReadyWithProcess(901, now, now)
	if err := controller.store.Save(old); err != nil {
		t.Fatal(err)
	}
	alive := map[int]bool{901: true}
	deps.alive = func(pid int) bool { return alive[pid] }
	deps.stop = func(pid int) error { alive[pid] = false; return nil }
	// The supervisor never brings a replacement back: no pid ever appears
	// alive again after 901 stops.
	deps.start = func(string, []string, string) (int, error) {
		t.Fatal("a timed-out supervised restart must not launch a detached daemon")
		return 0, nil
	}
	controller.deps = deps

	previous := daemonSupervisorRestartTimeout
	daemonSupervisorRestartTimeout = 2 * time.Second
	t.Cleanup(func() { daemonSupervisorRestartTimeout = previous })

	_, err := controller.RestartWithProgress(context.Background(), false, nil)
	if err == nil || !strings.Contains(err.Error(), "did not restart the daemon") || !strings.Contains(err.Error(), "systemd") {
		t.Fatalf("timeout error = %v", err)
	}
	stopped, found, loadErr := controller.store.Load()
	if loadErr != nil || !found || stopped.Status != daemon.StatusStopped {
		t.Fatalf("state after timeout = %#v, found=%t, err=%v", stopped, found, loadErr)
	}
}

// AC: a-detached-start-is-refused-under-a-supervisor
func TestDaemonStartRefusesADetachedStartUnderASupervisor(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := newDaemonController(deps, root)
	stopped := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner-token", deps.now())
	stopped.Supervisor = daemon.SupervisorSystemd
	stopped.MarkStopped(deps.now())
	if err := controller.store.Save(stopped); err != nil {
		t.Fatal(err)
	}
	deps.start = func(string, []string, string) (int, error) {
		t.Fatal("a refused start must not launch a daemon")
		return 0, nil
	}

	_, err := newDaemonController(deps, root).Start(context.Background(), daemonDefaultListen)
	if err == nil {
		t.Fatal("starting under a recorded systemd supervisor must be refused")
	}
	for _, want := range []string{"systemd", "supervisor", "wb daemon start"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not mention %q", err.Error(), want)
		}
	}
}

// A launchd-owned runtime is refused too, and names launchd's own remedy.
func TestDaemonStartRefusesUnderLaunchdAndNamesTheRemedy(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := newDaemonController(deps, root)
	stopped := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner-token", deps.now())
	stopped.Supervisor = daemon.SupervisorLaunchd
	stopped.MarkStopped(deps.now())
	if err := controller.store.Save(stopped); err != nil {
		t.Fatal(err)
	}
	deps.start = func(string, []string, string) (int, error) {
		t.Fatal("a refused start must not launch a daemon")
		return 0, nil
	}

	_, err := newDaemonController(deps, root).Start(context.Background(), daemonDefaultListen)
	if err == nil || !strings.Contains(err.Error(), "launchctl kickstart") {
		t.Fatalf("launchd refusal = %v", err)
	}
}

// wb daemon restart --if-running (the self-update after-update hook's own
// call) must refuse the same way rather than launching a fresh daemon.
func TestDaemonRestartIfRunningRefusesUnderASupervisorWhenNothingIsAlive(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := newDaemonController(deps, root)
	stopped := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner-token", deps.now())
	stopped.Supervisor = daemon.SupervisorSystemd
	stopped.MarkStopped(deps.now())
	if err := controller.store.Save(stopped); err != nil {
		t.Fatal(err)
	}
	deps.start = func(string, []string, string) (int, error) {
		t.Fatal("a refused restart must not launch a daemon")
		return 0, nil
	}
	controller.deps = deps

	// --if-running must still be a no-op when nothing is alive: the refusal
	// only applies to an unconditional restart/start that would otherwise
	// launch a detached daemon.
	result, err := controller.RestartWithProgress(context.Background(), true, nil)
	if err != nil {
		t.Fatalf("--if-running under a supervisor with nothing alive: %v", err)
	}
	if result.Managed != true || result.State.Status != daemon.StatusStopped {
		t.Fatalf("--if-running result = %#v", result)
	}

	// Without --if-running, the same state is refused.
	_, err = controller.RestartWithProgress(context.Background(), false, nil)
	if err == nil || !strings.Contains(err.Error(), "supervisor") {
		t.Fatalf("unconditional restart under a supervisor = %v", err)
	}
}

// AC: a-double-owner-is-flagged-when-observable
func TestDaemonStatusFlagsASupervisorMismatch(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	state := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner", deps.now())
	// Recorded supervisor is none, but the actual parent observation disagrees.
	state.MarkReadyWithProcess(901, deps.now(), deps.now())
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.alive = func(pid int) bool { return pid == 901 }
	deps.observedParentSupervisor = func(pid int) (daemon.Supervisor, bool) {
		if pid != 901 {
			t.Fatalf("observed parent probed unexpected pid %d", pid)
		}
		return daemon.SupervisorSystemd, true
	}

	result, err := newDaemonController(deps, root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.SupervisorMismatch == "" {
		t.Fatal("expected a supervisor mismatch to be reported")
	}
	for _, want := range []string{"none", "systemd", "901"} {
		if !strings.Contains(result.SupervisorMismatch, want) {
			t.Fatalf("mismatch %q does not mention %q", result.SupervisorMismatch, want)
		}
	}

	var buffer bytes.Buffer
	if err := writeDaemonResult(&buffer, "text", result); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buffer.String(), "supervisor_mismatch=") {
		t.Fatalf("text status = %q", buffer.String())
	}
}

// No mismatch is reported when the recorded and observed supervisors agree,
// or when the observation is unknown.
func TestDaemonStatusDoesNotFlagAgreementOrUnknownObservation(t *testing.T) {
	root := daemonTestRoot(t)
	for name, observe := range map[string]func(int) (daemon.Supervisor, bool){
		"agrees (both none)": func(int) (daemon.Supervisor, bool) { return daemon.SupervisorNone, true },
		"unknown":            func(int) (daemon.Supervisor, bool) { return "", false },
	} {
		t.Run(name, func(t *testing.T) {
			deps := daemonTestDependencies(t, root)
			state := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner", deps.now())
			state.MarkReadyWithProcess(901, deps.now(), deps.now())
			if err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}).Save(state); err != nil {
				t.Fatal(err)
			}
			deps.alive = func(pid int) bool { return pid == 901 }
			deps.observedParentSupervisor = observe

			result, err := newDaemonController(deps, root).Status(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if result.SupervisorMismatch != "" {
				t.Fatalf("unexpected mismatch: %q", result.SupervisorMismatch)
			}
		})
	}
}

// A nil observedParentSupervisor (a test double that never set it) must not
// panic status: the check is skipped, not attempted.
func TestDaemonStatusSkipsSupervisorMismatchWhenTheSeamIsNil(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.observedParentSupervisor = nil
	state := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner", deps.now())
	state.MarkReadyWithProcess(901, deps.now(), deps.now())
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.alive = func(pid int) bool { return pid == 901 }

	result, err := newDaemonController(deps, root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.SupervisorMismatch != "" {
		t.Fatalf("unexpected mismatch: %q", result.SupervisorMismatch)
	}
}

// The whole point: an executable-handoff branch of Start must also hand off
// to a live supervisor rather than launch a detached replacement.
func TestDaemonStartHandsOffAVersionMismatchToASupervisorInsteadOfLaunching(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	now := deps.now()
	deps.now = func() time.Time { return now }
	controller := newDaemonController(deps, root)

	old := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "old", SHA256: "old", Version: "old"}, "owner-token", now)
	old.Supervisor = daemon.SupervisorSystemd
	old.MarkReadyWithProcess(901, now, now)
	if err := controller.store.Save(old); err != nil {
		t.Fatal(err)
	}
	alive := map[int]bool{901: true}
	deps.alive = func(pid int) bool { return alive[pid] }
	deps.stop = func(pid int) error { alive[pid] = false; return nil }
	// See TestRestartOfASupervisedDaemonWaitsForTheSupervisorInsteadOfLaunching
	// for why the replacement is written from the poll loop's first sleep
	// rather than from deps.stop directly.
	replaced := false
	deps.sleep = func(d time.Duration) {
		now = now.Add(d)
		if replaced {
			return
		}
		replaced = true
		replacement, _, loadErr := controller.store.Load()
		if loadErr != nil {
			t.Errorf("load before replacement: %v", loadErr)
			return
		}
		current, provErr := controller.provenance()
		if provErr != nil {
			t.Errorf("provenance: %v", provErr)
			return
		}
		replacement.Provenance = current
		replacement.Supervisor = daemon.SupervisorSystemd
		replacement.MarkReadyWithProcess(902, now, now)
		alive[902] = true
		if saveErr := controller.store.Save(replacement); saveErr != nil {
			t.Errorf("save replacement: %v", saveErr)
		}
	}
	deps.start = func(string, []string, string) (int, error) {
		t.Fatal("a supervised version-mismatch handoff must not launch a detached daemon")
		return 0, nil
	}
	deps.ownedHealth = func(context.Context, string, int, uint64) error { return nil }
	controller.deps = deps

	result, err := controller.Start(context.Background(), daemonDefaultListen)
	if err != nil {
		t.Fatalf("supervised start handoff: %v", err)
	}
	if result.State.PID != 902 {
		t.Fatalf("start handoff result = %#v", result)
	}
}

func TestRefuseDetachedStartUnderSupervisorMessages(t *testing.T) {
	if got := refuseDetachedStartUnderSupervisor(daemon.State{}, false); got != "" {
		t.Fatalf("no record must not refuse: %q", got)
	}
	if got := refuseDetachedStartUnderSupervisor(daemon.State{Supervisor: daemon.SupervisorNone}, true); got != "" {
		t.Fatalf("supervisor none must not refuse: %q", got)
	}
	systemd := refuseDetachedStartUnderSupervisor(daemon.State{Supervisor: daemon.SupervisorSystemd}, true)
	if !strings.Contains(systemd, "systemctl --user") {
		t.Fatalf("systemd refusal = %q", systemd)
	}
	launchd := refuseDetachedStartUnderSupervisor(daemon.State{Supervisor: daemon.SupervisorLaunchd}, true)
	if !strings.Contains(launchd, "launchctl kickstart") {
		t.Fatalf("launchd refusal = %q", launchd)
	}
}
