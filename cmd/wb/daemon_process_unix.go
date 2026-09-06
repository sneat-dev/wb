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
)

func daemonProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func stopDaemonProcess(pid int) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

func startDaemonProcess(executable string, args []string, logPath string) (int, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return 0, err
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	command := exec.Command(executable, args...)
	command.Stdout, command.Stderr, command.Stdin = log, log, nil
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
