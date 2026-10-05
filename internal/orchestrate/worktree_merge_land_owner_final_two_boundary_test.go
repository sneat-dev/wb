package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/quality"
)

func TestLandOwnerPreparedTargetAncestryUsesActualLateNativeGraph(t *testing.T) {
	t.Parallel()
	fixture := newExplicitRootEngineFixture(t)
	initial := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	writeEngineFile(t, filepath.Join(fixture.canonical, "owned-prepared-target.txt"), "real native T1\n")
	runEngineGit(t, fixture.canonical, "add", "owned-prepared-target.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: real prepared target descendant T1")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	source := createMergeSource(t, fixture, "land-final-two-source", "feature/land-final-two-source", "owned-final-two-source.txt", "real candidate source\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if ancestor := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "merge-base", receipt.TargetSHA, receipt.Candidate.SHA)); ancestor != receipt.TargetSHA {
		t.Fatalf("original real prepared ancestry absent: %s", ancestor)
	}
	candidateTree := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", receipt.Candidate.SHA+"^{tree}"))
	replacement := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "commit-tree", candidateTree, "-p", initial, "-m", "test: owned late graph observation retains tree but bypasses T1"))
	writeEngineFile(t, filepath.Join(fixture.canonical, "owned-later-target.txt"), "real remote T2\n")
	runEngineGit(t, fixture.canonical, "add", "owned-later-target.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: native target advanced after preparation")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	target := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	phase := ""
	replaced := false
	t.Cleanup(func() {
		if replaced {
			runEngineGit(t, receipt.Candidate.Worktree, "replace", "-d", receipt.Candidate.SHA)
		}
	})
	run := &landOwnerFaultRunner{Runner: defaultRunner, refuse: func(dir, name string, args []string) bool {
		if !replaced && phase == "refresh_target" && dir == receipt.Candidate.Worktree && name == "git" && reflect.DeepEqual(args, []string{"merge-base", receipt.TargetSHA, receipt.Candidate.SHA}) {
			// Failure observation only: after the actual clean/HEAD/source/fetch
			// reads, install a real private replacement commit with a common T0.
			// The exact requested native merge-base must succeed with T0, not T1.
			runEngineGit(t, receipt.Candidate.Worktree, "replace", receipt.Candidate.SHA, replacement)
			replaced = true
		}
		return false
	}}
	got, runErr := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteDirect, run: run, Timeout: 5 * time.Second, Progress: func(e progress.Event) { phase = e.Phase }})
	if !replaced || run.refused || runErr == nil || !strings.Contains(runErr.Error(), "no longer contains recorded target") || got.Status != WorktreeMergeConflict {
		t.Fatalf("actual successful-false native ancestry=%+v %v replaced=%v calls=%v", got, runErr, replaced, run.calls)
	}
	if ancestor := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "merge-base", receipt.TargetSHA, receipt.Candidate.SHA)); ancestor != initial {
		t.Fatalf("negative native graph did not retain actual common ancestor: %s want %s", ancestor, initial)
	}
	if tree := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", receipt.Candidate.SHA+"^{tree}")); tree != candidateTree {
		t.Fatalf("negative observation changed candidate content tree: %s", tree)
	}
	stored, err := readWorktreeMergeReceipt(receipt.ReceiptPath)
	if err != nil || stored.Status != WorktreeMergeConflict || stored.Failure != runErr.Error() || stored.Candidate.SHA != receipt.Candidate.SHA || stored.TargetSHA != receipt.TargetSHA {
		t.Fatalf("actual durable negative ancestry evidence=%+v %v", stored, err)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); remote != target {
		t.Fatalf("refusal changed actual T2: %s", remote)
	}
	if got.Rebase != nil || got.PullRequest != "" || got.LandingSHA != "" || len(got.CleanedTasks) != 0 {
		t.Fatalf("false ancestry continued into mutation: %+v", got)
	}
	runEngineGit(t, receipt.Candidate.Worktree, "replace", "-d", receipt.Candidate.SHA)
	replaced = false
	if ancestor := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "merge-base", receipt.TargetSHA, receipt.Candidate.SHA)); ancestor != receipt.TargetSHA {
		t.Fatalf("restored native graph lost original prepared ancestry: %s", ancestor)
	}
}

