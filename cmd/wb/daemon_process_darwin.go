//go:build darwin

package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// runLaunchctl is the seam every launchctl invocation in this file goes
// through, so a test can fake launchd's own responses instead of reaching a
// real launch agent (sneat-dev/wb#622 review: a darwin test with real stop
// semantics, faked at the launchctl seam).
//
// Its DEFAULT implementation is guarded by daemonRefuseTestBinary, generally
// — not just at startDaemonProcess's own explicit call — so every caller
// that reaches a real, unfaked launchctl invocation (stopDaemonProcess,
// daemonProcessAlive, launchdPID's status probe) is covered uniformly
// (sneat-dev/wb#622 review item 7): before this, only startDaemonProcess
// guarded itself explicitly, so a test that forgot to fake runLaunchctl
// before reaching any OTHER caller could still have touched a real launch
// agent. A test that overrides runLaunchctl entirely (as every existing
// darwin process test does) never reaches this guard at all — it is only the
// real, unfaked exec.Command path that refuses.
var runLaunchctl = func(args ...string) ([]byte, error) {
	executable, err := os.Executable()
	if err != nil {
		executable = os.Args[0]
	}
	if guardErr := daemonRefuseTestBinary(executable); guardErr != nil {
		return nil, guardErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), runLaunchctlTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "launchctl", args...)
	// See cmd/wb's runSystemctl doc for why WaitDelay, not just the context
	// timeout, matters: a killed direct child does not close pipes a
	// grandchild still holds open (sneat-dev/wb#622 review round 4).
	command.WaitDelay = 2 * time.Second
	return command.CombinedOutput()
}

// runLaunchctlTimeout bounds every runLaunchctl invocation. It is a variable
// so a test can shrink it rather than waiting the real bound out.
//
// It MUST stay above daemonStopTimeout: this seam also carries `launchctl
// bootout` (stopDaemonProcess, both wb's own job and a foreign one), which
// blocks until the daemon it targets actually stops, and the daemon's own
// drain can legitimately take up to daemonStopTimeout. A runLaunchctlTimeout
// at or below that would kill bootout mid-drain, and the bootstrap that
// follows it (startDaemonProcess, re-installing wb's own job) would then
// fail with "already loaded" — breaking `wb daemon start`/`restart` on a
// real Mac (sneat-dev/wb#622 review round 4 follow-up).
var runLaunchctlTimeout = 30 * time.Second

func daemonLaunchdPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", daemonLaunchdLabel+".plist"), nil
}

func daemonLaunchdTarget() string {
	return launchdTargetFor(daemonLaunchdLabel)
}

func launchdTargetFor(label string) string {
	return fmt.Sprintf("gui/%d/%s", os.Getuid(), label)
}

func startDaemonProcess(executable string, args []string, logPath string) (int, error) {
	return startDaemonProcessInjected(executable, args, logPath, nil)
}

