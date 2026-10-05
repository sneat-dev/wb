package orchestrate

import (
	"context"
	"github.com/sneat-dev/wb/internal/runner"
	"reflect"
	"testing"
)

func landedFailureOwnerFixture(t *testing.T) (engineFixture, WorktreeMergeReceipt) {
	t.Helper()
	f := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, f, "landed-failure-owner-source", "feature/landed-failure-owner", "owner.txt", "native owner\n")
	r, err := PrepareWorktreeMerge(t.Context(), WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err != nil {
		t.Fatal(err)
	}
	r.Status = WorktreeMergeValidationFailed
	r.Failure = "historical failed validation"
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, f.canonical, "update-ref", "refs/heads/main", r.Candidate.SHA)
	runEngineGit(t, f.canonical, "push", "origin", "main")
	return f, r
}

type landedFailureOwnerRunner struct {
	runner.Runner
	path          string
	args          []string
	ordinal, seen int
	sentinel      error
}

func (r *landedFailureOwnerRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if dir == r.path && name == "git" && reflect.DeepEqual(args, r.args) {
		r.seen++
		if r.seen == r.ordinal {
			return runner.Result{}, r.sentinel
		}
	}
	return r.Runner.RunOpts(ctx, dir, opts, name, args...)
}
