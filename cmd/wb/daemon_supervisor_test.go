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
	deps.getpid = func() int { return 4242 }
	deps.getppid = func() int { return 1 }

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

// A systemd INVOCATION_ID inherited from a parent shell — without
// SYSTEMD_EXEC_PID naming THIS process — must not be recorded as systemd
// supervision of this daemon (sneat-dev/wb#622 review item 3, confirmed on a
// live host).
func TestServeDashboardTreatsInheritedInvocationIDAsUnsupervised(t *testing.T) {
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
	// INVOCATION_ID present, but no SYSTEMD_EXEC_PID: exactly the inherited
	// shape confirmed on a live host, where a shell or agent process started
	// inside a systemd-supervised session inherits the variable without ever
	// being exec'd by systemd itself.
	env := map[string]string{"INVOCATION_ID": "inherited-from-parent-shell"}
	deps.getenv = func(name string) string { return env[name] }
	deps.getpid = func() int { return 4242 }
	deps.getppid = func() int { return 1 }

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
	if state.Supervisor != daemon.SupervisorNone {
		t.Fatalf("recorded supervisor = %#v, want none", state)
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
	deps.getppid = func() int { return 1 }

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
	if state.Supervisor != daemon.SupervisorLaunchd || state.SupervisorExecPID != "" || state.SupervisorLabel != "dev.sneat.wb.daemon" {
		t.Fatalf("recorded supervisor = %#v", state)
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

// AC: JSON round-trips the supervisor field for every reportable value.
func TestDaemonResultJSONReportsSupervisor(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	state := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner", deps.now())
	state.Supervisor = daemon.SupervisorLaunchd
	state.SupervisorLabel = "dev.sneat.wb.daemon"
	state.MarkReadyWithProcess(os.Getpid(), daemonProcessStartedAt(os.Getpid()), deps.now())
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.alive = func(pid int) bool { return pid == os.Getpid() }

	result, err := newDaemonController(deps, root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	if err := writeDaemonResult(&buffer, "json", result); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"supervisor": "launchd"`, `"supervisor_label": "dev.sneat.wb.daemon"`} {
		if !strings.Contains(buffer.String(), want) {
			t.Fatalf("json status = %s, want to contain %q", buffer.String(), want)
		}
	}
}

// daemonSupervisorTestInstalledOld builds a state whose recorded executable
// path IS the fixture's own installed binary (the same file
// daemonTestDependencies wrote and controller.provenance() reads), the way a
// genuine self-update leaves it: the same path, now holding new content. This
// is what stopAndReplace's binary-match gate (sneat-dev/wb#622 review item 6)
// requires before it will touch a supervised daemon at all.
func daemonSupervisorTestInstalledOld(t *testing.T, root string, listen string, supervisor daemon.Supervisor, now time.Time) daemon.State {
	t.Helper()
	executable := filepath.Join(root, "wb")
	old := daemonTestState(t, root, listen, daemon.Provenance{Executable: executable, SHA256: "stale-recorded-sha-from-before-the-self-update", Version: "old"}, "owner-token", now)
	old.Supervisor = supervisor
	return old
}

// AC: a-supervised-restart-hands-off-not-doubles (the successful handoff).
func TestRestartOfASupervisedDaemonWaitsForTheSupervisorInsteadOfLaunching(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	now := deps.now()
	deps.now = func() time.Time { return now }
	controller := newDaemonController(deps, root)

	old := daemonSupervisorTestInstalledOld(t, root, daemonDefaultListen, daemon.SupervisorSystemd, now)
	old.MarkReadyWithProcess(901, now, now)
	if err := controller.store.Save(old); err != nil {
		t.Fatal(err)
	}
	alive := map[int]bool{901: true}
	deps.alive = func(pid int) bool { return alive[pid] }
	deps.stop = func(pid int, supervisor daemon.Supervisor, label string) error {
		if pid != 901 {
			t.Fatalf("stop signalled unexpected pid %d", pid)
		}
		if supervisor != daemon.SupervisorSystemd || label != "" {
			t.Fatalf("stop supervisor context = %s, %q", supervisor, label)
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

	result, err := controller.RestartWithProgress(context.Background(), false, nil, false)
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

	old := daemonSupervisorTestInstalledOld(t, root, daemonDefaultListen, daemon.SupervisorSystemd, now)
	old.MarkReadyWithProcess(901, now, now)
	if err := controller.store.Save(old); err != nil {
		t.Fatal(err)
	}
	alive := map[int]bool{901: true}
	deps.alive = func(pid int) bool { return alive[pid] }
	deps.stop = func(pid int, _ daemon.Supervisor, _ string) error { alive[pid] = false; return nil }
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

	_, err := controller.RestartWithProgress(context.Background(), false, nil, false)
	if err == nil || !strings.Contains(err.Error(), "did not restart the daemon") || !strings.Contains(err.Error(), "systemd") {
		t.Fatalf("timeout error = %v", err)
	}
	stopped, found, loadErr := controller.store.Load()
	if loadErr != nil || !found || stopped.Status != daemon.StatusStopped {
		t.Fatalf("state after timeout = %#v, found=%t, err=%v", stopped, found, loadErr)
	}
}

// AC: a-supervised-restart-hands-off-not-doubles (a different-binary
// replacement fails fast rather than waiting out the full bound).
func TestWaitForSupervisorReplacementFailsFastOnADifferentBinary(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	now := deps.now()
	deps.now = func() time.Time { return now }
	deps.sleep = func(d time.Duration) { now = now.Add(d) }
	controller := newDaemonController(deps, root)

	old := daemonSupervisorTestInstalledOld(t, root, daemonDefaultListen, daemon.SupervisorSystemd, now)
	old.MarkReadyWithProcess(901, now, now)
	if err := controller.store.Save(old); err != nil {
		t.Fatal(err)
	}
	alive := map[int]bool{901: true}
	deps.alive = func(pid int) bool { return alive[pid] }
	replacedWithWrongBinary := false
	deps.stop = func(pid int, _ daemon.Supervisor, _ string) error {
		alive[pid] = false
		return nil
	}
	deps.sleep = func(d time.Duration) {
		now = now.Add(d)
		if replacedWithWrongBinary {
			return
		}
		replacedWithWrongBinary = true
		// Something entirely different came up in the daemon's place — a
		// third build racing this handoff, not the executable this wait
		// targeted.
		replacement, _, loadErr := controller.store.Load()
		if loadErr != nil {
			t.Errorf("load before replacement: %v", loadErr)
			return
		}
		replacement.Provenance = daemon.Provenance{Executable: "/somewhere/else/wb", SHA256: "unrelated-build", Version: "unrelated"}
		replacement.Supervisor = daemon.SupervisorSystemd
		replacement.MarkReadyWithProcess(999, now, now)
		alive[999] = true
		if saveErr := controller.store.Save(replacement); saveErr != nil {
			t.Errorf("save replacement: %v", saveErr)
		}
	}
	deps.start = func(string, []string, string) (int, error) {
		t.Fatal("a mismatched replacement must not trigger a detached launch")
		return 0, nil
	}
	controller.deps = deps

	// A generous bound: the fail-fast must fire long before it, proving the
	// mismatch was detected rather than merely timed out.
	previous := daemonSupervisorRestartTimeout
	daemonSupervisorRestartTimeout = time.Hour
	t.Cleanup(func() { daemonSupervisorRestartTimeout = previous })

	_, err := controller.RestartWithProgress(context.Background(), false, nil, false)
	if err == nil {
		t.Fatal("a different-binary replacement must fail fast")
	}
	for _, want := range []string{"different executable", "/somewhere/else/wb", "not adopting"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("fail-fast error %q does not mention %q", err.Error(), want)
		}
	}
}

// The wait must watch its context, not only the deadline and the poll sleep.
func TestWaitForSupervisorReplacementWatchesContext(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := newDaemonController(deps, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := controller.waitForSupervisorReplacement(ctx, daemon.SupervisorSystemd, daemon.Provenance{}, "restart")
	if err == nil || err != context.Canceled {
		t.Fatalf("cancelled wait = %v, want context.Canceled", err)
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
	for _, want := range []string{"systemd", "supervisor", "wb daemon start", "--force-detached"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not mention %q", err.Error(), want)
		}
	}
}

// --force-detached bypasses the refusal explicitly.
func TestDaemonStartForceDetachedOverridesTheRefusal(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := newDaemonController(deps, root)
	stopped := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner-token", deps.now())
	stopped.Supervisor = daemon.SupervisorSystemd
	stopped.MarkStopped(deps.now())
	if err := controller.store.Save(stopped); err != nil {
		t.Fatal(err)
	}

	result, err := newDaemonController(deps, root).StartWithProgress(context.Background(), daemonDefaultListen, nil, true)
	if err != nil {
		t.Fatalf("--force-detached start: %v", err)
	}
	if !result.ProcessManagerRunning {
		t.Fatalf("forced start result = %#v", result)
	}
}

// A recorded supervisor that can no longer be confirmed to exist (a stale
// record) must not lock `wb daemon start` out forever, even without
// --force-detached (sneat-dev/wb#622 review item 4).
func TestDaemonStartAllowsADetachedStartWhenTheRecordedSupervisorIsStale(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := newDaemonController(deps, root)
	stopped := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner-token", deps.now())
	stopped.Supervisor = daemon.SupervisorSystemd
	stopped.MarkStopped(deps.now())
	if err := controller.store.Save(stopped); err != nil {
		t.Fatal(err)
	}
	deps.supervisorPresent = func(daemon.Supervisor, string) (bool, string) { return false, "" }

	result, err := newDaemonController(deps, root).Start(context.Background(), daemonDefaultListen)
	if err != nil {
		t.Fatalf("start with a stale recorded supervisor: %v", err)
	}
	if !result.ProcessManagerRunning {
		t.Fatalf("start result = %#v", result)
	}
}

// A recorded supervisor that IS confirmed present still refuses.
func TestDaemonStartRefusesWhenTheRecordedSupervisorIsConfirmedPresent(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := newDaemonController(deps, root)
	stopped := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner-token", deps.now())
	stopped.Supervisor = daemon.SupervisorSystemd
	stopped.MarkStopped(deps.now())
	if err := controller.store.Save(stopped); err != nil {
		t.Fatal(err)
	}
	confirmed := false
	deps.supervisorPresent = func(supervisor daemon.Supervisor, label string) (bool, string) {
		confirmed = true
		if supervisor != daemon.SupervisorSystemd {
			t.Fatalf("supervisorPresent probed %s, want systemd", supervisor)
		}
		return true, ""
	}

	_, err := newDaemonController(deps, root).Start(context.Background(), daemonDefaultListen)
	if err == nil {
		t.Fatal("a confirmed-present supervisor must still be refused")
	}
	if !confirmed {
		t.Fatal("supervisorPresent was never consulted")
	}
}

// wb's own self-managed launchd job is never treated as a foreign supervisor
// to refuse a cold start under: it is what `wb daemon start` itself
// (re)installs, so there is no separate owner to defer to
// (sneat-dev/wb#622 review item 1, applied to the refusal path).
func TestDaemonStartDoesNotRefuseUnderWBsOwnLaunchdJob(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := newDaemonController(deps, root)
	stopped := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner-token", deps.now())
	stopped.Supervisor = daemon.SupervisorLaunchd
	stopped.SupervisorLabel = daemonLaunchdLabel
	stopped.MarkStopped(deps.now())
	if err := controller.store.Save(stopped); err != nil {
		t.Fatal(err)
	}
	deps.supervisorPresent = func(daemon.Supervisor, string) (bool, string) {
		t.Fatal("wb's own launchd job must not even reach the existence check")
		return false, ""
	}

	result, err := newDaemonController(deps, root).Start(context.Background(), daemonDefaultListen)
	if err != nil {
		t.Fatalf("start under wb's own launchd job: %v", err)
	}
	if !result.ProcessManagerRunning {
		t.Fatalf("start result = %#v", result)
	}
}

// A FOREIGN launchd label (a job wb did not install) is refused, and names
// launchd's own remedy for that job specifically.
func TestDaemonStartRefusesUnderLaunchdAndNamesTheRemedy(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := newDaemonController(deps, root)
	stopped := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner-token", deps.now())
	stopped.Supervisor = daemon.SupervisorLaunchd
	stopped.SupervisorLabel = "com.example.foreign-wb-supervisor"
	stopped.MarkStopped(deps.now())
	if err := controller.store.Save(stopped); err != nil {
		t.Fatal(err)
	}
	deps.start = func(string, []string, string) (int, error) {
		t.Fatal("a refused start must not launch a daemon")
		return 0, nil
	}

	_, err := newDaemonController(deps, root).Start(context.Background(), daemonDefaultListen)
	if err == nil || !strings.Contains(err.Error(), "launchctl kickstart") || !strings.Contains(err.Error(), "com.example.foreign-wb-supervisor") {
		t.Fatalf("launchd refusal = %v", err)
	}
}

// Refusal text for a live supervised daemon on a different --listen must say
// so plainly, never phrased as "if it is not running" (sneat-dev/wb#622
// review item 15).
func TestDaemonStartRefusalOnADifferentListenDoesNotSayNotRunning(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := newDaemonController(deps, root)
	current, err := controller.provenance()
	if err != nil {
		t.Fatal(err)
	}
	running := daemonTestState(t, root, "127.0.0.1:9999", current, "owner-token", deps.now())
	running.Supervisor = daemon.SupervisorSystemd
	running.MarkReadyWithProcess(os.Getpid(), daemonProcessStartedAt(os.Getpid()), deps.now())
	if err := controller.store.Save(running); err != nil {
		t.Fatal(err)
	}
	deps.alive = func(pid int) bool { return pid == os.Getpid() }

	_, err = newDaemonController(deps, root).Start(context.Background(), daemonDefaultListen)
	if err == nil {
		t.Fatal("starting on a different listen while supervised must be refused")
	}
	if strings.Contains(err.Error(), "if it is not running") {
		t.Fatalf("refusal wrongly implies uncertainty about liveness: %q", err.Error())
	}
	for _, want := range []string{"already running", "127.0.0.1:9999", daemonDefaultListen} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not mention %q", err.Error(), want)
		}
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
	result, err := controller.RestartWithProgress(context.Background(), true, nil, false)
	if err != nil {
		t.Fatalf("--if-running under a supervisor with nothing alive: %v", err)
	}
	if result.Managed != true || result.State.Status != daemon.StatusStopped {
		t.Fatalf("--if-running result = %#v", result)
	}

	// Without --if-running, the same state is refused.
	_, err = controller.RestartWithProgress(context.Background(), false, nil, false)
	if err == nil || !strings.Contains(err.Error(), "supervisor") {
		t.Fatalf("unconditional restart under a supervisor = %v", err)
	}
}

// AC: a-double-owner-is-flagged-when-observable
func TestDaemonStatusFlagsASupervisorMismatch(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	state := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner", deps.now())
	// Recorded supervisor is none, but the observed cgroup disagrees.
	state.MarkReadyWithProcess(901, deps.now(), deps.now())
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.alive = func(pid int) bool { return pid == 901 }
	deps.observedSupervisor = func(pid int) (daemon.Supervisor, bool) {
		if pid != 901 {
			t.Fatalf("observed supervisor probed unexpected pid %d", pid)
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

// The reverse direction: recorded systemd, but the observed cgroup shows no
// systemd service membership at all.
func TestDaemonStatusFlagsASupervisorMismatchInTheOtherDirection(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	state := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner", deps.now())
	state.Supervisor = daemon.SupervisorSystemd
	state.MarkReadyWithProcess(901, deps.now(), deps.now())
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.alive = func(pid int) bool { return pid == 901 }
	deps.observedSupervisor = func(int) (daemon.Supervisor, bool) { return daemon.SupervisorNone, true }

	result, err := newDaemonController(deps, root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.SupervisorMismatch == "" {
		t.Fatal("expected a supervisor mismatch to be reported")
	}
	if !strings.Contains(result.SupervisorMismatch, "systemd") {
		t.Fatalf("mismatch %q does not mention systemd", result.SupervisorMismatch)
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
			deps.observedSupervisor = observe

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

// A nil observedSupervisor (a test double that never set it) must not panic
// status: the check is skipped, not attempted.
func TestDaemonStatusSkipsSupervisorMismatchWhenTheSeamIsNil(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.observedSupervisor = nil
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

	old := daemonSupervisorTestInstalledOld(t, root, daemonDefaultListen, daemon.SupervisorSystemd, now)
	old.MarkReadyWithProcess(901, now, now)
	if err := controller.store.Save(old); err != nil {
		t.Fatal(err)
	}
	alive := map[int]bool{901: true}
	deps.alive = func(pid int) bool { return alive[pid] }
	deps.stop = func(pid int, _ daemon.Supervisor, _ string) error { alive[pid] = false; return nil }
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

// AC: item 6 — a caller running a DIFFERENT, unrelated wb binary (a worktree
// build, an older or newer installed CLI) than the one actually running must
// not restart a supervised daemon merely because its own binary differs from
// the recorded provenance: that is also true of an implicit Start call from
// an unrelated command (cmd/wb/dashboard.go, daemon_rpc.go's
// daemonOperationClient). It must report the mismatch and leave the
// supervised daemon running untouched.
func TestDaemonStartDoesNotTouchASupervisedDaemonForAnUnrelatedBinary(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := newDaemonController(deps, root)

	// The recorded executable path is real, but its CURRENT on-disk content
	// (still "old installed binary", from daemonTestDependencies) never
	// becomes this test's own provenance: nothing here performs a
	// self-update, so the two SHAs never converge.
	unrelated := filepath.Join(root, "wb-unrelated-worktree-build")
	if err := os.WriteFile(unrelated, []byte("an unrelated worktree build"), 0o700); err != nil {
		t.Fatal(err)
	}
	deps.executable = func() (string, error) { return unrelated, nil }
	controller.deps = deps

	old := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: filepath.Join(root, "wb"), SHA256: "the-real-running-daemons-sha", Version: "production"}, "owner-token", deps.now())
	old.Supervisor = daemon.SupervisorSystemd
	old.MarkReadyWithProcess(901, deps.now(), deps.now())
	if err := controller.store.Save(old); err != nil {
		t.Fatal(err)
	}
	deps.alive = func(pid int) bool { return pid == 901 }
	deps.stop = func(int, daemon.Supervisor, string) error {
		t.Fatal("an unrelated binary must never stop a supervised production daemon")
		return nil
	}
	deps.start = func(string, []string, string) (int, error) {
		t.Fatal("an unrelated binary must never start a replacement for a supervised production daemon")
		return 0, nil
	}
	controller.deps = deps

	result, err := controller.Start(context.Background(), daemonDefaultListen)
	if err == nil {
		t.Fatal("an unrelated binary's implicit Start must report the mismatch, not succeed silently")
	}
	for _, want := range []string{"refusing to touch", "not this build", "leaving it running"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("mismatch error %q does not mention %q", err.Error(), want)
		}
	}
	if result.State.Status != daemon.StatusReady || result.State.PID != 901 {
		t.Fatalf("result state = %#v, want the untouched running daemon", result.State)
	}
	stillThere, found, loadErr := controller.store.Load()
	if loadErr != nil || !found || stillThere.Status != daemon.StatusReady || stillThere.PID != 901 {
		t.Fatalf("the supervised daemon's own record was disturbed: %#v, %t, %v", stillThere, found, loadErr)
	}
}

func TestRefuseDetachedStartUnderSupervisorMessages(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := newDaemonController(deps, root)
	if got := controller.refuseDetachedStartUnderSupervisor(daemon.State{}, false, false, daemonDefaultListen, false); got != "" {
		t.Fatalf("no record must not refuse: %q", got)
	}
	if got := controller.refuseDetachedStartUnderSupervisor(daemon.State{Supervisor: daemon.SupervisorNone}, true, false, daemonDefaultListen, false); got != "" {
		t.Fatalf("supervisor none must not refuse: %q", got)
	}
	if got := controller.refuseDetachedStartUnderSupervisor(daemon.State{Supervisor: daemon.SupervisorSystemd}, true, false, daemonDefaultListen, true); got != "" {
		t.Fatalf("--force-detached must not refuse: %q", got)
	}
	systemd := controller.refuseDetachedStartUnderSupervisor(daemon.State{Supervisor: daemon.SupervisorSystemd}, true, false, daemonDefaultListen, false)
	if !strings.Contains(systemd, "systemctl --user") || !strings.Contains(systemd, "--force-detached") {
		t.Fatalf("systemd refusal = %q", systemd)
	}
	launchd := controller.refuseDetachedStartUnderSupervisor(daemon.State{Supervisor: daemon.SupervisorLaunchd, SupervisorLabel: "com.example.foreign"}, true, false, daemonDefaultListen, false)
	if !strings.Contains(launchd, "launchctl kickstart") || !strings.Contains(launchd, "com.example.foreign") {
		t.Fatalf("launchd refusal = %q", launchd)
	}
	ownJob := controller.refuseDetachedStartUnderSupervisor(daemon.State{Supervisor: daemon.SupervisorLaunchd, SupervisorLabel: daemonLaunchdLabel}, true, false, daemonDefaultListen, false)
	if ownJob != "" {
		t.Fatalf("wb's own launchd job must not refuse: %q", ownJob)
	}
}

// A supervisor's own evidence must never leak into a detached child this
// build starts itself (sneat-dev/wb#622 review item 3).
func TestDaemonChildEnvironmentStripsSupervisorEvidence(t *testing.T) {
	t.Setenv("INVOCATION_ID", "should-not-be-inherited")
	t.Setenv("SYSTEMD_EXEC_PID", "1")
	t.Setenv("JOURNAL_STREAM", "8:1234")
	t.Setenv("XPC_SERVICE_NAME", "should-not-be-inherited-either")
	t.Setenv("WB_SUPERVISOR_ENV_TEST_KEPT", "kept")

	env := daemonChildEnvironment()
	for _, stripped := range []string{"INVOCATION_ID=", "SYSTEMD_EXEC_PID=", "JOURNAL_STREAM=", "XPC_SERVICE_NAME="} {
		for _, entry := range env {
			if strings.HasPrefix(entry, stripped) {
				t.Fatalf("child environment still carries %s: %v", stripped, env)
			}
		}
	}
	found := false
	for _, entry := range env {
		if entry == "WB_SUPERVISOR_ENV_TEST_KEPT=kept" {
			found = true
		}
	}
	if !found {
		t.Fatalf("child environment lost an unrelated variable: %v", env)
	}
}

// testing.Testing() reports true for the calling PROCESS, not for the
// executable argument, so daemonRefuseTestBinary refuses unconditionally
// whenever it is reached from inside a go test binary — this is exactly the
// defense in depth the guard exists for: the real incident was a test
// process reaching real production code, regardless of which path that
// process happened to be running under (sneat-dev/wb#622). That makes the
// "not a test binary" branch impossible to exercise as itself-not-refused
// from within this suite; the ".test" suffix check is verified in isolation
// instead, independent of testing.Testing().
func TestDaemonRefuseTestBinaryGuard(t *testing.T) {
	if err := daemonRefuseTestBinary(os.Args[0]); err == nil {
		t.Fatal("the running go test binary itself must be refused")
	}
	if err := daemonRefuseTestBinary("/usr/local/bin/wb"); err == nil {
		t.Fatal("every call from inside this test binary must be refused, regardless of the executable argument")
	}
	if err := daemonRefuseTestBinary("/tmp/build/wb.test"); err == nil {
		t.Fatal("a .test-suffixed path must be refused")
	}
}

// The ".test" suffix check specifically, independent of testing.Testing() —
// verified against the pure suffix rule rather than by trying to run outside
// a test binary (which this suite cannot do to itself).
func TestDaemonRefuseTestBinarySuffixRuleAloneWouldCatchIt(t *testing.T) {
	if !strings.HasSuffix(filepath.Base("/tmp/build/wb.test"), ".test") {
		t.Fatal("the suffix rule itself does not match a go test build's default binary name")
	}
	if strings.HasSuffix(filepath.Base("/usr/local/bin/wb"), ".test") {
		t.Fatal("a real install path unexpectedly matched the .test suffix rule")
	}
	// expected: a real install path never matches the suffix rule.
}
