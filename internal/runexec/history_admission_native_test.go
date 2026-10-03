package runexec

import (
	"bytes"
	"context"
	"github.com/sneat-dev/wb/internal/runlog"
	"github.com/sneat-dev/wb/internal/runqueue"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNativeHistoryRecordsQueueWaitAndAdmissionTime(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the WB fleet runs on macOS and Linux")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	root := t.TempDir()

	module, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(module, "go.mod"), "module tinyfixture\n\ngo 1.21\n")
	writeFixture(t, filepath.Join(module, "main.go"), "package main\nfunc main() {}\n")
	git := exec.Command("git", "init", "-b", "main")
	git.Dir = module
	if output, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	manifest := worktrees.Manifest{
		Version: 1, EffortID: "run-queue-visibility", EffortKind: worktrees.EffortKindFeature,
		Repository: "acme/app", Worktree: module, Branch: "run-queue-visibility", Base: "main",
		BaseSHA: strings.Repeat("a", 40), CreatedAt: time.Now().UTC(),
		RunID: "run-1", ClaimID: strings.Repeat("b", 64), Provenance: worktrees.ProvenanceCreated,
	}
	if err := worktrees.WriteManifest(module, manifest); err != nil {
		t.Fatal(err)
	}

	// Force the small-machine (N<8) legacy budget-sum pool: see the same
	// comment on TestAcquireWithQueueVisibilityEmitsQueuedHeartbeatsThenAdmitted.
	defer runqueue.SetNumCPUForTest(4)()
	budget := runqueue.Budget()
	held, _, err := runqueue.Acquire(context.Background(), root, budget, budget)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		testenv.WaitForQueued(t, root, budget)
		time.Sleep(holderHoldDuration)
		held.Release()
		close(released)
	}()
	t.Cleanup(func() { <-released })

	var stdout, stderr bytes.Buffer
	ops := defaultExecuteOperations()
	ops.Getwd = func() (string, error) { return module, nil }
	ops.ResolveLoad = func(string) (float64, string) { return 0, "env" }
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := (Executor{ops: &ops}).Run(ctx, ExecuteRequest{ProjectsRoot: root, Argv: []string{"go", "build", "./..."}, Stdout: &stdout, Stderr: &stderr})
	if err != nil || result.ExitCode != 0 || result.ChildFailed {
		t.Fatalf("result=%+v err=%v stderr=%s", result, err, stderr.String())
	}

	events, _, err := runlog.ReadCurrent(module)
	if err != nil {
		t.Fatal(err)
	}
	var completed *runlog.Event
	for index := range events {
		if events[index].State == "succeeded" {
			completed = &events[index]
		}
	}
	if completed == nil {
		t.Fatalf("no succeeded event among %d events", len(events))
	}
	if completed.QueueWaitMS < int64(AdmissionGrace/time.Millisecond) {
		t.Errorf("QueueWaitMS = %d, want at least the admission grace period", completed.QueueWaitMS)
	}
	if completed.AdmittedAt == nil || completed.AdmittedAt.IsZero() {
		t.Fatalf("AdmittedAt = %v, want a recorded admission time", completed.AdmittedAt)
	}
}
