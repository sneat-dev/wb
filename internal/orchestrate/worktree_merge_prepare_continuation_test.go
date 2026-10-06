package orchestrate

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
)

func advanceContinuationSource(t *testing.T, f conflictRecoveryFixture) []WorktreeMergeSource {
	t.Helper()
	source := f.receipt.Sources[0]
	writeEngineFile(t, filepath.Join(source.Worktree, "advance.txt"), "additive source advance\n")
	runEngineGit(t, source.Worktree, "add", "advance.txt")
	runEngineGit(t, source.Worktree, "commit", "-m", "test: additive continuation source")
	sources := append([]WorktreeMergeSource(nil), f.receipt.Sources...)
	sources[0].SHA = strings.TrimSpace(runEngineGit(t, source.Worktree, "rev-parse", "HEAD"))
	return sources
}

func TestPrepareContinuationRejectsShapeBeforeNativeEffects(t *testing.T) {
	t.Parallel()
	old := WorktreeMergeSource{Task: "source", Worktree: "/private/source", Branch: "feature/source", SHA: "old"}
	prior := WorktreeMergeReceipt{Status: WorktreeMergePrepared, Candidate: WorktreeMergeCandidate{Task: "operation", Worktree: "/private/candidate", Branch: "wb/candidate", SHA: "candidate"}, Sources: []WorktreeMergeSource{old}}
	for _, tc := range []struct {
		name   string
		mutate func(*WorktreeMergeReceipt)
	}{
		{"terminal", func(r *WorktreeMergeReceipt) { r.Status = WorktreeMergeComplete }},
		{"landing", func(r *WorktreeMergeReceipt) { r.LandingSHA = "landed" }},
		{"no worktree", func(r *WorktreeMergeReceipt) { r.Candidate.Worktree = "" }},
		{"no branch", func(r *WorktreeMergeReceipt) { r.Candidate.Branch = "" }},
	} {
		t.Run("refresh "+tc.name, func(t *testing.T) {
			t.Parallel()
			r := prior
			tc.mutate(&r)
			if ok, err := canRefreshWorktreeMergeReceipt(t.Context(), r, prior.Sources); ok || err != nil {
				t.Fatalf("shape admitted=%v error=%v", ok, err)
			}
		})
	}
	repair := prior
	repair.Status = WorktreeMergePostTargetCIFailed
	repair.LandingSHA = "landed"
	for _, tc := range []struct {
		name   string
		mutate func(*WorktreeMergeReceipt)
	}{
		{"status", func(r *WorktreeMergeReceipt) { r.Status = WorktreeMergePrepared }},
		{"landing", func(r *WorktreeMergeReceipt) { r.LandingSHA = "" }},
		{"worktree", func(r *WorktreeMergeReceipt) { r.Candidate.Worktree = "" }},
		{"branch", func(r *WorktreeMergeReceipt) { r.Candidate.Branch = "" }},
		{"head", func(r *WorktreeMergeReceipt) { r.Candidate.SHA = "" }},
	} {
		t.Run("repair "+tc.name, func(t *testing.T) {
			t.Parallel()
			r := repair
			tc.mutate(&r)
			if ok, err := canPreparePostTargetRepair(t.Context(), r, repair.Sources); ok || err != nil {
				t.Fatalf("shape admitted=%v error=%v", ok, err)
			}
		})
	}
	exact := prior
	exact.Phase = WorktreeMergePhasePrepare
	exact.Status = WorktreeMergePreparing
	exact.ID = "operation"
	exact.Lane = "lane"
	for _, tc := range []struct {
		name   string
		mutate func(*WorktreeMergeReceipt)
	}{
		{"phase", func(r *WorktreeMergeReceipt) { r.Phase = WorktreeMergePhaseLand }},
		{"status", func(r *WorktreeMergeReceipt) { r.Status = WorktreeMergePrepared }},
		{"lane", func(r *WorktreeMergeReceipt) { r.Lane = "other" }},
		{"operation", func(r *WorktreeMergeReceipt) { r.ID = "other" }},
		{"sources", func(r *WorktreeMergeReceipt) { r.Sources = nil }},
		{"task", func(r *WorktreeMergeReceipt) { r.Candidate.Task = "other" }},
		{"worktree", func(r *WorktreeMergeReceipt) { r.Candidate.Worktree = "" }},
		{"branch", func(r *WorktreeMergeReceipt) { r.Candidate.Branch = "" }},
	} {
		t.Run("exact "+tc.name, func(t *testing.T) {
			t.Parallel()
			r := exact
			tc.mutate(&r)
			if err := validateExactPreparingWorktreeMergeReceipt(t.Context(), r, "lane", "operation", exact.Sources); err == nil {
				t.Fatal("unsafe interrupted identity accepted")
			}
		})
	}
	for _, tc := range []struct {
		name   string
		mutate func(*WorktreeMergeReceipt)
	}{
		{"phase", func(r *WorktreeMergeReceipt) { r.Phase = WorktreeMergePhaseLand }},
		{"status", func(r *WorktreeMergeReceipt) { r.Status = WorktreeMergePrepared }},
		{"head", func(r *WorktreeMergeReceipt) { r.Candidate.SHA = "" }},
	} {
		t.Run("integrated "+tc.name, func(t *testing.T) {
			t.Parallel()
			r := exact
			tc.mutate(&r)
			if err := validatePreparingWorktreeMergeCandidate(t.Context(), r); err == nil {
				t.Fatal("inexact interrupted candidate accepted")
			}
		})
	}
	for _, tc := range []struct {
		name   string
		mutate func(*WorktreeMergeSource)
	}{
		{"task", func(s *WorktreeMergeSource) { s.Task = "other" }},
		{"branch", func(s *WorktreeMergeSource) { s.Branch = "other" }},
		{"path", func(s *WorktreeMergeSource) { s.Worktree = "other" }},
	} {
		t.Run("source "+tc.name, func(t *testing.T) {
			t.Parallel()
			s := old
			tc.mutate(&s)
			if ok, err := worktreeMergeSourcesAdvanced(t.Context(), runner.New(), prior.Sources, []WorktreeMergeSource{s}); ok || err != nil {
				t.Fatalf("foreign source admitted=%v error=%v", ok, err)
			}
		})
	}
	if ok, err := worktreeMergeSourcesAdvanced(t.Context(), runner.New(), prior.Sources, nil); ok || err != nil {
		t.Fatalf("count mismatch=%v error=%v", ok, err)
	}
	if ok, err := worktreeMergeSourcesAdvanced(t.Context(), runner.New(), prior.Sources, prior.Sources); ok || err != nil {
		t.Fatalf("unchanged sources=%v error=%v", ok, err)
	}
}
