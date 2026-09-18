//go:build !windows && !darwin

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/sneat-dev/wb/internal/daemon"
)

func daemonProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// stopDaemonProcess asks the process to exit. On this platform a plain
// SIGTERM is correct whether or not a supervisor (systemd) owns it: systemd's
// own Restart=always brings the process back on its own once it exits, and an
// unsupervised process simply exits. supervisor and label are unused here —
// they only disambiguate darwin's launchd-specific bootout-versus-kickstart
// choice — but are accepted so `wb daemon stop` and a supervised handoff share
// one cross-platform signature (sneat-dev/wb#622 review item 1).
func stopDaemonProcess(pid int, _ daemon.Supervisor, _ string) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

func startDaemonProcess(executable string, args []string, logPath string) (int, error) {
	if err := daemonRefuseTestBinary(executable); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return 0, err
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	command := exec.Command(executable, args...)
	command.Stdout, command.Stderr, command.Stdin = log, log, nil
	// A supervisor's own evidence (INVOCATION_ID, SYSTEMD_EXEC_PID,
	// JOURNAL_STREAM, XPC_SERVICE_NAME) must never leak into a process this
	// build starts itself: it would make the child misdetect a supervision it
	// does not actually have (sneat-dev/wb#622 review item 3).
	command.Env = daemonChildEnvironment()
	if err := command.Start(); err != nil {
		_ = log.Close()
		return 0, err
	}
	_ = log.Close()
	return command.Process.Pid, nil
}

func signalDaemonContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}
