//go:build darwin

package daemonruntime

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestLaunchdPlistPinsProjectsRootAndNoResolvedRuntimePath(t *testing.T) {
	t.Setenv(wbhome.EnvOverride, "/tmp/wb-root-fixture")
	data := launchdPlistBytes("/tmp/wb", []string{"--projects-root", "/tmp/projects", "daemon", "serve", "--listen", DefaultListen, "--managed-start"}, "/Users/someone/Library/Logs/wb/daemon.log")
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

func TestStopDaemonProcessBootsOutWBsOwnJob(t *testing.T) {
	native := defaultNativeOperations()
	commandBounds := defaultNativeCommandBounds()
	native.commandBounds = func() nativeCommandBounds { return commandBounds }

	calls := fakeLaunchctl(t, native, func(args []string) ([]byte, error) { return nil, nil })
	if err := native.stopDaemonProcess(4242, daemon.SupervisorLaunchd, LaunchdLabel); err != nil {
		t.Fatalf("stop wb's own job: %v", err)
	}
	if len(*calls) != 1 || (*calls)[0][0] != "bootout" || (*calls)[0][1] != daemonLaunchdTarget() {
		t.Fatalf("launchctl calls = %#v, want a single bootout of wb's own target", *calls)
	}
}

func TestStopDaemonProcessBootsOutWhenUnsupervised(t *testing.T) {
	native := defaultNativeOperations()
	commandBounds := defaultNativeCommandBounds()
	native.commandBounds = func() nativeCommandBounds { return commandBounds }

	calls := fakeLaunchctl(t, native, func(args []string) ([]byte, error) { return nil, nil })
	if err := native.stopDaemonProcess(4242, daemon.SupervisorNone, ""); err != nil {
		t.Fatalf("stop unsupervised: %v", err)
	}
	if len(*calls) != 1 || (*calls)[0][0] != "bootout" {
		t.Fatalf("launchctl calls = %#v, want a single bootout", *calls)
	}
}

func TestStopDaemonProcessKickstartsAForeignLaunchdJob(t *testing.T) {
	native := defaultNativeOperations()
	commandBounds := defaultNativeCommandBounds()
	native.commandBounds = func() nativeCommandBounds { return commandBounds }

	const foreignLabel = "com.example.foreign-wb-supervisor"
	calls := fakeLaunchctl(t, native, func(args []string) ([]byte, error) { return nil, nil })
	if err := native.stopDaemonProcess(4242, daemon.SupervisorLaunchd, foreignLabel); err != nil {
		t.Fatalf("stop foreign job: %v", err)
	}
	wantTarget := fmt.Sprintf("gui/%d/%s", os.Getuid(), foreignLabel)
	if len(*calls) != 1 || (*calls)[0][0] != "kickstart" || (*calls)[0][1] != "-k" || (*calls)[0][2] != wantTarget {
		t.Fatalf("launchctl calls = %#v, want a single kickstart -k of %s", *calls, wantTarget)
	}
}

func TestStartDaemonProcessRefusesATestBinaryBeforeTouchingLaunchd(t *testing.T) {
	native := defaultNativeOperations()
	commandBounds := defaultNativeCommandBounds()
	native.commandBounds = func() nativeCommandBounds { return commandBounds }

	calls := fakeLaunchctl(t, native, func(args []string) ([]byte, error) { return nil, nil })
	if _, err := native.startDaemonProcess(os.Args[0], nil, t.TempDir()+"/daemon.log"); err == nil {
		t.Fatal("starting the test binary itself must be refused")
	}
	if len(*calls) != 0 {
		t.Fatalf("launchctl was called before the test-binary guard: %#v", *calls)
	}
}

func TestStopDaemonProcessReportsAFailedForeignKickstart(t *testing.T) {
	native := defaultNativeOperations()
	commandBounds := defaultNativeCommandBounds()
	native.commandBounds = func() nativeCommandBounds { return commandBounds }

	fakeLaunchctl(t, native, func(args []string) ([]byte, error) { return []byte("no such process"), fmt.Errorf("exit 1") })
	err := native.stopDaemonProcess(4242, daemon.SupervisorLaunchd, "com.example.foreign-wb-supervisor")
	if err == nil || !strings.Contains(err.Error(), "kickstart") {
		t.Fatalf("failed foreign kickstart = %v", err)
	}
}

func TestRunLaunchctlDefaultGuardsAgainstATestBinaryGenerally(t *testing.T) {
	native := defaultNativeOperations()
	commandBounds := defaultNativeCommandBounds()
	native.commandBounds = func() nativeCommandBounds { return commandBounds }

	if _, err := native.runLaunchctl("print", "gui/0/dev.sneat.wb.daemon"); err == nil {
		t.Fatal("expected the unfaked runLaunchctl to refuse under a go test binary")
	} else if !strings.Contains(err.Error(), "Go test binary") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStopDaemonProcessGuardsAgainstATestBinaryWithoutFakingRunLaunchctl(t *testing.T) {
	native := defaultNativeOperations()
	commandBounds := defaultNativeCommandBounds()
	native.commandBounds = func() nativeCommandBounds { return commandBounds }

	if err := native.stopDaemonProcess(4242, daemon.SupervisorLaunchd, "com.example.foreign-wb-supervisor"); err == nil {
		t.Fatal("expected stopDaemonProcess to refuse a real launchctl call under a go test binary")
	} else if !strings.Contains(err.Error(), "Go test binary") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunLaunchctlTimeoutStaysAboveDaemonStopTimeout(t *testing.T) {
	native := defaultNativeOperations()
	commandBounds := defaultNativeCommandBounds()
	native.commandBounds = func() nativeCommandBounds { return commandBounds }

	if commandBounds.Launchctl <= daemonStopTimeout {
		t.Fatalf("runLaunchctlTimeout = %s, must be greater than daemonStopTimeout = %s", commandBounds.Launchctl, daemonStopTimeout)
	}
}

func TestAwaitLaunchdReadyReturnsTheReportedPID(t *testing.T) {
	virtual := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var slept []time.Duration
	now := func() time.Time { return virtual }
	sleep := func(d time.Duration) {
		slept = append(slept, d)
		virtual = virtual.Add(d)
	}
	remainingMisses := 3
	lookupPID := func() (int, bool) {
		if remainingMisses <= 0 {
			return 65918, true
		}
		remainingMisses--
		return 0, false
	}

	pid, ok := awaitLaunchdReady(now, sleep, virtual.Add(time.Hour), lookupPID)

	if !ok || pid != 65918 {
		t.Fatalf("awaitLaunchdReady = (%d, %t), want (65918, true)", pid, ok)
	}
	if len(slept) != 3 {
		t.Fatalf("awaitLaunchdReady slept %d times, want 3 (once per not-yet-ready check)", len(slept))
	}
	for _, d := range slept {
		if d != 50*time.Millisecond {
			t.Fatalf("awaitLaunchdReady slept %v, want every wait to be the 50ms poll step", slept)
		}
	}
}

func TestAwaitLaunchdReadyReturnsFalseWhenDeadlinePasses(t *testing.T) {
	virtual := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	deadline := virtual.Add(120 * time.Millisecond)
	var slept []time.Duration
	now := func() time.Time { return virtual }
	sleep := func(d time.Duration) {
		slept = append(slept, d)
		virtual = virtual.Add(d)
	}
	lookupPID := func() (int, bool) { return 0, false }

	pid, ok := awaitLaunchdReady(now, sleep, deadline, lookupPID)

	if ok || pid != 0 {
		t.Fatalf("awaitLaunchdReady = (%d, %t), want (0, false): the launch agent never reported a PID", pid, ok)
	}
	wantSleeps := int(120*time.Millisecond/(50*time.Millisecond)) + 1
	if len(slept) != wantSleeps {
		t.Fatalf("awaitLaunchdReady slept %d times, want exactly %d (120ms deadline in 50ms steps)", len(slept), wantSleeps)
	}
	for _, d := range slept {
		if d != 50*time.Millisecond {
			t.Fatalf("awaitLaunchdReady slept %v, want every wait to be the 50ms poll step", slept)
		}
	}
}

func TestDaemonStartRefusesWhenTheLaunchAgentServesAnotherProjectsRoot(t *testing.T) {
	other := t.TempDir()
	fixture := newLaunchGuardFixture(t, func(string) []byte { return launchdPlistFixtureFor(other, "127.0.0.1:18766") })

	_, err := fixture.controller.Start(context.Background(), DefaultListen)

	if err == nil {
		t.Fatal("a start for another projects root must be refused")
	}
	for _, want := range []string{other, "127.0.0.1:18766", "--replace-other-root", fixture.root} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not mention %q", err, want)
		}
	}
	fixture.assertNothingChanged(t)
}

func TestDaemonStartWithReplaceOtherRootProceedsPastAnotherProjectsRoot(t *testing.T) {
	other := t.TempDir()
	fixture := newLaunchGuardFixture(t, func(string) []byte { return launchdPlistFixtureFor(other, "127.0.0.1:18766") })

	result, err := fixture.controller.WithReplaceOtherRoot(true).Start(context.Background(), DefaultListen)

	if err != nil || !result.Reachable {
		t.Fatalf("explicit replacement = %#v, %v", result, err)
	}
	if *fixture.starts != 1 {
		t.Fatalf("start calls = %d, want 1", *fixture.starts)
	}
}

func TestDaemonStartProceedsWhenTheLaunchAgentServesTheSameProjectsRoot(t *testing.T) {
	fixture := newLaunchGuardFixture(t, func(root string) []byte { return launchdPlistFixtureFor(root, DefaultListen) })

	if _, err := fixture.controller.Start(context.Background(), DefaultListen); err != nil {
		t.Fatalf("same-root start refused: %v", err)
	}
	if *fixture.starts != 1 {
		t.Fatalf("start calls = %d, want 1", *fixture.starts)
	}
}

func TestDaemonStartProceedsWhenNoLaunchAgentIsRegistered(t *testing.T) {
	fixture := newLaunchGuardFixture(t, nil)

	if _, err := fixture.controller.Start(context.Background(), DefaultListen); err != nil {
		t.Fatalf("start with no plist refused: %v", err)
	}
	if *fixture.starts != 1 {
		t.Fatalf("start calls = %d, want 1", *fixture.starts)
	}
}

func TestDaemonStartRefusesAnUnreadableLaunchAgentFileAndNamesIt(t *testing.T) {
	fixture := newLaunchGuardFixture(t, func(string) []byte { return []byte("this is not a property list") })

	_, err := fixture.controller.Start(context.Background(), DefaultListen)

	if err == nil || !strings.Contains(err.Error(), fixture.plistPath) || !strings.Contains(err.Error(), "--replace-other-root") {
		t.Fatalf("malformed plist refusal = %v", err)
	}
	fixture.assertNothingChanged(t)
}

func TestDaemonStartWithReplaceOtherRootProceedsPastAnUnreadableLaunchAgentFile(t *testing.T) {
	fixture := newLaunchGuardFixture(t, func(string) []byte { return []byte("not a plist") })

	if _, err := fixture.controller.WithReplaceOtherRoot(true).Start(context.Background(), DefaultListen); err != nil {
		t.Fatalf("explicit replacement of an unreadable file refused: %v", err)
	}
}

func TestDaemonStartTreatsASymlinkedSpellingOfTheSameRootAsTheSameRoot(t *testing.T) {
	link := filepath.Join(t.TempDir(), "projects-link")
	fixture := newLaunchGuardFixture(t, func(root string) []byte {
		if err := os.Symlink(root, link); err != nil {
			t.Fatal(err)
		}
		return launchdPlistFixtureFor(link+"/", DefaultListen)
	})

	if _, err := fixture.controller.Start(context.Background(), DefaultListen); err != nil {
		t.Fatalf("a symlinked spelling of the same root was refused: %v", err)
	}
}

func TestCheckLaunchdRootOwnershipReportsAnUnreadablePlistPath(t *testing.T) {
	directory := t.TempDir()
	// A directory where the file should be: present, but not readable as one.
	err := checkLaunchdRootOwnership(directory, "/tmp/root", false, "/home/x")
	if err == nil || !strings.Contains(err.Error(), directory) {
		t.Fatalf("unreadable plist path = %v", err)
	}
}

func TestParseLaunchdServiceRootReadsBothArgumentSpellingsAndTheDefaultRoot(t *testing.T) {
	const prologue = xml.Header + `<plist version="1.0"><dict>`
	cases := []struct {
		name, body, wantRoot, wantListen string
	}{
		{"equals spellings", `<key>ProgramArguments</key><array><string>wb</string><string>--projects-root=/a/b</string><string>--listen=127.0.0.1:1</string></array>`, "/a/b", "127.0.0.1:1"},
		{"separate spellings", `<key>ProgramArguments</key><array><string>wb</string><string>--projects-root</string><string>/a/c</string><string>--listen</string><string>127.0.0.1:2</string></array>`, "/a/c", "127.0.0.1:2"},
		{"no root reads the unit environment", `<key>ProgramArguments</key><array><string>wb</string></array><key>EnvironmentVariables</key><dict><key>OTHER</key><string>x</string><key>` + wbhome.EnvOverride + `</key><string>/env/root</string></dict>`, "/env/root", DefaultListen},
		{"no root and no environment reads the default root", `<key>ProgramArguments</key><array><string>wb</string></array><key>RunAtLoad</key><true/>`, "/home/x/projects", DefaultListen},
	}
	for _, tc := range cases {
		got, err := parseLaunchdServiceRoot([]byte(prologue+tc.body+`</dict></plist>`), "/home/x")
		if err != nil || got.ProjectsRoot != tc.wantRoot || got.Listen != tc.wantListen {
			t.Errorf("%s: got %#v, %v; want root %q listen %q", tc.name, got, err, tc.wantRoot, tc.wantListen)
		}
	}
}

func TestParseLaunchdServiceRootRejectsPlistsItCannotUnderstand(t *testing.T) {
	const open = xml.Header + `<plist version="1.0">`
	for name, document := range map[string]string{
		"not xml":                  "garbage",
		"wrong root element":       xml.Header + `<other><dict/></other>`,
		"top-level array":          open + `<array/></plist>`,
		"key without value":        open + `<dict><key>Label</key></dict></plist>`,
		"entry that is not a key":  open + `<dict><string>a</string><string>b</string></dict></plist>`,
		"no program arguments":     open + `<dict><key>Label</key><string>x</string></dict></plist>`,
		"arguments not an array":   open + `<dict><key>ProgramArguments</key><string>wb</string></dict></plist>`,
		"non-string argument":      open + `<dict><key>ProgramArguments</key><array><true/></array></dict></plist>`,
		"environment not a dict":   open + `<dict><key>EnvironmentVariables</key><string>x</string><key>ProgramArguments</key><array/></dict></plist>`,
		"environment odd children": open + `<dict><key>EnvironmentVariables</key><dict><key>A</key></dict></dict></plist>`,
	} {
		if _, err := parseLaunchdServiceRoot([]byte(document), "/home/x"); err == nil {
			t.Errorf("%s: parsed without error", name)
		}
	}
}

func TestDaemonCheckOtherRootReadsTheFixtureHomePlist(t *testing.T) {
	native := defaultNativeOperations()
	commandBounds := defaultNativeCommandBounds()
	native.commandBounds = func() nativeCommandBounds { return commandBounds }

	other := t.TempDir()
	installLaunchdPlistFixture(t, launchdPlistFixtureFor(other, DefaultListen))

	if err := native.daemonCheckOtherRoot(t.TempDir(), false); err == nil || !strings.Contains(err.Error(), other) {
		t.Fatalf("production guard = %v", err)
	}
	if err := native.daemonCheckOtherRoot(t.TempDir(), true); err != nil {
		t.Fatalf("production guard with replace = %v", err)
	}
}

func TestDaemonCheckOtherRootReportsAnUnresolvableHome(t *testing.T) {
	native := defaultNativeOperations()
	commandBounds := defaultNativeCommandBounds()
	native.commandBounds = func() nativeCommandBounds { return commandBounds }

	t.Setenv("HOME", "")
	if err := native.daemonCheckOtherRoot("/tmp/root", false); err == nil {
		t.Fatal("expected an error when the home directory cannot be resolved")
	}
}

func TestCheckLaunchdRootOwnershipComparesAMissingRootByItsCleanedSpelling(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-created")
	plistPath := installLaunchdPlistFixture(t, launchdPlistFixtureFor(missing+"/sub/..", DefaultListen))

	if err := checkLaunchdRootOwnership(plistPath, missing, false, "/home/x"); err != nil {
		t.Fatalf("same missing root refused: %v", err)
	}
	if err := checkLaunchdRootOwnership(plistPath, missing+"-other", false, "/home/x"); err == nil {
		t.Fatal("a different missing root must be refused")
	}
}
