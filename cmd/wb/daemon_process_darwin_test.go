//go:build darwin

package main

import (
	"encoding/xml"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestLaunchdPlistUsesLaunchdDictionaryAndEscapesArguments(t *testing.T) {
	data := launchdPlistBytes("/tmp/wb&candidate", []string{"--projects-root", "/tmp/a<b"}, "/tmp/wb.log")
	text := string(data)
	if !strings.Contains(text, "<dict>") || !strings.Contains(text, "<key>ProgramArguments</key><array>") || strings.Contains(text, "<Dictionary>") {
		t.Fatalf("plist does not use launchd dictionary shape: %s", text)
	}
	var document struct {
		XMLName xml.Name `xml:"plist"`
	}
	if err := xml.Unmarshal(data, &document); err != nil {
		t.Fatalf("plist XML is invalid: %v", err)
	}
	if !strings.Contains(text, "/tmp/wb&amp;candidate") || !strings.Contains(text, "/tmp/a&lt;b") {
		t.Fatalf("plist arguments are not escaped: %s", text)
	}
}

func TestLaunchdPIDFromAuthoritativeJobState(t *testing.T) {
	output := "gui/501/dev.sneat.wb.daemon = {\n\tstate = running\n\tpid = 65918\n}"
	if pid, ok := launchdPIDFromOutput(output); !ok || pid != 65918 {
		t.Fatalf("launchd pid = %d, %t", pid, ok)
	}
	if pid, ok := launchdPIDFromOutput("state = exited\nlast exit code = 1"); ok || pid != 0 {
		t.Fatalf("stopped launchd job reported pid = %d, %t", pid, ok)
	}
}

// A unit written while a projects root override was set must carry that input,
// not a runtime path resolved from it: a resolved path outlives the root it was
// resolved from, which is how a daemon ended up serving an abandoned directory.
// The variable is the one the resolver reads (WB_PROJECTS_ROOT), because that
// is the input the generator can honestly pin.
func TestLaunchdPlistPinsProjectsRootAndNoResolvedRuntimePath(t *testing.T) {
	t.Setenv(wbhome.EnvOverride, "/tmp/wb-root-fixture")
	data := launchdPlistBytes("/tmp/wb", []string{"--projects-root", "/tmp/projects", "daemon", "serve", "--listen", daemonDefaultListen, "--managed-start"}, "/Users/someone/Library/Logs/wb/daemon.log")
	text := string(data)
	want := "<key>EnvironmentVariables</key><dict><key>" + wbhome.EnvOverride + "</key><string>/tmp/wb-root-fixture</string></dict>"
	if !strings.Contains(text, want) {
		t.Fatalf("plist does not pin %s: %s", wbhome.EnvOverride, text)
	}
	for _, forbidden := range []string{"runtime", "daemon-state.json", "--lifecycle-state"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("plist pins %q: %s", forbidden, text)
		}
	}

	t.Setenv(wbhome.EnvOverride, "")
	plain := string(launchdPlistBytes("/tmp/wb", []string{"--projects-root", "/tmp/projects", "daemon", "serve"}, "/tmp/wb.log"))
	if strings.Contains(plain, "EnvironmentVariables") {
		t.Fatalf("an unset projects root override must not be invented: %s", plain)
	}
}

// fakeLaunchctl records every invocation and lets a test control launchctl's
// own responses, so stopDaemonProcess's real branching (wb's own job vs. a
// foreign one) can be exercised without a real launchd
// (sneat-dev/wb#622 review item 1's test ask).
func fakeLaunchctl(t *testing.T, respond func(args []string) ([]byte, error)) *[][]string {
	t.Helper()
	var calls [][]string
	previous := runLaunchctl
	runLaunchctl = func(args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if respond != nil {
			return respond(args)
		}
		return nil, nil
	}
	t.Cleanup(func() { runLaunchctl = previous })
	return &calls
}

// wb's own self-managed launchd job is stopped via bootout — the path that
// lets the imminent bootstrap+kickstart in startDaemonProcess replace it
// cleanly — never via kickstart on some other job.
func TestStopDaemonProcessBootsOutWBsOwnJob(t *testing.T) {
	calls := fakeLaunchctl(t, func(args []string) ([]byte, error) { return nil, nil })
	if err := stopDaemonProcess(4242, daemon.SupervisorLaunchd, daemonLaunchdLabel); err != nil {
		t.Fatalf("stop wb's own job: %v", err)
	}
	if len(*calls) != 1 || (*calls)[0][0] != "bootout" || (*calls)[0][1] != daemonLaunchdTarget() {
		t.Fatalf("launchctl calls = %#v, want a single bootout of wb's own target", *calls)
	}
}

// A daemon with no recorded supervisor at all (an unmanaged local daemon,
// `wb daemon stop` on a plain dev box) is also stopped via wb's own bootout
// path: there is nothing else to hand off to.
func TestStopDaemonProcessBootsOutWhenUnsupervised(t *testing.T) {
	calls := fakeLaunchctl(t, func(args []string) ([]byte, error) { return nil, nil })
	if err := stopDaemonProcess(4242, daemon.SupervisorNone, ""); err != nil {
		t.Fatalf("stop unsupervised: %v", err)
	}
	if len(*calls) != 1 || (*calls)[0][0] != "bootout" {
		t.Fatalf("launchctl calls = %#v, want a single bootout", *calls)
	}
}

