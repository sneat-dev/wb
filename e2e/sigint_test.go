//go:build e2e

package e2e

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// sigintDeadline bounds every wait below: generous enough for a slow shared
// runner to build the real binary and start a shell, short enough that a
// genuine hang (the bug this whole lane exists to fix) fails the test
// instead of the job's own timeout.
const sigintDeadline = 60 * time.Second

var (
	sigintBinary    string
	sigintBuildErr  error
	sigintBuildOnce sync.Once
)

// buildRealWB compiles the actual wb binary once for this test file. Package
// e2e already runs as its own CI job (go test -tags e2e), separate from the
// coverage job's default `go test ./...`, so building and driving a real
// process here is in scope for the tier even though it never happens in the
// unit tier.
func buildRealWB(t *testing.T) string {
	t.Helper()
	sigintBuildOnce.Do(func() {
		directory, err := os.MkdirTemp("", "wb-e2e-sigint-")
		if err != nil {
			sigintBuildErr = err
			return
		}
		binary := filepath.Join(directory, "wb")
		if runtime.GOOS == "windows" {
			binary += ".exe"
		}
		// The module-qualified import path, not a relative directory, so the
		// build resolves correctly regardless of this test binary's own
		// working directory.
		build := exec.Command("go", "build", "-o", binary, "github.com/sneat-dev/wb/cmd/wb")
		build.Stdout, build.Stderr = os.Stderr, os.Stderr
		if err := build.Run(); err != nil {
			sigintBuildErr = fmt.Errorf("build wb: %w", err)
			return
		}
		sigintBinary = binary
	})
	if sigintBuildErr != nil {
		t.Fatalf("build wb: %v", sigintBuildErr)
	}
	return sigintBinary
}

// TestE2ESigintForwardsToWBsChildProcessGroup is the coordinator's ask: start
// wb on a command whose child blocks, send SIGINT, and assert the child
// process group is gone. It is the end-to-end proof for the whole lane's
// problem statement -- git/gh children now start in their own session
// (internal/process, Setsid) and no longer receive a terminal's Ctrl-C
// directly, so wb's own root context (cmd/wb's newRootContext) and the
// live-group registry (internal/process.SignalLiveGroups) are what must
// forward it on instead.
//
// wb run -- <command> is the vehicle: runExternalCommand (cmd/wb/run.go)
// drives its child through process.CommandContextInteractive(cmd.Context(),
// ...), and "sh"/"sleep" are not a runqueue.Classify kind, so this runs with
// no CPU-admission wait -- deterministic on a busy shared runner.
func TestE2ESigintForwardsToWBsChildProcessGroup(t *testing.T) {
	binary := buildRealWB(t)
	projectsRoot := t.TempDir()
	pidPath := filepath.Join(t.TempDir(), "child.pid")

	script := `echo $$ > "$WB_E2E_PID_PATH"; while :; do sleep 1; done`
	wb := exec.Command(binary, "--projects-root", projectsRoot, "run", "--", "sh", "-c", script)
	wb.Env = append(os.Environ(), "WB_E2E_PID_PATH="+pidPath)
	var stderr strings.Builder
	wb.Stderr = &stderr
	if err := wb.Start(); err != nil {
		t.Fatalf("start wb: %v", err)
	}

	childPID := readChildPID(t, pidPath)

	if err := wb.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("signal wb: %v", err)
	}

	waitErr := make(chan error, 1)
	go func() { waitErr <- wb.Wait() }()
	select {
	case err := <-waitErr:
		// wb is expected to report the interrupted command as a failure
		// (runqueue's own child-killed error), not hang -- exactly the bug
		// report this lane fixes: "interrupting wb pr land / wb worktree
		// merge can leave git fetch/push running."
		if err == nil {
			t.Fatal("wb exited 0 after SIGINT, want it to report the interrupted child")
		}
	case <-time.After(sigintDeadline):
		_ = wb.Process.Kill()
		t.Fatalf("wb did not exit within %s of SIGINT; stderr so far: %s", sigintDeadline, stderr.String())
	}

	// wb's own runExternalCommand waits on its child before it can exit, so
	// by the time wb.Wait() above returned, sh (and the sleep it forked, in
	// the same process group) has already been reaped by wb itself -- not a
	// zombie under this test's own process, so kill(pid, 0) reliably reports
	// ESRCH once it is really gone.
	deadline := time.Now().Add(sigintDeadline)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(childPID, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child pid %d (wb's process group) survived wb's own exit; stderr: %s", childPID, stderr.String())
}

func readChildPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(sigintDeadline)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			value := strings.TrimSpace(string(raw))
			if value != "" {
				pid, parseErr := strconv.Atoi(value)
				if parseErr != nil || pid <= 0 {
					t.Fatalf("child PID = %q: %v", raw, parseErr)
				}
				return pid
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read child PID: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("wb's child never recorded its PID")
	return 0
}
