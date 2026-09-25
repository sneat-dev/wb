package lifecyclehooks

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

// TestRunInvocationWithRunnerWritesCapturedOutputAndPropagatesError covers
// runInvocationWithRunner directly against a scripted runner.Runner: the
// exact argv/dir/env it issues, and that captured stdout/stderr reach the
// invocation's writers even on a failing run.
func TestRunInvocationWithRunnerWritesCapturedOutputAndPropagatesError(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.Expect(func(c runnertest.Call) bool {
		return c.Op == "RunOpts" && c.Name == "indexer" && c.Dir == "/checkout" &&
			len(c.Args) == 1 && c.Args[0] == "sync" && len(c.Opts.Env) == 1 && c.Opts.Env[0] == "X=1"
	}, runner.Result{Stdout: "out", Stderr: "warn"}, errors.New("exit status 1"))

	var stdout, stderr bytes.Buffer
	invocation := Invocation{Run: "indexer", Args: []string{"sync"}, Dir: "/checkout", Env: []string{"X=1"}, Stdout: &stdout, Stderr: &stderr}
	err := runInvocationWithRunner(context.Background(), invocation, fake)
	if err == nil {
		t.Fatal("want the runner's own failure propagated")
	}
	if stdout.String() != "out" || stderr.String() != "warn" {
		t.Fatalf("stdout/stderr = %q/%q", stdout.String(), stderr.String())
	}
}

// TestRunInvocationWithRunnerToleratesNilWriters proves a nil
// Stdout/Stderr (the shape an Invocation built without diagnostics carries)
// never panics.
func TestRunInvocationWithRunnerToleratesNilWriters(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"indexer"}, runner.Result{Stdout: "out"}, nil)
	invocation := Invocation{Run: "indexer"}
	if err := runInvocationWithRunner(context.Background(), invocation, fake); err != nil {
		t.Fatalf("runInvocationWithRunner: %v", err)
	}
}

// TestLaunchWorkerWithRunnerUsesAnInjectedRunner covers the resolveRunner
// "r != nil" branch directly: a scripted runner.Runner answers Detach
// instead of the production runner.Runner.
func TestLaunchWorkerWithRunnerUsesAnInjectedRunner(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.Expect(func(c runnertest.Call) bool { return c.Op == "Detach" }, runner.Result{}, nil)

	request := WorkerRequest{ConfigPath: "/wb.yaml", StateDir: "/state", ReceiptPath: "/receipts.jsonl"}
	if err := launchWorkerWithRunner(request, fake); err != nil {
		t.Fatalf("launchWorkerWithRunner: %v", err)
	}
}

// TestLaunchWorkerWithRunnerPropagatesADetachFailure covers Detach's own
// error branch.
func TestLaunchWorkerWithRunnerPropagatesADetachFailure(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.Expect(func(c runnertest.Call) bool { return c.Op == "Detach" }, runner.Result{}, errors.New("fork/exec: resource temporarily unavailable"))

	request := WorkerRequest{ConfigPath: "/wb.yaml", StateDir: "/state", ReceiptPath: "/receipts.jsonl"}
	if err := launchWorkerWithRunner(request, fake); err == nil {
		t.Fatal("want the Detach failure propagated")
	}
}
