package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemonruntime"

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
	deps.Getenv = func(name string) string { return env[name] }
	deps.Getpid = func() int { return 4242 }
	deps.Getppid = func() int { return 1 }

	address := freeLoopbackAddress(t)
	command := &cobra.Command{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command.SetContext(ctx)
	var stdout bytes.Buffer
	stderr := &lockedBuffer{} // the daemon's goroutines write to it together
	command.SetOut(&stdout)
	command.SetErr(stderr)

	store := daemon.Store{Path: mustDaemonPath(t, daemonruntime.StatePath, root)}
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
	deps.ObservedCgroupUnit = func(pid int) (string, bool) {
		if pid != 4242 {
			t.Fatalf("observedCgroupUnit probed unexpected pid %d", pid)
		}
		return "wb.service", true
	}
	deps.Getpid = func() int { return 4242 }
	deps.Getppid = func() int { return 1 }

	address := freeLoopbackAddress(t)
	command := &cobra.Command{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command.SetContext(ctx)
	var stdout bytes.Buffer
	stderr := &lockedBuffer{} // the daemon's goroutines write to it together
	command.SetOut(&stdout)
	command.SetErr(stderr)

	store := daemon.Store{Path: mustDaemonPath(t, daemonruntime.StatePath, root)}
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

// `wb daemon status` MUST prefer the daemon's OWN recorded unit
// (State.SystemdUnit) over this invocation's own configured/default guess:
// a status invocation run without the same WB_DAEMON_SYSTEMD_UNIT the
// daemon itself was supervised under would otherwise query the WRONG unit
// and see a false "no systemd service membership" (sneat-dev/wb#622 review
// round 3, item M3).

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
	deps.Getenv = func(name string) string { return env[name] }
	deps.Getpid = func() int { return 4242 }
	deps.Getppid = func() int { return 1 }

	address := freeLoopbackAddress(t)
	command := &cobra.Command{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command.SetContext(ctx)
	var stdout bytes.Buffer
	stderr := &lockedBuffer{} // the daemon's goroutines write to it together
	command.SetOut(&stdout)
	command.SetErr(stderr)

	store := daemon.Store{Path: mustDaemonPath(t, daemonruntime.StatePath, root)}
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
	deps.Getenv = func(name string) string { return env[name] }
	deps.Getppid = func() int { return 1 }

	address := freeLoopbackAddress(t)
	command := &cobra.Command{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command.SetContext(ctx)
	var stdout bytes.Buffer
	stderr := &lockedBuffer{} // the daemon's goroutines write to it together
	command.SetOut(&stdout)
	command.SetErr(stderr)

	store := daemon.Store{Path: mustDaemonPath(t, daemonruntime.StatePath, root)}
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
	state := daemonTestState(t, root, daemonruntime.DefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner", time.Now())
	state.MarkReadyWithProcess(os.Getpid(), daemonruntime.ProcessStartedAt(os.Getpid()), time.Now())
	// Supervisor is left at its zero value, exactly as an unmarshalled record
	// written before this field existed would be.
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonruntime.StatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.Alive = func(pid int) bool { return pid == os.Getpid() }

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
	state := daemonTestState(t, root, daemonruntime.DefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner", deps.Now())
	state.Supervisor = daemon.SupervisorLaunchd
	state.SupervisorLabel = "dev.sneat.wb.daemon"
	state.MarkReadyWithProcess(os.Getpid(), daemonruntime.ProcessStartedAt(os.Getpid()), deps.Now())
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonruntime.StatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.Alive = func(pid int) bool { return pid == os.Getpid() }

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
// daemonTestDependencies wrote and controller.Provenance() reads), the way a
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

// The first self-update into a build that records the supervisor field at
// all must not recreate #617 on a systemd host: a pre-#622 build's record
// has an EMPTY (legacy) supervisor field, which normalizes to `none` via
// ReportedSupervisor — exactly what an unsupervised daemon also reports.
// Without an independent fallback, `wb daemon restart` (and the self-update
// hook, which just shells out to it) would SIGTERM the real systemd-managed
// process and then launch a detached --managed-start child, which then
// fails to bind once systemd's own Restart=always brings the original back
// (sneat-dev/wb#622 review round 3, item S1).

// The same fallback applies to Start's executable-handoff branch (an
// implicit `wb dashboard --local` or RPC bootstrap after a self-update hook
// timeout would otherwise reach exactly this shape).

// A genuinely unsupervised daemon (recorded none, and independently
// confirmed as not systemd-managed — or with no independent observation
// available at all) must keep taking the ordinary detached-launch path:
// the fallback must not manufacture supervision that was never there.

// AC: a-supervised-restart-hands-off-not-doubles (the timeout).

// AC: a-supervised-restart-hands-off-not-doubles (a different-binary
// replacement fails fast rather than waiting out the full bound).

// The wait must watch its context, not only the deadline and the poll sleep.

// AC: a-detached-start-is-refused-under-a-supervisor

// --force-detached bypasses the refusal explicitly.

// A recorded supervisor that can no longer be confirmed to exist (a stale
// record) must not lock `wb daemon start` out forever, even without
// --force-detached (sneat-dev/wb#622 review item 4).

// A recorded supervisor that IS confirmed present still refuses.

// wb's own self-managed launchd job is never treated as a foreign supervisor
// to refuse a cold start under: it is what `wb daemon start` itself
// (re)installs, so there is no separate owner to defer to
// (sneat-dev/wb#622 review item 1, applied to the refusal path).

// A FOREIGN launchd label (a job wb did not install) is refused, and names
// launchd's own remedy for that job specifically.

// Refusal text for a live supervised daemon on a different --listen must say
// so plainly, never phrased as "if it is not running" (sneat-dev/wb#622
// review item 15).

// wb daemon restart --if-running (the self-update after-update hook's own
// call) must refuse the same way rather than launching a fresh daemon.

// AC: a-double-owner-is-flagged-when-observable
func TestDaemonStatusFlagsASupervisorMismatch(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	state := daemonTestState(t, root, daemonruntime.DefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner", deps.Now())
	// Recorded supervisor is none, but the observed cgroup disagrees.
	state.MarkReadyWithProcess(901, deps.Now(), deps.Now())
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonruntime.StatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.Alive = func(pid int) bool { return pid == 901 }
	deps.ObservedSupervisor = func(pid int) (daemon.Supervisor, bool) {
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

// No mismatch is reported when the recorded and observed supervisors agree,
// or when the observation is unknown.

// A nil observedSupervisor (a test double that never set it) must not panic
// status: the check is skipped, not attempted.

// The sneat-dev/wb#617 detector this feature exists for: a specific systemd
// unit exists and is failing to keep the daemon up, while the process
// actually answering the port recorded no supervisor at all — confirmed
// live: an orphaned `daemon serve` (a session scope, not the unit's own
// cgroup) served the port while wb-daemon.service sat ActiveState=failed
// with NRestarts=4468 (sneat-dev/wb#622 review item 2). The cgroup-based
// seam agrees (recorded none, observed none) — the systemd-unit detector is
// what catches this, not the cgroup fallback.

// A unit that is merely activating for the first time (no restarts yet), or
// healthy and active, is not evidence of anything wrong.

// The systemd-unit detector only applies when the RECORDED supervisor is
// none: a daemon that already recorded supervisor=systemd is not the shape
// this detector exists for (the cgroup-based fallback covers that
// direction), and probing an unrelated unit's state would be meaningless.

// Nil systemdUnitState/systemdUnitName seams (a test double that never set
// them, or a platform with no implementation) must not panic status, and
// must fall back to the cgroup-based check.

// daemonObservedSystemdUnitState is exercised through its own runSystemctl
// seam with fake output, including systemctl being entirely absent — never
// a real systemd user manager.

// The unit name comes from config (an environment variable override — this
// build has no other daemon configuration file) if one is set, and from
// daemonDefaultSystemdUnit otherwise (sneat-dev/wb#622 review item 2).

// daemonSupervisorPresent's systemd branch matches only the known
// `is-system-running` states; anything else — an unrecognized state, or
// systemctl being entirely absent — counts as not present
// (sneat-dev/wb#622 review item 5).

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

// wb's own self-managed launchd job's executable-handoff path (a live
// process running a different binary) must go through the ordinary launch
// path — wb's own bootstrap+kickstart cycle IS its restart mechanism —
// never through waitForSupervisorReplacement, which would wait for a
// foreign supervisor that does not exist (sneat-dev/wb#622 review item 3).

// The whole point: an executable-handoff branch of Start must also hand off
// to a live supervisor rather than launch a detached replacement.

// AC: item 6 — a caller running a DIFFERENT, unrelated wb binary (a worktree
// build, an older or newer installed CLI) than the one actually running must
// not restart a supervised daemon merely because its own binary differs from
// the recorded provenance: that is also true of an implicit Start call from
// an unrelated command (cmd/wb/dashboard.go, daemon_rpc.go's
// daemonOperationClient). It must report the mismatch and leave the
// supervised daemon running untouched.

// A supervisor's own evidence must never leak into a detached child this
// build starts itself (sneat-dev/wb#622 review item 3).

// testing.Testing() reports true for the calling PROCESS, not for the
// executable argument, so daemonRefuseTestBinary refuses unconditionally
// whenever it is reached from inside a go test binary — this is exactly the
// defense in depth the guard exists for: the real incident was a test
// process reaching real production code, regardless of which path that
// process happened to be running under (sneat-dev/wb#622). That makes the
// "not a test binary" branch impossible to exercise as itself-not-refused
// from within this suite; the ".test" suffix check is verified in isolation
// instead, independent of testing.Testing().

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
		{"wb's own launchd job prints no false hint", daemonruntime.LaunchdLabel, false},
		{"a legacy record with no recorded label is treated as wb's own", "", false},
		{"a foreign launchd label prints the real remedy", "com.example.foreign-wb-supervisor", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := daemonTestRoot(t)
			deps := daemonTestDependencies(t, root)
			now := deps.Now()
			state := daemonSupervisorTestInstalledOld(t, root, daemonruntime.DefaultListen, daemon.SupervisorLaunchd, now)
			state.SupervisorLabel = tc.label
			state.MarkReadyWithProcess(901, now, now)
			if err := (daemon.Store{Path: mustDaemonPath(t, daemonruntime.StatePath, root)}).Save(state); err != nil {
				t.Fatal(err)
			}
			// deps.Stop MUST flip aliveness: daemonTestDependencies' now()
			// is a fixed clock and its sleep is a no-op, so stop()'s poll
			// loop (daemon.go's deadline := controller.deps.Now().Add(...))
			// never advances on its own — a stop fake that does not mark the
			// process dead spins forever instead of failing fast.
			alive := true
			deps.Alive = func(pid int) bool { return pid == 901 && alive }
			deps.Stop = func(int, daemon.Supervisor, string) error { alive = false; return nil }

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
	now := deps.Now()
	state := daemonSupervisorTestInstalledOld(t, root, daemonruntime.DefaultListen, daemon.SupervisorSystemd, now)
	state.MarkReadyWithProcess(901, now, now)
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonruntime.StatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	// See TestDaemonStopHintNamesTheRealRemedyOnlyForAForeignLaunchdLabel for
	// why deps.Stop must flip aliveness rather than being a bare no-op.
	alive := true
	deps.Alive = func(pid int) bool { return pid == 901 && alive }
	deps.Stop = func(int, daemon.Supervisor, string) error { alive = false; return nil }

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

// The matching case: nothing raced it, so the expected PID/OwnerToken are
// still current, and the write proceeds normally.

// found=false (the record vanished entirely between the caller's read and
// this call — for example a concurrent `wb daemon recover` reclaiming it)
// also leaves nothing to overwrite: it returns the caller's own expected
// state as a best-effort answer rather than fabricating a stopped record for
// a state this store no longer holds.
