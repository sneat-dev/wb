//go:build e2e

package orchestrate

import (
	"context"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

func TestRunCommandPreservesCombinedOutputOrder(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "status"}, runner.Result{
		Stdout: "stdout-only", Stderr: "stderr-only", CombinedOutput: "out\nerr\nout again\n",
	}, nil)
	output, attempts, err := runCommand(context.Background(), fake, 0, 0, t.TempDir(), "git", "status")
	if err != nil || attempts != 1 || output != "out\nerr\nout again\n" {
		t.Fatalf("runCommand output=%q attempts=%d err=%v", output, attempts, err)
	}
	if calls := fake.Calls(); len(calls) != 1 || !calls[0].Opts.CaptureCombined {
		t.Fatalf("runner calls = %+v, want combined capture", calls)
	}
}
