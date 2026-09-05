//go:build !windows

package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
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

func signalDaemonContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}
