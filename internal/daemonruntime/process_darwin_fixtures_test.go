//go:build darwin

package daemonruntime

import (
	"os"
	"path/filepath"
	"testing"
)

// A unit written while a projects root override was set must carry that input,
// not a runtime path resolved from it: a resolved path outlives the root it was
// resolved from, which is how a daemon ended up serving an abandoned directory.
// The variable is the one the resolver reads (WB_PROJECTS_ROOT), because that
// is the input the generator can honestly pin.

// fakeLaunchctl records every invocation and lets a test control launchctl's
// own responses, so stopDaemonProcess's real branching (wb's own job vs. a
// foreign one) can be exercised without a real launchd
// (sneat-dev/wb#622 review item 1's test ask).
func fakeLaunchctl(t *testing.T, native *nativeOperations, respond func(args []string) ([]byte, error)) *[][]string {
	t.Helper()
	var calls [][]string
	previous := native.runLaunchctl
	native.runLaunchctl = func(args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if respond != nil {
			return respond(args)
		}
		return nil, nil
	}
	t.Cleanup(func() { native.runLaunchctl = previous })
	return &calls
}

// wb's own self-managed launchd job is stopped via bootout — the path that
// lets the imminent bootstrap+kickstart in startDaemonProcess replace it
// cleanly — never via kickstart on some other job.

// A daemon with no recorded supervisor at all (an unmanaged local daemon,
// `wb daemon stop` on a plain dev box) is also stopped via wb's own bootout
// path: there is nothing else to hand off to.

// A FOREIGN launchd label — a job wb did not install — is stopped by asking
// launchd to kickstart -k THAT job's own target, never wb's own bootout
// (which would target the wrong, and likely never-bootstrapped, job).

// startDaemonProcess refuses a Go test binary before touching launchd at all
// — no plist write, no launchctl call — so a test that reaches this function
// by mistake can never install or bootstrap a real launch agent
// (sneat-dev/wb#622: this previously overwrote the founder's real
// ~/Library/LaunchAgents/dev.sneat.wb.daemon.plist and took the real daemon
// down).

// A failed kickstart on a foreign job is reported, not silently swallowed the
// way wb's own bootout's ignorable failure is (bootout can legitimately fail
// when the job was never bootstrapped in the first place).

// The DEFAULT (unfaked) native.runLaunchctl refuses under a go test binary before
// ever touching a real launch agent — not just at startDaemonProcess's own
// explicit guard, but generally, for every caller that reaches it: a print
// (status probe), a kickstart, or a bootout (sneat-dev/wb#622 review item 7).
// This deliberately does NOT call fakeLaunchctl: the whole point is to prove
// the guard fires on the real default closure.

// stopDaemonProcess itself is guarded the same way, through the exact same
// mechanism (native.runLaunchctl's default), for a caller that reaches it without
// having faked native.runLaunchctl first.

// native.runLaunchctlTimeout also bounds `launchctl bootout` (stopDaemonProcess),
// which blocks until the daemon it targets actually stops. It must stay
// above daemonStopTimeout, or a bootout gets killed mid-drain and the
// bootstrap that follows fails with "already loaded" — breaking `wb daemon
// start`/`restart` on a real Mac (sneat-dev/wb#622 review round 4 follow-up).

// TestAwaitLaunchdReadyReturnsTheReportedPID proves the ready-wait loop
// (startDaemonProcessInjected's launchd-ready poll) sleeps exactly once per
// still-not-ready check, at exactly the 50ms poll step, and returns the
// instant lookupPID reports one — on a fake clock, so no real wait is
// needed to prove the exact call count.

// TestAwaitLaunchdReadyReturnsFalseWhenDeadlinePasses proves the timeout
// branch: a launch agent that never reports a PID makes awaitLaunchdReady
// stop polling once now() reaches the deadline, reporting false, never
// blocking past it.

// installLaunchdPlistFixture points $HOME at a fresh directory and, when
// plist is non-nil, installs it at the launch agent path there, so the guard
// reads a fixture and never the founder's real ~/Library/LaunchAgents. It
// returns that path.
func installLaunchdPlistFixture(t *testing.T, plist []byte) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	path, err := daemonLaunchdPath()
	if err != nil {
		t.Fatal(err)
	}
	if plist == nil {
		return path
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, plist, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func launchdPlistFixtureFor(root, listen string) []byte {
	return launchdPlistBytes("/usr/local/bin/wb", []string{"--projects-root", root, "daemon", "serve", "--listen", listen, "--managed-start"}, "/tmp/wb.log")
}

// launchEnv is a controller whose start seam and launchctl seam both record
// every call, with the production darwin guard wired in against the fixture
// $HOME, so a refusal is proven to leave launchd, the plist and the lifecycle
// record untouched.
type launchGuardFixture struct {
	controller Controller
	root       string
	plistPath  string
	plist      []byte
	starts     *int
	launchctl  *[][]string
}

// newLaunchGuardFixture installs the plist that plistFor builds for the
// controller's own projects root (nil: no plist at all).
func newLaunchGuardFixture(t *testing.T, plistFor func(root string) []byte) launchGuardFixture {
	t.Helper()
	root := daemonTestRoot(t)
	var plist []byte
	if plistFor != nil {
		plist = plistFor(root)
	}
	plistPath := installLaunchdPlistFixture(t, plist)
	native := defaultNativeOperations()
	launchctl := fakeLaunchctl(t, native, nil)
	deps := daemonTestDependencies(t, root)
	starts := 0
	inner := deps.Start
	deps.Start = func(executable string, args []string, logPath string) (int, error) {
		starts++
		return inner(executable, args, logPath)
	}
	deps.CheckOtherRoot = native.daemonCheckOtherRoot
	return launchGuardFixture{controller: NewController(deps, root), root: root, plistPath: plistPath, plist: plist, starts: &starts, launchctl: launchctl}
}

func (fixture launchGuardFixture) assertNothingChanged(t *testing.T) {
	t.Helper()
	if *fixture.starts != 0 || len(*fixture.launchctl) != 0 {
		t.Fatalf("a refused start reached the process start (%d) or launchctl (%#v)", *fixture.starts, *fixture.launchctl)
	}
	if _, found, err := fixture.controller.store.Load(); err != nil || found {
		t.Fatalf("a refused start wrote lifecycle state: found=%t err=%v", found, err)
	}
	after, err := os.ReadFile(fixture.plistPath)
	if err != nil || string(after) != string(fixture.plist) {
		t.Fatalf("a refused start changed the plist: %v\n%s", err, after)
	}
}

// A root that does not exist yet cannot be symlink-resolved; it is compared by
// its cleaned spelling, so a redundant spelling of it is still the same root.
