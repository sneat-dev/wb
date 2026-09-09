//go:build !windows

package main

import "syscall"

func terminateJourneyProcess(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }
