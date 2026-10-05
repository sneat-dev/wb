package daemonruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/testenv"
)

func TestDaemonSystemdUnitNameNormalizesAMissingServiceSuffix(t *testing.T) {
	t.Parallel()
	unsuffixed := func(name string) string {
		if name == "WB_DAEMON_SYSTEMD_UNIT" {
			return "my-custom-wb"
		}
		return ""
	}
	if got := DaemonSystemdUnitName(unsuffixed); got != "my-custom-wb.service" {
		t.Fatalf("unit name = %q, want the .service suffix appended", got)
	}
	suffixed := func(name string) string {
		if name == "WB_DAEMON_SYSTEMD_UNIT" {
			return "my-custom-wb.service"
		}
		return ""
	}
	if got := DaemonSystemdUnitName(suffixed); got != "my-custom-wb.service" {
		t.Fatalf("unit name = %q, want it left unchanged", got)
	}
}

func TestDaemonStatusPrefersTheDaemonsOwnRecordedSystemdUnitOverTheInvokersConfig(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	state := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner", deps.Now())
	state.SystemdUnit = "wb.service"
	state.MarkReadyWithProcess(901, deps.Now(), deps.Now())
	if err := (daemon.Store{Path: mustDaemonPath(t, StatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.Alive = func(pid int) bool { return pid == 901 }
	deps.ObservedSupervisor = func(int) (daemon.Supervisor, bool) { return daemon.SupervisorNone, true }
	deps.SystemdUnitName = func() string { return "wb-daemon.service" }
	deps.SystemdUnitState = func(unit string) (daemon.SystemdUnitState, bool) {
		if unit != "wb.service" {
			t.Fatalf("systemdUnitState probed %q, want the daemon's own recorded unit wb.service", unit)
		}
		return daemon.SystemdUnitState{ActiveState: "failed", NRestarts: 3}, true
	}

	result, err := NewController(deps, root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.SupervisorMismatch == "" || !strings.Contains(result.SupervisorMismatch, "wb.service") {
		t.Fatalf("mismatch = %q, want it naming the daemon's own recorded unit", result.SupervisorMismatch)
	}
}

func TestRestartOfASupervisedDaemonWaitsForTheSupervisorInsteadOfLaunching(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	now := deps.Now()
	deps.Now = func() time.Time { return now }
	controller := NewController(deps, root)

	old := daemonSupervisorTestInstalledOld(t, root, DefaultListen, daemon.SupervisorSystemd, now)
	old.MarkReadyWithProcess(901, now, now)
	if err := controller.store.Save(old); err != nil {
		t.Fatal(err)
	}
	alive := map[int]bool{901: true}
	deps.Alive = func(pid int) bool { return alive[pid] }
	deps.Stop = func(pid int, supervisor daemon.Supervisor, label string) error {
		if pid != 901 {
			t.Fatalf("stop signalled unexpected pid %d", pid)
		}
		if supervisor != daemon.SupervisorSystemd || label != "" {
			t.Fatalf("stop supervisor context = %s, %q", supervisor, label)
		}
		alive[901] = false
		return nil
	}
	// stop() sees the old process dead on its very first check (deps.Stop set
	// that synchronously above) and writes its own MarkStopped record before
	// this ever runs, so the poll loop's *first* sleep — after it has already
	// observed "not ready yet" once — is where the supervisor's replacement
	// appears. Nothing here writes it any earlier: that would race stop()'s
	// own final write the way a synchronous test double otherwise would,
	// which a real supervisor (always slower than one poll tick) never does.
	replaced := false
	deps.Sleep = func(d time.Duration) {
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
		current, provErr := controller.Provenance()
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
	deps.Start = func(string, []string, string) (int, error) {
		t.Fatal("a supervised restart must not launch a detached daemon")
		return 0, nil
	}
	deps.OwnedHealth = func(context.Context, string, int, uint64) error { return nil }
	controller.deps = deps

	result, err := controller.RestartWithProgress(context.Background(), false, nil, false)
	if err != nil {
		t.Fatalf("supervised restart: %v", err)
	}
	if !result.ReadyVerified || result.State.PID != 902 || !result.AutomaticVersionHandoff {
		t.Fatalf("supervised restart result = %#v", result)
	}
}

func TestStopAndReplaceTreatsALegacyRecordAsSupervisedWhenCgroupConfirmsSystemd(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	now := deps.Now()
	deps.Now = func() time.Time { return now }
	controller := NewController(deps, root)

	old := daemonSupervisorTestInstalledOld(t, root, DefaultListen, "", now) // legacy: predates the field
	old.MarkReadyWithProcess(901, now, now)
	if err := controller.store.Save(old); err != nil {
		t.Fatal(err)
	}
	alive := map[int]bool{901: true}
	deps.Alive = func(pid int) bool { return alive[pid] }
	deps.Stop = func(pid int, supervisor daemon.Supervisor, label string) error {
		alive[901] = false
		return nil
	}
	deps.ObservedSupervisor = func(pid int) (daemon.Supervisor, bool) {
		if pid != 901 {
			t.Fatalf("observedSupervisor probed unexpected pid %d", pid)
		}
		return daemon.SupervisorSystemd, true
	}
	replaced := false
	deps.Sleep = func(d time.Duration) {
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
		current, provErr := controller.Provenance()
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
	deps.Start = func(string, []string, string) (int, error) {
		t.Fatal("a legacy record independently confirmed as systemd-managed must not launch a detached daemon")
		return 0, nil
	}
	deps.OwnedHealth = func(context.Context, string, int, uint64) error { return nil }
	controller.deps = deps

	result, err := controller.RestartWithProgress(context.Background(), false, nil, false)
	if err != nil {
		t.Fatalf("legacy-record supervised restart: %v", err)
	}
	if !result.ReadyVerified || result.State.PID != 902 || !result.AutomaticVersionHandoff {
		t.Fatalf("legacy-record supervised restart result = %#v", result)
	}
}

func TestDaemonStartHandoffTreatsALegacyRecordAsSupervisedWhenCgroupConfirmsSystemd(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	now := deps.Now()
	deps.Now = func() time.Time { return now }
	controller := NewController(deps, root)

	old := daemonSupervisorTestInstalledOld(t, root, DefaultListen, "", now)
	old.MarkReadyWithProcess(901, now, now)
	if err := controller.store.Save(old); err != nil {
		t.Fatal(err)
	}
	alive := map[int]bool{901: true}
	deps.Alive = func(pid int) bool { return alive[pid] }
	deps.Stop = func(pid int, supervisor daemon.Supervisor, label string) error {
		alive[901] = false
		return nil
	}
	deps.ObservedSupervisor = func(pid int) (daemon.Supervisor, bool) { return daemon.SupervisorSystemd, true }
	replaced := false
	deps.Sleep = func(d time.Duration) {
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
		current, provErr := controller.Provenance()
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
	deps.Start = func(string, []string, string) (int, error) {
		t.Fatal("an implicit Start against a legacy record independently confirmed as systemd-managed must not launch a detached daemon")
		return 0, nil
	}
	deps.OwnedHealth = func(context.Context, string, int, uint64) error { return nil }
	controller.deps = deps

	result, err := controller.Start(context.Background(), DefaultListen)
	if err != nil {
		t.Fatalf("legacy-record supervised start handoff: %v", err)
	}
	if !result.ReadyVerified || result.State.PID != 902 || !result.AutomaticVersionHandoff {
		t.Fatalf("legacy-record supervised start handoff result = %#v", result)
	}
}

func TestStopAndReplaceLeavesAGenuinelyUnsupervisedRecordUnchanged(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		observed func(int) (daemon.Supervisor, bool)
	}{
		{"cgroup confirms no systemd membership", func(int) (daemon.Supervisor, bool) { return daemon.SupervisorNone, true }},
		{"no independent observation available", func(int) (daemon.Supervisor, bool) { return "", false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := daemonTestRoot(t)
			deps := daemonTestDependencies(t, root)
			controller := NewController(deps, root)

			old := daemonSupervisorTestInstalledOld(t, root, DefaultListen, daemon.SupervisorNone, deps.Now())
			old.MarkReadyWithProcess(901, deps.Now(), deps.Now())
			if err := controller.store.Save(old); err != nil {
				t.Fatal(err)
			}
			aliveSet := map[int]bool{901: true}
			deps.Alive = func(pid int) bool { return aliveSet[pid] }
			deps.Stop = func(pid int, _ daemon.Supervisor, _ string) error { aliveSet[901] = false; return nil }
			deps.ObservedSupervisor = tc.observed
			launched := false
			deps.Start = func(_ string, args []string, _ string) (int, error) {
				launched = true
				for _, argument := range args {
					if argument == "--lifecycle-state" {
						t.Fatalf("daemon start pinned a resolved lifecycle path: %v", args)
					}
				}
				aliveSet[902] = true
				return 902, nil
			}
			deps.OwnedHealth = func(context.Context, string, int, uint64) error { return nil }
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

func TestRestartOfASupervisedDaemonTimesOutWithoutLaunchingADetachedReplacement(t *testing.T) {
	t.Parallel()
	lifecycleBounds := DefaultLifecycleBounds()

	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.Bounds = func() LifecycleBounds { return lifecycleBounds }
	now := deps.Now()
	deps.Now = func() time.Time { return now }
	deps.Sleep = func(d time.Duration) { now = now.Add(d) }
	controller := NewController(deps, root)

	old := daemonSupervisorTestInstalledOld(t, root, DefaultListen, daemon.SupervisorSystemd, now)
	old.MarkReadyWithProcess(901, now, now)
	if err := controller.store.Save(old); err != nil {
		t.Fatal(err)
	}
	alive := map[int]bool{901: true}
	deps.Alive = func(pid int) bool { return alive[pid] }
	deps.Stop = func(pid int, _ daemon.Supervisor, _ string) error { alive[pid] = false; return nil }
	// The supervisor never brings a replacement back: no pid ever appears
	// alive again after 901 stops.
	deps.Start = func(string, []string, string) (int, error) {
		t.Fatal("a timed-out supervised restart must not launch a detached daemon")
		return 0, nil
	}
	controller.deps = deps

	previous := lifecycleBounds.SupervisorRestart
	lifecycleBounds.SupervisorRestart = 2 * time.Second
	t.Cleanup(func() { lifecycleBounds.SupervisorRestart = previous })

	_, err := controller.RestartWithProgress(context.Background(), false, nil, false)
	if err == nil || !strings.Contains(err.Error(), "did not restart the daemon") || !strings.Contains(err.Error(), "systemd") {
		t.Fatalf("timeout error = %v", err)
	}
	stopped, found, loadErr := controller.store.Load()
	if loadErr != nil || !found || stopped.Status != daemon.StatusStopped {
		t.Fatalf("state after timeout = %#v, found=%t, err=%v", stopped, found, loadErr)
	}
}

func TestWaitForSupervisorReplacementFailsFastOnADifferentBinary(t *testing.T) {
	t.Parallel()
	lifecycleBounds := DefaultLifecycleBounds()

	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.Bounds = func() LifecycleBounds { return lifecycleBounds }
	now := deps.Now()
	deps.Now = func() time.Time { return now }
	deps.Sleep = func(d time.Duration) { now = now.Add(d) }
	controller := NewController(deps, root)

	old := daemonSupervisorTestInstalledOld(t, root, DefaultListen, daemon.SupervisorSystemd, now)
	old.MarkReadyWithProcess(901, now, now)
	if err := controller.store.Save(old); err != nil {
		t.Fatal(err)
	}
	alive := map[int]bool{901: true}
	deps.Alive = func(pid int) bool { return alive[pid] }
	replacedWithWrongBinary := false
	deps.Stop = func(pid int, _ daemon.Supervisor, _ string) error {
		alive[pid] = false
		return nil
	}
	deps.Sleep = func(d time.Duration) {
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
	deps.Start = func(string, []string, string) (int, error) {
		t.Fatal("a mismatched replacement must not trigger a detached launch")
		return 0, nil
	}
	controller.deps = deps

	// A generous bound: the fail-fast must fire long before it, proving the
	// mismatch was detected rather than merely timed out.
	previous := lifecycleBounds.SupervisorRestart
	lifecycleBounds.SupervisorRestart = time.Hour
	t.Cleanup(func() { lifecycleBounds.SupervisorRestart = previous })

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

func TestWaitForSupervisorReplacementWatchesContext(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := controller.waitForSupervisorReplacement(ctx, daemon.SupervisorSystemd, daemon.Provenance{}, "restart")
	if err == nil || err != context.Canceled {
		t.Fatalf("cancelled wait = %v, want context.Canceled", err)
	}
}

func TestDaemonStartRefusesADetachedStartUnderASupervisor(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)
	stopped := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner-token", deps.Now())
	stopped.Supervisor = daemon.SupervisorSystemd
	stopped.MarkStopped(deps.Now())
	if err := controller.store.Save(stopped); err != nil {
		t.Fatal(err)
	}
	deps.Start = func(string, []string, string) (int, error) {
		t.Fatal("a refused start must not launch a daemon")
		return 0, nil
	}

	_, err := NewController(deps, root).Start(context.Background(), DefaultListen)
	if err == nil {
		t.Fatal("starting under a recorded systemd supervisor must be refused")
	}
	for _, want := range []string{"systemd", "supervisor", "wb daemon start", "--force-detached"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not mention %q", err.Error(), want)
		}
	}
}

func TestDaemonStartForceDetachedOverridesTheRefusal(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)
	stopped := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner-token", deps.Now())
	stopped.Supervisor = daemon.SupervisorSystemd
	stopped.MarkStopped(deps.Now())
	if err := controller.store.Save(stopped); err != nil {
		t.Fatal(err)
	}

	result, err := NewController(deps, root).StartWithProgress(context.Background(), DefaultListen, nil, true)
	if err != nil {
		t.Fatalf("--force-detached start: %v", err)
	}
	if !result.ProcessManagerRunning {
		t.Fatalf("forced start result = %#v", result)
	}
}

func TestDaemonStartAllowsADetachedStartWhenTheRecordedSupervisorIsStale(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)
	stopped := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner-token", deps.Now())
	stopped.Supervisor = daemon.SupervisorSystemd
	stopped.MarkStopped(deps.Now())
	if err := controller.store.Save(stopped); err != nil {
		t.Fatal(err)
	}
	deps.SupervisorPresent = func(daemon.Supervisor, string) (bool, string) { return false, "" }

	result, err := NewController(deps, root).Start(context.Background(), DefaultListen)
	if err != nil {
		t.Fatalf("start with a stale recorded supervisor: %v", err)
	}
	if !result.ProcessManagerRunning {
		t.Fatalf("start result = %#v", result)
	}
}

func TestDaemonStartRefusesWhenTheRecordedSupervisorIsConfirmedPresent(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)
	stopped := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner-token", deps.Now())
	stopped.Supervisor = daemon.SupervisorSystemd
	stopped.MarkStopped(deps.Now())
	if err := controller.store.Save(stopped); err != nil {
		t.Fatal(err)
	}
	confirmed := false
	deps.SupervisorPresent = func(supervisor daemon.Supervisor, label string) (bool, string) {
		confirmed = true
		if supervisor != daemon.SupervisorSystemd {
			t.Fatalf("supervisorPresent probed %s, want systemd", supervisor)
		}
		return true, ""
	}

	_, err := NewController(deps, root).Start(context.Background(), DefaultListen)
	if err == nil {
		t.Fatal("a confirmed-present supervisor must still be refused")
	}
	if !confirmed {
		t.Fatal("supervisorPresent was never consulted")
	}
}

func TestDaemonStartDoesNotRefuseUnderWBsOwnLaunchdJob(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)
	stopped := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner-token", deps.Now())
	stopped.Supervisor = daemon.SupervisorLaunchd
	stopped.SupervisorLabel = LaunchdLabel
	stopped.MarkStopped(deps.Now())
	if err := controller.store.Save(stopped); err != nil {
		t.Fatal(err)
	}
	deps.SupervisorPresent = func(daemon.Supervisor, string) (bool, string) {
		t.Fatal("wb's own launchd job must not even reach the existence check")
		return false, ""
	}

	result, err := NewController(deps, root).Start(context.Background(), DefaultListen)
	if err != nil {
		t.Fatalf("start under wb's own launchd job: %v", err)
	}
	if !result.ProcessManagerRunning {
		t.Fatalf("start result = %#v", result)
	}
}

func TestDaemonStartRefusesUnderLaunchdAndNamesTheRemedy(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)
	stopped := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner-token", deps.Now())
	stopped.Supervisor = daemon.SupervisorLaunchd
	stopped.SupervisorLabel = "com.example.foreign-wb-supervisor"
	stopped.MarkStopped(deps.Now())
	if err := controller.store.Save(stopped); err != nil {
		t.Fatal(err)
	}
	deps.Start = func(string, []string, string) (int, error) {
		t.Fatal("a refused start must not launch a daemon")
		return 0, nil
	}

	_, err := NewController(deps, root).Start(context.Background(), DefaultListen)
	if err == nil || !strings.Contains(err.Error(), "launchctl kickstart") || !strings.Contains(err.Error(), "com.example.foreign-wb-supervisor") {
		t.Fatalf("launchd refusal = %v", err)
	}
}

func TestDaemonStartRefusalOnADifferentListenDoesNotSayNotRunning(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)
	current, err := controller.Provenance()
	if err != nil {
		t.Fatal(err)
	}
	running := daemonTestState(t, root, "127.0.0.1:9999", current, "owner-token", deps.Now())
	running.Supervisor = daemon.SupervisorSystemd
	running.MarkReadyWithProcess(os.Getpid(), ProcessStartedAt(os.Getpid()), deps.Now())
	if err := controller.store.Save(running); err != nil {
		t.Fatal(err)
	}
	deps.Alive = func(pid int) bool { return pid == os.Getpid() }

	_, err = NewController(deps, root).Start(context.Background(), DefaultListen)
	if err == nil {
		t.Fatal("starting on a different listen while supervised must be refused")
	}
	if strings.Contains(err.Error(), "if it is not running") {
		t.Fatalf("refusal wrongly implies uncertainty about liveness: %q", err.Error())
	}
	for _, want := range []string{"already running", "127.0.0.1:9999", DefaultListen} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not mention %q", err.Error(), want)
		}
	}
}

func TestDaemonRestartIfRunningRefusesUnderASupervisorWhenNothingIsAlive(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)
	stopped := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner-token", deps.Now())
	stopped.Supervisor = daemon.SupervisorSystemd
	stopped.MarkStopped(deps.Now())
	if err := controller.store.Save(stopped); err != nil {
		t.Fatal(err)
	}
	deps.Start = func(string, []string, string) (int, error) {
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

func TestDaemonStatusFlagsASupervisorMismatchInTheOtherDirection(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	state := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner", deps.Now())
	state.Supervisor = daemon.SupervisorSystemd
	state.MarkReadyWithProcess(901, deps.Now(), deps.Now())
	if err := (daemon.Store{Path: mustDaemonPath(t, StatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.Alive = func(pid int) bool { return pid == 901 }
	deps.ObservedSupervisor = func(int) (daemon.Supervisor, bool) { return daemon.SupervisorNone, true }

	result, err := NewController(deps, root).Status(context.Background())
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

func TestDaemonStatusDoesNotFlagAgreementOrUnknownObservation(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	for name, observe := range map[string]func(int) (daemon.Supervisor, bool){
		"agrees (both none)": func(int) (daemon.Supervisor, bool) { return daemon.SupervisorNone, true },
		"unknown":            func(int) (daemon.Supervisor, bool) { return "", false },
	} {
		//nolint:paralleltest // Rows overwrite the same parent-owned private daemon state path.
		t.Run(name, func(t *testing.T) {
			deps := daemonTestDependencies(t, root)
			state := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner", deps.Now())
			state.MarkReadyWithProcess(901, deps.Now(), deps.Now())
			if err := (daemon.Store{Path: mustDaemonPath(t, StatePath, root)}).Save(state); err != nil {
				t.Fatal(err)
			}
			deps.Alive = func(pid int) bool { return pid == 901 }
			deps.ObservedSupervisor = observe

			result, err := NewController(deps, root).Status(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if result.SupervisorMismatch != "" {
				t.Fatalf("unexpected mismatch: %q", result.SupervisorMismatch)
			}
		})
	}
}

func TestDaemonStatusSkipsSupervisorMismatchWhenTheSeamIsNil(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.ObservedSupervisor = nil
	state := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner", deps.Now())
	state.MarkReadyWithProcess(901, deps.Now(), deps.Now())
	if err := (daemon.Store{Path: mustDaemonPath(t, StatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.Alive = func(pid int) bool { return pid == 901 }

	result, err := NewController(deps, root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.SupervisorMismatch != "" {
		t.Fatalf("unexpected mismatch: %q", result.SupervisorMismatch)
	}
}

func TestDaemonStatusFlagsAFailedSystemdUnitEvenWithoutACgroupMismatch(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	state := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner", deps.Now())
	state.MarkReadyWithProcess(901, deps.Now(), deps.Now())
	if err := (daemon.Store{Path: mustDaemonPath(t, StatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.Alive = func(pid int) bool { return pid == 901 }
	deps.ObservedSupervisor = func(int) (daemon.Supervisor, bool) { return daemon.SupervisorNone, true }
	deps.SystemdUnitName = func() string { return "wb-daemon.service" }
	deps.SystemdUnitState = func(unit string) (daemon.SystemdUnitState, bool) {
		if unit != "wb-daemon.service" {
			t.Fatalf("systemdUnitState probed unexpected unit %q", unit)
		}
		return daemon.SystemdUnitState{ActiveState: "failed", NRestarts: 4468}, true
	}

	result, err := NewController(deps, root).Status(context.Background())
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

func TestDaemonStatusDoesNotFlagAHealthySystemdUnit(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	for name, unitState := range map[string]daemon.SystemdUnitState{
		"active":                   {ActiveState: "active"},
		"activating, no restarts":  {ActiveState: "activating", NRestarts: 0},
		"inactive (never started)": {ActiveState: "inactive"},
	} {
		//nolint:paralleltest // Rows overwrite the same parent-owned private daemon state path.
		t.Run(name, func(t *testing.T) {
			deps := daemonTestDependencies(t, root)
			state := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner", deps.Now())
			state.MarkReadyWithProcess(901, deps.Now(), deps.Now())
			if err := (daemon.Store{Path: mustDaemonPath(t, StatePath, root)}).Save(state); err != nil {
				t.Fatal(err)
			}
			deps.Alive = func(pid int) bool { return pid == 901 }
			deps.ObservedSupervisor = func(int) (daemon.Supervisor, bool) { return daemon.SupervisorNone, true }
			deps.SystemdUnitName = func() string { return "wb-daemon.service" }
			deps.SystemdUnitState = func(string) (daemon.SystemdUnitState, bool) { return unitState, true }

			result, err := NewController(deps, root).Status(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if result.SupervisorMismatch != "" {
				t.Fatalf("unexpected mismatch: %q", result.SupervisorMismatch)
			}
		})
	}
}

func TestDaemonStatusSystemdUnitDetectorOnlyAppliesWhenRecordedIsNone(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	state := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner", deps.Now())
	state.Supervisor = daemon.SupervisorSystemd
	state.MarkReadyWithProcess(901, deps.Now(), deps.Now())
	if err := (daemon.Store{Path: mustDaemonPath(t, StatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.Alive = func(pid int) bool { return pid == 901 }
	deps.ObservedSupervisor = func(int) (daemon.Supervisor, bool) { return daemon.SupervisorSystemd, true }
	called := false
	deps.SystemdUnitName = func() string { return "wb-daemon.service" }
	deps.SystemdUnitState = func(string) (daemon.SystemdUnitState, bool) {
		called = true
		return daemon.SystemdUnitState{ActiveState: "failed", NRestarts: 1}, true
	}

	result, err := NewController(deps, root).Status(context.Background())
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

func TestDaemonStatusFallsBackToCgroupWhenSystemdUnitSeamsAreNil(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.SystemdUnitState = nil
	deps.SystemdUnitName = nil
	state := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner", deps.Now())
	state.MarkReadyWithProcess(901, deps.Now(), deps.Now())
	if err := (daemon.Store{Path: mustDaemonPath(t, StatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.Alive = func(pid int) bool { return pid == 901 }
	deps.ObservedSupervisor = func(int) (daemon.Supervisor, bool) { return daemon.SupervisorSystemd, true }

	result, err := NewController(deps, root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.SupervisorMismatch == "" {
		t.Fatal("expected the cgroup-based fallback to still flag the mismatch")
	}
}

func TestDaemonObservedSystemdUnitStateParsesFakeOutput(t *testing.T) {
	t.Parallel()
	native := defaultNativeOperations()
	commandBounds := defaultNativeCommandBounds()
	native.commandBounds = func() nativeCommandBounds { return commandBounds }

	previous := native.runSystemctl
	t.Cleanup(func() { native.runSystemctl = previous })
	var capturedArgs []string
	native.runSystemctl = func(args ...string) ([]byte, error) {
		capturedArgs = args
		return []byte("ActiveState=failed\nResult=exit-code\nNRestarts=4468\n"), nil
	}
	state, known := native.daemonObservedSystemdUnitState("wb-daemon.service")
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
	t.Parallel()
	native := defaultNativeOperations()
	commandBounds := defaultNativeCommandBounds()
	native.commandBounds = func() nativeCommandBounds { return commandBounds }

	previous := native.runSystemctl
	t.Cleanup(func() { native.runSystemctl = previous })
	native.runSystemctl = func(args ...string) ([]byte, error) {
		return nil, errors.New(`exec: "systemctl": executable file not found in $PATH`)
	}
	if _, known := native.daemonObservedSystemdUnitState("wb-daemon.service"); known {
		t.Fatal("systemctl being absent must report unknown, not a healthy unit")
	}
}

func TestDaemonObservedSystemdUnitStateRejectsAnEmptyUnitName(t *testing.T) {
	t.Parallel()
	native := defaultNativeOperations()
	commandBounds := defaultNativeCommandBounds()
	native.commandBounds = func() nativeCommandBounds { return commandBounds }

	if _, known := native.daemonObservedSystemdUnitState("   "); known {
		t.Fatal("an empty unit name must report unknown")
	}
}

func TestDaemonSystemdUnitNameDefaultsAndReadsAConfiguredOverride(t *testing.T) {
	t.Parallel()
	if got := DaemonSystemdUnitName(func(string) string { return "" }); got != daemonDefaultSystemdUnit {
		t.Fatalf("default unit name = %q, want %q", got, daemonDefaultSystemdUnit)
	}
	if got := DaemonSystemdUnitName(func(name string) string {
		if name == "WB_DAEMON_SYSTEMD_UNIT" {
			return "my-custom-wb.service"
		}
		return ""
	}); got != "my-custom-wb.service" {
		t.Fatalf("configured unit name = %q", got)
	}
	if got := DaemonSystemdUnitName(nil); got != daemonDefaultSystemdUnit {
		t.Fatalf("nil getenv = %q, want default", got)
	}
}

func TestDaemonSupervisorPresentSystemdOnlyMatchesKnownIsSystemRunningStates(t *testing.T) {
	t.Parallel()
	native := defaultNativeOperations()
	commandBounds := defaultNativeCommandBounds()
	native.commandBounds = func() nativeCommandBounds { return commandBounds }

	previous := native.runSystemctl
	t.Cleanup(func() { native.runSystemctl = previous })
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
		//nolint:paralleltest // Rows replace the same parent-owned native runSystemctl callback.
		t.Run(tc.name, func(t *testing.T) {
			native.runSystemctl = func(args ...string) ([]byte, error) { return []byte(tc.output), nil }
			present, _ := native.daemonSupervisorPresent(daemon.SupervisorSystemd, "")
			if present != tc.want {
				t.Fatalf("present(%q) = %t, want %t", tc.output, present, tc.want)
			}
		})
	}
}

func TestDaemonSupervisorPresentSystemdWhenSystemctlIsAbsent(t *testing.T) {
	t.Parallel()
	native := defaultNativeOperations()
	commandBounds := defaultNativeCommandBounds()
	native.commandBounds = func() nativeCommandBounds { return commandBounds }

	previous := native.runSystemctl
	t.Cleanup(func() { native.runSystemctl = previous })
	native.runSystemctl = func(args ...string) ([]byte, error) {
		return nil, errors.New(`exec: "systemctl": executable file not found in $PATH`)
	}
	present, _ := native.daemonSupervisorPresent(daemon.SupervisorSystemd, "")
	if present {
		t.Fatal("systemctl being absent must report not present")
	}
}

//nolint:paralleltest // This native fixture or its helper changes process-wide HOME, PATH or supervisor environment; testing restores it.
func TestRunSystemctlDefaultTimesOutRatherThanHangingForever(t *testing.T) {
	native := defaultNativeOperations()
	commandBounds := defaultNativeCommandBounds()
	native.commandBounds = func() nativeCommandBounds { return commandBounds }

	previousTimeout := commandBounds.Systemctl
	commandBounds.Systemctl = 50 * time.Millisecond
	t.Cleanup(func() { commandBounds.Systemctl = previousTimeout })

	dir := t.TempDir()
	fakeSystemctl := filepath.Join(dir, "systemctl")
	if err := testenv.WriteExecutableFile(fakeSystemctl, []byte("#!/bin/sh\nexec sleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	start := time.Now()
	if _, err := native.runSystemctl("--user", "is-system-running"); err == nil {
		t.Fatal("expected the hanging fake systemctl to be killed by the timeout")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("runSystemctl took %s, want it bounded near runSystemctlTimeout (%s) plus WaitDelay", elapsed, commandBounds.Systemctl)
	}
}

func TestStartAndRestartHandoffUnderWBsOwnLaunchdJobTakeTheLaunchPathNotTheSupervisorWait(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name string
		run  func(controller Controller) (Result, error)
	}{
		{"start", func(controller Controller) (Result, error) {
			return controller.Start(context.Background(), DefaultListen)
		}},
		{"restart", func(controller Controller) (Result, error) {
			return controller.RestartWithProgress(context.Background(), false, nil, false)
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			root := daemonTestRoot(t)
			deps := daemonTestDependencies(t, root)

			// A PID far outside the 900-series counter daemonTestDependencies'
			// own default start/alive fakes use, so the old (about to be
			// stopped) process and the new (about to be launched) one can
			// never coincide on the same synthetic PID.
			const oldPID = 5000
			old := daemonSupervisorTestInstalledOld(t, root, DefaultListen, daemon.SupervisorLaunchd, deps.Now())
			old.SupervisorLabel = LaunchdLabel
			old.MarkReadyWithProcess(oldPID, deps.Now(), deps.Now())
			controller := NewController(deps, root)
			if err := controller.store.Save(old); err != nil {
				t.Fatal(err)
			}

			oldAlive := true
			originalAlive := deps.Alive
			deps.Alive = func(pid int) bool {
				if pid == oldPID {
					return oldAlive
				}
				return originalAlive(pid)
			}
			originalStop := deps.Stop
			deps.Stop = func(pid int, supervisor daemon.Supervisor, label string) error {
				if pid == oldPID {
					oldAlive = false
				}
				return originalStop(pid, supervisor, label)
			}
			launched := false
			originalStart := deps.Start
			deps.Start = func(executable string, args []string, logPath string) (int, error) {
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

func TestDaemonStartHandsOffAVersionMismatchToASupervisorInsteadOfLaunching(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	now := deps.Now()
	deps.Now = func() time.Time { return now }
	controller := NewController(deps, root)

	old := daemonSupervisorTestInstalledOld(t, root, DefaultListen, daemon.SupervisorSystemd, now)
	old.MarkReadyWithProcess(901, now, now)
	if err := controller.store.Save(old); err != nil {
		t.Fatal(err)
	}
	alive := map[int]bool{901: true}
	deps.Alive = func(pid int) bool { return alive[pid] }
	deps.Stop = func(pid int, _ daemon.Supervisor, _ string) error { alive[pid] = false; return nil }
	// See TestRestartOfASupervisedDaemonWaitsForTheSupervisorInsteadOfLaunching
	// for why the replacement is written from the poll loop's first sleep
	// rather than from deps.Stop directly.
	replaced := false
	deps.Sleep = func(d time.Duration) {
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
		current, provErr := controller.Provenance()
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
	deps.Start = func(string, []string, string) (int, error) {
		t.Fatal("a supervised version-mismatch handoff must not launch a detached daemon")
		return 0, nil
	}
	deps.OwnedHealth = func(context.Context, string, int, uint64) error { return nil }
	controller.deps = deps

	result, err := controller.Start(context.Background(), DefaultListen)
	if err != nil {
		t.Fatalf("supervised start handoff: %v", err)
	}
	if result.State.PID != 902 {
		t.Fatalf("start handoff result = %#v", result)
	}
}

func TestDaemonStartDoesNotTouchASupervisedDaemonForAnUnrelatedBinary(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)

	// The recorded executable path is real, but its CURRENT on-disk content
	// (still "old installed binary", from daemonTestDependencies) never
	// becomes this test's own provenance: nothing here performs a
	// self-update, so the two SHAs never converge.
	unrelated := filepath.Join(root, "wb-unrelated-worktree-build")
	if err := testenv.WriteExecutableFile(unrelated, []byte("an unrelated worktree build"), 0o700); err != nil {
		t.Fatal(err)
	}
	deps.Executable = func() (string, error) { return unrelated, nil }
	controller.deps = deps

	old := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: filepath.Join(root, "wb"), SHA256: "the-real-running-daemons-sha", Version: "production"}, "owner-token", deps.Now())
	old.Supervisor = daemon.SupervisorSystemd
	old.MarkReadyWithProcess(901, deps.Now(), deps.Now())
	if err := controller.store.Save(old); err != nil {
		t.Fatal(err)
	}
	deps.Alive = func(pid int) bool { return pid == 901 }
	deps.Stop = func(int, daemon.Supervisor, string) error {
		t.Fatal("an unrelated binary must never stop a supervised production daemon")
		return nil
	}
	deps.Start = func(string, []string, string) (int, error) {
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
	result, err := controller.Start(context.Background(), DefaultListen)
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
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)
	if got := controller.refuseDetachedStartUnderSupervisor(daemon.State{}, false, false, DefaultListen, false); got != "" {
		t.Fatalf("no record must not refuse: %q", got)
	}
	if got := controller.refuseDetachedStartUnderSupervisor(daemon.State{Supervisor: daemon.SupervisorNone}, true, false, DefaultListen, false); got != "" {
		t.Fatalf("supervisor none must not refuse: %q", got)
	}
	if got := controller.refuseDetachedStartUnderSupervisor(daemon.State{Supervisor: daemon.SupervisorSystemd}, true, false, DefaultListen, true); got != "" {
		t.Fatalf("--force-detached must not refuse: %q", got)
	}
	systemd := controller.refuseDetachedStartUnderSupervisor(daemon.State{Supervisor: daemon.SupervisorSystemd}, true, false, DefaultListen, false)
	if !strings.Contains(systemd, "systemctl --user") || !strings.Contains(systemd, "--force-detached") {
		t.Fatalf("systemd refusal = %q", systemd)
	}
	launchd := controller.refuseDetachedStartUnderSupervisor(daemon.State{Supervisor: daemon.SupervisorLaunchd, SupervisorLabel: "com.example.foreign"}, true, false, DefaultListen, false)
	if !strings.Contains(launchd, "launchctl kickstart") || !strings.Contains(launchd, "com.example.foreign") {
		t.Fatalf("launchd refusal = %q", launchd)
	}
	ownJob := controller.refuseDetachedStartUnderSupervisor(daemon.State{Supervisor: daemon.SupervisorLaunchd, SupervisorLabel: LaunchdLabel}, true, false, DefaultListen, false)
	if ownJob != "" {
		t.Fatalf("wb's own launchd job must not refuse: %q", ownJob)
	}
}

//nolint:paralleltest // This native fixture or its helper changes process-wide HOME, PATH or supervisor environment; testing restores it.
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

func TestDaemonRefuseTestBinaryGuard(t *testing.T) {
	t.Parallel()
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

func TestMarkStoppedIfUnchangedDoesNotOverwriteAConcurrentReplacement(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)

	original := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner-token-1", deps.Now())
	original.MarkReadyWithProcess(901, deps.Now(), deps.Now())
	if err := controller.store.Save(original); err != nil {
		t.Fatal(err)
	}

	// Simulate the race: something else already wrote a NEW ready record (a
	// different PID and owner token) before this call runs.
	replacement := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner-token-2", deps.Now())
	replacement.MarkReadyWithProcess(902, deps.Now(), deps.Now())
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

func TestMarkStoppedIfUnchangedWritesWhenNothingRaced(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)

	original := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner-token-1", deps.Now())
	original.MarkReadyWithProcess(901, deps.Now(), deps.Now())
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

func TestMarkStoppedIfUnchangedWhenTheRecordIsGone(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)

	expected := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner-token-1", deps.Now())
	expected.MarkReadyWithProcess(901, deps.Now(), deps.Now())
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