// startDaemonProcessInjected is startDaemonProcess's test seam (task-9
// PR-2): every production call site reaches it only through
// startDaemonProcess, which always passes a nil *filewrite.Injector, so
// production behaviour is unchanged; a test passes its own Injector
// directly to reach a create/chmod/write/close/rename failure branch
// deterministically. This file builds only on darwin, so Linux CI's
// coverage ratchet cannot see any of it either way; verification here is
// a darwin cross-compile (`GOOS=darwin go vet ./cmd/wb/`) plus this
// package's own code review -- there is no launchctl-faked test exercising
// this seam, on darwin or otherwise, as of task-9 PR-2's review round.
func startDaemonProcessInjected(executable string, args []string, logPath string, inj *filewrite.Injector) (int, error) {
	if err := daemonRefuseTestBinary(executable); err != nil {
		return 0, err
	}
	plistPath, err := daemonLaunchdPath()
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o700); err != nil {
		return 0, err
	}
	data := launchdPlistBytes(executable, args, logPath)
	temporary, err := filewrite.CreateTemp(filepath.Dir(plistPath), ".wb-daemon-*.plist", inj)
	if err != nil {
		return 0, err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := filewrite.ChmodFile(temporary, 0o600, temporaryName, inj); err != nil {
		_ = temporary.Close()
		return 0, err
	}
	if err := filewrite.Write(temporary, data, temporaryName, inj); err != nil {
		_ = temporary.Close()
		return 0, err
	}
	if err := filewrite.Close(temporary, temporaryName, inj); err != nil {
		return 0, err
	}
	if err := filewrite.Rename(temporaryName, plistPath, inj); err != nil {
		return 0, err
	}
	// Re-bootstrap the exact per-user service so an older executable or
	// changed arguments cannot survive a start/handoff transition. This path
	// is wb's own self-managed launchd job (daemonLaunchdLabel): it is the
	// mechanism that already correctly hands a new binary to a running mac
	// daemon, which is exactly why stopAndReplace treats this job as NOT
	// "supervised" for handoff purposes (sneat-dev/wb#622 review item 1) —
	// there is no separate external supervisor to hand off to here, wb IS the
	// supervisor's installer.
	_, _ = runLaunchctl("bootout", daemonLaunchdTarget())
	if output, err := runLaunchctl("bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), plistPath); err != nil {
		return 0, fmt.Errorf("bootstrap WB launch agent: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if output, err := runLaunchctl("kickstart", "-k", daemonLaunchdTarget()); err != nil {
		return 0, fmt.Errorf("start WB launch agent: %w: %s", err, strings.TrimSpace(string(output)))
	}
	deadline := time.Now().Add(daemonReadyTimeout)
	for time.Now().Before(deadline) {
		if pid, ok := launchdPID(daemonLaunchdTarget()); ok {
			return pid, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0, fmt.Errorf("WB launch agent did not report a running PID within %s", daemonReadyTimeout)
}

func launchdPlistBytes(executable string, args []string, logPath string) []byte {
	var body bytes.Buffer
	body.WriteString(xml.Header)
	body.WriteString("<plist version=\"1.0\"><dict>")
	writeString := func(key, value string) {
		body.WriteString("<key>")
		_ = xml.EscapeText(&body, []byte(key))
		body.WriteString("</key><string>")
		_ = xml.EscapeText(&body, []byte(value))
		body.WriteString("</string>")
	}
	writeString("Label", daemonLaunchdLabel)
	body.WriteString("<key>ProgramArguments</key><array>")
	for _, argument := range append([]string{executable}, args...) {
		body.WriteString("<string>")
		_ = xml.EscapeText(&body, []byte(argument))
		body.WriteString("</string>")
	}
	body.WriteString("</array>")
	// launchd starts a job with a minimal environment, so an explicit projects
	// root has to travel in the unit or the daemon would resolve the default
	// root instead. This is the *input* the operator chose, not a path resolved
	// at install time: the daemon still derives its own state and runtime
	// directory at startup, so a later root move cannot leave this unit pointing
	// at an abandoned directory.
	if home := strings.TrimSpace(os.Getenv(wbhome.EnvOverride)); home != "" {
		body.WriteString("<key>EnvironmentVariables</key><dict>")
		writeString(wbhome.EnvOverride, home)
		body.WriteString("</dict>")
	}
	body.WriteString("<key>RunAtLoad</key><true/><key>KeepAlive</key><true/>")
	writeString("ProcessType", "Background")
	writeString("StandardOutPath", logPath)
	writeString("StandardErrorPath", logPath)
	body.WriteString("</dict></plist>\n")
	return body.Bytes()
}

func launchdPID(target string) (int, bool) {
	output, err := runLaunchctl("print", target)
	if err != nil {
		return 0, false
	}
	return launchdPIDFromOutput(string(output))
}

func launchdPIDFromOutput(output string) (int, bool) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "pid = ") {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "pid = ")))
		if err == nil && pid > 0 {
			return pid, true
		}
	}
	return 0, false
}

func daemonProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	managedPID, running := launchdPID(daemonLaunchdTarget())
	return running && managedPID == pid
}

// stopDaemonProcess asks the process to exit. Its supervisor context decides
// how:
//
//   - Supervisor is none, or launchd under wb's own label: the caller is
//     about to bootstrap+kickstart a fresh replacement itself (the unsupervised
//     stopAndReplace branch, or a plain `wb daemon stop`), so bootout-then-kill
//     is correct — it fully unloads wb's own job so the imminent re-bootstrap
//     cannot race a leftover instance.
//   - Supervisor is a FOREIGN launchd label (a job wb did not install, under a
//     label that is not daemonLaunchdLabel): wb must never bootout wb's own
//     (different, and likely never-bootstrapped) target here, and a plain
//     SIGTERM depends on that job's own KeepAlive configuration to restart it.
//     `launchctl kickstart -k` on the FOREIGN job's own target is the
//     unconditional, explicit ask that a genuine hand-off needs
//     (sneat-dev/wb#622 review item 1).
func stopDaemonProcess(pid int, supervisor daemon.Supervisor, label string) error {
	if supervisor == daemon.SupervisorLaunchd && label != "" && label != daemonLaunchdLabel {
		target := launchdTargetFor(label)
		if output, err := runLaunchctl("kickstart", "-k", target); err != nil {
			return fmt.Errorf("kickstart foreign launch agent %s: %w: %s", target, err, strings.TrimSpace(string(output)))
		}
		return nil
	}
	if _, err := runLaunchctl("bootout", daemonLaunchdTarget()); err == nil {
		return nil
	}
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

func signalDaemonContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}
