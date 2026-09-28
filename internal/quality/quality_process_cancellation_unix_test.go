//go:build !windows

package quality

// Process-group cancellation behavior: killing a forked child tree relies on
// POSIX process groups and signals (syscall.Kill), which have no equivalent
// on Windows. Kept in its own file so the rest of this package's tests stay
// buildable and runnable under GOOS=windows.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunWithOptionsCancellationTerminatesForkedProcessTree(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("WB process-tree cancellation is supported on Darwin and Linux")
	}
	for _, test := range []struct {
		name, startupDelay string
	}{
		{name: "immediate", startupDelay: ""},
		// This exceeds the former one-second PID polling deadline. It proves
		// readiness, rather than a race with the attempt deadline, owns start.
		{name: "delayed-start", startupDelay: "sleep 1.2\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			pidsPath := filepath.Join(dir, "pids")
			tool := filepath.Join(dir, "forking-cancellation-tool")
			writeQualityExecutableFile(t, tool, "#!/bin/sh\n"+test.startupDelay+"sleep 30 &\nchild=$!\nprintf '%s %s' \"$$\" \"$child\" > \""+pidsPath+"\"\nwhile :; do sleep 1; done\n")

			type result struct {
				output   string
				attempts int
				err      error
			}
			resultCh := make(chan result, 1)
			done := make(chan struct{})
			ctx, cancel := context.WithCancel(context.Background())
			var recordedPIDs []int
			parentGroupID := 0
			t.Cleanup(func() {
				cancel()
				drained := false
				select {
				case <-done:
					drained = true
				case <-time.After(time.Second):
				}
				// The assertions below normally prove these PIDs are already gone.
				// If a mutation regresses group cancellation and an assertion aborts
				// first, kill only the group whose recorded parent was proved to own
				// it, so this test never leaves its child sleep running for 30s.
				if parentGroupID != 0 && qualityProcessesAlive(recordedPIDs) {
					if err := syscall.Kill(-parentGroupID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
						t.Errorf("kill recorded process group %d: %v", parentGroupID, err)
					}
					if drained {
						t.Error("recorded process survived cancellation; test cleanup killed its group")
					}
				}
				if !drained {
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Error("forking process did not drain after test cleanup cancellation")
					}
				}
			})
			go func() {
				// The 30-second attempt deadline is only a safety net. The separate
				// real-deadline test above owns timeout-to-error mapping; this test
				// owns readiness and whole-process-tree cancellation.
				output, attempts, err := runWithOptions(ctx, RunOptions{Timeout: 30 * time.Second}, dir, tool)
				resultCh <- result{output: output, attempts: attempts, err: err}
				close(done)
			}()

			// The script owns readiness by writing both PIDs. Its bounded watchdog
			// detects a startup failure and reports an early command exit instead
			// of competing with the cancellation behavior being asserted.
			readinessDeadline := time.NewTimer(10 * time.Second)
			defer readinessDeadline.Stop()
			var pids []string
			for len(pids) == 0 {
				select {
				case outcome := <-resultCh:
					t.Fatalf("forking process exited before readiness: err %v, attempts %d, output %q", outcome.err, outcome.attempts, outcome.output)
				case <-readinessDeadline.C:
					t.Fatal("timed out waiting for forked process readiness")
				default:
				}
				raw, err := os.ReadFile(pidsPath)
				if err == nil && strings.TrimSpace(string(raw)) != "" {
					pids = strings.Fields(string(raw))
					break
				}
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("read %s: %v", pidsPath, err)
				}
				time.Sleep(10 * time.Millisecond)
			}
			if len(pids) != 2 {
				t.Fatalf("recorded PIDs = %q, want parent and child", pids)
			}
			for _, rawPID := range pids {
				pid, parseErr := strconv.Atoi(rawPID)
				if parseErr != nil || pid <= 0 {
					t.Fatalf("recorded PID %q: %v", rawPID, parseErr)
				}
				recordedPIDs = append(recordedPIDs, pid)
				assertQualityProcessAlive(t, pid)
			}
			parentGroupID = qualityOwnedProcessGroup(t, recordedPIDs[0])
			cancel()
			var outcome result
			select {
			case outcome = <-resultCh:
			case <-time.After(5 * time.Second):
				t.Fatal("forking process did not return within five seconds of cancellation")
			}
			if ctx.Err() != context.Canceled || outcome.err == nil || strings.Contains(outcome.err.Error(), "timed out after") || outcome.attempts != 1 {
				t.Fatalf("cancellation result = context %v, err %v, attempts %d, output %q", ctx.Err(), outcome.err, outcome.attempts, outcome.output)
			}
			for _, rawPID := range pids {
				pid, parseErr := strconv.Atoi(rawPID)
				if parseErr != nil || pid <= 0 {
					t.Fatalf("recorded PID %q: %v", rawPID, parseErr)
				}
				assertQualityProcessGone(t, pid)
			}
		})
	}
}

func assertQualityProcessAlive(t *testing.T, pid int) {
	t.Helper()
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("probe ready PID %d: %v", pid, err)
	}
}

func qualityOwnedProcessGroup(t *testing.T, pid int) int {
	t.Helper()
	output, err := exec.Command("ps", "-o", "pgid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("read process group for %d: %v", pid, err)
	}
	groupID, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil || groupID != pid {
		t.Fatalf("process group for recorded parent %d = %q; want its own group", pid, output)
	}
	return groupID
}

func qualityProcessesAlive(pids []int) bool {
	for _, pid := range pids {
		if syscall.Kill(pid, 0) == nil {
			return true
		}
	}
	return false
}

func assertQualityProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil {
			t.Fatalf("probe PID %d: %v", pid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("forked process PID %d survived cancellation", pid)
}
