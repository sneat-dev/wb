//go:build e2e

package orchestrate

import (
	"path/filepath"
	"strings"
	"testing"
)

func sourcePROwnerNativeHeadsFixture(t *testing.T) (engineFixture, WorktreeMergeReceipt, string) {
	t.Helper()
	f := newExplicitRootEngineFixture(t)
	target := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, f.canonical, "checkout", "-b", "source-pr-input")
	writeEngineFile(t, filepath.Join(f.canonical, "source-pr-input.txt"), "actual private source\n")
	runEngineGit(t, f.canonical, "add", "-A")
	runEngineGit(t, f.canonical, "commit", "-m", "private source")
	source := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, f.canonical, "checkout", "main")
	runEngineGit(t, f.canonical, "merge", "--no-ff", "--no-edit", "source-pr-input")
	candidate := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	receipt := WorktreeMergeReceipt{Repository: "acme/app", Target: "main", TargetSHA: target, LandingSHA: candidate, Candidate: WorktreeMergeCandidate{SHA: candidate}, Sources: []WorktreeMergeSource{{SHA: source, Merged: true}, {SHA: target, Merged: true}, {SHA: candidate, Merged: true}, {SHA: "unmerged-record", Merged: false}, {Merged: true}}}
	if ok, err := isMergeAncestor(t.Context(), f.canonical, source, candidate); err != nil || !ok {
		t.Fatalf("actual candidate does not contain source: %t %v", ok, err)
	}
	return f, receipt, source
}
