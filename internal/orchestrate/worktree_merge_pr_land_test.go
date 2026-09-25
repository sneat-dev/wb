package orchestrate

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

func TestUpdateBranchMergeTargetParentPicksTheNonPreviousParent(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		parents []string
		want    string
		wantErr bool
	}{
		{name: "previous first", parents: []string{"aaa", "bbb"}, want: "bbb"},
		{name: "previous second", parents: []string{"bbb", "aaa"}, want: "bbb"},
		{name: "not a merge commit", parents: []string{"aaa"}, wantErr: true},
		{name: "neither parent is the previous head", parents: []string{"ccc", "ddd"}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := updateBranchMergeTargetParent(test.parents, "aaa")
			if test.wantErr {
				if err == nil {
					t.Fatalf("got %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("target parent = %q, want %q", got, test.want)
			}
		})
	}
}

// TestAbsorbedSourceHeadsExcludesAnUnrelatedPullRequestOnceTargetSHAAdvances
// proves red-team finding M1: without advancing receipt.TargetSHA to the
// update-branch merge's own target parent, absorbedSourceHeads
// (worktree_merge_source_prs.go) walks `git rev-list --merges
// TargetSHA..Candidate.SHA` across every commit main picked up since the
// STALE TargetSHA — including an unrelated pull request's own merge — and
// reports it as absorbed by this candidate. Advancing TargetSHA the way
// adoptWorktreeMergeUpdateBranchAdvance does (via updateBranchMergeTargetParent)
// closes the range and excludes it.
//
//nolint:paralleltest // calls runnertest.AllowRealProcess, which Go's testing package forbids combined with t.Parallel
func TestAbsorbedSourceHeadsExcludesAnUnrelatedPullRequestOnceTargetSHAAdvances(t *testing.T) {
	runnertest.AllowRealProcess(t)
	dir := t.TempDir()
	runEngineGit(t, dir, "init", "-b", "main")
	runEngineGit(t, dir, "config", "user.name", "WB Test")
	runEngineGit(t, dir, "config", "user.email", "wb@example.test")
	writeEngineFile(t, filepath.Join(dir, "base.txt"), "base\n")
	runEngineGit(t, dir, "add", "-A")
	runEngineGit(t, dir, "commit", "-m", "initial")
	staleTarget := strings.TrimSpace(runEngineGit(t, dir, "rev-parse", "HEAD"))

	// An unrelated pull request lands on main after staleTarget: a merge
	// commit whose second parent is a branch this receipt never heard of.
	runEngineGit(t, dir, "checkout", "-b", "unrelated-feature")
	writeEngineFile(t, filepath.Join(dir, "unrelated.txt"), "unrelated\n")
	runEngineGit(t, dir, "add", "-A")
	runEngineGit(t, dir, "commit", "-m", "feat: unrelated change")
	unrelatedTip := strings.TrimSpace(runEngineGit(t, dir, "rev-parse", "HEAD"))
	runEngineGit(t, dir, "checkout", "main")
	runEngineGit(t, dir, "merge", "--no-ff", "-m", "Merge pull request #99 from acme/unrelated-feature", "unrelated-feature")
	freshTarget := strings.TrimSpace(runEngineGit(t, dir, "rev-parse", "HEAD"))

	// The candidate: WB's own source branch off the stale target.
	runEngineGit(t, dir, "checkout", "-b", "wb/candidate", staleTarget)
	writeEngineFile(t, filepath.Join(dir, "candidate.txt"), "candidate\n")
	runEngineGit(t, dir, "add", "-A")
	runEngineGit(t, dir, "commit", "-m", "feat: candidate change")
	candidateHead := strings.TrimSpace(runEngineGit(t, dir, "rev-parse", "HEAD"))

	// The server-side update-branch merge this PR's headUpdated hook
	// observes: candidate merged with main's new head, which absorbed the
	// unrelated PR.
	runEngineGit(t, dir, "merge", "--no-ff", "-m", "Merge branch 'main' into wb/candidate", "main")
	updatedHead := strings.TrimSpace(runEngineGit(t, dir, "rev-parse", "HEAD"))
	parents := strings.Fields(strings.TrimSpace(runEngineGit(t, dir, "log", "--pretty=%P", "-1", updatedHead)))
	if len(parents) != 2 {
		t.Fatalf("expected a two-parent update-branch merge, got %v", parents)
	}

	receipt := WorktreeMergeReceipt{
		Repository: "acme/app", Target: "main", TargetSHA: staleTarget,
		Sources:   []WorktreeMergeSource{{Branch: "wb/candidate", SHA: candidateHead, Merged: true}},
		Candidate: WorktreeMergeCandidate{Branch: "wb/candidate", Worktree: dir, SHA: updatedHead},
	}

	// Sanity: without the fix, the stale TargetSHA really does surface the
	// unrelated pull request's tip as absorbed. If this stops being true the
	// rest of the test is not exercising what it claims to.
	staleHeads, err := absorbedSourceHeads(context.Background(), dir, receipt, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !containsHead(staleHeads, unrelatedTip) {
		t.Fatalf("fixture invariant broken: expected the stale-TargetSHA walk to surface %s as absorbed, got %v", unrelatedTip, staleHeads)
	}

	// updateBranchMergeTargetParent is the pure core of
	// adoptWorktreeMergeUpdateBranchAdvance's M1 fix: advance TargetSHA to
	// the update merge's own target parent.
	newTarget, targetErr := updateBranchMergeTargetParent(parents, candidateHead)
	if targetErr != nil {
		t.Fatal(targetErr)
	}
	if newTarget != freshTarget {
		t.Fatalf("advanced target = %s, want %s", newTarget, freshTarget)
	}
	advanced := receipt
	advanced.TargetSHA = newTarget

	heads, err := absorbedSourceHeads(context.Background(), dir, advanced, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if containsHead(heads, unrelatedTip) {
		t.Fatalf("advanced-TargetSHA receipt still reported unrelated pull request %s as absorbed: %v", unrelatedTip, heads)
	}
}

func containsHead(heads []string, target string) bool {
	for _, head := range heads {
		if head == target {
			return true
		}
	}
	return false
}
