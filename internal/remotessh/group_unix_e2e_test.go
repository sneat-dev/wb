//go:build e2e && !windows

package remotessh

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These tests start real processes (/bin/sh and sleep), so they are in the e2e
// tier. None of them starts ssh.

func TestE2EGroupRunnerDeliversStdinAndReportsOutputAndExitStatus(t *testing.T) {
	t.Parallel()
	stdout, stderr := NewLimitedBuffer(1024), NewTailBuffer(1024)
	err := GroupRunner{}.Run(context.Background(), "/bin/sh", []string{"-c", "cat; echo boom >&2; exit 3"}, []byte("payload"), stdout, stderr)
	var exited interface{ ExitCode() int }
	if !errors.As(err, &exited) || exited.ExitCode() != 3 {
		t.Fatalf("Run error = %v, want exit status 3", err)
	}
	if string(stdout.Bytes()) != "payload" || !strings.Contains(string(stderr.Bytes()), "boom") {
		t.Fatalf("stdout = %q, stderr = %q", stdout.Bytes(), stderr.Bytes())
	}
}

// TestE2EGroupRunnerGivesItsCommandOnlyTheAllowedEnvironment: whatever this
// process holds (a test binary holds far more than the allow-list), the command
// sees the allow-list alone.
func TestE2EGroupRunnerGivesItsCommandOnlyTheAllowedEnvironment(t *testing.T) {
	t.Parallel()
	dropped := 0
	for _, entry := range os.Environ() {
		if name, _, _ := strings.Cut(entry, "="); !strings.Contains(" PATH LANG HOME USER LOGNAME SSH_AUTH_SOCK ", " "+name+" ") {
			dropped++
		}
	}
	if dropped == 0 {
		t.Skip("this process holds no variable outside the allow-list")
	}
	stdout := NewLimitedBuffer(1 << 16)
	if err := (GroupRunner{}).Run(context.Background(), "/usr/bin/env", nil, nil, stdout, NewTailBuffer(64)); err != nil {
		t.Fatal(err)
	}
	seen := string(stdout.Bytes())
	if !strings.Contains(seen, "PATH=/usr/bin:/bin:/usr/sbin:/sbin\n") || !strings.Contains(seen, "LANG=C\n") {
		t.Fatalf("the command's environment = %q", seen)
	}
	for _, line := range strings.Split(strings.TrimSpace(seen), "\n") {
		name, _, _ := strings.Cut(line, "=")
		if !strings.Contains(" PATH LANG HOME USER LOGNAME SSH_AUTH_SOCK ", " "+name+" ") {
			t.Errorf("the command was given %s", name)
		}
	}
}

// helperOf reads the process id the test's shell printed for its helper.
func helperOf(t *testing.T, stdout *LimitedBuffer) int {
	t.Helper()
	helper, err := strconv.Atoi(strings.TrimSpace(string(stdout.Bytes())))
	if err != nil || helper <= 1 {
		t.Fatalf("the helper's process id was not printed: %q", stdout.Bytes())
	}
	return helper
}

func TestE2EGroupRunnerKillsTheWholeProcessGroupWhenItsContextEnds(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	t.Cleanup(cancel)
	stdout := NewLimitedBuffer(64)
	started := time.Now()
	// The shell starts a helper that would outlive it, says its process id and
	// waits: both hold the output pipe open.
	err := GroupRunner{}.Run(ctx, "/bin/sh", []string{"-c", "sleep 60 & echo $!; wait"}, nil, stdout, NewTailBuffer(64))
	if err == nil {
		t.Fatal("a killed command must be reported")
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("Run returned after %s: the command was not killed at its deadline", elapsed)
	}
	helper := helperOf(t, stdout)
	deadline := time.Now().Add(10 * time.Second)
	for syscall.Kill(helper, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("the helper process %d outlived the command: the group was not killed", helper)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestE2EGroupRunnerReturnsAlthoughAHelperOutsideTheGroupHoldsThePipe: a helper
// that left the process group (job control gives it one of its own) survives
// the kill and keeps the output pipe open. Run still returns, groupWaitDelay
// after the deadline, instead of waiting for the helper to end.
func TestE2EGroupRunnerReturnsAlthoughAHelperOutsideTheGroupHoldsThePipe(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	t.Cleanup(cancel)
	stdout := NewLimitedBuffer(64)
	started := time.Now()
	err := GroupRunner{}.Run(ctx, "/bin/sh", []string{"-c", "set -m; sleep 30 & echo $!; wait"}, nil, stdout, NewTailBuffer(64))
	elapsed := time.Since(started)
	helper := helperOf(t, stdout)
	survived := syscall.Kill(helper, 0) == nil
	_ = syscall.Kill(helper, syscall.SIGKILL)
	if err == nil || elapsed > 20*time.Second {
		t.Fatalf("Run = %v after %s, want a failure within the wait delay", err, elapsed)
	}
	if !survived {
		t.Skip("this shell did not give the helper a process group of its own")
	}
	if elapsed < groupWaitDelay {
		t.Fatalf("Run returned after %s although the helper held the pipe", elapsed)
	}
}
