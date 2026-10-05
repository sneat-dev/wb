package orchestrate

import (
	"bytes"
	"context"
	"github.com/sneat-dev/wb/internal/runner"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type conflictReplacementNativeFixture struct {
	fixture                  engineFixture
	receipt                  WorktreeMergeReceipt
	options                  WorktreeMergeConflictCandidateRefreshOptions
	claimPath                string
	receiptBytes, claimBytes []byte
}

// All claims and DAG observations come from real private native Prepare/Guard/
// Work Log. The initial conflict is physically resolved without changing its receipt.
func newConflictReplacementNativeFixture(t *testing.T) conflictReplacementNativeFixture {
	t.Helper()
	return newConflictReplacementNativeFixtureWithFixture(t, newExplicitRootEngineFixture(t))
}

func newConflictReplacementNativeFixtureWithFixture(t *testing.T, f engineFixture) conflictReplacementNativeFixture {
	t.Helper()
	first := createMergeSource(t, f, "replacement-owner-first", "feature/replacement-owner-first", "shared.txt", "first\n")
	second := createMergeSource(t, f, "replacement-owner-second", "feature/replacement-owner-second", "shared.txt", "second\n")
	r, err := PrepareWorktreeMerge(t.Context(), WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{first.WorktreeDir, second.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err == nil || r.Status != WorktreeMergeConflict {
		t.Fatalf("native initial conflict=%+v err=%v", r, err)
	}
	cmd := exec.CommandContext(t.Context(), "git", "merge", "--no-commit", r.Sources[1].SHA)
	cmd.Dir = r.Candidate.Worktree
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("native second-source conflict unexpectedly succeeded %s", out)
	}
	writeEngineFile(t, filepath.Join(r.Candidate.Worktree, "shared.txt"), "resolved\n")
	runEngineGit(t, r.Candidate.Worktree, "add", "shared.txt")
	runEngineGit(t, r.Candidate.Worktree, "commit", "-m", "test: resolve authentic conflict replacement input")
	claim, observed, err := validatePrepareFailureSupersessionCandidate(t.Context(), f.githubDir, r)
	if err != nil || observed == "" {
		t.Fatalf("native resolved input=%+v observed=%s err=%v", claim, observed, err)
	}
	rb, err := os.ReadFile(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := os.ReadFile(claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := worktreeMergeReceiptSHA256(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	target, err := fetchExactMergeTarget(t.Context(), r.Candidate.Worktree, r.Target)
	if err != nil {
		t.Fatal(err)
	}
	o := WorktreeMergeConflictCandidateRefreshOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Sources: []string{first.WorktreeDir, second.WorktreeDir}, ExpectedSourceSHAs: []string{r.Sources[0].SHA, r.Sources[1].SHA}, ExpectedReceiptSHA256: hash, ExpectedImmutableClaimSHA256: sha256Hex(cb), ExpectedCurrentTargetSHA: target, Actor: "reviewer", Reason: "native complete replacement contract", Timeout: 10 * time.Second}
	return conflictReplacementNativeFixture{f, r, o, claim.ClaimPath, rb, cb}
}

func (f conflictReplacementNativeFixture) assertHistoricalBytesAndUnlocked(t *testing.T) {
	t.Helper()
	for _, record := range []struct {
		path string
		want []byte
	}{{f.receipt.ReceiptPath, f.receiptBytes}, {f.claimPath, f.claimBytes}} {
		b, err := os.ReadFile(record.path)
		if err != nil || !bytes.Equal(b, record.want) {
			t.Fatalf("historical bytes changed %s: %v", record.path, err)
		}
	}
	lock, err := AcquireOperationLock(f.fixture.githubDir, f.receipt.Lane, true)
	if err != nil {
		t.Fatalf("native lane remained held: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}

func (f conflictReplacementNativeFixture) noReplacement(t *testing.T) {
	t.Helper()
	assertNoConflictCandidateRefresh(t, f.fixture, f.receipt, f.options)
}

type conflictReplacementObservedRunner struct {
	runner.Runner
	before func(context.Context, string, string, []string) error
	after  func(context.Context, string, string, []string, runner.Result, error)
}

func (r conflictReplacementObservedRunner) RunOpts(ctx context.Context, dir string, o runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if r.before != nil {
		if err := r.before(ctx, dir, name, args); err != nil {
			return runner.Result{}, err
		}
	}
	result, err := r.Runner.RunOpts(ctx, dir, o, name, args...)
	if r.after != nil {
		r.after(ctx, dir, name, args, result, err)
	}
	return result, err
}
