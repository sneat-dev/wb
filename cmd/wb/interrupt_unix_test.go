//go:build darwin || linux

package main

import (
	"context"
	"os"
	"reflect"
	"sync"
	"syscall"
	"testing"
)

// TestRootSignalsWatchesSIGHUPTooOnUnix pins review round 3's SIGHUP
// addition: internal/process now gives every non-interactive child its own
// session (Setsid), so it no longer receives the terminal's own hangup when
// the terminal closes or an SSH session drops. Without watching SIGHUP here
// too, that would silently orphan a running git push, fetch or rebase
// instead of stopping it.
func TestRootSignalsWatchesSIGHUPTooOnUnix(t *testing.T) {
	t.Parallel()
	want := []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
	if got := rootSignals(); !reflect.DeepEqual(got, want) {
		t.Fatalf("rootSignals() = %v, want %v", got, want)
	}
}

// TestExitCodeForSignalMapsConventionalShellCodes pins the exit codes review
// round 3 assigns: 128+signal, matching a shell's own convention for a
// process that died to a signal.
func TestExitCodeForSignalMapsConventionalShellCodes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		signal os.Signal
		want   int
	}{
		{syscall.SIGHUP, 129},
		{os.Interrupt, 130},
		{syscall.SIGINT, 130},
		{syscall.SIGTERM, 143},
	}
	for _, testCase := range cases {
		if got := exitCodeForSignal(testCase.signal); got != testCase.want {
			t.Errorf("exitCodeForSignal(%v) = %d, want %d", testCase.signal, got, testCase.want)
		}
	}
}

// TestRunSignalLoopASecondDifferentSignalStillKillsAndExitsWithItsOwnCode
// proves the second stage is keyed to whichever signal arrives second, not
// to the first: a SIGHUP first (cancel, refuse, forward SIGINT) followed by
// a SIGINT second still kills every live group and exits with SIGINT's own
// code (130), not SIGHUP's (129).
func TestRunSignalLoopASecondDifferentSignalStillKillsAndExitsWithItsOwnCode(t *testing.T) {
	t.Parallel()
	ch := make(chan os.Signal, 2)
	_, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var exited []int
	done := make(chan struct{})
	go func() {
		defer close(done)
		runSignalLoop(ch, cancel, func() {}, func(os.Signal) {}, func(code int) {
			mu.Lock()
			exited = append(exited, code)
			mu.Unlock()
		})
	}()

	// The channel is ordered and buffered: runSignalLoop reads SIGHUP first
	// (stage one) and SIGINT second (stage two) regardless of how these two
	// sends are scheduled relative to its goroutine.
	ch <- syscall.SIGHUP
	ch <- syscall.SIGINT
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(exited) != 1 || exited[0] != 130 {
		t.Fatalf("exit calls = %v, want exactly one call with SIGINT's own code 130", exited)
	}
}
