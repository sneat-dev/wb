package orchestrate

import (
	"context"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type prepareOwnerFixture struct {
	engine  engineFixture
	source  worktrees.CreateResult
	sources []WorktreeMergeSource
	options WorktreeMergePrepareOptions
	receipt WorktreeMergeReceipt
}

func newPrepareOwnerFixture(t *testing.T) prepareOwnerFixture {
	t.Helper()
	f := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, f, "owner-source", "feature/owner-source", "source.txt", "source\n")
	head := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	physical, err := filepath.EvalSymlinks(source.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	sources := []WorktreeMergeSource{{Task: "owner-source", Worktree: physical, Branch: source.Branch, SHA: head}}
	lane := worktreeMergeLaneID(f.repository.Slug, "main")
	operation := worktreeMergeOperationID(lane, sources)
	home, err := wbhome.Root(f.githubDir)
	if err != nil {
		t.Fatal(err)
	}
	receipt := WorktreeMergeReceipt{SchemaVersion: WorktreeMergeSchemaVersion, ID: operation, Lane: lane, Repository: f.repository.Slug, Target: "main", TargetSHA: source.BaseSHA, Phase: WorktreeMergePhasePrepare, Status: WorktreeMergePreparing, Sources: sources, ReceiptPath: filepath.Join(home, "reports", "worktree-merge", operation+".json")}
	options := WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", Route: WorktreeMergeRoute("unsupported-owner-route")}
	return prepareOwnerFixture{engine: f, source: source, sources: sources, options: options, receipt: receipt}
}

func (f *prepareOwnerFixture) createInterruptedCandidate(t *testing.T) {
	t.Helper()
	prompt, err := writeWorktreeMergePrompt(f.engine.repository.Slug, "main", f.sources)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Remove(prompt); err != nil {
			t.Error(err)
		}
	}()
	created, err := worktrees.Create(t.Context(), []string{f.engine.repository.Slug}, worktrees.CreateOptions{
		ProjectsRoot: f.engine.githubDir, Operation: f.receipt.ID, Branch: "wb/integration/main/" + mergeOperationSuffix(f.receipt.ID), BranchChosen: true, Base: "main",
		WorkLog: worktrees.WorkLogOptions{EffortID: f.receipt.ID, RunID: f.receipt.ID, AgentRuntime: "test", Model: "test-model", OriginalPrompt: prompt, RequireOriginalPrompt: true},
	})
	if err != nil || len(created) != 1 {
		t.Fatalf("actual interrupted native creation = %+v, %v", created, err)
	}
	candidate := created[0]
	f.receipt.Candidate = WorktreeMergeCandidate{Task: f.receipt.ID, Worktree: candidate.WorktreeDir, Branch: candidate.Branch, SHA: strings.TrimSpace(runEngineGit(t, candidate.WorktreeDir, "rev-parse", "HEAD"))}
	f.receipt.CreatedAt = time.Now().Add(-time.Hour).UTC()
	if err := persistWorktreeMergeReceipt(f.receipt); err != nil {
		t.Fatal(err)
	}
	if err := validateExactPreparingWorktreeMergeReceipt(t.Context(), f.receipt, f.receipt.Lane, f.receipt.ID, f.sources); err != nil {
		t.Fatalf("native interrupted candidate preconditions: %v", err)
	}
}

// prepareOwnerObservedRunner has no successful-result substitution. A named
// negative observation or private native mutation runs before delegating Git.
type prepareOwnerObservedRunner struct {
	runner.Runner
	before func(context.Context, string, string, []string) error
}

func (r prepareOwnerObservedRunner) RunOpts(ctx context.Context, dir string, options runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if err := r.before(ctx, dir, name, args); err != nil {
		return runner.Result{}, err
	}
	return r.Runner.RunOpts(ctx, dir, options, name, args...)
}
