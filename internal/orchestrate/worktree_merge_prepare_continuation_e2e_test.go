//go:build e2e

package orchestrate

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/runner"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestE2EPrepareContinuationNativeDefaultsAndPublicationPolicies(t *testing.T) {
	t.Parallel()
	f := newConflictRecoveryFixture(t)
	exact := preparingContinuationReceipt(f)
	before := exact
	if err := validateExactPreparingWorktreeMergeReceipt(t.Context(), exact, exact.Lane, exact.ID, exact.Sources); err != nil {
		t.Fatal(err)
	}
	if err := validatePreparingWorktreeMergeCandidate(t.Context(), exact); err != nil {
		t.Fatal(err)
	}
	empty := exact
	empty.Candidate.SHA = ""
	if err := validateExactPreparingWorktreeMergeReceipt(t.Context(), empty, empty.Lane, empty.ID, empty.Sources); err != nil {
		t.Fatalf("legacy empty candidate snapshot: %v", err)
	}
	current := advanceContinuationSource(t, f)
	prior := exact
	prior.Status = WorktreeMergePrepared
	if ok, err := canRefreshWorktreeMergeReceipt(t.Context(), prior, current); err != nil || !ok {
		t.Fatalf("native unpublished refresh=%v error=%v", ok, err)
	}
	repair := prior
	repair.Status = WorktreeMergePostTargetCIFailed
	repair.LandingSHA = f.head
	if ok, err := canPreparePostTargetRepair(t.Context(), repair, current); err != nil || !ok {
		t.Fatalf("native retained post-target candidate=%v error=%v", ok, err)
	}
	runEngineGit(t, prior.Candidate.Worktree, "push", "origin", prior.Candidate.Branch)
	if ok, err := canRefreshWorktreeMergeReceipt(t.Context(), prior, current); err != nil || ok {
		t.Fatalf("unrecorded publication refresh=%v error=%v", ok, err)
	}
	if err := validateExactPreparingWorktreeMergeReceipt(t.Context(), exact, exact.Lane, exact.ID, exact.Sources); err == nil || !strings.Contains(err.Error(), "was published") {
		t.Fatalf("exact preparing published branch=%v", err)
	}
	prior.PullRequest = "https://example.test/acme/app/pull/41"
	for _, published := range []string{"", f.head} {
		prior.PublishedCandidateSHA = published
		repair.PublishedCandidateSHA = published
		if ok, err := canRefreshWorktreeMergeReceipt(t.Context(), prior, current); err != nil || !ok {
			t.Fatalf("native exact recorded publication=%v error=%v", ok, err)
		}
		if ok, err := canPreparePostTargetRepair(t.Context(), repair, current); err != nil || !ok {
			t.Fatalf("native exact/fallback post-target publication=%v error=%v", ok, err)
		}
	}
	prior.PublishedCandidateSHA = prior.TargetSHA
	repair.PublishedCandidateSHA = prior.TargetSHA
	if ok, err := canRefreshWorktreeMergeReceipt(t.Context(), prior, current); err != nil || ok {
		t.Fatalf("foreign publication refresh=%v error=%v", ok, err)
	}
	if ok, err := canPreparePostTargetRepair(t.Context(), repair, current); err != nil || ok {
		t.Fatalf("foreign publication repair=%v error=%v", ok, err)
	}
	prior.PublishedCandidateSHA = f.head
	prior.Candidate.SHA = prior.TargetSHA
	repair.Candidate.SHA = repair.TargetSHA
	if ok, err := canRefreshWorktreeMergeReceipt(t.Context(), prior, current); err != nil || ok {
		t.Fatalf("drifted retained head refresh=%v error=%v", ok, err)
	}
	if ok, err := canPreparePostTargetRepair(t.Context(), repair, current); err != nil || ok {
		t.Fatalf("drifted retained head repair=%v error=%v", ok, err)
	}
	if !reflect.DeepEqual(exact, before) {
		t.Fatal("continuation mutated immutable input receipt")
	}
}

