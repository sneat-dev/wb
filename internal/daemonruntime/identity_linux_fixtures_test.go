//go:build linux

package daemonruntime

// AC: recycled-pids-are-not-mistaken-for-a-live-daemon
//
// Linux is the platform where WB can observe a process start time, so this is
// where a recycled PID can be told apart from the process the record was
// written for.

// A recycled PID must not be signalled: stop is the one path where mistaking a
// stranger for the daemon does damage rather than merely misleading output.
