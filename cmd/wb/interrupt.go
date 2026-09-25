package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/sneat-dev/wb/internal/process"
)

// newRootContext wires wb's interrupt handling for one process invocation.
// The returned context is cancelled by the first signal in rootSignals(),
// and stop unregisters the handler: every caller must call it once the
// invocation is done (runWithStdin defers it), so a command that finishes
// normally leaves no signal listener or goroutine behind.
//
// Ctrl-C used to reach a child directly, because it shared wb's own
// foreground process group. Since git/gh children now start through
// internal/process in their own session (Setsid, so they cannot be stopped
// by SIGTTIN/SIGTTOU for touching a controlling terminal they no longer
// have), a terminal signal only reaches wb itself; runSignalLoop is what
// forwards it on.
func newRootContext() (context.Context, func()) {
	return wireRootContext(signal.Notify, rootSignals())
}

// wireRootContext is newRootContext's testable seam: production calls it with
// signal.Notify itself, and a test supplies a fake that records the signals
// it was asked to watch and hands back a channel it controls directly --
// so the signal wiring can be verified with synthetic os.Signal values sent
// on an ordinary channel, never a real signal delivered to the test process.
func wireRootContext(notify func(chan<- os.Signal, ...os.Signal), signals []os.Signal) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 4)
	notify(ch, signals...)
	go runSignalLoop(ch, cancel, process.RefuseNewStarts, process.SignalLiveGroups, os.Exit)
	return ctx, func() {
		signal.Stop(ch)
		close(ch)
	}
}

// runSignalLoop drives wb's two-stage interrupt protocol against signals
// delivered on ch, until ch is closed (which newRootContext's stop does,
// after signal.Stop guarantees no further send). The handler stays
// registered for the whole run rather than reverting to default OS behaviour
// after the first signal: children run in their own session now, so a second
// terminal Ctrl-C reaches only wb, and it must forward that on too or a
// child started after the first signal would never be signalled at all.
//
// First signal: cancel the command context, refuse any further child whose
// own context can never be cancelled (refuseNewStarts; see
// process.RefuseNewStarts for why a bounded rollback is exempt), and forward
// SIGINT to every currently-live child process group (signalLiveGroups) --
// exactly what the terminal would have delivered directly before children
// moved into their own session.
//
// Any later signal is treated as a second Ctrl-C: it SIGKILLs every live
// group and exits the process directly with a signal-specific code
// (exitCodeForSignal), rather than waiting for cobra to unwind -- the
// terminal's own third-strike expectation, now that wb keeps the handler
// installed instead of reverting to default behaviour.
func runSignalLoop(
	ch <-chan os.Signal,
	cancel context.CancelFunc,
	refuseNewStarts func(),
	signalLiveGroups func(os.Signal),
	exit func(int),
) {
	stage := 0
	for sig := range ch {
		if stage == 0 {
			stage = 1
			cancel()
			refuseNewStarts()
			signalLiveGroups(forwardSignal())
			continue
		}
		signalLiveGroups(killSignal())
		exit(exitCodeForSignal(sig))
		return
	}
}
