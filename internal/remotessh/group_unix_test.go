//go:build !windows

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

func TestGroupRunnerDeliversStdinAndReportsOutputAndExitStatus(t *testing.T) {
	t.Parallel()
	stdout, stderr := NewLimitedBuffer(1024), NewLimitedBuffer(1024)
	err := GroupRunner{}.Run(context.Background(), "/bin/sh", []string{"-c", "cat; echo boom >&2; exit 3"}, []byte("payload"), stdout, stderr)
	var exited interface{ ExitCode() int }
	if !errors.As(err, &exited) || exited.ExitCode() != 3 {
		t.Fatalf("Run error = %v, want exit status 3", err)
	}
	if string(stdout.Bytes()) != "payload" || !strings.Contains(string(stderr.Bytes()), "boom") {
		t.Fatalf("stdout = %q, stderr = %q", stdout.Bytes(), stderr.Bytes())
	}
}

func TestGroupRunnerKillsTheWholeProcessGroupWhenItsContextEnds(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	t.Cleanup(cancel)
	stdout := NewLimitedBuffer(64)
	started := time.Now()
	// The shell starts a helper that would outlive it, says its process id and
	// waits: both hold the output pipe open.
	err := GroupRunner{}.Run(ctx, "/bin/sh", []string{"-c", "sleep 60 & echo $!; wait"}, nil, stdout, NewLimitedBuffer(64))
	if err == nil {
		t.Fatal("a killed command must be reported")
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("Run returned after %s: the command was not killed at its deadline", elapsed)
	}
	helper, convErr := strconv.Atoi(strings.TrimSpace(string(stdout.Bytes())))
	if convErr != nil || helper <= 1 {
		t.Fatalf("the helper's process id was not printed: %q", stdout.Bytes())
	}
	deadline := time.Now().Add(10 * time.Second)
	for syscall.Kill(helper, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("the helper process %d outlived the command: the group was not killed", helper)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestKillGroupReportsAGroupThatIsGoneAsTheProcessBeingDone(t *testing.T) {
	t.Parallel()
	// No process group has this id: it is above every platform's largest process id.
	if err := killGroup(0x7ffffff0); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("killGroup = %v, want os.ErrProcessDone", err)
	}
}
