package orchestrate

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMergeReceiptOperationIdentityRequiresCompleteRecordedSources(t *testing.T) {
	t.Parallel()
	old := []WorktreeMergeSource{{Task: "source", Worktree: "/repo/source", Branch: "feature", SHA: "old"}}
	current := []WorktreeMergeSource{{Task: "source", Worktree: "/repo/source", Branch: "feature", SHA: "new"}}
	lane := worktreeMergeLaneID("acme/repo", "main")
	oldID := worktreeMergeOperationID(lane, old)
	base := WorktreeMergeReceipt{Lane: lane, ID: oldID, Sources: current}
	if worktreeMergeOperationIDMatchesRecordedSourceSet(base) {
		t.Fatal("an unrecorded old source set was accepted")
	}
	base.SourceRefreshes = []WorktreeMergeSourceRefresh{{Sources: append([]WorktreeMergeSource(nil), old...)}}
	if worktreeMergeOperationIDMatchesRecordedSourceSet(base) {
		t.Fatal("a refresh without a timestamp was accepted")
	}
	base.SourceRefreshes[0].RecordedAt = time.Now()
	base.SourceRefreshes[0].Sources[0].Task = ""
	if worktreeMergeOperationIDMatchesRecordedSourceSet(base) {
		t.Fatal("an incomplete source identity was accepted")
	}
	base.SourceRefreshes[0].Sources = old
	if !worktreeMergeOperationIDMatchesRecordedSourceSet(base) {
		t.Fatal("the complete historical source set was rejected")
	}
	base.ID = worktreeMergeOperationID(lane, current)
	base.SourceRefreshes = nil
	if !worktreeMergeOperationIDMatchesRecordedSourceSet(base) {
		t.Fatal("the current source set was rejected")
	}
}

func TestSupersededMergeReceiptIdentityRequiresDeterministicPathChain(t *testing.T) {
	t.Parallel()
	sources := []WorktreeMergeSource{{Task: "source", Worktree: "/repo/source", Branch: "feature", SHA: "old"}}
	lane := worktreeMergeLaneID("acme/repo", "main")
	root := worktreeMergeOperationID(lane, sources)
	dir := t.TempDir()
	rootPath := filepath.Join(dir, root+".json")
	first := worktreeMergeSupersededOperationID(root, rootPath)
	firstPath := filepath.Join(dir, first+".json")
	second := worktreeMergeSupersededOperationID(first, firstPath)
	if err := validateWorktreeMergeSupersededOperationID(second, filepath.Join(dir, second+".json"), lane, sources); err != nil {
		t.Fatalf("valid chain: %v", err)
	}
	cases := []struct{ name, operation, path, message string }{
		{"wrong path", first, rootPath, "does not name operation"},
		{"unrelated operation", "arbitrary", filepath.Join(dir, "arbitrary.json"), "does not descend"},
		{"short suffix", root + "-superseded-abc", filepath.Join(dir, root+"-superseded-abc.json"), "invalid supersession suffix"},
		{"nonhex suffix", root + "-superseded-zzzzzzzzzzzz", filepath.Join(dir, root+"-superseded-zzzzzzzzzzzz.json"), "invalid supersession suffix"},
		{"uppercase suffix", root + "-superseded-ABCDEF012345", filepath.Join(dir, root+"-superseded-ABCDEF012345.json"), "invalid supersession suffix"},
		{"forged successor", root + "-superseded-000000000000", filepath.Join(dir, root+"-superseded-000000000000.json"), "not the deterministic successor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := validateWorktreeMergeSupersededOperationID(tc.operation, tc.path, lane, sources); err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("error = %v, want %q", err, tc.message)
			}
		})
	}
	current := []WorktreeMergeSource{{Task: "source", Worktree: "/repo/source", Branch: "feature", SHA: "new"}}
	receipt := WorktreeMergeReceipt{Lane: lane, ID: second, Sources: current,
		SourceRefreshes: []WorktreeMergeSourceRefresh{{RecordedAt: time.Now(), Sources: sources}}}
	if err := validateWorktreeMergeSupersededOperationIDMatchesRecordedSourceSet(receipt, filepath.Join(dir, second+".json")); err != nil {
		t.Fatalf("historical source set: %v", err)
	}
	receipt.SourceRefreshes[0].Sources[0].Branch = ""
	if err := validateWorktreeMergeSupersededOperationIDMatchesRecordedSourceSet(receipt, filepath.Join(dir, second+".json")); err == nil {
		t.Fatal("incomplete historical source set was accepted")
	}
}

