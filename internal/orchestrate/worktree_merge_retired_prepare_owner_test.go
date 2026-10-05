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
)

// Each observation delegates native positive results and can refuse only its
// named negative stage; it never supplies a successful custody or Git result.
type retirementOwnerRunner struct {
	runner.Runner
	before func(context.Context, string, string, []string) error
}

func (r retirementOwnerRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if r.before != nil {
		if err := r.before(ctx, dir, name, args); err != nil {
			return runner.Result{}, err
		}
	}
	return r.Runner.RunOpts(ctx, dir, opts, name, args...)
}

// The original legacy fixture recipe uses explicit roots here. Its sources are
// recorded legacy schema, not a claim that those sources have native custody.
func retirementOwnerFixture(t *testing.T, diverged bool) (engineFixture, WorktreeMergeReceipt) {
	t.Helper()
	f := newExplicitRootEngineFixture(t)
	runEngineGit(t, f.repository.CloneURL, "symbolic-ref", "HEAD", "refs/heads/main")
	runEngineGit(t, f.canonical, "branch", "deleted-target", "main")
	if diverged {
		runEngineGit(t, f.canonical, "checkout", "deleted-target")
		writeEngineFile(t, filepath.Join(f.canonical, "only-on-deleted-target.txt"), "target only\n")
		runEngineGit(t, f.canonical, "add", "only-on-deleted-target.txt")
		runEngineGit(t, f.canonical, "commit", "-m", "target only")
		runEngineGit(t, f.canonical, "checkout", "main")
	}
	runEngineGit(t, f.canonical, "push", "origin", "deleted-target")
	created, err := worktrees.Create(t.Context(), []string{f.repository.Slug}, worktrees.CreateOptions{ProjectsRoot: f.githubDir, Operation: "retired-prepare-candidate", Base: "deleted-target", WorkLog: worktrees.WorkLogOptions{Model: "test-model"}})
	if err != nil || len(created) != 1 {
		t.Fatalf("native legacy candidate: %+v %v", created, err)
	}
	c := created[0]
	home, err := wbhome.Root(f.githubDir)
	if err != nil {
		t.Fatal(err)
	}
	r := WorktreeMergeReceipt{SchemaVersion: WorktreeMergeSchemaVersion, ID: "retired-prepare-receipt", Lane: worktreeMergeLaneID(f.repository.Slug, "deleted-target"), Phase: WorktreeMergePhasePrepare, Status: WorktreeMergeConflict, Repository: f.repository.Slug, Target: "deleted-target", TargetSHA: c.BaseSHA, Candidate: WorktreeMergeCandidate{Task: "retired-prepare-candidate", Worktree: c.WorktreeDir, Branch: c.Branch}, Sources: []WorktreeMergeSource{{Task: "source", Worktree: filepath.Join(f.githubDir, "source"), Branch: "feature/source", SHA: strings.Repeat("b", 40)}}, ReceiptPath: filepath.Join(home, "reports", "worktree-merge", "retired-prepare-receipt.json")}
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, f.canonical, "push", "origin", ":deleted-target")
	return f, r
}
func retirementOwnerReceiptBytes(t *testing.T, r WorktreeMergeReceipt) func() {
	t.Helper()
	before, err := os.ReadFile(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		after, err := os.ReadFile(r.ReceiptPath)
		if err != nil || string(after) != string(before) {
			t.Fatalf("immutable receipt changed: %v", err)
		}
	}
}
func retirementOwnerReleased(t *testing.T, root, lane string) {
	t.Helper()
	lock, err := AcquireOperationLock(root, lane, true)
	if err != nil {
		t.Fatalf("owned lock not released: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}
func retirementOwnerOptions(f engineFixture, r WorktreeMergeReceipt) WorktreeMergeRetiredPrepareCandidateAcknowledgementOptions {
	return WorktreeMergeRetiredPrepareCandidateAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "native legacy candidate is contained and target absent"}
}

func TestRetiredPrepareOwnerReceiptPolicyContracts(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipt.json")
	r := WorktreeMergeReceipt{ReceiptPath: path, ID: "legacy", Repository: "acme/app", Target: "gone", TargetSHA: strings.Repeat("a", 40), Phase: WorktreeMergePhasePrepare, Status: WorktreeMergeConflict, Candidate: WorktreeMergeCandidate{Task: "candidate", Worktree: "/candidate", Branch: "wb/candidate"}, Sources: []WorktreeMergeSource{{Task: "source", Worktree: "/source", Branch: "feature/source", SHA: strings.Repeat("b", 40)}}}
	r.Lane = worktreeMergeLaneID(r.Repository, r.Target)
	if err := validateRetiredPrepareCandidateReceipt(r, path); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, want string
		mutate     func(*WorktreeMergeReceipt)
	}{
		{"path", "inconsistent", func(r *WorktreeMergeReceipt) { r.ReceiptPath = "different" }},
		{"id", "inconsistent", func(r *WorktreeMergeReceipt) { r.ID = "" }},
		{"lane", "inconsistent", func(r *WorktreeMergeReceipt) { r.Lane = "" }},
		{"phase", "failed prepare conflict", func(r *WorktreeMergeReceipt) { r.Phase = WorktreeMergePhaseLand }},
		{"status", "failed prepare conflict", func(r *WorktreeMergeReceipt) { r.Status = WorktreeMergePrepared }},
		{"landing", "failed prepare conflict", func(r *WorktreeMergeReceipt) { r.LandingSHA = r.TargetSHA }},
		{"pr", "failed prepare conflict", func(r *WorktreeMergeReceipt) { r.PullRequest = "17" }},
		{"published", "failed prepare conflict", func(r *WorktreeMergeReceipt) { r.PublishedCandidateSHA = r.TargetSHA }},
		{"target sha", "empty-candidate", func(r *WorktreeMergeReceipt) { r.TargetSHA = "" }},
		{"candidate sha", "empty-candidate", func(r *WorktreeMergeReceipt) { r.Candidate.SHA = r.TargetSHA }},
		{"confused source", "candidate-confused", func(r *WorktreeMergeReceipt) { r.Sources[0].Worktree = r.Candidate.Worktree }},
		{"missing source", "candidate-confused", func(r *WorktreeMergeReceipt) { r.Sources = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			copy := r
			copy.Sources = append([]WorktreeMergeSource(nil), r.Sources...)
			tc.mutate(&copy)
			if err := validateRetiredPrepareCandidateReceipt(copy, path); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("actual schema refusal: %v want %s", err, tc.want)
			}
		})
	}
}