//nolint:paralleltest // actual strict hosted-check fixture changes process-wide PATH/XDG; repositories, hook and durable records remain private
func TestLandOwnerStaleDeferralSuccessCompletesBeforeNativePrePushRefusal(t *testing.T) {
	fixture := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, fixture, "land-final-stale-source", "feature/land-final-stale-source", "owned-final-stale-source.txt", "actual native strict PR source\n")
	sourceSHA := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	gh := installWorktreeMergeEngineGH(t, fixture, sourceSHA, "feature/land-final-stale-source")
	receipt, prepareErr := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", Route: WorktreeMergeRoutePullRequest, Timeout: 5 * time.Second})
	if prepareErr != nil || receipt.Status != WorktreeMergePrepared || receipt.Validation.Status != quality.StatusSkipped || receipt.ValidationDeferral == nil || receipt.ValidationDeferral.Route != WorktreeMergeRoutePullRequest || receipt.ValidationDeferral.CandidateSHA != receipt.Candidate.SHA {
		t.Fatalf("actual strict PR preparation did not establish exact skipped deferral: %+v %v", receipt, prepareErr)
	}
	gh.writeState(t, "head", receipt.Candidate.SHA)
	gh.writeState(t, "head-ref", receipt.Candidate.Branch)
	published, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest, StopBeforeMerge: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond, WaitSlice: 5 * time.Second})
	if err != nil || published.Status != WorktreeMergePublished || published.Phase != WorktreeMergePhaseLand || published.PullRequest != gh.pr || published.ValidationDeferral == nil || published.Validation.Status != quality.StatusSkipped {
		t.Fatalf("real strict publish-only deferral premise=%+v %v", published, err)
	}
	marker := filepath.Join(t.TempDir(), "owned-native-hook-observed")
	completed := false
	got, runErr := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: published.ReceiptPath, Route: WorktreeMergeRoutePullRequest, ValidateLocally: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond, WaitSlice: 5 * time.Second, Progress: func(e progress.Event) {
		if e.Phase == "revalidate_candidate" && e.State == progress.Completed && !completed {
			saved, err := readWorktreeMergeReceipt(published.ReceiptPath)
			if err != nil || saved.Status != WorktreeMergePrepared || saved.Validation.Status != quality.StatusPassed || saved.Validation.Revision != published.Candidate.SHA || saved.ValidationDeferral != nil {
				t.Fatalf("Completed event preceded genuine successful native validation Save: %+v %v", saved, err)
			}
			hooks := t.TempDir()
			script := "#!/bin/sh\nprintf observed > " + strconv.Quote(marker) + "\necho owned-stale-validation-after-completion >&2\nexit 1\n"
			writeEngineFile(t, filepath.Join(hooks, "pre-push"), script)
			if err := os.Chmod(filepath.Join(hooks, "pre-push"), 0755); err != nil {
				t.Fatal(err)
			}
			runEngineGit(t, published.Candidate.Worktree, "config", "core.hooksPath", hooks)
			completed = true
		}
	}})
	if !completed || runErr == nil || !strings.Contains(runErr.Error(), "owned-stale-validation-after-completion") || got.Status != WorktreeMergeConflict || got.Validation.Status != quality.StatusPassed || got.Validation.Revision != published.Candidate.SHA || got.ValidationDeferral != nil {
		t.Fatalf("actual completed revalidation then native prepush refusal=%+v %v completed=%v", got, runErr, completed)
	}
	if b, err := os.ReadFile(marker); err != nil || string(b) != "observed" {
		t.Fatalf("actual late native hook did not run: %q %v", b, err)
	}
	stored, err := readWorktreeMergeReceipt(published.ReceiptPath)
	if err != nil || stored.Failure != runErr.Error() || stored.Validation.Status != quality.StatusPassed || stored.ValidationDeferral != nil || stored.Candidate.SHA != published.Candidate.SHA {
		t.Fatalf("actual completed validation durable refusal=%+v %v", stored, err)
	}
	if candidate := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+published.Candidate.Branch)); candidate != published.PublishedCandidateSHA {
		t.Fatalf("late refusal changed published candidate: %s", candidate)
	}
	if target := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); target != published.TargetSHA {
		t.Fatalf("late refusal changed target: %s", target)
	}
	if got.LandingSHA != "" || got.CanonicalSync != "" || len(got.CleanedTasks) != 0 {
		t.Fatalf("native refusal performed later landing: %+v", got)
	}
}
