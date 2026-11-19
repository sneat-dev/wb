package orchestrate

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewedRepairRetainsEveryAdvanceAndRerunsValidation(t *testing.T) {
	t.Parallel()
	f := newExplicitRootEngineFixture(t)
	writeEngineGoModule(t, f.canonical, "package app\n")
	runEngineGit(t, f.canonical, "add", "go.mod", "app.go")
	runEngineGit(t, f.canonical, "commit", "-m", "test: establish valid target")
	runEngineGit(t, f.canonical, "push", "origin", "main")
	a := createMergeSource(t, f, "review-a", "topic/a", "broken.go", "package app\nfunc fail() { unresolved() }\n")
	b := createMergeSource(t, f, "review-b", "topic/b", "b.txt", "original\n")
	prepare := WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{a.WorktreeDir, b.WorktreeDir}, Target: "main", Model: "fixture", AgentRuntime: "fixture", ValidateLocally: true}
	receipt, err := PrepareWorktreeMerge(t.Context(), prepare)
	if err == nil || receipt.Status != WorktreeMergeValidationFailed {
		t.Fatalf("expected real candidate validation failure; status=%s, error=%v", receipt.Status, err)
	}
	before, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	commit := func(dir, name, value string) string {
		writeEngineFile(t, filepath.Join(dir, name), value)
		runEngineGit(t, dir, "add", name)
		runEngineGit(t, dir, "commit", "-m", "fix: repair "+name)
		return strings.TrimSpace(runEngineGit(t, dir, "rev-parse", "HEAD"))
	}
	advanceA := commit(a.WorktreeDir, "broken.go", "package app\nfunc fail() {}\n")
	advanceB := commit(b.WorktreeDir, "b.txt", "reviewed\n")
	opt := WorktreeMergeValidationFailureSupersessionOptions{ProjectsRoot: f.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: b.WorktreeDir, Apply: true, Actor: "reviewer", Reason: "both repairs reviewed"}
	reject := func(reason string) {
		t.Helper()
		if _, err := SupersedeValidationFailedWorktreeMerge(t.Context(), opt); err == nil || !strings.Contains(err.Error(), reason) {
			t.Fatalf("wanted refusal %q, got %v", reason, err)
		}
		if _, err := os.Stat(validationFailureSupersessionPath(receipt.ReceiptPath)); !os.IsNotExist(err) {
			t.Fatalf("refusal wrote sidecar: %v", err)
		}
	}
	reject("differs from landed target")
	opt.ReviewedSources = []string{"topic/b=" + advanceB}
	reject("pin every")
	opt.ReviewedSources = append(opt.ReviewedSources, "topic/a="+receipt.Sources[0].SHA)
	reject("no exact reviewed head")
	opt.ReviewedSources[1] = "topic/a=" + advanceA
	reject("does not contain required immutable root")
	runEngineGit(t, b.WorktreeDir, "merge", "--no-edit", advanceA)
	replacement := strings.TrimSpace(runEngineGit(t, b.WorktreeDir, "rev-parse", "HEAD"))
	opt.ReviewedSources[0] = "topic/b=" + replacement
	opt.Apply = false
	preview, err := SupersedeValidationFailedWorktreeMerge(t.Context(), opt)
	if err != nil || len(preview.ReviewedSourceHeads) != 2 || preview.Replacement.SHA != replacement {
		t.Fatalf("bad dry-run proof: %+v %v", preview, err)
	}
	if _, err := os.Stat(preview.AcknowledgementPath); !os.IsNotExist(err) {
		t.Fatal("dry run wrote an acknowledgement")
	}
	opt.Apply = true
	ack, err := SupersedeValidationFailedWorktreeMerge(t.Context(), opt)
	if err != nil || ack.ID != preview.ID {
		t.Fatalf("apply: %v %s %s", err, ack.ID, preview.ID)
	}
	if replay, err := SupersedeValidationFailedWorktreeMerge(t.Context(), opt); err != nil || replay.ID != ack.ID {
		t.Fatalf("replay: %v", err)
	}
	after, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatal("failed receipt changed")
	}
	next, err := PrepareWorktreeMerge(t.Context(), prepare)
	if err != nil || next.Status != WorktreeMergePrepared || next.ID == receipt.ID || next.Candidate.SHA == receipt.Candidate.SHA || len(next.Sources) != 2 || len(next.Validation.Results) == 0 {
		t.Fatalf("fresh validated preparation after repair: status=%s, error=%v", next.Status, err)
	}
}

func TestReviewedSourceEvidenceRejectsMalformedAndForeignIdentities(t *testing.T) {
	t.Parallel()
	old := strings.Repeat("0", 40)
	a := strings.Repeat("a", 40)
	b := strings.Repeat("b", 40)
	r := WorktreeMergeReceipt{TargetSHA: old, Phase: WorktreeMergePhasePrepare, Status: WorktreeMergeValidationFailed, Sources: []WorktreeMergeSource{{Branch: "a", Task: "a", Worktree: "/a", SHA: old}, {Branch: "b", Task: "b", Worktree: "/b", SHA: old}}}
	replacement := WorktreeMergeCandidate{Task: "b", Worktree: "/b", Branch: "b", SHA: b}
	for _, heads := range [][]string{{a}, {a, "bad"}, {old, b}, {a, a}} {
		if err := validateReviewedSourceEvidence(r, replacement, old, heads); err == nil {
			t.Errorf("accepted invalid heads %v", heads)
		}
	}
	for _, modify := range []func(*WorktreeMergeReceipt){func(x *WorktreeMergeReceipt) { x.Status = WorktreeMergeConflict }, func(x *WorktreeMergeReceipt) { x.PublishedCandidateSHA = a }, func(x *WorktreeMergeReceipt) { x.PullRequest = "3" }, func(x *WorktreeMergeReceipt) { x.TargetSHA = b }} {
		other := r
		modify(&other)
		if err := validateReviewedSourceEvidence(other, replacement, old, []string{a, b}); err == nil {
			t.Error("accepted wrong failure or target")
		}
	}
}
