package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/runner"
)

func prepareOnlyFixture(t *testing.T) (conflictRecoveryFixture, WorktreeMergeLandOptions) {
	t.Helper()
	f := newConflictRecoveryFixture(t)
	f.receipt.ReceiptPath = filepath.Join(f.engine.githubDir, ".wb", "reports", "worktree-merge", f.receipt.ID+".json")
	if err := os.MkdirAll(filepath.Dir(f.receipt.ReceiptPath), 0700); err != nil {
		t.Fatal(err)
	}
	f.receipt.Candidate.SHA = f.head
	f.receipt.Status = WorktreeMergePreparing
	f.receipt.Lane = worktreeMergeLaneID(f.receipt.Repository, f.receipt.Target)
	f.receipt.ForwardRepairs = []WorktreeMergeForwardRepairReceipt{{Status: WorktreeMergePostTargetCIFailed, LandingSHA: f.receipt.TargetSHA}}
	if err := persistWorktreeMergeReceipt(f.receipt); err != nil {
		t.Fatal(err)
	}
	return f, WorktreeMergeLandOptions{ProjectsRoot: f.engine.githubDir, Receipt: f.receipt.ReceiptPath, PrepareOnly: true, Route: WorktreeMergeRoutePullRequest, Timeout: 10 * time.Second}
}
func TestPrepareOnlyRecoveryKeepsTargetAndPublicationUntouchedThenAllowsRebatch(t *testing.T) {
	f, o := prepareOnlyFixture(t)
	got, err := LandWorktreeMerge(t.Context(), o)
	if err != nil || got.Status != WorktreeMergePrepared || got.Phase != WorktreeMergePhasePrepare || got.ValidationDeferral != nil {
		t.Fatalf("recovery=%+v err=%v", got, err)
	}
	if len(got.ForwardRepairs) != 1 || got.PullRequest != "" || got.LandingSHA != "" {
		t.Fatalf("lost failure or published: %+v", got)
	}
	if head := strings.TrimSpace(runEngineGit(t, f.engine.canonical, "ls-remote", "origin", "refs/heads/main")); !strings.HasPrefix(head, f.receipt.TargetSHA+"\t") {
		t.Fatalf("target moved: %s", head)
	}
	if published := strings.TrimSpace(runEngineGit(t, got.Candidate.Worktree, "ls-remote", "origin", "refs/heads/"+got.Candidate.Branch)); published != "" {
		t.Fatalf("candidate published: %s", published)
	}
	second := createMergeSource(t, f.engine, "second-repair", "feature/second-repair", "second.txt", "second repair\n")
	combined, err := PrepareWorktreeMerge(t.Context(), WorktreeMergePrepareOptions{ProjectsRoot: f.engine.githubDir, Sources: []string{got.Sources[0].Worktree, second.WorktreeDir}, Target: got.Target, RebatchReceipt: got.ReceiptPath, Model: "test-model", AgentRuntime: "test"})
	if err != nil || combined.RebatchOf != got.ReceiptPath || len(combined.Sources) != 2 {
		t.Fatalf("rebatch=%+v err=%v", combined, err)
	}
	for _, root := range []string{got.Candidate.SHA, got.Sources[0].SHA, combined.Sources[1].SHA} {
		if ancestor, err := isMergeAncestor(t.Context(), combined.Candidate.Worktree, root, combined.Candidate.SHA); err != nil || !ancestor {
			t.Fatalf("missing %s %v", root, err)
		}
	}
}
func TestPrepareOnlyRecoveryRefusesWrongShapesAndLandingFlags(t *testing.T) {
	t.Parallel()
	base := WorktreeMergeReceipt{Phase: WorktreeMergePhasePrepare, Status: WorktreeMergePreparing, Candidate: WorktreeMergeCandidate{SHA: "sha"}, ForwardRepairs: []WorktreeMergeForwardRepairReceipt{{}}}
	for _, tc := range []struct {
		name   string
		mutate func(*WorktreeMergeReceipt, *WorktreeMergeLandOptions)
	}{
		{"cleanup", func(_ *WorktreeMergeReceipt, o *WorktreeMergeLandOptions) { o.Cleanup = true }},
		{"handoff", func(_ *WorktreeMergeReceipt, o *WorktreeMergeLandOptions) { o.StopBeforeMerge = true }},
		{"defer", func(_ *WorktreeMergeReceipt, o *WorktreeMergeLandOptions) { o.DirectCIPullRequest = "42" }},
		{"landed", func(r *WorktreeMergeReceipt, _ *WorktreeMergeLandOptions) { r.LandingSHA = "landed" }},
		{"published", func(r *WorktreeMergeReceipt, _ *WorktreeMergeLandOptions) { r.PublishedCandidateSHA = "published" }},
		{"pr", func(r *WorktreeMergeReceipt, _ *WorktreeMergeLandOptions) { r.PullRequest = "42" }},
		{"non-forward", func(r *WorktreeMergeReceipt, _ *WorktreeMergeLandOptions) { r.ForwardRepairs = nil }},
		{"wrong-status", func(r *WorktreeMergeReceipt, _ *WorktreeMergeLandOptions) { r.Status = WorktreeMergeComplete }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			o := WorktreeMergeLandOptions{}
			tc.mutate(&r, &o)
			if err := validatePrepareOnlyShape(r, o); err == nil {
				t.Fatal("unsafe recovery accepted")
			}
		})
	}
}
func TestPrepareOnlyRecoveryRefusesDriftAndReadFailures(t *testing.T) {
	for _, mode := range []string{"dirty candidate", "source moved", "target moved", "published", "target read", "publication read", "not-forward"} {
		t.Run(mode, func(t *testing.T) {
			f, o := prepareOnlyFixture(t)
			switch mode {
			case "not-forward":
				f.receipt.ForwardRepairs = nil
				if err := persistWorktreeMergeReceipt(f.receipt); err != nil {
					t.Fatal(err)
				}
			case "dirty candidate":
				writeEngineFile(t, filepath.Join(f.receipt.Candidate.Worktree, "dirty.txt"), "dirty")
			case "source moved":
				runEngineGit(t, f.receipt.Sources[0].Worktree, "commit", "--allow-empty", "-m", "advance source")
			case "target moved":
				runEngineGit(t, f.engine.canonical, "commit", "--allow-empty", "-m", "advance target")
				runEngineGit(t, f.engine.canonical, "push", "origin", "main")
			case "published":
				runEngineGit(t, f.receipt.Candidate.Worktree, "push", "origin", f.receipt.Candidate.Branch)
			case "target read":
				o.run = conflictNegativeRunner{Runner: runner.New(), dir: f.receipt.Candidate.Worktree, argv: "fetch --no-tags origin +refs/heads/main:refs/remotes/origin/main", err: errors.New("target read refused")}
			case "publication read":
				o.run = conflictNegativeRunner{Runner: runner.New(), dir: f.receipt.Candidate.Worktree, argv: "ls-remote --heads origin refs/heads/" + f.receipt.Candidate.Branch, err: errors.New("publication read refused")}
			}
			if _, err := LandWorktreeMerge(context.Background(), o); err == nil {
				t.Fatalf("accepted %s", mode)
			}
		})
	}
}
func TestPrepareOnlyRecoveryValidationFailureDoesNotPublish(t *testing.T) {
	f, o := prepareOnlyFixture(t)
	writeEngineGoModule(t, f.receipt.Candidate.Worktree, "package app\n")
	writeEngineFile(t, filepath.Join(f.receipt.Candidate.Worktree, "app_test.go"), "package app\nimport \"testing\"\nfunc TestBroken(t *testing.T){t.Fatal(\"actual failing check\")}\n")
	runEngineGit(t, f.receipt.Candidate.Worktree, "add", "go.mod", "app.go", "app_test.go")
	runEngineGit(t, f.receipt.Candidate.Worktree, "commit", "-m", "add real failing check")
	f.receipt.Candidate.SHA = strings.TrimSpace(runEngineGit(t, f.receipt.Candidate.Worktree, "rev-parse", "HEAD"))
	if err := persistWorktreeMergeReceipt(f.receipt); err != nil {
		t.Fatal(err)
	}
	got, err := LandWorktreeMerge(t.Context(), o)
	if err == nil || got.Status != WorktreeMergeValidationFailed {
		t.Fatalf("failed check accepted: %+v %v", got, err)
	}
	if published := strings.TrimSpace(runEngineGit(t, got.Candidate.Worktree, "ls-remote", "origin", "refs/heads/"+got.Candidate.Branch)); published != "" {
		t.Fatal("published failed candidate")
	}
	if _, err = os.Stat(got.Candidate.Worktree); err != nil {
		t.Fatal("candidate lost", err)
	}
}

func TestPrepareOnlyRecoveryRefusesSourceDriftDuringValidation(t *testing.T) {
	f, o := prepareOnlyFixture(t)
	o.Progress = func(event progress.Event) {
		if event.Phase == "validate_candidate" && event.State == progress.Completed {
			runEngineGit(t, f.receipt.Sources[0].Worktree, "commit", "--allow-empty", "-m", "source moved during validation")
		}
	}
	got, err := LandWorktreeMerge(t.Context(), o)
	if err == nil || got.Status != WorktreeMergeConflict {
		t.Fatalf("source drift accepted: %+v %v", got, err)
	}
	if published := strings.TrimSpace(runEngineGit(t, got.Candidate.Worktree, "ls-remote", "origin", "refs/heads/"+got.Candidate.Branch)); published != "" {
		t.Fatal("published stale candidate")
	}
}