func TestPublishedValidationReplayRequiresExactSourceAndPublication(t *testing.T) {
	t.Parallel()
	sources := []WorktreeMergeSource{{Task: "source", Worktree: "/repo/source", Branch: "feature", SHA: "source-head"}}
	receipt := WorktreeMergeReceipt{Status: WorktreeMergeValidationFailed, PullRequest: "42", PublishedCandidateSHA: "published",
		Candidate: WorktreeMergeCandidate{SHA: "published"}, Sources: sources}
	ok, err := isExactPublishedValidationFailureReplay(context.Background(), "", receipt, sources)
	if err != nil || !ok {
		t.Fatalf("exact replay = %t, %v", ok, err)
	}
	for _, change := range []struct {
		name   string
		mutate func(*WorktreeMergeReceipt)
	}{
		{"different status", func(r *WorktreeMergeReceipt) { r.Status = WorktreeMergePrepared }},
		{"no PR", func(r *WorktreeMergeReceipt) { r.PullRequest = "" }},
		{"no published head", func(r *WorktreeMergeReceipt) { r.PublishedCandidateSHA = "" }},
		{"no candidate head", func(r *WorktreeMergeReceipt) { r.Candidate.SHA = "" }},
		{"different sources", func(r *WorktreeMergeReceipt) { r.Sources[0].SHA = "other" }},
		{"advanced without refresh", func(r *WorktreeMergeReceipt) { r.Candidate.SHA = "new" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			t.Parallel()
			changed := receipt
			changed.Sources = append([]WorktreeMergeSource(nil), receipt.Sources...)
			change.mutate(&changed)
			ok, err := isExactPublishedValidationFailureReplay(context.Background(), "", changed, sources)
			if err != nil || ok {
				t.Fatalf("invalid replay = %t, %v", ok, err)
			}
		})
	}
}

func TestPostTargetRepairRejectsIncompleteOrDifferentSourceIdentity(t *testing.T) {
	t.Parallel()
	old := WorktreeMergeSource{Task: "source", Worktree: "/repo/source", Branch: "feature", SHA: "old"}
	prior := WorktreeMergeReceipt{Status: WorktreeMergePostTargetCIFailed, LandingSHA: "landing",
		Candidate: WorktreeMergeCandidate{Worktree: "/repo/candidate", Branch: "merge", SHA: "candidate"}, Sources: []WorktreeMergeSource{old}}
	for _, tc := range []struct {
		name   string
		mutate func(*WorktreeMergeReceipt, *[]WorktreeMergeSource)
	}{
		{"wrong status", func(r *WorktreeMergeReceipt, _ *[]WorktreeMergeSource) { r.Status = WorktreeMergePrepared }},
		{"no landing", func(r *WorktreeMergeReceipt, _ *[]WorktreeMergeSource) { r.LandingSHA = "" }},
		{"no candidate worktree", func(r *WorktreeMergeReceipt, _ *[]WorktreeMergeSource) { r.Candidate.Worktree = "" }},
		{"no candidate branch", func(r *WorktreeMergeReceipt, _ *[]WorktreeMergeSource) { r.Candidate.Branch = "" }},
		{"no candidate head", func(r *WorktreeMergeReceipt, _ *[]WorktreeMergeSource) { r.Candidate.SHA = "" }},
		{"different count", func(_ *WorktreeMergeReceipt, s *[]WorktreeMergeSource) { *s = nil }},
		{"different task", func(_ *WorktreeMergeReceipt, s *[]WorktreeMergeSource) { (*s)[0].Task = "other" }},
		{"different branch", func(_ *WorktreeMergeReceipt, s *[]WorktreeMergeSource) { (*s)[0].Branch = "other" }},
		{"different worktree", func(_ *WorktreeMergeReceipt, s *[]WorktreeMergeSource) { (*s)[0].Worktree = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := prior
			s := []WorktreeMergeSource{old}
			tc.mutate(&r, &s)
			ok, err := canPreparePostTargetRepair(context.Background(), r, s)
			if err != nil || ok {
				t.Fatalf("unsafe repair = %t, %v", ok, err)
			}
		})
	}
}
