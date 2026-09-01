//go:build darwin || linux

package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCommandContextCancellationTerminatesForkedChild(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	parentPIDPath := filepath.Join(filepath.Dir(pidPath), "parent.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	command := CommandContext(ctx, os.Args[0], "-test.run=^TestProcessHelper$")
	command.Env = append(os.Environ(), "WB_PROCESS_HELPER=fork-child", "WB_PROCESS_CHILD_PID_PATH="+pidPath, "WB_PROCESS_PARENT_PID_PATH="+parentPIDPath)
	type result struct {
		output []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		output, err := command.CombinedOutput()
		done <- result{output: output, err: err}
	}()
	parentPID := readProcessID(t, parentPIDPath)
	childPID := readProcessID(t, pidPath)
	outcome := <-done
	output, err := outcome.output, outcome.err
	if err == nil {
		t.Fatalf("command unexpectedly succeeded: %s", output)
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("context error = %v, want deadline exceeded; command error=%v output=%q", ctx.Err(), err, output)
	}
	assertProcessGone(t, childPID)
	assertProcessGone(t, parentPID)
}

func TestProcessHelper(t *testing.T) {
	switch os.Getenv("WB_PROCESS_HELPER") {
	case "sleep-child":
		sleepUntilKilled()
	case "fork-child":
		pidPath := os.Getenv("WB_PROCESS_CHILD_PID_PATH")
		parentPIDPath := os.Getenv("WB_PROCESS_PARENT_PID_PATH")
		if pidPath == "" || parentPIDPath == "" {
			os.Exit(2)
		}
		if err := os.WriteFile(parentPIDPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "write parent pid: %v", err)
			os.Exit(2)
		}
		child := exec.Command(os.Args[0], "-test.run=^TestProcessHelper$")
		child.Env = replaceEnvironment(os.Environ(), "WB_PROCESS_HELPER", "sleep-child")
		if err := child.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "start child: %v", err)
			os.Exit(2)
		}
		if err := os.WriteFile(pidPath, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "write child pid: %v", err)
			os.Exit(2)
		}
		sleepUntilKilled()
	}
}

func sleepUntilKilled() {
	for {
		time.Sleep(time.Second)
	}
}

func replaceEnvironment(environment []string, name, value string) []string {
	prefix := name + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func readProcessID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			value := strings.TrimSpace(string(raw))
			if value == "" {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			pid, parseErr := strconv.Atoi(value)
			if parseErr != nil || pid <= 0 {
				t.Fatalf("child PID = %q: %v", raw, parseErr)
			}
			return pid
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read child PID: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("forked child never recorded its PID")
	return 0
}

func assertProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil {
			t.Fatalf("probe child PID %d: %v", pid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("forked child PID %d survived command cancellation", pid)
}
