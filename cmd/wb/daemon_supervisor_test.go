package main

import (
	"bytes"
	"context"
	"errors"
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
	projectsRoot := root

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
	go func() {
		served <- serveDashboard(&invocation{projectsRoot: projectsRoot}, command, deps, address, store, "owner-token", true, false)
	}()
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

// `daemon serve` records its OWN observed systemd unit (from its own
// /proc/self/cgroup, via the observedCgroupUnit seam) at startup — not a
// later, separate `wb daemon status` invocation's own configured/default
// guess, which may not even agree with reality (sneat-dev/wb#622 review
// round 3, item M3).
func TestServeDashboardRecordsItsOwnObservedSystemdUnit(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "wb-supervisor-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	pinDaemonHome(t, root)
	projectsRoot := root

	deps := daemonTestDependencies(t, root)
	deps.observedCgroupUnit = func(pid int) (string, bool) {
		if pid != 4242 {
			t.Fatalf("observedCgroupUnit probed unexpected pid %d", pid)
		}
		return "wb.service", true
	}
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
	go func() {
		served <- serveDashboard(&invocation{projectsRoot: projectsRoot}, command, deps, address, store, "owner-token", true, false)
	}()
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
	if state.SystemdUnit != "wb.service" {
		t.Fatalf("recorded systemd unit = %q, want wb.service", state.SystemdUnit)
	}
}

// A configured WB_DAEMON_SYSTEMD_UNIT missing the ".service" suffix is
// normalized by appending it: systemctl accepts either form, but this
// build's own unit-identity comparisons always carry the suffix
// (sneat-dev/wb#622 review round 3, item M3).
func TestDaemonSystemdUnitNameNormalizesAMissingServiceSuffix(t *testing.T) {
	unsuffixed := func(name string) string {
		if name == "WB_DAEMON_SYSTEMD_UNIT" {
			return "my-custom-wb"
		}
		return ""
	}
	if got := daemonSystemdUnitName(unsuffixed); got != "my-custom-wb.service" {
		t.Fatalf("unit name = %q, want the .service suffix appended", got)
	}
	suffixed := func(name string) string {
		if name == "WB_DAEMON_SYSTEMD_UNIT" {
			return "my-custom-wb.service"
		}
		return ""
	}
	if got := daemonSystemdUnitName(suffixed); got != "my-custom-wb.service" {
		t.Fatalf("unit name = %q, want it left unchanged", got)
	}
}

// `wb daemon status` MUST prefer the daemon's OWN recorded unit
// (State.SystemdUnit) over this invocation's own configured/default guess:
// a status invocation run without the same WB_DAEMON_SYSTEMD_UNIT the
// daemon itself was supervised under would otherwise query the WRONG unit
// and see a false "no systemd service membership" (sneat-dev/wb#622 review
// round 3, item M3).
func TestDaemonStatusPrefersTheDaemonsOwnRecordedSystemdUnitOverTheInvokersConfig(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	state := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner", deps.now())
	state.SystemdUnit = "wb.service"
	state.MarkReadyWithProcess(901, deps.now(), deps.now())
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.alive = func(pid int) bool { return pid == 901 }
	deps.observedSupervisor = func(int) (daemon.Supervisor, bool) { return daemon.SupervisorNone, true }
	deps.systemdUnitName = func() string { return "wb-daemon.service" }
	deps.systemdUnitState = func(unit string) (daemon.SystemdUnitState, bool) {
		if unit != "wb.service" {
			t.Fatalf("systemdUnitState probed %q, want the daemon's own recorded unit wb.service", unit)
		}
		return daemon.SystemdUnitState{ActiveState: "failed", NRestarts: 3}, true
	}

	result, err := newDaemonController(deps, root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.SupervisorMismatch == "" || !strings.Contains(result.SupervisorMismatch, "wb.service") {
		t.Fatalf("mismatch = %q, want it naming the daemon's own recorded unit", result.SupervisorMismatch)
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
	projectsRoot := root

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
	go func() {
		served <- serveDashboard(&invocation{projectsRoot: projectsRoot}, command, deps, address, store, "owner-token", true, false)
	}()
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
	projectsRoot := root

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
	go func() {
		served <- serveDashboard(&invocation{projectsRoot: projectsRoot}, command, deps, address, store, "owner-token", true, false)
	}()
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

// The first self-update into a build that records the supervisor field at
// all must not recreate #617 on a systemd host: a pre-#622 build's record
// has an EMPTY (legacy) supervisor field, which normalizes to `none` via
// ReportedSupervisor — exactly what an unsupervised daemon also reports.
// Without an independent fallback, `wb daemon restart` (and the self-update
// hook, which just shells out to it) would SIGTERM the real systemd-managed
// process and then launch a detached --managed-start child, which then
// fails to bind once systemd's own Restart=always brings the original back
// (sneat-dev/wb#622 review round 3, item S1).
func TestStopAndReplaceTreatsALegacyRecordAsSupervisedWhenCgroupConfirmsSystemd(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	now := deps.now()
	deps.now = func() time.Time { return now }
	controller := newDaemonController(deps, root)

	old := daemonSupervisorTestInstalledOld(t, root, daemonDefaultListen, "", now) // legacy: predates the field
	old.MarkReadyWithProcess(901, now, now)
	if err := controller.store.Save(old); err != nil {
		t.Fatal(err)
	}
	alive := map[int]bool{901: true}
	deps.alive = func(pid int) bool { return alive[pid] }
	deps.stop = func(pid int, supervisor daemon.Supervisor, label string) error {
		alive[901] = false
		return nil
	}
	deps.observedSupervisor = func(pid int) (daemon.Supervisor, bool) {
		if pid != 901 {
			t.Fatalf("observedSupervisor probed unexpected pid %d", pid)
		}
		return daemon.SupervisorSystemd, true
	}
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
		t.Fatal("a legacy record independently confirmed as systemd-managed must not launch a detached daemon")
		return 0, nil
	}
	deps.ownedHealth = func(context.Context, string, int, uint64) error { return nil }
	controller.deps = deps

	result, err := controller.RestartWithProgress(context.Background(), false, nil, false)
	if err != nil {
		t.Fatalf("legacy-record supervised restart: %v", err)
	}
	if !result.ReadyVerified || result.State.PID != 902 || !result.AutomaticVersionHandoff {
		t.Fatalf("legacy-record supervised restart result = %#v", result)
	}
}

// The same fallback applies to Start's executable-handoff branch (an
// implicit `wb dashboard --local` or RPC bootstrap after a self-update hook
// timeout would otherwise reach exactly this shape).
func TestDaemonStartHandoffTreatsALegacyRecordAsSupervisedWhenCgroupConfirmsSystemd(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	now := deps.now()
	deps.now = func() time.Time { return now }
	controller := newDaemonController(deps, root)

	old := daemonSupervisorTestInstalledOld(t, root, daemonDefaultListen, "", now)
	old.MarkReadyWithProcess(901, now, now)
	if err := controller.store.Save(old); err != nil {
		t.Fatal(err)
	}
	alive := map[int]bool{901: true}
	deps.alive = func(pid int) bool { return alive[pid] }
	deps.stop = func(pid int, supervisor daemon.Supervisor, label string) error {
		alive[901] = false
		return nil
	}
	deps.observedSupervisor = func(pid int) (daemon.Supervisor, bool) { return daemon.SupervisorSystemd, true }
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
		t.Fatal("an implicit Start against a legacy record independently confirmed as systemd-managed must not launch a detached daemon")
		return 0, nil
	}
	deps.ownedHealth = func(context.Context, string, int, uint64) error { return nil }
	controller.deps = deps

	result, err := controller.Start(context.Background(), daemonDefaultListen)
	if err != nil {
		t.Fatalf("legacy-record supervised start handoff: %v", err)
	}
	if !result.ReadyVerified || result.State.PID != 902 || !result.AutomaticVersionHandoff {
		t.Fatalf("legacy-record supervised start handoff result = %#v", result)
	}
}

// A genuinely unsupervised daemon (recorded none, and independently
// confirmed as not systemd-managed — or with no independent observation
// available at all) must keep taking the ordinary detached-launch path:
// the fallback must not manufacture supervision that was never there.
func TestStopAndReplaceLeavesAGenuinelyUnsupervisedRecordUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name     string
		observed func(int) (daemon.Supervisor, bool)
	}{
		{"cgroup confirms no systemd membership", func(int) (daemon.Supervisor, bool) { return daemon.SupervisorNone, true }},
		{"no independent observation available", func(int) (daemon.Supervisor, bool) { return "", false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := daemonTestRoot(t)
			deps := daemonTestDependencies(t, root)
			controller := newDaemonController(deps, root)

			old := daemonSupervisorTestInstalledOld(t, root, daemonDefaultListen, daemon.SupervisorNone, deps.now())
			old.MarkReadyWithProcess(901, deps.now(), deps.now())
			if err := controller.store.Save(old); err != nil {
				t.Fatal(err)
			}
			aliveSet := map[int]bool{901: true}
			deps.alive = func(pid int) bool { return aliveSet[pid] }
			deps.stop = func(pid int, _ daemon.Supervisor, _ string) error { aliveSet[901] = false; return nil }
			deps.observedSupervisor = tc.observed
			launched := false
			deps.start = func(_ string, args []string, _ string) (int, error) {
				launched = true
				for _, argument := range args {
					if argument == "--lifecycle-state" {
						t.Fatalf("daemon start pinned a resolved lifecycle path: %v", args)
					}
				}
				aliveSet[902] = true
				return 902, nil
			}
			deps.ownedHealth = func(context.Context, string, int, uint64) error { return nil }
			controller.deps = deps

			result, err := controller.RestartWithProgress(context.Background(), false, nil, false)
			if err != nil {
				t.Fatalf("unsupervised restart: %v", err)
			}
			if !launched {
				t.Fatal("a genuinely unsupervised daemon must still be replaced by a detached launch")
			}
			if result.State.PID != 902 {
				t.Fatalf("result = %#v", result)
			}
		})
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

// The sneat-dev/wb#617 detector this feature exists for: a specific systemd
// unit exists and is failing to keep the daemon up, while the process
// actually answering the port recorded no supervisor at all — confirmed
// live: an orphaned `daemon serve` (a session scope, not the unit's own
// cgroup) served the port while wb-daemon.service sat ActiveState=failed
// with NRestarts=4468 (sneat-dev/wb#622 review item 2). The cgroup-based
// seam agrees (recorded none, observed none) — the systemd-unit detector is
// what catches this, not the cgroup fallback.
func TestDaemonStatusFlagsAFailedSystemdUnitEvenWithoutACgroupMismatch(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	state := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner", deps.now())
	state.MarkReadyWithProcess(901, deps.now(), deps.now())
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.alive = func(pid int) bool { return pid == 901 }
	deps.observedSupervisor = func(int) (daemon.Supervisor, bool) { return daemon.SupervisorNone, true }
	deps.systemdUnitName = func() string { return "wb-daemon.service" }
	deps.systemdUnitState = func(unit string) (daemon.SystemdUnitState, bool) {
		if unit != "wb-daemon.service" {
			t.Fatalf("systemdUnitState probed unexpected unit %q", unit)
		}
		return daemon.SystemdUnitState{ActiveState: "failed", NRestarts: 4468}, true
	}

	result, err := newDaemonController(deps, root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.SupervisorMismatch == "" {
		t.Fatal("expected the failed systemd unit to be flagged")
	}
	for _, want := range []string{"wb-daemon.service", "failed", "4468"} {
		if !strings.Contains(result.SupervisorMismatch, want) {
			t.Fatalf("mismatch %q does not mention %q", result.SupervisorMismatch, want)
		}
	}
}

// A unit that is merely activating for the first time (no restarts yet), or
// healthy and active, is not evidence of anything wrong.
func TestDaemonStatusDoesNotFlagAHealthySystemdUnit(t *testing.T) {
	root := daemonTestRoot(t)
	for name, unitState := range map[string]daemon.SystemdUnitState{
		"active":                   {ActiveState: "active"},
		"activating, no restarts":  {ActiveState: "activating", NRestarts: 0},
		"inactive (never started)": {ActiveState: "inactive"},
	} {
		t.Run(name, func(t *testing.T) {
			deps := daemonTestDependencies(t, root)
			state := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner", deps.now())
			state.MarkReadyWithProcess(901, deps.now(), deps.now())
			if err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}).Save(state); err != nil {
				t.Fatal(err)
			}
			deps.alive = func(pid int) bool { return pid == 901 }
			deps.observedSupervisor = func(int) (daemon.Supervisor, bool) { return daemon.SupervisorNone, true }
			deps.systemdUnitName = func() string { return "wb-daemon.service" }
			deps.systemdUnitState = func(string) (daemon.SystemdUnitState, bool) { return unitState, true }

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

// The systemd-unit detector only applies when the RECORDED supervisor is
// none: a daemon that already recorded supervisor=systemd is not the shape
// this detector exists for (the cgroup-based fallback covers that
// direction), and probing an unrelated unit's state would be meaningless.
func TestDaemonStatusSystemdUnitDetectorOnlyAppliesWhenRecordedIsNone(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	state := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner", deps.now())
	state.Supervisor = daemon.SupervisorSystemd
	state.MarkReadyWithProcess(901, deps.now(), deps.now())
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.alive = func(pid int) bool { return pid == 901 }
	deps.observedSupervisor = func(int) (daemon.Supervisor, bool) { return daemon.SupervisorSystemd, true }
	called := false
	deps.systemdUnitName = func() string { return "wb-daemon.service" }
	deps.systemdUnitState = func(string) (daemon.SystemdUnitState, bool) {
		called = true
		return daemon.SystemdUnitState{ActiveState: "failed", NRestarts: 1}, true
	}

	result, err := newDaemonController(deps, root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("systemdUnitState must only be consulted when the recorded supervisor is none")
	}
	if result.SupervisorMismatch != "" {
		t.Fatalf("unexpected mismatch: %q", result.SupervisorMismatch)
	}
}

// Nil systemdUnitState/systemdUnitName seams (a test double that never set
// them, or a platform with no implementation) must not panic status, and
// must fall back to the cgroup-based check.
func TestDaemonStatusFallsBackToCgroupWhenSystemdUnitSeamsAreNil(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.systemdUnitState = nil
	deps.systemdUnitName = nil
	state := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner", deps.now())
	state.MarkReadyWithProcess(901, deps.now(), deps.now())
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.alive = func(pid int) bool { return pid == 901 }
	deps.observedSupervisor = func(int) (daemon.Supervisor, bool) { return daemon.SupervisorSystemd, true }

	result, err := newDaemonController(deps, root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.SupervisorMismatch == "" {
		t.Fatal("expected the cgroup-based fallback to still flag the mismatch")
	}
}

// daemonObservedSystemdUnitState is exercised through its own runSystemctl
// seam with fake output, including systemctl being entirely absent — never
// a real systemd user manager.
func TestDaemonObservedSystemdUnitStateParsesFakeOutput(t *testing.T) {
	previous := runSystemctl
	t.Cleanup(func() { runSystemctl = previous })
	var capturedArgs []string
	runSystemctl = func(args ...string) ([]byte, error) {
		capturedArgs = args
		return []byte("ActiveState=failed\nResult=exit-code\nNRestarts=4468\n"), nil
	}
	state, known := daemonObservedSystemdUnitState("wb-daemon.service")
	if !known {
		t.Fatal("known = false, want true")
	}
	if state.ActiveState != "failed" || state.NRestarts != 4468 {
		t.Fatalf("state = %#v", state)
	}
	if len(capturedArgs) == 0 || capturedArgs[len(capturedArgs)-1] != "wb-daemon.service" {
		t.Fatalf("systemctl args = %v, want the unit name last", capturedArgs)
	}
}

func TestDaemonObservedSystemdUnitStateWhenSystemctlIsAbsent(t *testing.T) {
	previous := runSystemctl
	t.Cleanup(func() { runSystemctl = previous })
	runSystemctl = func(args ...string) ([]byte, error) {
		return nil, errors.New(`exec: "systemctl": executable file not found in $PATH`)
	}
	if _, known := daemonObservedSystemdUnitState("wb-daemon.service"); known {
		t.Fatal("systemctl being absent must report unknown, not a healthy unit")
	}
}

func TestDaemonObservedSystemdUnitStateRejectsAnEmptyUnitName(t *testing.T) {
	if _, known := daemonObservedSystemdUnitState("   "); known {
		t.Fatal("an empty unit name must report unknown")
	}
}

// The unit name comes from config (an environment variable override — this
// build has no other daemon configuration file) if one is set, and from
// daemonDefaultSystemdUnit otherwise (sneat-dev/wb#622 review item 2).
func TestDaemonSystemdUnitNameDefaultsAndReadsAConfiguredOverride(t *testing.T) {
	if got := daemonSystemdUnitName(func(string) string { return "" }); got != daemonDefaultSystemdUnit {
		t.Fatalf("default unit name = %q, want %q", got, daemonDefaultSystemdUnit)
	}
	if got := daemonSystemdUnitName(func(name string) string {
		if name == "WB_DAEMON_SYSTEMD_UNIT" {
			return "my-custom-wb.service"
		}
		return ""
	}); got != "my-custom-wb.service" {
		t.Fatalf("configured unit name = %q", got)
	}
	if got := daemonSystemdUnitName(nil); got != daemonDefaultSystemdUnit {
		t.Fatalf("nil getenv = %q, want default", got)
	}
}

// daemonSupervisorPresent's systemd branch matches only the known
// `is-system-running` states; anything else — an unrecognized state, or
// systemctl being entirely absent — counts as not present
// (sneat-dev/wb#622 review item 5).
func TestDaemonSupervisorPresentSystemdOnlyMatchesKnownIsSystemRunningStates(t *testing.T) {
	previous := runSystemctl
	t.Cleanup(func() { runSystemctl = previous })
	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{"running", "running\n", true},
		{"degraded", "degraded\n", true},
		{"starting", "starting\n", true},
		{"initializing", "initializing\n", true},
		{"maintenance", "maintenance\n", true},
		{"stopping", "stopping\n", true},
		{"offline", "offline\n", false},
		{"empty (systemctl absent or unreachable)", "", false},
		{"an unrecognized state", "some-unrecognized-state\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runSystemctl = func(args ...string) ([]byte, error) { return []byte(tc.output), nil }
			present, _ := daemonSupervisorPresent(daemon.SupervisorSystemd, "")
			if present != tc.want {
				t.Fatalf("present(%q) = %t, want %t", tc.output, present, tc.want)
			}
		})
	}
}

func TestDaemonSupervisorPresentSystemdWhenSystemctlIsAbsent(t *testing.T) {
	previous := runSystemctl
	t.Cleanup(func() { runSystemctl = previous })
	runSystemctl = func(args ...string) ([]byte, error) {
		return nil, errors.New(`exec: "systemctl": executable file not found in $PATH`)
	}
	present, _ := daemonSupervisorPresent(daemon.SupervisorSystemd, "")
	if present {
		t.Fatal("systemctl being absent must report not present")
	}
}

// runSystemctl's DEFAULT implementation must not hang indefinitely on an
// unresponsive or wedged systemd user manager: it is bounded by
// runSystemctlTimeout (sneat-dev/wb#622 review round 3, item M4). Exercised
// through the seam with a real (but fast-killed) subprocess, not a fake
// runSystemctl override, since the whole point is to prove the DEFAULT
// closure's own timeout wiring.
//
// The fake script uses `exec sleep`, not a bare `sleep` command, so the
// shell replaces its own process image rather than forking sleep as a
// child: a forked grandchild inherits the stdout/stderr pipes
// CombinedOutput reads, and killing only the DIRECT child (what
// exec.CommandContext does on its own) leaves that grandchild holding them
// open — Wait then blocks until the grandchild independently exits, which
// hung this exact test for 35 minutes on Linux CI (sneat-dev/wb#622 review
// round 4) despite runSystemctlTimeout firing correctly. `exec` is the
// belt; runSystemctl's own command.WaitDelay (see daemon.go) is the
// suspenders — the actual fix for a REAL wedged systemctl that forks a real
// grandchild, which this test cannot control the shape of.
func TestRunSystemctlDefaultTimesOutRatherThanHangingForever(t *testing.T) {
	previousTimeout := runSystemctlTimeout
	runSystemctlTimeout = 50 * time.Millisecond
	t.Cleanup(func() { runSystemctlTimeout = previousTimeout })

	dir := t.TempDir()
	fakeSystemctl := filepath.Join(dir, "systemctl")
	if err := os.WriteFile(fakeSystemctl, []byte("#!/bin/sh\nexec sleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	start := time.Now()
	if _, err := runSystemctl("--user", "is-system-running"); err == nil {
		t.Fatal("expected the hanging fake systemctl to be killed by the timeout")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("runSystemctl took %s, want it bounded near runSystemctlTimeout (%s) plus WaitDelay", elapsed, runSystemctlTimeout)
	}
}

// wb's own self-managed launchd job's executable-handoff path (a live
// process running a different binary) must go through the ordinary launch
// path — wb's own bootstrap+kickstart cycle IS its restart mechanism —
// never through waitForSupervisorReplacement, which would wait for a
// foreign supervisor that does not exist (sneat-dev/wb#622 review item 3).
func TestStartAndRestartHandoffUnderWBsOwnLaunchdJobTakeTheLaunchPathNotTheSupervisorWait(t *testing.T) {
	for _, scenario := range []struct {
		name string
		run  func(controller daemonController) (daemonResult, error)
	}{
		{"start", func(controller daemonController) (daemonResult, error) {
			return controller.Start(context.Background(), daemonDefaultListen)
		}},
		{"restart", func(controller daemonController) (daemonResult, error) {
			return controller.Restart(context.Background(), false)
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root := daemonTestRoot(t)
			deps := daemonTestDependencies(t, root)

			// A PID far outside the 900-series counter daemonTestDependencies'
			// own default start/alive fakes use, so the old (about to be
			// stopped) process and the new (about to be launched) one can
			// never coincide on the same synthetic PID.
			const oldPID = 5000
			old := daemonSupervisorTestInstalledOld(t, root, daemonDefaultListen, daemon.SupervisorLaunchd, deps.now())
			old.SupervisorLabel = daemonLaunchdLabel
			old.MarkReadyWithProcess(oldPID, deps.now(), deps.now())
			controller := newDaemonController(deps, root)
			if err := controller.store.Save(old); err != nil {
				t.Fatal(err)
			}

			oldAlive := true
			originalAlive := deps.alive
			deps.alive = func(pid int) bool {
				if pid == oldPID {
					return oldAlive
				}
				return originalAlive(pid)
			}
			originalStop := deps.stop
			deps.stop = func(pid int, supervisor daemon.Supervisor, label string) error {
				if pid == oldPID {
					oldAlive = false
				}
				return originalStop(pid, supervisor, label)
			}
			launched := false
			originalStart := deps.start
			deps.start = func(executable string, args []string, logPath string) (int, error) {
				launched = true
				return originalStart(executable, args, logPath)
			}
			controller.deps = deps

			result, err := scenario.run(controller)
			if err != nil {
				t.Fatalf("%s under wb's own launchd job: %v", scenario.name, err)
			}
			if !launched {
				t.Fatalf("%s under wb's own launchd job must take the launch path, not wait for a foreign supervisor that does not exist", scenario.name)
			}
			if !result.ProcessManagerRunning {
				t.Fatalf("%s result = %#v", scenario.name, result)
			}
		})
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

	// An implicit Start (exactly the shape `wb dashboard --local` and
	// daemon_rpc.go's daemonOperationClient reach) must not fail outright
	// merely because THIS invocation's own binary differs from a healthy
	// running supervised daemon's — that would break every such caller for a
	// production daemon it never asked to manage the lifecycle of
	// (sneat-dev/wb#622 review item 9). It gets the live daemon back, with
	// the mismatch reported as a warning, not an error.
	result, err := controller.Start(context.Background(), daemonDefaultListen)
	if err != nil {
		t.Fatalf("an unrelated binary's implicit Start must succeed against a live supervised daemon: %v", err)
	}
	if result.ProvenanceMatches {
		t.Fatal("provenance must be reported as not matching")
	}
	if !result.ProcessManagerRunning {
		t.Fatal("the live supervised daemon must still be reported as running")
	}
	for _, want := range []string{"does not match", "leaving it running"} {
		if !strings.Contains(result.Warning, want) {
			t.Fatalf("warning %q does not mention %q", result.Warning, want)
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

// wb's own self-managed launchd job's Stop() already boots the job out
// completely — unlike a foreign job's KeepAlive, nothing is left to bring it
// back — so the launchd remedy hint printed after `wb daemon stop` is false
// on every Mac stop of wb's own daemon (sneat-dev/wb#622 review item 1). A
// genuinely foreign launchd label still gets the real remedy, and an
// old/legacy record with no recorded label predates any foreign-job concept
// (so it is almost certainly wb's own too) and is treated the same way.
func TestDaemonStopHintNamesTheRealRemedyOnlyForAForeignLaunchdLabel(t *testing.T) {
	cases := []struct {
		name     string
		label    string
		wantHint bool
	}{
		{"wb's own launchd job prints no false hint", daemonLaunchdLabel, false},
		{"a legacy record with no recorded label is treated as wb's own", "", false},
		{"a foreign launchd label prints the real remedy", "com.example.foreign-wb-supervisor", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := daemonTestRoot(t)
			deps := daemonTestDependencies(t, root)
			now := deps.now()
			state := daemonSupervisorTestInstalledOld(t, root, daemonDefaultListen, daemon.SupervisorLaunchd, now)
			state.SupervisorLabel = tc.label
			state.MarkReadyWithProcess(901, now, now)
			controller := newDaemonController(deps, root)
			if err := controller.store.Save(state); err != nil {
				t.Fatal(err)
			}
			// deps.stop MUST flip aliveness: daemonTestDependencies' now()
			// is a fixed clock and its sleep is a no-op, so stop()'s poll
			// loop (daemon.go's deadline := controller.deps.now().Add(...))
			// never advances on its own — a stop fake that does not mark the
			// process dead spins forever instead of failing fast.
			alive := true
			deps.alive = func(pid int) bool { return pid == 901 && alive }
			deps.stop = func(int, daemon.Supervisor, string) error { alive = false; return nil }

			projectsRoot := root
			command := newDaemonStopCmd(&invocation{projectsRoot: projectsRoot}, deps)
			var stdout, stderr bytes.Buffer
			command.SetOut(&stdout)
			command.SetErr(&stderr)
			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			hasHint := strings.Contains(stderr.String(), "launchctl bootout")
			if hasHint != tc.wantHint {
				t.Fatalf("hint present = %t, want %t; stderr=%q", hasHint, tc.wantHint, stderr.String())
			}
		})
	}
}

// The systemd stop hint is unconditional: nothing in this build installs or
// manages its own systemd unit the way it does its own launchd job, so there
// is no "wb's own unit" exemption to make for it.
func TestDaemonStopHintNamesTheSystemdRemedyUnconditionally(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	now := deps.now()
	state := daemonSupervisorTestInstalledOld(t, root, daemonDefaultListen, daemon.SupervisorSystemd, now)
	state.MarkReadyWithProcess(901, now, now)
	controller := newDaemonController(deps, root)
	if err := controller.store.Save(state); err != nil {
		t.Fatal(err)
	}
	// See TestDaemonStopHintNamesTheRealRemedyOnlyForAForeignLaunchdLabel for
	// why deps.stop must flip aliveness rather than being a bare no-op.
	alive := true
	deps.alive = func(pid int) bool { return pid == 901 && alive }
	deps.stop = func(int, daemon.Supervisor, string) error { alive = false; return nil }

	projectsRoot := root
	command := newDaemonStopCmd(&invocation{projectsRoot: projectsRoot}, deps)
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "systemctl --user stop") {
		t.Fatalf("expected the systemd hint, got %q", stderr.String())
	}
}

// markStoppedIfUnchanged is the CAS write stop() uses so a concurrent
// replacement (a racing supervisor restart, or another goroutine's own
// stop-then-launch) that already wrote a NEW record for a NEW PID/OwnerToken
// between the caller's read and this write is never clobbered
// (sneat-dev/wb#622 review item 11).
func TestMarkStoppedIfUnchangedDoesNotOverwriteAConcurrentReplacement(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := newDaemonController(deps, root)

	original := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner-token-1", deps.now())
	original.MarkReadyWithProcess(901, deps.now(), deps.now())
	if err := controller.store.Save(original); err != nil {
		t.Fatal(err)
	}

	// Simulate the race: something else already wrote a NEW ready record (a
	// different PID and owner token) before this call runs.
	replacement := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner-token-2", deps.now())
	replacement.MarkReadyWithProcess(902, deps.now(), deps.now())
	if err := controller.store.Save(replacement); err != nil {
		t.Fatal(err)
	}

	result, err := controller.markStoppedIfUnchanged(original, 901, "owner-token-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.PID != 902 || result.Status != daemon.StatusReady {
		t.Fatalf("markStoppedIfUnchanged clobbered a concurrent replacement: %#v", result)
	}
	stillThere, found, loadErr := controller.store.Load()
	if loadErr != nil || !found || stillThere.PID != 902 || stillThere.Status != daemon.StatusReady {
		t.Fatalf("the concurrent replacement's own record was disturbed: %#v, %t, %v", stillThere, found, loadErr)
	}
}

// The matching case: nothing raced it, so the expected PID/OwnerToken are
// still current, and the write proceeds normally.
func TestMarkStoppedIfUnchangedWritesWhenNothingRaced(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := newDaemonController(deps, root)

	original := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner-token-1", deps.now())
	original.MarkReadyWithProcess(901, deps.now(), deps.now())
	if err := controller.store.Save(original); err != nil {
		t.Fatal(err)
	}

	result, err := controller.markStoppedIfUnchanged(original, 901, "owner-token-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != daemon.StatusStopped {
		t.Fatalf("markStoppedIfUnchanged = %#v, want Stopped", result)
	}
	stillThere, found, loadErr := controller.store.Load()
	if loadErr != nil || !found || stillThere.Status != daemon.StatusStopped {
		t.Fatalf("the record was not persisted as stopped: %#v, %t, %v", stillThere, found, loadErr)
	}
}

// found=false (the record vanished entirely between the caller's read and
// this call — for example a concurrent `wb daemon recover` reclaiming it)
// also leaves nothing to overwrite: it returns the caller's own expected
// state as a best-effort answer rather than fabricating a stopped record for
// a state this store no longer holds.
func TestMarkStoppedIfUnchangedWhenTheRecordIsGone(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := newDaemonController(deps, root)

	expected := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner-token-1", deps.now())
	expected.MarkReadyWithProcess(901, deps.now(), deps.now())
	// Deliberately never saved: the store holds nothing at all.

	result, err := controller.markStoppedIfUnchanged(expected, 901, "owner-token-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.PID != expected.PID {
		t.Fatalf("markStoppedIfUnchanged = %#v, want the caller's own expected state back", result)
	}
	if _, found, loadErr := controller.store.Load(); loadErr != nil || found {
		t.Fatalf("a stopped record must not be fabricated for a state the store no longer holds: found=%t err=%v", found, loadErr)
	}
}
