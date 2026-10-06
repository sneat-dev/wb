//go:build e2e

package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
)

// Only a selected negative command is substituted. Every positive observation
// is made by the native runner; after observes a successful actual command.
type adoptionClosureRunner struct {
	runner.Runner
	before func(string, string, []string) error
	after  func(string, string, []string)
}

func (r adoptionClosureRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if r.before != nil {
		if err := r.before(dir, name, args); err != nil {
			return runner.Result{}, err
		}
	}
	result, err := r.Runner.RunOpts(ctx, dir, opts, name, args...)
	if err == nil && r.after != nil {
		r.after(dir, name, args)
	}
	return result, err
}

func adoptionClosureCleanupGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, _, err := runCommand(ctx, defaultRunner, 0, 0, dir, "git", args...); err != nil {
		t.Errorf("restore native Git: %v", err)
	}
}

func adoptionClosureBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func adoptionClosureRestore(t *testing.T, path string) []byte {
	t.Helper()
	b := adoptionClosureBytes(t, path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.WriteFile(path, b, info.Mode().Perm()); err != nil {
			t.Errorf("restore %s: %v", path, err)
		}
	})
	return b
}

func adoptionClosureNoAck(t *testing.T, r WorktreeMergeReceipt) {
	t.Helper()
	if _, err := os.Stat(publishedCandidateAdoptionPath(r.ReceiptPath)); !os.IsNotExist(err) {
		t.Fatalf("unexpected acknowledgement: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(r.ReceiptPath), ".published-candidate-adoption-*.tmp"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("staging files=%v err=%v", matches, err)
	}
}

func adoptionClosureFixture(t *testing.T) (engineFixture, WorktreeMergeReceipt) {
	t.Helper()
	f := newEngineFixture(t)
	source := createMergeSource(t, f, "adoption-closure-source", "feature/adoption-closure-source", "source.txt", "source\n")
	r, err := PrepareWorktreeMerge(t.Context(), WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, r.Candidate.Worktree, "push", "origin", r.Candidate.SHA+":refs/heads/"+r.Candidate.Branch)
	r.Status, r.Phase = WorktreeMergeConflict, WorktreeMergePhasePrepare
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	installPublishedCandidateAdoptionGH(t)
	for key, value := range map[string]string{"WB_TEST_PR_STATE": "open", "WB_TEST_PR_BRANCH": r.Candidate.Branch, "WB_TEST_PR_SHA": r.Candidate.SHA, "WB_TEST_PR_HEAD_REPO": r.Repository, "WB_TEST_PR_BASE": r.Target, "WB_TEST_PR_BASE_REPO": r.Repository} {
		t.Setenv(key, value)
	}
	if err := provePublishedCandidateAdoption(t.Context(), defaultRunner, f.githubDir, r, r.ReceiptPath, "7"); err != nil {
		t.Fatalf("native adoption baseline: %v", err)
	}
	return f, r
}

func adoptionClosureOptions(f engineFixture, r WorktreeMergeReceipt) WorktreeMergePublishedCandidateAdoptionOptions {
	return WorktreeMergePublishedCandidateAdoptionOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, PullRequest: "7", Apply: true, Actor: "reviewer", Reason: "actual publication adoption"}
}
