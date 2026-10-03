// Package integration exercises the actual cmdrun/runexec boundary with native
// child and queue mechanisms. It remains in the default Go test suite while
// argument and rendering tests stay in the cheap cmdrun package.
package integration

import (
	"bytes"
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/cmdrun"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/runexec"
	"github.com/sneat-dev/wb/internal/runqueue"
	"github.com/sneat-dev/wb/internal/testenv"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const holderHoldDuration = runexec.AdmissionGrace + 150*time.Millisecond

func TestMain(m *testing.M) {
	testenv.IsolateProcess()
	testenv.IsolateHarnessProcess()
	testenv.GitAutoMaintenanceOffProcess()
	remove, err := testenv.IsolateUserState()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	remove()
	os.Exit(code)
}
func TestRunCommandReportsQueueVisibilityOnStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the WB fleet runs on macOS and Linux")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	// Force the small-machine (N<8) legacy budget-sum pool: see the same
	// comment on TestAcquireWithQueueVisibilityEmitsQueuedHeartbeatsThenAdmitted.
	defer runqueue.SetNumCPUForTest(4)()
	t.Setenv("WB_ADMISSION_LOAD_FLOOR", "100000")
	root := t.TempDir()

	module := t.TempDir()
	for name, content := range map[string]string{"go.mod": "module tinyfixture\n\ngo 1.21\n", "main.go": "package main\n\nfunc main() {}\n"} {
		if err := os.WriteFile(filepath.Join(module, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(module)

	budget := runqueue.Budget()
	held, _, err := runqueue.Acquire(context.Background(), root, budget, budget)
	if err != nil {
		t.Fatal(err)
	}
	holderAnnouncement := held.Announce(runqueue.Participant{PID: os.Getpid(), Summary: "go build"})
	released := make(chan struct{})
	go func() {
		testenv.WaitForQueued(t, root, budget)
		time.Sleep(holderHoldDuration)
		holderAnnouncement.Cleanup()
		held.Release()
		close(released)
	}()
	t.Cleanup(func() { <-released })

	var stdout, stderr bytes.Buffer
	flags := shared.Flags{}
	command := cmdrun.New(shared.Runtime{Flags: func() shared.Flags { return flags }, ExitError: func(code int, message string) error { return fmt.Errorf("exit%d: %s", code, message) }}, cmdrun.Dependencies{Execute: (runexec.Executor{QueueHeartbeat: 5 * time.Millisecond}).Run, LoadHint: runexec.LoadHint})
	command.Flags().StringVar(&flags.ProjectsRoot, "projects-root", "", "root")
	command.SetArgs([]string{"--projects-root", root, "--", "go", "build", "./..."})
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SilenceErrors = true
	command.SilenceUsage = true
	err = command.Execute()
	code := 0
	if err != nil {
		code = 1
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want %d; stderr=%s", code, 0, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want the wrapped command's own (empty, on success) output only", stdout.String())
	}
	rendered := stderr.String()
	for _, want := range []string{
		"wb run: queued go build",
		"wb run: still queued",
		"wb run: admitted after",
		"wb run: done in",
		"(exit 0)",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("stderr missing %q: %q", want, rendered)
		}
	}
}
