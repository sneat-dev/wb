package orchestrate

import (
	"context"
	"errors"
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
	receipt.Route.Route = WorktreeMergeRoutePullRequest
	receipt.ValidationDeferral = &WorktreeMergeValidationDeferral{Route: WorktreeMergeRoutePullRequest, CandidateSHA: f.head}
	receipt.Failure = "managed pre-push gate failed before opening the push connection: host load"
	before := receipt
	changed, err := advanceResolvedConflictCandidateWithRunner(t.Context(), runner.New(), f.engine.githubDir, &receipt, 5*time.Second, 0, nativeConflictAdvanceStore())
	if err != nil || changed || !reflect.DeepEqual(receipt, before) {
		t.Fatalf("unchanged unpublished land candidate: changed=%v receipt=%+v err=%v", changed, receipt, err)
	}

	receipt.Candidate.SHA = f.receipt.Candidate.SHA
	receipt.ValidationDeferral.CandidateSHA = receipt.Candidate.SHA
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

func TestUnpublishedLandConflictRejectsPublishedOrUnobservableCandidate(t *testing.T) {
	f := newConflictRecoveryFixture(t)
	receipt := f.receipt
	receipt.Phase = WorktreeMergePhaseLand
	receipt.Candidate.SHA = f.head
	receipt.Route.Route = WorktreeMergeRoutePullRequest
	receipt.ValidationDeferral = &WorktreeMergeValidationDeferral{Route: WorktreeMergeRoutePullRequest, CandidateSHA: f.head}
	receipt.Failure = "managed pre-push gate failed before opening the push connection: host load"
	before := receipt
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", f.head+":refs/heads/"+receipt.Candidate.Branch)
	changed, err := advanceResolvedConflictCandidateWithRunner(t.Context(), runner.New(), f.engine.githubDir, &receipt, 5*time.Second, 0, nativeConflictAdvanceStore())
	if changed || err == nil || !strings.Contains(err.Error(), "already published") || !reflect.DeepEqual(receipt, before) {
		t.Fatalf("published candidate accepted: changed=%v err=%v", changed, err)
	}
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", ":refs/heads/"+receipt.Candidate.Branch)
	refusal := errors.New("remote read refused")
	run := conflictNegativeRunner{Runner: runner.New(), dir: receipt.Candidate.Worktree, argv: "ls-remote --heads origin refs/heads/" + receipt.Candidate.Branch, err: refusal}
	changed, err = advanceResolvedConflictCandidateWithRunner(t.Context(), run, f.engine.githubDir, &receipt, 5*time.Second, 0, nativeConflictAdvanceStore())
	if changed || !errors.Is(err, refusal) || !reflect.DeepEqual(receipt, before) {
		t.Fatalf("unobservable candidate accepted: changed=%v err=%v", changed, err)
	}

	receipt.Failure = "unrelated land conflict"
	changed, err = advanceResolvedConflictCandidateWithRunner(t.Context(), runner.New(), f.engine.githubDir, &receipt, 5*time.Second, 0, nativeConflictAdvanceStore())
	if changed || err == nil || !strings.Contains(err.Error(), "pre-push refusal") {
		t.Fatalf("unrelated land conflict accepted: changed=%v err=%v", changed, err)
	}
	receipt.Failure = "receipt " + receipt.ReceiptPath + " has no recoverable conflict candidate"
	receipt.HostLoadAdmission = &WorktreeMergeHostLoadAdmission{Load: 35, Floor: 36}
	changed, err = advanceResolvedConflictCandidateWithRunner(t.Context(), runner.New(), f.engine.githubDir, &receipt, 5*time.Second, 0, nativeConflictAdvanceStore())
	if changed || err != nil {
		t.Fatalf("legacy pre-push receipt refused: changed=%v err=%v", changed, err)
	}
	receipt.ValidationDeferral = nil
	changed, err = advanceResolvedConflictCandidateWithRunner(t.Context(), runner.New(), f.engine.githubDir, &receipt, 5*time.Second, 0, nativeConflictAdvanceStore())
	if changed || err == nil || !strings.Contains(err.Error(), "pre-push refusal") {
		t.Fatalf("unproven legacy refusal accepted: changed=%v err=%v", changed, err)
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