func TestE2EPrepareContinuationSuppliedRunnerFailuresAndReadOrder(t *testing.T) {
	t.Parallel()
	f := newConflictRecoveryFixture(t)
	current := advanceContinuationSource(t, f)
	prior := preparingContinuationReceipt(f)
	prior.Status = WorktreeMergePrepared
	prior.PullRequest = "41"
	runEngineGit(t, prior.Candidate.Worktree, "push", "origin", prior.Candidate.Branch)
	repair := prior
	repair.Status = WorktreeMergePostTargetCIFailed
	repair.LandingSHA = f.head
	old := f.receipt.Sources[0]
	ancestor := "merge-base " + old.SHA + " " + current[0].SHA
	remote := "ls-remote --heads origin refs/heads/" + prior.Candidate.Branch
	head := "rev-parse --verify HEAD^{commit}"
	status := "status --porcelain=v1"
	sentinel := errors.New("controlled exact continuation read refused")
	for _, tc := range []struct {
		name, dir, args string
		silent          bool
	}{
		{"source ancestry", old.Worktree, ancestor, false},
		{"candidate cleanliness", prior.Candidate.Worktree, status, true},
		{"candidate publication", prior.Candidate.Worktree, remote, false},
		{"candidate HEAD", prior.Candidate.Worktree, head, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			run := conflictNegativeRunner{Runner: runner.New(), dir: tc.dir, argv: tc.args, err: sentinel}
			ok, err := canRefreshWorktreeMergeReceiptWithRunner(t.Context(), run, prior, current)
			if ok || tc.silent && err != nil || !tc.silent && !errors.Is(err, sentinel) {
				t.Fatalf("refresh named read=%s ok=%v error=%v", tc.name, ok, err)
			}
			ok, err = canPreparePostTargetRepairWithRunner(t.Context(), run, repair, current)
			if ok || tc.silent && err != nil || !tc.silent && !errors.Is(err, sentinel) {
				t.Fatalf("repair named read=%s ok=%v error=%v", tc.name, ok, err)
			}
		})
	}
	for _, tc := range []struct {
		name    string
		receipt WorktreeMergeReceipt
		call    func(context.Context, *publishedObservedRunner, WorktreeMergeReceipt) (bool, error)
		want    []string
	}{
		{"refresh", prior, func(ctx context.Context, r *publishedObservedRunner, p WorktreeMergeReceipt) (bool, error) {
			return canRefreshWorktreeMergeReceiptWithRunner(ctx, r, p, current)
		}, []string{old.Worktree + "\x00git " + ancestor, prior.Candidate.Worktree + "\x00git " + status, prior.Candidate.Worktree + "\x00git " + remote, prior.Candidate.Worktree + "\x00git " + head}},
		{"post-target repair", repair, func(ctx context.Context, r *publishedObservedRunner, p WorktreeMergeReceipt) (bool, error) {
			return canPreparePostTargetRepairWithRunner(ctx, r, p, current)
		}, []string{old.Worktree + "\x00git " + ancestor, prior.Candidate.Worktree + "\x00git " + status, prior.Candidate.Worktree + "\x00git " + head, prior.Candidate.Worktree + "\x00git " + remote}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			run := &publishedObservedRunner{Runner: runner.New()}
			if ok, err := tc.call(t.Context(), run, tc.receipt); err != nil || !ok || !reflect.DeepEqual(run.reads, tc.want) {
				t.Fatalf("native read order=%q want=%q ok=%v error=%v", run.reads, tc.want, ok, err)
			}
		})
	}
}

func TestE2EPrepareContinuationNativeDirtyAndRewrittenSourceRefusals(t *testing.T) {
	t.Parallel()
	f := newConflictRecoveryFixture(t)
	current := advanceContinuationSource(t, f)
	prior := preparingContinuationReceipt(f)
	prior.Status = WorktreeMergePrepared
	repair := prior
	repair.Status = WorktreeMergePostTargetCIFailed
	repair.LandingSHA = f.head
	// Reversed actual DAG endpoints have a successful merge-base that is not
	// the recorded source, so no manufactured ancestry observation is needed.
	reversed := prior
	reversed.Sources = append([]WorktreeMergeSource(nil), current...)
	if ok, err := canRefreshWorktreeMergeReceipt(t.Context(), reversed, prior.Sources); ok || err != nil {
		t.Fatalf("rewritten source refresh=%v error=%v", ok, err)
	}
	reversed.Status = WorktreeMergePostTargetCIFailed
	reversed.LandingSHA = f.head
	if ok, err := canPreparePostTargetRepair(t.Context(), reversed, prior.Sources); ok || err != nil {
		t.Fatalf("rewritten source repair=%v error=%v", ok, err)
	}
	writeEngineFile(t, filepath.Join(prior.Candidate.Worktree, "dirty.txt"), "retain actual private data\n")
	if ok, err := canRefreshWorktreeMergeReceipt(t.Context(), prior, current); ok || err != nil {
		t.Fatalf("dirty refresh=%v error=%v", ok, err)
	}
	if ok, err := canPreparePostTargetRepair(t.Context(), repair, current); ok || err != nil {
		t.Fatalf("dirty repair=%v error=%v", ok, err)
	}
	prior.Status = WorktreeMergePreparing
	if err := validateExactPreparingWorktreeMergeReceipt(t.Context(), prior, prior.Lane, prior.ID, prior.Sources); err == nil || !strings.Contains(err.Error(), "not safely resumable") {
		t.Fatalf("dirty exact preparing=%v", err)
	}
	if err := validatePreparingWorktreeMergeCandidate(t.Context(), prior); err == nil || !strings.Contains(err.Error(), "not clean") {
		t.Fatalf("dirty integrated preparing=%v", err)
	}
}

