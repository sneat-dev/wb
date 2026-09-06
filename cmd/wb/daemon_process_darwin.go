//go:build darwin

package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const daemonLaunchdLabel = "dev.sneat.wb.daemon"

func daemonLaunchdPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", daemonLaunchdLabel+".plist"), nil
}

func daemonLaunchdTarget() string {
	return fmt.Sprintf("gui/%d/%s", os.Getuid(), daemonLaunchdLabel)
}

func startDaemonProcess(executable string, args []string, logPath string) (int, error) {
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
	temporary, err := os.CreateTemp(filepath.Dir(plistPath), ".wb-daemon-*.plist")
	if err != nil {
		return 0, err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return 0, err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return 0, err
	}
	if err := temporary.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(temporaryName, plistPath); err != nil {
		return 0, err
	}
	// Re-bootstrap the exact per-user service so an older executable or
	// changed arguments cannot survive a start/handoff transition.
	_ = exec.Command("launchctl", "bootout", daemonLaunchdTarget()).Run()
	if output, err := exec.Command("launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), plistPath).CombinedOutput(); err != nil {
		return 0, fmt.Errorf("bootstrap WB launch agent: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if output, err := exec.Command("launchctl", "kickstart", "-k", daemonLaunchdTarget()).CombinedOutput(); err != nil {
		return 0, fmt.Errorf("start WB launch agent: %w: %s", err, strings.TrimSpace(string(output)))
	}
	deadline := time.Now().Add(daemonReadyTimeout)
	for time.Now().Before(deadline) {
		if pid, ok := launchdPID(); ok {
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
	body.WriteString("</array><key>RunAtLoad</key><true/><key>KeepAlive</key><true/>")
	writeString("ProcessType", "Background")
	writeString("StandardOutPath", logPath)
	writeString("StandardErrorPath", logPath)
	body.WriteString("</dict></plist>\n")
	return body.Bytes()
}

func launchdPID() (int, bool) {
	output, err := exec.Command("launchctl", "print", daemonLaunchdTarget()).CombinedOutput()
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(output), "\n") {
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
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func stopDaemonProcess(pid int) error {
	if err := exec.Command("launchctl", "bootout", daemonLaunchdTarget()).Run(); err == nil {
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
