//go:build !windows

package procalive

import (
	"errors"
	"syscall"
)

// ReadProcStat reads /proc/<pid>/stat for pid on Linux. Tests may replace it
// to simulate zombie or dead processes without spawning one.
var ReadProcStat = defaultReadProcStat

// Probe checks whether pid refers to a live process on Unix.
//
// It returns syscall.ESRCH when pid <= 0, when kill(pid, 0) returns ESRCH, or
// when /proc/<pid>/stat reports state Z (zombie) or X (dead). Otherwise it
// returns the kill(pid, 0) result (nil when alive, syscall.EPERM when owned by
// another user, or any other kernel error). If /proc/<pid>/stat is unreadable
// or unparseable, Probe falls back to the kill(pid, 0) result.
func Probe(pid int) error {
	return probeWith(pid, defaultKill, ReadProcStat)
}

// Alive reports whether pid refers to a process that is live or cannot be
// proved absent. Only non-positive PIDs, ESRCH, and Linux zombie/dead states
// (Z/X) report false; EPERM and other probe errors report true.
func Alive(pid int) bool {
	return !errors.Is(Probe(pid), syscall.ESRCH)
}

func defaultKill(pid int) error {
	return syscall.Kill(pid, 0)
}

func probeWith(pid int, kill func(int) error, readProcStat func(int) ([]byte, error)) error {
	if pid <= 0 {
		return syscall.ESRCH
	}
	err := kill(pid)
	if errors.Is(err, syscall.ESRCH) {
		return err
	}
	if data, readErr := readProcStat(pid); readErr == nil && isDeadProcStat(data) {
		return syscall.ESRCH
	}
	return err
}
