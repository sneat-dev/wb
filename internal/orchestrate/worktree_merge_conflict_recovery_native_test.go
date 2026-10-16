package orchestrate

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
)

type conflictRecoveryFixture struct {
	engine    engineFixture
	receipt   WorktreeMergeReceipt
	head      string
	claimPath string
}

func TestUnpublishedLandConflictRetryRequiresExactCleanCandidate(t *testing.T) {
	f := newConflictRecoveryFixture(t)
	receipt := f.receipt
	receipt.Phase = WorktreeMergePhaseLand
	receipt.Candidate.SHA = f.head
	before := receipt
	changed, err := advanceResolvedConflictCandidateWithRunner(t.Context(), runner.New(), f.engine.githubDir, &receipt, 5*time.Second, 0, nativeConflictAdvanceStore())
	if err != nil || changed || !reflect.DeepEqual(receipt, before) {
		t.Fatalf("unchanged unpublished land candidate: changed=%v receipt=%+v err=%v", changed, receipt, err)
	}

	receipt.Candidate.SHA = f.receipt.Candidate.SHA
	before = receipt
	changed, err = advanceResolvedConflictCandidateWithRunner(t.Context(), runner.New(), f.engine.githubDir, &receipt, 5*time.Second, 0, nativeConflictAdvanceStore())
	if changed || err == nil || !strings.Contains(err.Error(), "unpublished land-phase candidate HEAD drifted") || !reflect.DeepEqual(receipt, before) {
		t.Fatalf("drifted unpublished land candidate: changed=%v receipt=%+v err=%v", changed, receipt, err)
	}

	receipt = f.receipt
	receipt.Phase = WorktreeMergePhaseLand
	receipt.Candidate.SHA = f.head
	receipt.PublishedCandidateSHA = f.head
	receipt.PullRequest = "https://example.test/pull/1"
	changed, err = advanceResolvedConflictCandidateWithRunner(t.Context(), runner.New(), f.engine.githubDir, &receipt, 5*time.Second, 0, nativeConflictAdvanceStore())
	if changed || err != nil {
		t.Fatalf("published candidate should use published recovery: changed=%v err=%v", changed, err)
	}
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