// A FOREIGN launchd label — a job wb did not install — is stopped by asking
// launchd to kickstart -k THAT job's own target, never wb's own bootout
// (which would target the wrong, and likely never-bootstrapped, job).
func TestStopDaemonProcessKickstartsAForeignLaunchdJob(t *testing.T) {
	const foreignLabel = "com.example.foreign-wb-supervisor"
	calls := fakeLaunchctl(t, func(args []string) ([]byte, error) { return nil, nil })
	if err := stopDaemonProcess(4242, daemon.SupervisorLaunchd, foreignLabel); err != nil {
		t.Fatalf("stop foreign job: %v", err)
	}
	wantTarget := fmt.Sprintf("gui/%d/%s", os.Getuid(), foreignLabel)
	if len(*calls) != 1 || (*calls)[0][0] != "kickstart" || (*calls)[0][1] != "-k" || (*calls)[0][2] != wantTarget {
		t.Fatalf("launchctl calls = %#v, want a single kickstart -k of %s", *calls, wantTarget)
	}
}

// startDaemonProcess refuses a Go test binary before touching launchd at all
// — no plist write, no launchctl call — so a test that reaches this function
// by mistake can never install or bootstrap a real launch agent
// (sneat-dev/wb#622: this previously overwrote the founder's real
// ~/Library/LaunchAgents/dev.sneat.wb.daemon.plist and took the real daemon
// down).
func TestStartDaemonProcessRefusesATestBinaryBeforeTouchingLaunchd(t *testing.T) {
	calls := fakeLaunchctl(t, func(args []string) ([]byte, error) { return nil, nil })
	if _, err := startDaemonProcess(os.Args[0], nil, t.TempDir()+"/daemon.log"); err == nil {
		t.Fatal("starting the test binary itself must be refused")
	}
	if len(*calls) != 0 {
		t.Fatalf("launchctl was called before the test-binary guard: %#v", *calls)
	}
}

// A failed kickstart on a foreign job is reported, not silently swallowed the
// way wb's own bootout's ignorable failure is (bootout can legitimately fail
// when the job was never bootstrapped in the first place).
func TestStopDaemonProcessReportsAFailedForeignKickstart(t *testing.T) {
	fakeLaunchctl(t, func(args []string) ([]byte, error) { return []byte("no such process"), fmt.Errorf("exit 1") })
	err := stopDaemonProcess(4242, daemon.SupervisorLaunchd, "com.example.foreign-wb-supervisor")
	if err == nil || !strings.Contains(err.Error(), "kickstart") {
		t.Fatalf("failed foreign kickstart = %v", err)
	}
}

// The DEFAULT (unfaked) runLaunchctl refuses under a go test binary before
// ever touching a real launch agent — not just at startDaemonProcess's own
// explicit guard, but generally, for every caller that reaches it: a print
// (status probe), a kickstart, or a bootout (sneat-dev/wb#622 review item 7).
// This deliberately does NOT call fakeLaunchctl: the whole point is to prove
// the guard fires on the real default closure.
func TestRunLaunchctlDefaultGuardsAgainstATestBinaryGenerally(t *testing.T) {
	if _, err := runLaunchctl("print", "gui/0/dev.sneat.wb.daemon"); err == nil {
		t.Fatal("expected the unfaked runLaunchctl to refuse under a go test binary")
	} else if !strings.Contains(err.Error(), "Go test binary") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// stopDaemonProcess itself is guarded the same way, through the exact same
// mechanism (runLaunchctl's default), for a caller that reaches it without
// having faked runLaunchctl first.
func TestStopDaemonProcessGuardsAgainstATestBinaryWithoutFakingRunLaunchctl(t *testing.T) {
	if err := stopDaemonProcess(4242, daemon.SupervisorLaunchd, "com.example.foreign-wb-supervisor"); err == nil {
		t.Fatal("expected stopDaemonProcess to refuse a real launchctl call under a go test binary")
	} else if !strings.Contains(err.Error(), "Go test binary") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// runLaunchctlTimeout also bounds `launchctl bootout` (stopDaemonProcess),
// which blocks until the daemon it targets actually stops. It must stay
// above daemonStopTimeout, or a bootout gets killed mid-drain and the
// bootstrap that follows fails with "already loaded" — breaking `wb daemon
// start`/`restart` on a real Mac (sneat-dev/wb#622 review round 4 follow-up).
func TestRunLaunchctlTimeoutStaysAboveDaemonStopTimeout(t *testing.T) {
	if runLaunchctlTimeout <= daemonStopTimeout {
		t.Fatalf("runLaunchctlTimeout = %s, must be greater than daemonStopTimeout = %s", runLaunchctlTimeout, daemonStopTimeout)
	}
}
