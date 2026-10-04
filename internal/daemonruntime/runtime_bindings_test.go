package daemonruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemon"
)

func TestRuntimeDefaultsBindIndependentClocksAndSafeNativeObservers(t *testing.T) {
	sentinel := errors.New("typed usage refusal")
	deps := DefaultDependencies(func(string) error { return sentinel })
	if got := deps.Bounds(); got != (LifecycleBounds{5 * time.Second, 5 * time.Second, 30 * time.Second, 200 * time.Millisecond}) {
		t.Fatalf("bounds = %#v", got)
	}
	before := time.Now()
	if now := deps.Now(); now.Location() != time.UTC || now.Before(before.Add(-time.Second)) {
		t.Fatalf("UTC clock = %v", now)
	}
	if now := deps.LockNow(); now.Before(before) {
		t.Fatalf("lock clock = %v", now)
	}
	if executable, err := deps.Executable(); err != nil || executable == "" {
		t.Fatalf("executable = %q, %v", executable, err)
	}
	if deps.Getpid() != os.Getpid() || deps.Getppid() != os.Getppid() {
		t.Fatal("native process bindings changed")
	}
	if deps.Alive(-1) || ProcessAlive(-1) {
		t.Fatal("native liveness changed")
	}
	if deps.Version().Platform == "" {
		t.Fatal("version snapshot omitted platform")
	}
	if token, err := deps.Token(); err != nil || token == "" {
		t.Fatalf("token = %q,%v", token, err)
	}
	if _, known := deps.ObservedSupervisor(0); known {
		t.Fatal("invalid PID gained supervisor evidence")
	}
	if _, known := deps.ObservedCgroupUnit(0); known {
		t.Fatal("invalid PID gained cgroup evidence")
	}
	if _, known := deps.ProcessStartTime(0); known {
		t.Fatal("invalid PID gained process generation")
	}
	if present, _ := deps.SupervisorPresent(daemon.SupervisorNone, ""); present {
		t.Fatal("none supervisor became present")
	}
	if deps.SystemdUnitName() == "" || deps.HubConfigPath() == "" {
		t.Fatal("actual configuration bindings omitted")
	}
	if deps.Getenv("WB_RUNTIME_NONEXISTENT_TEST_VARIABLE") != "" {
		t.Fatal("unexpected environment binding")
	}
	root := daemonTestRoot(t)
	if _, found, err := NewController(deps, root).LoadState(); err != nil || found {
		t.Fatalf("empty state = %t,%v", found, err)
	}
	if _, err := NewController(deps, root).Start(context.Background(), "0.0.0.0:1"); !errors.Is(err, sentinel) {
		t.Fatalf("usage identity = %v", err)
	}
	for _, factory := range []func(time.Duration) (<-chan time.Time, func()){deps.GuardTicker, deps.RestartTicker} {
		ticks, stop := factory(time.Nanosecond)
		select {
		case <-ticks:
		case <-time.After(time.Second):
			t.Fatal("actual timer did not tick")
		}
		stop()
	}
}

func TestLifecycleBoundsRemainPerControllerAndLazy(t *testing.T) {
	t.Parallel()
	bounds := DefaultLifecycleBounds()
	first := NewController(Dependencies{Bounds: func() LifecycleBounds { return bounds }}, "unused")
	second := NewController(Dependencies{}, "unused")
	bounds.Stop = time.Millisecond
	if first.bounds().Stop != time.Millisecond || second.bounds().Stop != 5*time.Second {
		t.Fatal("instance bounds leaked or were snapshotted")
	}
}

func TestBoundedNativeCommandCapturesPrivateChildOutput(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "private-runtime-child")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf private-output"), 0700); err != nil {
		t.Fatal(err)
	}
	got, err := runBoundedNativeCommand(path, time.Second)
	if err != nil || string(got) != "private-output" {
		t.Fatalf("native child = %q,%v", got, err)
	}
	ctx, cancel := SignalContext(context.Background())
	cancel()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("signal context = %v", ctx.Err())
	}
}

func TestNativeObserverAdaptersPreserveUnknownGenerationAndPolicyResolutionRefusals(t *testing.T) {
	t.Parallel()
	failure := errors.New("OS account resolution refused")
	if allowed, path, err := daemonRawPolicyWithResolver("private-root", func() (string, error) { return "", failure }); allowed || path != "" || !errors.Is(err, failure) {
		t.Fatalf("policy=%t,%q,%v", allowed, path, err)
	}
	observed := time.Date(2026, 10, 4, 1, 2, 3, 0, time.UTC)
	for _, known := range []bool{false, true} {
		got := processStartedAtWithObserver(72, func(pid int) (time.Time, bool) {
			if pid != 72 {
				t.Errorf("pid=%d", pid)
			}
			return observed, known
		})
		if known && !got.Equal(observed) {
			t.Fatalf("known generation=%v", got)
		}
		if !known && !got.IsZero() {
			t.Fatalf("unknown generation became evidence: %v", got)
		}
	}
}
