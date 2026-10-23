package orchestrate

import (
	"context"
	"github.com/sneat-dev/wb/internal/runner"
	"strings"
	"testing"
)

func landOwnerNativeFixture(t *testing.T) (engineFixture, WorktreeMergeReceipt) {
	t.Helper()
	fixture := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, fixture, "land-owner-source", "feature/land-owner-source", "land-owner.txt", "land owner\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return fixture, receipt
}

// landOwnerFaultRunner refuses one exact observation and delegates every
// successful command to the guarded real runner. It cannot grant custody.
type landOwnerFaultRunner struct {
	runner.Runner
	refuse  func(string, string, []string) bool
	failure error
	refused bool
	later   int
	calls   []string
}

func (r *landOwnerFaultRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	r.calls = append(r.calls, dir+": "+name+" "+strings.Join(args, " "))
	if r.refused {
		r.later++
	}
	if !r.refused && r.refuse(dir, name, args) {
		r.refused = true
		return runner.Result{}, r.failure
	}
	return r.Runner.RunOpts(ctx, dir, opts, name, args...)
}
