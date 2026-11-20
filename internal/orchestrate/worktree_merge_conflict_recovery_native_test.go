package orchestrate

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
)

type conflictRecoveryFixture struct {
	engine    engineFixture
	receipt   WorktreeMergeReceipt
	head      string
	claimPath string
}

func newConflictRecoveryFixture(t *testing.T) conflictRecoveryFixture {
	t.Helper()
	fixture := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, fixture, "recovery-source", "feature/recovery-source", "source.txt", "source\n")
	candidate := createMergeSource(t, fixture, "recovery-candidate", "wb/recovery-candidate", "candidate.txt", "candidate\n")
	before := strings.TrimSpace(runEngineGit(t, candidate.WorktreeDir, "rev-parse", "HEAD"))
	sourceSHA := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	runEngineGit(t, candidate.WorktreeDir, "merge", "--no-edit", sourceSHA)
	head := strings.TrimSpace(runEngineGit(t, candidate.WorktreeDir, "rev-parse", "HEAD"))
	receipt := WorktreeMergeReceipt{SchemaVersion: WorktreeMergeSchemaVersion, ID: "recovery-contract", Lane: "recovery-lane", Repository: fixture.repository.Slug, Target: "main", TargetSHA: candidate.BaseSHA, Phase: WorktreeMergePhasePrepare, Status: WorktreeMergeConflict, Failure: "prior conflict", Candidate: WorktreeMergeCandidate{Task: "recovery-candidate", Worktree: candidate.WorktreeDir, Branch: candidate.Branch, SHA: before}, Sources: []WorktreeMergeSource{{Task: "recovery-source", Worktree: source.WorktreeDir, Branch: source.Branch, SHA: sourceSHA}}, ReceiptPath: filepath.Join(t.TempDir(), "receipt.json")}
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	return conflictRecoveryFixture{engine: fixture, receipt: receipt, head: head, claimPath: candidate.WorkLogPath}
}

// conflictNegativeRunner refuses only the named read. Every other operation,
// including all positive custody/DAG observations, runs through native Git.
type conflictNegativeRunner struct {
	runner.Runner
	dir, argv string
	err       error
}

func (r conflictNegativeRunner) RunOpts(ctx context.Context, dir string, options runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if name == "git" && dir == r.dir && strings.Join(args, " ") == r.argv {
		return runner.Result{}, r.err
	}
	return r.Runner.RunOpts(ctx, dir, options, name, args...)
}
