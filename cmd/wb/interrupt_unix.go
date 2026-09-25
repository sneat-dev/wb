//go:build darwin || linux

package main

import (
	"os"
	"syscall"
)

// rootSignals is the set wb's root context watches for interruption: the two
// signals a terminal or supervisor conventionally sends (Ctrl-C/os.Interrupt,
// SIGTERM) plus SIGHUP. internal/process gives every non-interactive child
// its own session (Setsid), so it no longer receives the terminal's own
// hangup when the terminal closes or an SSH session drops -- without
// watching SIGHUP here too, that would silently orphan a running git push,
// fetch or rebase instead of stopping it.
func rootSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
}

// forwardSignal is what a first signal forwards to every live child group: a
// real SIGINT, regardless of which of rootSignals wb itself received, so a
// SIGHUP-triggered shutdown still asks children to stop exactly the way a
// terminal Ctrl-C would.
func forwardSignal() os.Signal { return syscall.SIGINT }

// killSignal is what a second signal sends to every live child group.
func killSignal() os.Signal { return syscall.SIGKILL }

// exitCodeForSignal maps the signal that caused wb to exit onto the
// conventional 128+signal shell exit code.
func exitCodeForSignal(sig os.Signal) int {
	switch sig {
	case syscall.SIGHUP:
		return 129
	case syscall.SIGTERM:
		return 143
	default: // os.Interrupt / SIGINT
		return 130
	}
}
