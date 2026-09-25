//go:build !darwin && !linux

package main

import "os"

// rootSignals is the set wb's root context watches for interruption. Windows
// has no SIGTERM/SIGHUP in the os/signal sense; os.Interrupt (Ctrl-C or
// Ctrl-Break) is the only portable signal Go delivers there, matching the
// daemon supervisor's own signalDaemonContext for this platform.
func rootSignals() []os.Signal { return []os.Signal{os.Interrupt} }

// forwardSignal and killSignal are never actually delivered anywhere on this
// platform: internal/process's live-group registry no-ops here, since there
// is no process-group primitive to signal, so the concrete value returned is
// unobservable. Windows has no Setsid equivalent wired up either, so
// console's ssh BatchMode injection (internal/console) remains the only
// guard here against a wb-started child prompting on a console.
func forwardSignal() os.Signal { return os.Interrupt }
func killSignal() os.Signal    { return os.Interrupt }

// exitCodeForSignal matches the conventional 128+2 shell code for SIGINT,
// the closest real signal os.Interrupt represents.
func exitCodeForSignal(os.Signal) int { return 130 }