func TestE2EPrepareContinuationIntegratedGraphAndExactReadErrors(t *testing.T) {
	t.Parallel()
	f := newConflictRecoveryFixture(t)
	receipt := preparingContinuationReceipt(f)
	sentinel := errors.New("controlled exact interrupted candidate read refused")
	path := receipt.Candidate.Worktree
	for _, tc := range []struct{ name, args string }{
		{"status", "status --porcelain=v1"},
		{"HEAD", "rev-parse --verify HEAD^{commit}"},
		{"remote", "ls-remote --heads origin refs/heads/" + receipt.Candidate.Branch},
	} {
		t.Run("exact "+tc.name, func(t *testing.T) {
			t.Parallel()
			run := conflictNegativeRunner{Runner: runner.New(), dir: path, argv: tc.args, err: sentinel}
			if err := validateExactPreparingWorktreeMergeReceiptWithRunner(t.Context(), run, receipt, receipt.Lane, receipt.ID, receipt.Sources); !errors.Is(err, sentinel) {
				t.Fatalf("exact read=%s error=%v", tc.name, err)
			}
		})
	}
	for _, tc := range []struct{ name, args string }{
		{"status", "status --porcelain=v1"},
		{"HEAD", "rev-parse --verify HEAD^{commit}"},
		{"target ancestry", "merge-base " + receipt.TargetSHA + " " + f.head},
		{"source ancestry", "merge-base " + receipt.Sources[0].SHA + " " + f.head},
	} {
		t.Run("integrated "+tc.name, func(t *testing.T) {
			t.Parallel()
			run := conflictNegativeRunner{Runner: runner.New(), dir: path, argv: tc.args, err: sentinel}
			if err := validatePreparingWorktreeMergeCandidateWithRunner(t.Context(), run, receipt); !errors.Is(err, sentinel) {
				t.Fatalf("integrated read=%s error=%v", tc.name, err)
			}
		})
	}
	drifted := receipt
	drifted.Candidate.SHA = receipt.TargetSHA
	if err := validateExactPreparingWorktreeMergeReceipt(t.Context(), drifted, drifted.Lane, drifted.ID, drifted.Sources); err == nil || !strings.Contains(err.Error(), "head drifted") {
		t.Fatalf("exact drift=%v", err)
	}
	if err := validatePreparingWorktreeMergeCandidate(t.Context(), drifted); err == nil || !strings.Contains(err.Error(), "head drifted") {
		t.Fatalf("integrated drift=%v", err)
	}
	extra := createMergeSource(t, f.engine, "continuation-unmerged", "feature/continuation-unmerged", "extra.txt", "extra\n")
	unrelated := strings.TrimSpace(runEngineGit(t, extra.WorktreeDir, "rev-parse", "HEAD"))
	missingTarget := receipt
	missingTarget.TargetSHA = unrelated
	if err := validatePreparingWorktreeMergeCandidate(t.Context(), missingTarget); err == nil || !strings.Contains(err.Error(), "does not contain target") {
		t.Fatalf("unmerged target=%v", err)
	}
	missingSource := receipt
	missingSource.Sources = append([]WorktreeMergeSource(nil), receipt.Sources...)
	missingSource.Sources[0].SHA = unrelated
	if err := validatePreparingWorktreeMergeCandidate(t.Context(), missingSource); err == nil || !strings.Contains(err.Error(), "does not contain source") {
		t.Fatalf("unmerged source=%v", err)
	}
}
