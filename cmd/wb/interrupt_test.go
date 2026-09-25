package main

import (
	"context"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"
)

// TestWireRootContextRegistersRootSignalsWithoutARealSignal is the "injected
// notify function" unit test: it proves newRootContext's wiring asks to
// watch exactly rootSignals() and hands the loop a channel it can drive
// itself, all without signal.Notify (or any real OS signal) ever being
// involved.
func TestWireRootContextRegistersRootSignalsWithoutARealSignal(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var got []os.Signal
	var ch chan<- os.Signal
	notify := func(c chan<- os.Signal, signals ...os.Signal) {
		mu.Lock()
		defer mu.Unlock()
		ch = c
		got = append([]os.Signal(nil), signals...)
	}

	ctx, stop := wireRootContext(notify, rootSignals())
	t.Cleanup(stop)

	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(got, rootSignals()) {
		t.Fatalf("notify was asked to watch %v, want %v", got, rootSignals())
	}
	if ch == nil {
		t.Fatal("notify was never called with a channel")
	}
	select {
	case <-ctx.Done():
		t.Fatal("ctx already cancelled before any signal was delivered")
	default:
	}
}

// TestRunSignalLoopFirstSignalCancelsRefusesAndForwardsSIGINT pins stage one
// of the two-stage protocol (review round 2/3): the first signal -- of any
// of the three kinds wb watches -- cancels the command context, refuses
// further uncancellable starts, and forwards a SIGINT-shaped signal to live
// child groups, all without exiting the process.
func TestRunSignalLoopFirstSignalCancelsRefusesAndForwardsSIGINT(t *testing.T) {
	t.Parallel()
	ch := make(chan os.Signal, 1)
	ctx, cancel := context.WithCancel(context.Background())
	var refused int
	var forwarded []os.Signal
	var exited []int
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		runSignalLoop(ch,
			cancel,
			func() { mu.Lock(); refused++; mu.Unlock() },
			func(sig os.Signal) { mu.Lock(); forwarded = append(forwarded, sig); mu.Unlock() },
			func(code int) { mu.Lock(); exited = append(exited, code); mu.Unlock() },
		)
	}()

	ch <- os.Interrupt
	waitForCondition(t, func() bool {
		select {
		case <-ctx.Done():
			return true
		default:
			return false
		}
	})

	mu.Lock()
	defer mu.Unlock()
	if refused != 1 {
		t.Fatalf("refuseNewStarts called %d times after the first signal, want 1", refused)
	}
	if len(forwarded) != 1 || forwarded[0] != forwardSignal() {
		t.Fatalf("signalLiveGroups calls = %v, want exactly [%v]", forwarded, forwardSignal())
	}
	if len(exited) != 0 {
		t.Fatalf("exit called after only one signal: %v", exited)
	}
	close(ch)
	<-done
}

// TestRunSignalLoopSecondSignalKillsGroupsAndExits pins stage two: any signal
// after the first SIGKILLs every live group and exits with that second
// signal's own code, instead of waiting for cobra to unwind. The handler
// stays installed for this (review round 2): it does not revert to default
// OS behaviour after the first signal the way the original stop()-based
// design did. interrupt_unix_test.go covers a second signal of a *different*
// kind (e.g. SIGHUP then SIGINT) and the exact exit-code mapping; this test
// stays portable by using the one signal every platform's rootSignals()
// carries, os.Interrupt.
func TestRunSignalLoopSecondSignalKillsGroupsAndExits(t *testing.T) {
	t.Parallel()
	ch := make(chan os.Signal, 2)
	ctx, cancel := context.WithCancel(context.Background())
	var forwarded []os.Signal
	var exited []int
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		runSignalLoop(ch,
			cancel,
			func() {},
			func(sig os.Signal) { mu.Lock(); forwarded = append(forwarded, sig); mu.Unlock() },
			func(code int) { mu.Lock(); exited = append(exited, code); mu.Unlock() },
		)
	}()

	ch <- os.Interrupt
	waitForCondition(t, func() bool {
		select {
		case <-ctx.Done():
			return true
		default:
			return false
		}
	})
	ch <- os.Interrupt
	<-done // runSignalLoop returns right after calling exit, on the second signal.

	mu.Lock()
	defer mu.Unlock()
	if len(forwarded) != 2 {
		t.Fatalf("signalLiveGroups calls = %v, want a stage-one SIGINT and a stage-two kill", forwarded)
	}
	if forwarded[1] != killSignal() {
		t.Fatalf("second signalLiveGroups call = %v, want the kill signal %v", forwarded[1], killSignal())
	}
	if len(exited) != 1 || exited[0] != exitCodeForSignal(os.Interrupt) {
		t.Fatalf("exit calls = %v, want exactly one call with code %d", exited, exitCodeForSignal(os.Interrupt))
	}
}

// waitForCondition polls until cond is true or fails the test after a bound
// -- used here only to synchronise with runSignalLoop's own goroutine, never
// as a fix for flakiness in the production code under test.
func waitForCondition(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition never became true")
}
