//go:build e2e

package orchestrate

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestE2EResumeWorktreeMergeRecoversResolvedConflictWithEmptyCandidateSHA(t *testing.T) {
	fixture := newEngineFixture(t)
	writeEngineGoModule(t, fixture.canonical, "package app\n")
	runEngineGit(t, fixture.canonical, "add", "go.mod", "app.go")
	runEngineGit(t, fixture.canonical, "commit", "-m", "add Go validation fixture")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	source := createMergeSource(t, fixture, "empty-candidate-source", "feature/empty-candidate", "TECH-STACK.md", "source\n")
	writeEngineFile(t, filepath.Join(fixture.canonical, "TECH-STACK.md"), "target\n")
	runEngineGit(t, fixture.canonical, "add", "TECH-STACK.md")
	runEngineGit(t, fixture.canonical, "commit", "-m", "advance target into add/add conflict")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")

	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err == nil || receipt.Status != WorktreeMergeConflict || receipt.Candidate.SHA != "" {
		t.Fatalf("conflicting prepare receipt=%+v err=%v", receipt, err)
	}

	merge := exec.Command("git", "merge", "--no-commit", receipt.Sources[0].SHA)
	merge.Dir = receipt.Candidate.Worktree
	if output, mergeErr := merge.CombinedOutput(); mergeErr == nil {
		t.Fatalf("manual conflict reproduction unexpectedly merged: %s", output)
	}
	writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "TECH-STACK.md"), "resolved\n")
	runEngineGit(t, receipt.Candidate.Worktree, "add", "TECH-STACK.md")
	runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "resolve receipted add/add conflict")
	writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "recovery_failure.go"), "package app\n\nfunc RecoveryFailure() { missingRecoverySymbol }\n")
	runEngineGit(t, receipt.Candidate.Worktree, "add", "recovery_failure.go")
	runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "test: make recovered candidate fail validation")
	if _, validationErr := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath}); validationErr == nil {
		t.Fatal("recovered candidate validation unexpectedly passed")
	}
	failed, err := readWorktreeMergeReceipt(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != WorktreeMergeValidationFailed || failed.Candidate.SHA != "" {
		t.Fatalf("failed recovered receipt = %+v", failed)
	}
	runEngineGit(t, receipt.Candidate.Worktree, "rm", "recovery_failure.go")
	runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "test: repair recovered candidate validation")
	resolved := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))

	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_TARGET_SHA", resolved)
	landed, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if landed.Status != WorktreeMergeLanded || landed.Candidate.SHA != resolved || landed.LandingSHA != resolved {
		t.Fatalf("resumed receipt = %+v, want recovered candidate %s", landed, resolved)
	}
	persisted, err := readWorktreeMergeReceipt(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Candidate.SHA != resolved || persisted.Failure != "" {
		t.Fatalf("persisted recovered receipt = %+v", persisted)
	}
}

// A candidate may be created from one immutable base, then have a target-drift
// conflict resolved manually. The receipt records the newer target snapshot,
// but the Work Log must retain its original claim base. Resume may normalize
// that proven state without replaying the already-integrated source.
func TestE2EResumeWorktreeMergeRecoversResolvedTargetDriftConflictWithHistoricalClaimBase(t *testing.T) {
	fixture := newEngineFixture(t)
	writeEngineGoModule(t, fixture.canonical, "package app\n")
	runEngineGit(t, fixture.canonical, "add", "go.mod", "app.go")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: add Go validation fixture")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	source := createMergeSource(t, fixture, "historical-claim-base-source", "feature/historical-claim-base", "TECH-STACK.md", "source\n")

	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil || receipt.Status != WorktreeMergePrepared {
		t.Fatalf("initial candidate = %+v err=%v", receipt, err)
	}
	claimBefore, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{
		ProjectsRoot: fixture.githubDir, Worktree: receipt.Candidate.Worktree,
	})
	if err != nil || claimBefore.Claim == nil {
		t.Fatalf("candidate Work Log = %+v err=%v", claimBefore, err)
	}
	originalClaimBase := claimBefore.Claim.BaseSHA
	if originalClaimBase != receipt.TargetSHA {
		t.Fatalf("initial Work Log base = %s, receipt target = %s", originalClaimBase, receipt.TargetSHA)
	}

	writeEngineFile(t, filepath.Join(fixture.canonical, "TECH-STACK.md"), "target\n")
	runEngineGit(t, fixture.canonical, "add", "TECH-STACK.md")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: advance target into target-drift conflict")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	currentTarget := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))

	merge := exec.Command("git", "merge", "--no-edit", currentTarget)
	merge.Dir = receipt.Candidate.Worktree
	if output, mergeErr := merge.CombinedOutput(); mergeErr == nil {
		t.Fatalf("target-drift reproduction unexpectedly merged cleanly: %s", output)
	}
	writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "TECH-STACK.md"), "resolved\n")
	runEngineGit(t, receipt.Candidate.Worktree, "add", "TECH-STACK.md")
	runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "resolve target-drift conflict")
	resolved := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))
	if status := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "status", "--porcelain")); status != "" {
		t.Fatalf("resolved candidate remains dirty: %q", status)
	}

	// This models the durable historical receipt state: the target snapshot has
	// advanced, the human resolution is clean, but the immutable Work Log claim
	// remains bound to the candidate's original base.
	receipt.TargetSHA = currentTarget
	receipt.Candidate.SHA = ""
	receipt.Status = WorktreeMergeConflict
	receipt.Failure = "target drift conflict required resolution"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}

	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_TARGET_SHA", resolved)
	t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
	landed, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if landed.Status != WorktreeMergeLanded || landed.Candidate.SHA != resolved || landed.LandingSHA != resolved {
		t.Fatalf("normalized target-drift recovery = %+v, want exact resolved head %s", landed, resolved)
	}
	claimAfter, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{
		ProjectsRoot: fixture.githubDir, Worktree: receipt.Candidate.Worktree,
	})
	if err != nil || claimAfter.Claim == nil || claimAfter.Claim.BaseSHA != originalClaimBase {
		t.Fatalf("recovery rewrote immutable Work Log claim: %+v err=%v", claimAfter.Claim, err)
	}
	persisted, err := readWorktreeMergeReceipt(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.TargetSHA != currentTarget || persisted.Candidate.SHA != resolved {
		t.Fatalf("recovery did not retain the historical target snapshot and resolved candidate: %+v", persisted)
	}
	containsSource, sourceErr := isMergeAncestor(context.Background(), receipt.Candidate.Worktree, receipt.Sources[0].SHA, resolved)
	if sourceErr != nil || !containsSource {
		t.Fatalf("resolved candidate lost receipted source: contains=%t err=%v", containsSource, sourceErr)
	}
}

func TestE2EPrepareWorktreeMergeRefusesValidationFailedReceiptAfterSourceAdvanceWithoutMutation(t *testing.T) {
	fixture := newEngineFixture(t)
	writeEngineGoModule(t, fixture.canonical, "package app\n")
	runEngineGit(t, fixture.canonical, "add", "go.mod", "app.go")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: add Go validation fixture")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	source := createMergeSource(t, fixture, "failed-receipt-source", "feature/failed-receipt", "candidate.go", "package app\n\nfunc Candidate() { missingCandidate }\n")

	failed, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err == nil || failed.Status != WorktreeMergeValidationFailed || failed.Candidate.SHA == "" {
		t.Fatalf("initial validation failure = receipt %+v err %v", failed, err)
	}
	receiptBefore, err := os.ReadFile(failed.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	candidateView, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{
		ProjectsRoot: fixture.githubDir, Worktree: failed.Candidate.Worktree,
	})
	if err != nil || candidateView.Claim == nil {
		t.Fatalf("load candidate Work Log: view=%+v err=%v", candidateView, err)
	}
	claimBefore, err := os.ReadFile(candidateView.Claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	candidateHeadBefore := strings.TrimSpace(runEngineGit(t, failed.Candidate.Worktree, "rev-parse", "HEAD"))
	candidateStatusBefore := runEngineGit(t, failed.Candidate.Worktree, "status", "--porcelain")

	writeEngineFile(t, filepath.Join(source.WorktreeDir, "advance.txt"), "advance\n")
	runEngineGit(t, source.WorktreeDir, "add", "advance.txt")
	runEngineGit(t, source.WorktreeDir, "commit", "-m", "test: advance failed source")

	blocked, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err == nil || !strings.Contains(err.Error(), "only an exact preparing receipt may resume") {
		t.Fatalf("advanced source prepare = receipt %+v err %v", blocked, err)
	}
	if current, readErr := os.ReadFile(failed.ReceiptPath); readErr != nil || !bytes.Equal(current, receiptBefore) {
		t.Fatalf("validation-failed receipt changed after refusal: err=%v", readErr)
	}
	if current, readErr := os.ReadFile(candidateView.Claim.ClaimPath); readErr != nil || !bytes.Equal(current, claimBefore) {
		t.Fatalf("candidate Work Log changed after refusal: err=%v", readErr)
	}
	if current := strings.TrimSpace(runEngineGit(t, failed.Candidate.Worktree, "rev-parse", "HEAD")); current != candidateHeadBefore {
		t.Fatalf("candidate head changed from %s to %s", candidateHeadBefore, current)
	}
	if current := runEngineGit(t, failed.Candidate.Worktree, "status", "--porcelain"); current != candidateStatusBefore {
		t.Fatalf("candidate status changed from %q to %q", candidateStatusBefore, current)
	}
}

func TestE2ELandWorktreeMergeRebaseConflictAbortsWithoutChangingSources(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "rebase-conflict-source", "feature/rebase-conflict", "shared.txt", "source\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceHead := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	writeEngineFile(t, filepath.Join(fixture.canonical, "shared.txt"), "target\n")
	runEngineGit(t, fixture.canonical, "add", "shared.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "feat: conflicting target advance")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	advancedTarget := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))

	failed, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteDirect,
		Cleanup: true, OnFailure: "revert", AllowUnfenced: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond, ProgressRequested: true,
	})
	if err == nil || !strings.Contains(err.Error(), "conflicts while rebasing") || failed.Status != WorktreeMergeConflict {
		t.Fatalf("rebase conflict receipt=%+v err=%v", failed, err)
	}
	if got := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD")); got != receipt.Candidate.SHA {
		t.Fatalf("candidate changed after aborted rebase: got %s want %s", got, receipt.Candidate.SHA)
	}
	if got := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD")); got != sourceHead {
		t.Fatalf("source changed after aborted rebase: got %s want %s", got, sourceHead)
	}
	if status := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "status", "--porcelain")); status != "" {
		t.Fatalf("candidate retained rebase conflict state: %q", status)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "ls-remote", "origin", "refs/heads/main")); !strings.HasPrefix(got, advancedTarget+"\t") {
		t.Fatalf("remote target changed during failed rebase: %q", got)
	}
	persisted, readErr := readWorktreeMergeReceipt(receipt.ReceiptPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !failed.Cleanup || !persisted.Cleanup || persisted.Route.Requested != WorktreeMergeRouteDirect || persisted.OnFailure != "revert" || !persisted.AllowUnfenced {
		t.Fatalf("landing intent was not durable across interruption: returned=%+v persisted=%+v", failed, persisted)
	}
	resume := strings.Join(persisted.ResumeArgs, " ")
	for _, required := range []string{"--route direct", "--cleanup", "--progress", "--on-failure revert", "--allow-unfenced"} {
		if !strings.Contains(resume, required) {
			t.Fatalf("resume args %q lost %q", resume, required)
		}
	}
	bareResume := WorktreeMergeLandOptions{Route: WorktreeMergeRouteAuto, OnFailure: "stop"}
	if retainWorktreeMergeLandIntent(&persisted, &bareResume) {
		t.Fatal("bare resume unexpectedly changed already-durable landing intent")
	}
	if bareResume.Route != WorktreeMergeRouteDirect || !bareResume.Cleanup || bareResume.OnFailure != "revert" || !bareResume.ProgressRequested || !bareResume.AllowUnfenced {
		t.Fatalf("bare resume did not restore durable landing intent: %+v", bareResume)
	}
}

func TestE2EAdvanceResolvedConflictCandidateRefusesUnsafeEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, fixture engineFixture, receipt *WorktreeMergeReceipt)
		want   string
	}{
		{
			name: "dirty candidate",
			mutate: func(t *testing.T, _ engineFixture, receipt *WorktreeMergeReceipt) {
				writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "uncommitted.txt"), "dirty\n")
			},
			want: "dirty",
		},
		{
			name: "unrelated candidate head",
			mutate: func(t *testing.T, fixture engineFixture, receipt *WorktreeMergeReceipt) {
				unrelated := createMergeSource(t, fixture, "unrelated-resolved-candidate", "feature/unrelated-resolved-candidate", "unrelated.txt", "unrelated\n")
				receipt.Candidate.SHA = strings.TrimSpace(runEngineGit(t, unrelated.WorktreeDir, "rev-parse", "HEAD"))
				if err := persistWorktreeMergeReceipt(*receipt); err != nil {
					t.Fatal(err)
				}
			},
			want: "is not a descendant",
		},
		{
			name: "missing receipted source",
			mutate: func(t *testing.T, fixture engineFixture, receipt *WorktreeMergeReceipt) {
				const task = "missing-resolved-source"
				missing := createMergeSource(t, fixture, task, "feature/missing-resolved-source", "missing.txt", "missing\n")
				receipt.Sources = append(receipt.Sources, WorktreeMergeSource{Task: task, Worktree: missing.WorktreeDir, Branch: missing.Branch, SHA: strings.TrimSpace(runEngineGit(t, missing.WorktreeDir, "rev-parse", "HEAD"))})
				if err := persistWorktreeMergeReceipt(*receipt); err != nil {
					t.Fatal(err)
				}
			},
			want: "does not contain required immutable root",
		},
		{
			name: "target drift",
			mutate: func(t *testing.T, fixture engineFixture, _ *WorktreeMergeReceipt) {
				writeEngineFile(t, filepath.Join(fixture.canonical, "target-drift.txt"), "drift\n")
				runEngineGit(t, fixture.canonical, "add", "target-drift.txt")
				runEngineGit(t, fixture.canonical, "commit", "-m", "test: advance target after conflict")
				runEngineGit(t, fixture.canonical, "push", "origin", "main")
			},
			want: "target drifted",
		},
		{
			name: "inconsistent published predecessor",
			mutate: func(t *testing.T, _ engineFixture, receipt *WorktreeMergeReceipt) {
				runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", receipt.Candidate.Branch)
			},
			want: "published without a consistent published predecessor",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newEngineFixture(t)
			first := createMergeSource(t, fixture, "unsafe-resolved-first-"+strings.ReplaceAll(test.name, " ", "-"), "feature/unsafe-resolved-first-"+strings.ReplaceAll(test.name, " ", "-"), "shared.txt", "first\n")
			second := createMergeSource(t, fixture, "unsafe-resolved-second-"+strings.ReplaceAll(test.name, " ", "-"), "feature/unsafe-resolved-second-"+strings.ReplaceAll(test.name, " ", "-"), "shared.txt", "second\n")
			receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{first.WorktreeDir, second.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
			if err == nil || receipt.Status != WorktreeMergeConflict || receipt.Candidate.SHA == "" {
				t.Fatalf("prepare = %+v err=%v", receipt, err)
			}
			merge := exec.Command("git", "merge", "--no-commit", receipt.Sources[1].SHA)
			merge.Dir = receipt.Candidate.Worktree
			if output, mergeErr := merge.CombinedOutput(); mergeErr == nil {
				t.Fatalf("fixture merge unexpectedly succeeded: %s", output)
			}
			writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "shared.txt"), "resolved\n")
			runEngineGit(t, receipt.Candidate.Worktree, "add", "shared.txt")
			runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "test: resolve conflict before refusal")
			test.mutate(t, fixture, &receipt)
			if _, advanceErr := advanceResolvedConflictWorktreeMergeCandidate(context.Background(), fixture.githubDir, &receipt, 5*time.Second, 0); advanceErr == nil || !strings.Contains(advanceErr.Error(), test.want) {
				t.Fatalf("advance error = %v, want %q", advanceErr, test.want)
			}
		})
	}
}

func TestE2ESyncCanonicalMergeTargetNotifiesOnlyWhenHeadChanges(t *testing.T) {
	fixture := newEngineFixture(t)
	before := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	updater := filepath.Join(t.TempDir(), "updater")
	runEngineGit(t, filepath.Dir(updater), "clone", fixture.repository.CloneURL, updater)
	runEngineGit(t, updater, "config", "user.name", "WB Test")
	runEngineGit(t, updater, "config", "user.email", "wb@example.test")
	writeEngineFile(t, filepath.Join(updater, "updated.txt"), "updated\n")
	runEngineGit(t, updater, "add", "updated.txt")
	runEngineGit(t, updater, "commit", "-m", "update target")
	runEngineGit(t, updater, "push", "origin", "main")
	after := strings.TrimSpace(runEngineGit(t, updater, "rev-parse", "HEAD"))

	var updates []CheckoutUpdate
	notify := func(_ context.Context, update CheckoutUpdate) {
		updates = append(updates, update)
	}
	status, err := syncCanonicalMergeTarget(context.Background(), fixture.canonical, "main", after, 5*time.Second, 0, notify)
	if err != nil || status != "fast_forwarded" {
		t.Fatalf("sync status=%q err=%v", status, err)
	}
	if len(updates) != 1 || updates[0].OldSHA != before || updates[0].NewSHA != after || updates[0].Cause != "merge-land" {
		t.Fatalf("updates=%+v", updates)
	}
	status, err = syncCanonicalMergeTarget(context.Background(), fixture.canonical, "main", after, 5*time.Second, 0, notify)
	if err != nil || status != "fast_forwarded" || len(updates) != 1 {
		t.Fatalf("unchanged sync status=%q err=%v updates=%+v", status, err, updates)
	}
}

func TestE2EPrepareWorktreeMergeCarriesForwardRepairAfterTargetCIFailure(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "post-target-repair-source", "feature/post-target-repair", "first.txt", "first\n")
	first, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, first.Candidate.Worktree, "push", "origin", first.Candidate.SHA+":refs/heads/"+first.Candidate.Branch)
	runEngineGit(t, fixture.canonical, "merge", "--squash", first.Candidate.SHA)
	runEngineGit(t, fixture.canonical, "commit", "-m", "squash first candidate")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	landing := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	containsCandidate, ancestorErr := isMergeAncestor(context.Background(), fixture.canonical, first.Candidate.SHA, landing)
	if ancestorErr != nil || containsCandidate {
		t.Fatalf("squash fixture unexpectedly contains candidate %s: contains=%t err=%v", first.Candidate.SHA, containsCandidate, ancestorErr)
	}

	first.Phase = WorktreeMergePhaseLand
	first.Status = WorktreeMergePostTargetCIFailed
	first.Route = WorktreeMergeRouteDecision{Requested: WorktreeMergeRouteAuto, Route: WorktreeMergeRoutePullRequest}
	first.PullRequest = "https://example.test/acme/app/pull/29"
	first.PublishedCandidateSHA = first.Candidate.SHA
	first.PreviousTargetSHA = first.TargetSHA
	first.LandingSHA = landing
	first.Checks = githubchecks.PullRequestWaitResult{Status: githubchecks.PullRequestWaitFailed, Repository: "acme/app", Target: "main", Head: landing, Reason: "target test failed"}
	first.Failure = "required target check failed"
	first.Cleanup = true
	mismatchedLanding := first
	mismatchedLanding.LandingSHA = first.TargetSHA
	absorbed, graphContained, absorptionErr := worktreeMergeCandidateAbsorbed(context.Background(), first.Candidate.Worktree, mismatchedLanding, landing)
	if absorptionErr != nil || absorbed || graphContained {
		t.Fatalf("tree-mismatched PR receipt accepted candidate absorption: absorbed=%t graph=%t err=%v", absorbed, graphContained, absorptionErr)
	}
	if err := persistWorktreeMergeReceipt(first); err != nil {
		t.Fatal(err)
	}

	writeEngineFile(t, filepath.Join(source.WorktreeDir, "repair.txt"), "repair\n")
	runEngineGit(t, source.WorktreeDir, "add", "repair.txt")
	runEngineGit(t, source.WorktreeDir, "commit", "-m", "fix: repair target CI")
	advancedSource := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))

	repaired, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if repaired.ID != first.ID || repaired.ReceiptPath != first.ReceiptPath || repaired.Candidate.Worktree != first.Candidate.Worktree {
		t.Fatalf("forward repair abandoned its retained lane: first=%+v repaired=%+v", first, repaired)
	}
	if len(repaired.ForwardRepairs) != 1 || repaired.ForwardRepairs[0].Status != WorktreeMergePostTargetCIFailed ||
		repaired.ForwardRepairs[0].LandingSHA != landing || repaired.ForwardRepairs[0].CandidateSHA != first.Candidate.SHA ||
		repaired.ForwardRepairs[0].PullRequest != first.PullRequest || repaired.ForwardRepairs[0].Failure != first.Failure {
		t.Fatalf("forward repair audit = %+v, want exact failed landing %+v", repaired.ForwardRepairs, first)
	}
	if repaired.PullRequest != "" || repaired.PublishedCandidateSHA != "" || repaired.LandingSHA != "" || repaired.PreviousTargetSHA != "" {
		t.Fatalf("forward repair inherited completed landing identity: %+v", repaired)
	}
	if repaired.TargetSHA != landing || repaired.Sources[0].SHA != advancedSource || !repaired.Cleanup {
		t.Fatalf("forward repair exact target/source/intent = %+v", repaired)
	}
	for _, ancestor := range []string{landing, advancedSource} {
		contains, ancestorErr := isMergeAncestor(context.Background(), repaired.Candidate.Worktree, ancestor, repaired.Candidate.SHA)
		if ancestorErr != nil || !contains {
			t.Fatalf("repair candidate %s does not contain %s: %v", repaired.Candidate.SHA, ancestor, ancestorErr)
		}
	}
}

// TestE2ELandWorktreeMergeLandPhaseResumeRefusesUnrevalidatedAdvancedCandidate
// covers the review finding for the 2026-09-07 incident: a receipt already in
// the land phase — an open pull request, PublishedCandidateSHA naming that
// PR's old head — whose candidate had since advanced to a new SHA and
// recorded validation_failed fell straight through the old
// requireWorktreeMergePublishedValidation carve-out (it only checked that a
// PR and a PublishedCandidateSHA existed, never that PublishedCandidateSHA
// still named the exact current candidate) and pushed the unvalidated,
// still-failing candidate to the PR branch on an ordinary `resume`. Resume
// must instead re-validate the exact candidate SHA before any push, and
// refuse without touching the remote when that re-validation still fails.
func TestE2ELandWorktreeMergeLandPhaseResumeRefusesUnrevalidatedAdvancedCandidate(t *testing.T) {
	fixture := newEngineFixture(t)
	writeEngineGoModule(t, fixture.canonical, "package app\n")
	runEngineGit(t, fixture.canonical, "add", "go.mod", "app.go")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: add Go validation fixture")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	source := createMergeSource(t, fixture, "land-phase-advance-refuse-source", "feature/land-phase-advance-refuse", "candidate.go", "package app\n\nfunc Candidate() {}\n")

	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil || receipt.Status != WorktreeMergePrepared {
		t.Fatalf("initial prepared candidate = %+v err=%v", receipt, err)
	}

	installWorktreeMergePublishOnlyPRGH(t)
	t.Setenv("WB_TEST_CANDIDATE_SHA", receipt.Candidate.SHA)
	t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
	t.Setenv("WB_TEST_GH_LOG", filepath.Join(t.TempDir(), "gh.log"))

	published, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest,
		StopBeforeMerge: true, Timeout: 20 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil || published.Status != WorktreeMergePublished || published.PullRequest == "" || published.PublishedCandidateSHA != receipt.Candidate.SHA {
		t.Fatalf("publish-only handoff = %+v err=%v", published, err)
	}

	// Advance the candidate past the published head with a change that is
	// still broken, and mirror the exact incident receipt shape: land phase,
	// an open PR at the old published head, validation_failed status.
	writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "candidate.go"), "package app\n\nfunc Candidate() { missingCandidate }\n")
	runEngineGit(t, receipt.Candidate.Worktree, "add", "candidate.go")
	runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "fix: advance candidate with a still-broken change")
	brokenDescendant := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))
	advanced := published
	advanced.Candidate.SHA = brokenDescendant
	advanced.Status = WorktreeMergeValidationFailed
	advanced.Failure = "candidate check failed"
	advanced.Validation.Status = quality.StatusFailed
	if err := persistWorktreeMergeReceipt(advanced); err != nil {
		t.Fatal(err)
	}
	remoteBefore := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "ls-remote", "origin", "refs/heads/"+receipt.Candidate.Branch))
	if !strings.HasPrefix(remoteBefore, receipt.Candidate.SHA+"\t") {
		t.Fatalf("candidate branch remote before resume = %q, want the original published head %s", remoteBefore, receipt.Candidate.SHA)
	}

	t.Setenv("WB_TEST_CANDIDATE_SHA", brokenDescendant)
	refused, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: advanced.ReceiptPath, Route: WorktreeMergeRoutePullRequest,
		Timeout: 20 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err == nil {
		t.Fatalf("resume of an advanced, still-failing land-phase candidate was not refused: receipt=%+v", refused)
	}
	if refused.Phase != WorktreeMergePhaseLand || refused.Status != WorktreeMergeValidationFailed ||
		refused.PullRequest != published.PullRequest || refused.PublishedCandidateSHA != published.PublishedCandidateSHA ||
		refused.Candidate.SHA != brokenDescendant {
		t.Fatalf("refused land-phase receipt = %+v", refused)
	}
	remoteAfter := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "ls-remote", "origin", "refs/heads/"+receipt.Candidate.Branch))
	if remoteAfter != remoteBefore {
		t.Fatalf("resume pushed the still-failing advanced candidate: before=%q after=%q", remoteBefore, remoteAfter)
	}
	stored, readErr := readWorktreeMergeReceipt(advanced.ReceiptPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if stored.Phase != WorktreeMergePhaseLand || stored.Status != WorktreeMergeValidationFailed || stored.PullRequest != published.PullRequest {
		t.Fatalf("persisted refused land-phase receipt = %+v", stored)
	}
}

func TestE2EAdvancePublishedWorktreeMergeCandidateAcceptsRecordedDescendantChain(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "published-chain-source", "feature/published-chain", "published-chain.txt", "published\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	published := receipt.Candidate.SHA
	receipt.PullRequest, receipt.PublishedCandidateSHA = "https://example.test/acme/app/pull/41", published

	writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "recorded.txt"), "recorded\n")
	runEngineGit(t, receipt.Candidate.Worktree, "add", "recorded.txt")
	runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "fix: record validated descendant")
	receipt.Candidate.SHA = strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))

	writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "resolved.txt"), "resolved\n")
	runEngineGit(t, receipt.Candidate.Worktree, "add", "resolved.txt")
	runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "fix: resolve later target conflict")
	head := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))

	advanced, err := advancePublishedWorktreeMergeCandidate(context.Background(), nil, defaultRunner, &receipt)
	if err != nil {
		t.Fatalf("advance exact published ancestry chain: %v", err)
	}
	if !advanced || receipt.Candidate.SHA != head {
		t.Fatalf("advanced=%v candidate=%s, want %s", advanced, receipt.Candidate.SHA, head)
	}

	receipt.Candidate.SHA = head
	receipt.PublishedCandidateSHA = strings.Repeat("f", 40)
	writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "untrusted.txt"), "untrusted\n")
	runEngineGit(t, receipt.Candidate.Worktree, "add", "untrusted.txt")
	runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "fix: untrusted ancestry probe")
	if _, err := advancePublishedWorktreeMergeCandidate(context.Background(), nil, defaultRunner, &receipt); err == nil || !strings.Contains(err.Error(), "published candidate predecessor") {
		t.Fatalf("unrelated published predecessor was not refused: %v", err)
	}
}

//nolint:paralleltest // t.Setenv confines the validation cache to this real-Go fixture.
func TestE2EVerifyWorktreeMergeTargetProvidesCandidateOriginRemoteContext(t *testing.T) {
	t.Setenv("WB_VALIDATION_CACHE", filepath.Join(t.TempDir(), "cache"))
	fixture := newEngineFixture(t)
	writeEngineGoModule(t, fixture.canonical, "package app\n\nfunc Value() int { return 1 }\n")
	writeEngineFile(t, filepath.Join(fixture.canonical, "spec", "README.md"), "# Example\n")
	runEngineGit(t, fixture.canonical, "add", "go.mod", "app.go", "spec/README.md")
	runEngineGit(t, fixture.canonical, "commit", "-m", "feat: add target baseline fixture")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	target := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	wantOrigin := strings.TrimSpace(runEngineGit(t, fixture.canonical, "remote", "get-url", "origin"))
	observedOrigin := filepath.Join(t.TempDir(), "baseline-origin.txt")
	bin := t.TempDir()
	specscore := filepath.Join(bin, "specscore")
	if err := testenv.WriteExecutableFile(specscore, []byte("#!/bin/sh\nset -eu\nif [ \"$1 $2\" != \"spec lint\" ]; then exit 2; fi\ngit remote get-url origin >\"$WB_TEST_BASELINE_ORIGIN\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_BASELINE_ORIGIN", observedOrigin)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	report, err := verifyWorktreeMergeTarget(context.Background(), fixture.repository.Slug, fixture.canonical, target, 0, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != quality.StatusPassed {
		t.Fatalf("target baseline report = %+v", report)
	}
	got, err := os.ReadFile(observedOrigin)
	if err != nil || strings.TrimSpace(string(got)) != wantOrigin {
		t.Fatalf("target baseline origin = %q err=%v, want %q", got, err, wantOrigin)
	}
}

func TestE2EResumeWorktreeMergeAdvancesResolvedConflictCandidateDescendant(t *testing.T) {
	fixture := newEngineFixture(t)
	writeEngineGoModule(t, fixture.canonical, "package app\n")
	runEngineGit(t, fixture.canonical, "add", "go.mod", "app.go")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: add Go validation fixture")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	first := createMergeSource(t, fixture, "resolved-descendant-first", "feature/resolved-descendant-first", "shared.txt", "first\n")
	second := createMergeSource(t, fixture, "resolved-descendant-second", "feature/resolved-descendant-second", "shared.txt", "second\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{first.WorktreeDir, second.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err == nil || receipt.Status != WorktreeMergeConflict || receipt.Candidate.SHA == "" {
		t.Fatalf("initial prepare = %+v err=%v, want conflict with recorded candidate", receipt, err)
	}
	receiptedCandidate := receipt.Candidate.SHA
	merge := exec.Command("git", "merge", "--no-commit", receipt.Sources[1].SHA)
	merge.Dir = receipt.Candidate.Worktree
	if output, mergeErr := merge.CombinedOutput(); mergeErr == nil {
		t.Fatalf("manual conflict reproduction unexpectedly merged: %s", output)
	}
	writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "shared.txt"), "resolved\n")
	runEngineGit(t, receipt.Candidate.Worktree, "add", "shared.txt")
	runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "test: resolve receipted conflict")
	resolved := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))

	// Simulate a crash after the append-only evidence is durable and before the
	// mutable receipt is rewritten. A normal resume must consume that exact
	// evidence and validate before it can publish.
	inMemory := receipt
	advanced, advanceErr := advanceResolvedConflictWorktreeMergeCandidate(context.Background(), fixture.githubDir, &inMemory, 5*time.Second, 0)
	if advanceErr != nil || !advanced || inMemory.Candidate.SHA != resolved {
		t.Fatalf("advance conflict candidate = %+v advanced=%t err=%v", inMemory, advanced, advanceErr)
	}
	ack, err := readConflictCandidateAdvance(conflictCandidateAdvancePath(receipt.ReceiptPath))
	if err != nil || ack.OriginalCandidate.SHA != receiptedCandidate || ack.AdvancedCandidateSHA != resolved || ack.CurrentTargetSHA != receipt.TargetSHA {
		t.Fatalf("persisted conflict advance = %+v err=%v", ack, err)
	}
	unchanged, err := readWorktreeMergeReceipt(receipt.ReceiptPath)
	if err != nil || unchanged.Candidate.SHA != receiptedCandidate || unchanged.Status != WorktreeMergeConflict {
		t.Fatalf("crash-window receipt = %+v err=%v", unchanged, err)
	}

	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_TARGET_SHA", resolved)
	t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
	landed, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil || landed.Status != WorktreeMergeLanded || landed.Candidate.SHA != resolved || landed.LandingSHA != resolved || landed.Validation.Revision != resolved {
		t.Fatalf("resume resolved conflict descendant = %+v err=%v", landed, err)
	}
	retried, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil || retried.Status != WorktreeMergeLanded || retried.Candidate.SHA != resolved || retried.LandingSHA != resolved {
		t.Fatalf("idempotent resume = %+v err=%v", retried, err)
	}
}

func TestE2EPrepareWorktreeMergeCreatesIsolatedConsumableCandidate(t *testing.T) {
	fixture := newEngineFixture(t)
	canonicalHead := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	sourceA := createMergeSource(t, fixture, "merge-source-a", "feature/a", "a.txt", "a\n")
	sourceB := createMergeSource(t, fixture, "merge-source-b", "feature/b", "b.txt", "b\n")
	sourceAHead := strings.TrimSpace(runEngineGit(t, sourceA.WorktreeDir, "rev-parse", "HEAD"))
	sourceBHead := strings.TrimSpace(runEngineGit(t, sourceB.WorktreeDir, "rev-parse", "HEAD"))

	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir,
		Sources:      []string{sourceA.WorktreeDir, sourceB.WorktreeDir},
		Target:       "main",
		Model:        "test-model",
		AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != WorktreeMergePrepared || receipt.Phase != WorktreeMergePhasePrepare {
		t.Fatalf("receipt = %+v", receipt)
	}
	if receipt.Repository != "acme/app" || receipt.Target != "main" || receipt.TargetSHA != canonicalHead {
		t.Fatalf("target receipt = %+v, want acme/app main at %s", receipt, canonicalHead)
	}
	if receipt.Candidate.Worktree == "" || receipt.Candidate.Branch == "main" || receipt.Candidate.SHA == "" {
		t.Fatalf("candidate = %+v", receipt.Candidate)
	}
	if len(receipt.Sources) != 2 || receipt.Sources[0].SHA != sourceAHead || receipt.Sources[1].SHA != sourceBHead {
		t.Fatalf("sources = %+v", receipt.Sources)
	}
	for _, head := range []string{canonicalHead, sourceAHead, sourceBHead} {
		if got := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "merge-base", "--is-ancestor", head, receipt.Candidate.SHA)); got != "" {
			t.Fatalf("unexpected merge-base output for %s: %q", head, got)
		}
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); got != canonicalHead {
		t.Fatalf("canonical HEAD changed from %s to %s", canonicalHead, got)
	}
	if got := strings.TrimSpace(runEngineGit(t, sourceA.WorktreeDir, "rev-parse", "HEAD")); got != sourceAHead {
		t.Fatalf("source A changed from %s to %s", sourceAHead, got)
	}
	if got := strings.TrimSpace(runEngineGit(t, sourceB.WorktreeDir, "rev-parse", "HEAD")); got != sourceBHead {
		t.Fatalf("source B changed from %s to %s", sourceBHead, got)
	}
	if _, err := os.Stat(receipt.ReceiptPath); err != nil {
		t.Fatalf("durable receipt missing: %v", err)
	}
}

// TestE2ELandWorktreeMergeRefreshesPublishedCandidateForTargetDrift covers
// evidence receipt merge-sneat-dev-wb-main-1cbbf49dd60f-e69e39368098.json
// (2026-09-07): a published candidate whose target advanced used to make WB
// refuse to rewrite the published branch, stranding the receipt in
// prepare/conflict with the PR still recorded. WB now refreshes the
// candidate in place instead: it merges the new target into the published
// head, records a target_refreshes entry, re-validates the exact new
// candidate, and fast-forward pushes the same PR branch. See
// TestLandWorktreeMergeRefreshedTargetConflictReportsPathsWithoutPushing,
// TestLandWorktreeMergeRefreshAfterAdvancedSourceMergesOnTopOfDescendant,
// TestLandWorktreeMergeRefreshValidationFailureLeavesNothingPushed, and
// TestLandWorktreeMergeRefreshIsNoOpWithoutTargetAdvance in
// worktree_merge_target_refresh_test.go for the remaining refresh contract.
func TestE2ELandWorktreeMergeRefreshesPublishedCandidateForTargetDrift(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "published-drift-source", "feature/published-drift", "published.txt", "candidate\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/"+receipt.Candidate.Branch)
	receipt.Phase, receipt.Status = WorktreeMergePhaseLand, WorktreeMergePublished
	receipt.PullRequest, receipt.PublishedCandidateSHA = "https://example.test/acme/app/pull/41", receipt.Candidate.SHA
	receipt.Route.Requested = WorktreeMergeRoutePullRequest
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	originalCandidate := receipt.Candidate.SHA
	writeEngineFile(t, filepath.Join(fixture.canonical, "advanced.txt"), "target\n")
	runEngineGit(t, fixture.canonical, "add", "advanced.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "feat: advance published target")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	advancedTarget := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	installWorktreeMergePublishOnlyPRGH(t)
	t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
	t.Setenv("WB_TEST_CANDIDATE_SHA", receipt.Candidate.SHA)
	logPath := filepath.Join(t.TempDir(), "gh.log")
	t.Setenv("WB_TEST_GH_LOG", logPath)

	refreshed, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
		Progress: func(event progress.Event) {
			if event.Phase == "refresh_published_candidate" && event.State == progress.Completed {
				sha := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))
				t.Setenv("WB_TEST_CANDIDATE_SHA", sha)
			}
		},
	})
	if err == nil {
		t.Fatalf("expected the exact-head checks boundary past the refresh, got receipt=%+v", refreshed)
	}
	if strings.Contains(err.Error(), "refusing to rewrite the published branch without force-push") {
		t.Fatalf("target drift under a published candidate was refused instead of refreshed: %v", err)
	}
	if refreshed.TargetSHA != advancedTarget {
		t.Fatalf("target_sha = %s, want refreshed %s", refreshed.TargetSHA, advancedTarget)
	}
	if refreshed.Candidate.SHA == originalCandidate {
		t.Fatal("candidate SHA did not change after target refresh")
	}
	if contains, ancestorErr := isMergeAncestor(context.Background(), receipt.Candidate.Worktree, originalCandidate, refreshed.Candidate.SHA); ancestorErr != nil || !contains {
		t.Fatalf("refreshed candidate %s is not a descendant of published candidate %s: %v", refreshed.Candidate.SHA, originalCandidate, ancestorErr)
	}
	if len(refreshed.TargetRefreshes) != 1 {
		t.Fatalf("target_refreshes = %+v, want exactly one entry", refreshed.TargetRefreshes)
	}
	entry := refreshed.TargetRefreshes[0]
	if entry.PreviousTargetSHA != receipt.TargetSHA || entry.NewTargetSHA != advancedTarget ||
		entry.PreviousCandidateSHA != originalCandidate || entry.NewCandidateSHA != refreshed.Candidate.SHA {
		t.Fatalf("target refresh entry = %+v", entry)
	}
	if refreshed.ValidationIdentity == nil || refreshed.ValidationIdentity.CandidateSHA != refreshed.Candidate.SHA {
		t.Fatalf("refreshed candidate was not re-validated at its exact new SHA: %+v", refreshed.ValidationIdentity)
	}
	if refreshed.PublishedCandidateSHA != refreshed.Candidate.SHA {
		t.Fatalf("published_candidate_sha = %s, want the fast-forwarded %s", refreshed.PublishedCandidateSHA, refreshed.Candidate.SHA)
	}
	if got := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "ls-remote", "origin", "refs/heads/"+receipt.Candidate.Branch)); !strings.HasPrefix(got, refreshed.Candidate.SHA+"\t") {
		t.Fatalf("PR branch was not fast-forwarded: %q, want head %s", got, refreshed.Candidate.SHA)
	}
}

// TestE2ELandWorktreeMergeLandPhaseResumeRevalidatesAdvancedCandidateThenPushes
// is the pass-case counterpart to the refusal test above: once the advanced
// candidate SHA re-validates clean, resume must push it to the already-open
// PR branch rather than refusing, or reusing the stale failed validation
// recorded against the old, already-published head.
func TestE2ELandWorktreeMergeLandPhaseResumeRevalidatesAdvancedCandidateThenPushes(t *testing.T) {
	fixture := newEngineFixture(t)
	writeEngineGoModule(t, fixture.canonical, "package app\n")
	runEngineGit(t, fixture.canonical, "add", "go.mod", "app.go")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: add Go validation fixture")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	source := createMergeSource(t, fixture, "land-phase-advance-pass-source", "feature/land-phase-advance-pass", "candidate.go", "package app\n\nfunc Candidate() {}\n")

	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil || receipt.Status != WorktreeMergePrepared {
		t.Fatalf("initial prepared candidate = %+v err=%v", receipt, err)
	}

	installWorktreeMergePublishOnlyPRGH(t)
	t.Setenv("WB_TEST_CANDIDATE_SHA", receipt.Candidate.SHA)
	t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
	t.Setenv("WB_TEST_GH_LOG", filepath.Join(t.TempDir(), "gh.log"))

	published, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest,
		StopBeforeMerge: true, Timeout: 20 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil || published.Status != WorktreeMergePublished || published.PullRequest == "" || published.PublishedCandidateSHA != receipt.Candidate.SHA {
		t.Fatalf("publish-only handoff = %+v err=%v", published, err)
	}

	writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "candidate.go"), "package app\n\nfunc Candidate() {}\n\nfunc CandidateAdvanced() {}\n")
	runEngineGit(t, receipt.Candidate.Worktree, "add", "candidate.go")
	runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "feat: advance candidate with a clean change")
	descendant := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))
	advanced := published
	advanced.Candidate.SHA = descendant
	advanced.Status = WorktreeMergeValidationFailed
	advanced.Failure = "candidate check failed"
	advanced.Validation.Status = quality.StatusFailed
	if err := persistWorktreeMergeReceipt(advanced); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WB_TEST_CANDIDATE_SHA", descendant)
	var events []progress.Event
	resumed, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: advanced.ReceiptPath, Route: WorktreeMergeRoutePullRequest,
		Timeout: 20 * time.Second, CheckPollInterval: time.Millisecond,
		Progress: func(event progress.Event) { events = append(events, event) },
	})
	// The mocked PR exposes zero check-runs, which never resolves to a merge,
	// so the flow legitimately stops at the exact-head checks boundary (the
	// same boundary TestE2EResumeWorktreeMergeStopBeforeMergePublishesAndPreservesExactPRHandoff
	// exercises for an ordinary, non-StopBeforeMerge resume). What is under
	// test here is that re-validation ran and pushed the fixed descendant
	// before that boundary, not that the mocked remote CI ever completes.
	if err == nil || resumed.Status != WorktreeMergeChecksFailed {
		t.Fatalf("advanced candidate resume did not reach the exact-head checks boundary: receipt=%+v err=%v", resumed, err)
	}
	if resumed.Candidate.SHA != descendant || resumed.PublishedCandidateSHA != descendant {
		t.Fatalf("advanced candidate was not published at its exact descendant SHA: %+v", resumed)
	}
	foundRevalidate := false
	for _, event := range events {
		if event.Phase == "revalidate_candidate" && event.State == progress.Completed {
			foundRevalidate = true
		}
	}
	if !foundRevalidate {
		t.Fatalf("resume did not report re-validating the advanced candidate before publishing: %+v", events)
	}
	if got := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "ls-remote", "origin", "refs/heads/"+receipt.Candidate.Branch)); !strings.HasPrefix(got, descendant+"\t") {
		t.Fatalf("descendant was not pushed to the PR branch: %q, want prefix %s", got, descendant)
	}
	stored, readErr := readWorktreeMergeReceipt(advanced.ReceiptPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if stored.Status == WorktreeMergeValidationFailed {
		t.Fatalf("persisted receipt still shows validation_failed after a passing re-validation: %+v", stored)
	}
}

//nolint:paralleltest // t.Setenv confines the validation cache to this real-Go fixture.
func TestE2ELandWorktreeMergeAllowsUnchangedFailingAdvancedTargetValidation(t *testing.T) {
	t.Setenv("WB_VALIDATION_CACHE", filepath.Join(t.TempDir(), "cache"))
	fixture := newEngineFixture(t)
	writeEngineGoModule(t, fixture.canonical, "package app\n\nfunc Value() int { return 1 }\n")
	runEngineGit(t, fixture.canonical, "add", "go.mod", "app.go")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: seed passing target validation")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	source := createMergeSource(t, fixture, "drift-baseline-source", "feature/drift-baseline", "note.txt", "source is unrelated\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	writeEngineFile(t, filepath.Join(fixture.canonical, "bad.go"), "package app\n\nfunc Broken() { missingAdvancedTarget }\n")
	runEngineGit(t, fixture.canonical, "add", "bad.go")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: advance failing target validation")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	advancedTarget := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))

	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
	landed, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("unchanged failing advanced target validation blocked landing: receipt=%+v err=%v", landed, err)
	}
	if landed.Status != WorktreeMergeLanded || landed.Rebase == nil || landed.TargetSHA != advancedTarget ||
		landed.BaselineValidation.Status != quality.StatusFailed || landed.Validation.Status != quality.StatusFailed ||
		landed.BaselineValidation.Revision != advancedTarget || landed.Validation.Revision != landed.Candidate.SHA {
		t.Fatalf("landed validation receipt = %+v", landed)
	}
	persisted, readErr := readWorktreeMergeReceipt(receipt.ReceiptPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if persisted.BaselineValidation.Revision != advancedTarget || persisted.Validation.Revision != landed.Candidate.SHA {
		t.Fatalf("durable land validation receipt = %+v", persisted)
	}
}

//nolint:paralleltest // newEngineFixture changes the process environment with t.Setenv
func TestE2EValidateWorktreeMergeCandidateStopsBeforeTestsForNewLintFailure(t *testing.T) {
	fixture := newEngineFixture(t)
	writeEngineGoModule(t, fixture.canonical, "package app\n\nfunc Value() int { return 1 }\n")
	writeEngineFile(t, filepath.Join(fixture.canonical, ".wb", "quality.yaml"), "version: 1\ngo_lint:\n  commands:\n    - [sh, -c, 'test ! -f lint-fail']\n")
	runEngineGit(t, fixture.canonical, "add", "go.mod", "app.go", ".wb/quality.yaml")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: seed passing lint")
	target := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	writeEngineFile(t, filepath.Join(fixture.canonical, "lint-fail"), "candidate lint regression\n")
	writeEngineFile(t, filepath.Join(fixture.canonical, "candidate_test.go"), "package app\nimport (\"os\"; \"testing\")\nfunc TestMain(m *testing.M) { _ = os.WriteFile(os.Getenv(\"WB_TEST_MARKER\"), []byte(\"ran\"), 0600); os.Exit(m.Run()) }\n")
	runEngineGit(t, fixture.canonical, "add", "lint-fail", "candidate_test.go")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: introduce lint failure")
	marker := filepath.Join(t.TempDir(), "candidate-test-ran")
	t.Setenv("WB_TEST_MARKER", marker)
	t.Setenv("WB_VALIDATION_CACHE", filepath.Join(t.TempDir(), "cache"))
	receipt := WorktreeMergeReceipt{Repository: fixture.repository.Slug, TargetSHA: target}
	receipt.Candidate.Worktree = fixture.canonical
	receipt.Candidate.SHA = strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	err := validateWorktreeMergeCandidate(context.Background(), &receipt, time.Minute, 0, 0, 0, nil)
	if err == nil || !strings.Contains(err.Error(), "introduced or changed failure") {
		t.Fatalf("candidate lint regression = %v, report %+v", err, receipt.Validation)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("candidate test ran before lint regression was rejected: %v", statErr)
	}
	for _, entry := range receipt.Validation.Results {
		if entry.Check == quality.CheckTest {
			t.Fatalf("test result recorded after early lint rejection: %+v", receipt.Validation)
		}
	}
}

//nolint:paralleltest // newEngineFixture changes the process environment with t.Setenv
func TestE2EValidateWorktreeMergeCandidateContinuesAfterInheritedLintFailure(t *testing.T) {
	fixture := newEngineFixture(t)
	writeEngineGoModule(t, fixture.canonical, "package app\n\nfunc Value() int { return 1 }\n")
	writeEngineFile(t, filepath.Join(fixture.canonical, ".wb", "quality.yaml"), "version: 1\ngo_lint:\n  commands:\n    - [sh, -c, 'echo inherited-lint-failure; exit 1']\n")
	runEngineGit(t, fixture.canonical, "add", "go.mod", "app.go", ".wb/quality.yaml")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: seed failing lint")
	target := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	writeEngineFile(t, filepath.Join(fixture.canonical, "candidate_test.go"), "package app\nimport (\"os\"; \"testing\")\nfunc TestMain(m *testing.M) { _ = os.WriteFile(os.Getenv(\"WB_TEST_MARKER\"), []byte(\"ran\"), 0600); os.Exit(m.Run()) }\n")
	runEngineGit(t, fixture.canonical, "add", "candidate_test.go")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: add candidate test")
	marker := filepath.Join(t.TempDir(), "candidate-test-ran")
	t.Setenv("WB_TEST_MARKER", marker)
	t.Setenv("WB_VALIDATION_CACHE", filepath.Join(t.TempDir(), "cache"))
	receipt := WorktreeMergeReceipt{Repository: fixture.repository.Slug, TargetSHA: target}
	receipt.Candidate.Worktree = fixture.canonical
	receipt.Candidate.SHA = strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	if err := validateWorktreeMergeCandidate(context.Background(), &receipt, time.Minute, 0, 0, 0, nil); err != nil {
		t.Fatalf("inherited lint failure blocked full validation: %v, report %+v", err, receipt.Validation)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("candidate test did not run after inherited lint failure: %v", err)
	}
	if receipt.BaselineValidation.Status != quality.StatusFailed || receipt.Validation.Status != quality.StatusFailed {
		t.Fatalf("full baseline comparison missing: baseline %+v candidate %+v", receipt.BaselineValidation, receipt.Validation)
	}
}

func TestE2EResumeWorktreeMergeStopBeforeMergeRefusesTargetOrSourceDrift(t *testing.T) {
	for _, test := range []struct {
		name  string
		drift func(t *testing.T, fixture engineFixture, source worktrees.CreateResult)
		want  string
	}{
		{
			name: "target",
			drift: func(t *testing.T, fixture engineFixture, _ worktrees.CreateResult) {
				writeEngineFile(t, filepath.Join(fixture.canonical, "target-drift.txt"), "target drift\n")
				runEngineGit(t, fixture.canonical, "add", "target-drift.txt")
				runEngineGit(t, fixture.canonical, "commit", "-m", "test: target drift")
				runEngineGit(t, fixture.canonical, "push", "origin", "main")
			},
			want: "target drifted",
		},
		{
			name: "source",
			drift: func(t *testing.T, _ engineFixture, source worktrees.CreateResult) {
				writeEngineFile(t, filepath.Join(source.WorktreeDir, "source-drift.txt"), "source drift\n")
				runEngineGit(t, source.WorktreeDir, "add", "source-drift.txt")
				runEngineGit(t, source.WorktreeDir, "commit", "-m", "test: source drift")
			},
			want: "advanced from",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newEngineFixture(t)
			source := createMergeSource(t, fixture, "published-stop-drift-"+test.name, "feature/published-stop-drift-"+test.name, "drift.txt", "candidate\n")
			receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
				ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
			})
			if err != nil {
				t.Fatal(err)
			}
			test.drift(t, fixture, source)
			failed, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
				ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest,
				StopBeforeMerge: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
			})
			if err == nil || !strings.Contains(err.Error(), test.want) || failed.Status != WorktreeMergeConflict {
				t.Fatalf("drift=%s receipt=%+v err=%v", test.name, failed, err)
			}
			if got := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD")); got != receipt.Candidate.SHA {
				t.Fatalf("preserved candidate changed after %s drift: got %s want %s", test.name, got, receipt.Candidate.SHA)
			}
		})
	}
}

func TestE2EResumeWorktreeMergeStopBeforeMergePublishesAndPreservesExactPRHandoff(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "published-stop-source", "feature/published-stop", "published-stop.txt", "published stop\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ValidationIdentity == nil || receipt.ValidationIdentity.CandidateSHA != receipt.Candidate.SHA || receipt.ValidationIdentity.TargetSHA != receipt.TargetSHA {
		t.Fatalf("prepare did not publish exact validation identity: %+v", receipt.ValidationIdentity)
	}
	reusable, identityErr := preparedValidationStillValidContext(context.Background(), receipt, worktreeMergeValidationPlan{}, 0, 0, 0)
	if identityErr != nil || !reusable {
		t.Fatalf("unchanged prepared validation was not reusable: reusable=%t err=%v", reusable, identityErr)
	}
	drifted := receipt
	drifted.ValidationIdentity = &WorktreeMergeValidationIdentity{CandidateSHA: receipt.Candidate.SHA, TargetSHA: receipt.TargetSHA, QualityPolicySHA: "drifted", WBBuild: receipt.ValidationIdentity.WBBuild, WBExecutableSHA: receipt.ValidationIdentity.WBExecutableSHA, Validators: receipt.ValidationIdentity.Validators, SourceSHAs: receipt.ValidationIdentity.SourceSHAs}
	reusable, identityErr = preparedValidationStillValidContext(context.Background(), drifted, worktreeMergeValidationPlan{}, 0, 0, 0)
	if identityErr != nil || reusable {
		t.Fatalf("validation identity drift was incorrectly reusable: reusable=%t err=%v", reusable, identityErr)
	}
	validatorDrifted := receipt
	validatorIdentity := *receipt.ValidationIdentity
	validatorIdentity.Validators = map[string]string{"go": "drifted"}
	validatorDrifted.ValidationIdentity = &validatorIdentity
	reusable, identityErr = preparedValidationStillValidContext(context.Background(), validatorDrifted, worktreeMergeValidationPlan{}, 0, 0, 0)
	if identityErr != nil || reusable {
		t.Fatalf("validator executable drift was incorrectly reusable: reusable=%t err=%v", reusable, identityErr)
	}
	wbDrifted := receipt
	wbIdentity := *receipt.ValidationIdentity
	wbIdentity.WBExecutableSHA = "drifted"
	wbDrifted.ValidationIdentity = &wbIdentity
	reusable, identityErr = preparedValidationStillValidContext(context.Background(), wbDrifted, worktreeMergeValidationPlan{}, 0, 0, 0)
	if identityErr != nil || reusable {
		t.Fatalf("WB executable drift was incorrectly reusable: reusable=%t err=%v", reusable, identityErr)
	}
	persistedPrepare, readErr := readWorktreeMergeReceipt(receipt.ReceiptPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if reusable, identityErr = preparedValidationStillValidContext(context.Background(), persistedPrepare, worktreeMergeValidationPlan{}, 0, 0, 0); identityErr != nil || !reusable {
		current, _ := worktreeMergeValidationIdentity(persistedPrepare)
		t.Fatalf("persisted prepared validation was not reusable: reusable=%t err=%v status=%s phase=%s persisted=%+v current=%+v validation=%+v clean=%t", reusable, identityErr, persistedPrepare.Status, persistedPrepare.Phase, persistedPrepare.ValidationIdentity, current, persistedPrepare.Validation, persistedPrepare.Validation.WorkspaceClean)
	}
	installWorktreeMergePublishOnlyPRGH(t)
	t.Setenv("WB_TEST_CANDIDATE_SHA", receipt.Candidate.SHA)
	t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
	logPath := filepath.Join(t.TempDir(), "gh.log")
	t.Setenv("WB_TEST_GH_LOG", logPath)
	var resumeEvents []progress.Event

	published, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest,
		StopBeforeMerge: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
		Progress: func(event progress.Event) { resumeEvents = append(resumeEvents, event) },
	})
	if err != nil {
		t.Fatalf("publish-only resume failed: receipt=%+v err=%v", published, err)
	}
	if published.Status != WorktreeMergePublished || published.PullRequest != "https://example.test/acme/app/pull/41" ||
		published.PublishedCandidateSHA != receipt.Candidate.SHA || published.LandingSHA != "" || published.Checks.Status != "" {
		t.Fatalf("published handoff receipt = %+v", published)
	}
	for _, event := range resumeEvents {
		if event.Phase == "validate_preserved_candidate" && event.State == progress.Started {
			t.Fatal("exact prepared validation was rerun during unchanged stop-before-merge resume")
		}
	}
	if got := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "ls-remote", "origin", "refs/heads/"+receipt.Candidate.Branch)); !strings.HasPrefix(got, receipt.Candidate.SHA+"\t") {
		t.Fatalf("remote candidate = %q, want exact %s", got, receipt.Candidate.SHA)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); got != receipt.TargetSHA {
		t.Fatalf("publish-only handoff changed target from %s to %s", receipt.TargetSHA, got)
	}
	logContents, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, forbidden := range []string{"pr merge", "check-runs", "/status?"} {
		if strings.Contains(string(logContents), forbidden) {
			t.Fatalf("publish-only handoff invoked %q:\n%s", forbidden, logContents)
		}
	}
	persisted, readErr := readWorktreeMergeReceipt(receipt.ReceiptPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if persisted.Status != WorktreeMergePublished || persisted.Candidate.SHA != receipt.Candidate.SHA || persisted.TargetSHA != receipt.TargetSHA {
		t.Fatalf("persisted published handoff = %+v", persisted)
	}

	continued, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err == nil || continued.Status != WorktreeMergeChecksFailed {
		t.Fatalf("ordinary resume did not continue into the candidate-check boundary: receipt=%+v err=%v", continued, err)
	}
	logContents, readErr = os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	// The checks stage is now vendored: it reads the pull request and the head
	// commit's own check runs through `gh api`, never `gh pr checks --json`,
	// which the installed 2.45 does not support.
	if !strings.Contains(string(logContents), "api repos/acme/app/pulls/41") ||
		!strings.Contains(string(logContents), "/check-runs?per_page=100") ||
		strings.Contains(string(logContents), "pr merge") {
		t.Fatalf("ordinary resume did not continue from the published handoff at checks without merging:\n%s", logContents)
	}

	writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "published-repair.txt"), "repair\n")
	runEngineGit(t, receipt.Candidate.Worktree, "add", "published-repair.txt")
	runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "fix: advance published candidate after failed checks")
	descendant := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))
	continued.Status = WorktreeMergeConflict
	continued.Failure = "older WB rejected recoverable published candidate drift"
	if err := persistWorktreeMergeReceipt(continued); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_CANDIDATE_SHA", descendant)
	advanced, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err == nil || advanced.Status != WorktreeMergeChecksFailed {
		t.Fatalf("published descendant resume did not reach exact-head checks: receipt=%+v err=%v", advanced, err)
	}
	if advanced.Candidate.SHA != descendant || advanced.PublishedCandidateSHA != descendant || advanced.PushGate.PreviousRemoteSHA != receipt.Candidate.SHA {
		t.Fatalf("published descendant receipt = %+v", advanced)
	}
	if got := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "ls-remote", "origin", "refs/heads/"+receipt.Candidate.Branch)); !strings.HasPrefix(got, descendant+"\t") {
		t.Fatalf("remote descendant = %q, want %s", got, descendant)
	}
}

func TestE2EPrepareWorktreeMergeRebatchesPreparedReceiptAdditivelyAndPreservesOldEvidence(t *testing.T) {
	fixture := newEngineFixture(t)
	firstSource := createMergeSource(t, fixture, "rebatch-first", "feature/rebatch-first", "first.txt", "first\n")
	secondSource := createMergeSource(t, fixture, "rebatch-second", "feature/rebatch-second", "second.txt", "second\n")
	first, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{firstSource.WorktreeDir, secondSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	originalReceipt, err := os.ReadFile(first.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	writeEngineFile(t, filepath.Join(firstSource.WorktreeDir, "advance.txt"), "advance\n")
	runEngineGit(t, firstSource.WorktreeDir, "add", "advance.txt")
	runEngineGit(t, firstSource.WorktreeDir, "commit", "-m", "feat: advance first rebatch source")
	advancedFirst := strings.TrimSpace(runEngineGit(t, firstSource.WorktreeDir, "rev-parse", "HEAD"))
	if contains, err := isMergeAncestor(context.Background(), firstSource.WorktreeDir, first.Candidate.SHA, advancedFirst); err != nil || contains {
		t.Fatalf("fixture did not create a non-ancestor original candidate DAG: contains=%t err=%v", contains, err)
	}
	thirdSource := createMergeSource(t, fixture, "rebatch-third", "feature/rebatch-third", "third.txt", "third\n")

	replacement, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{firstSource.WorktreeDir, secondSource.WorktreeDir, thirdSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", RebatchReceipt: first.ReceiptPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ReceiptPath == first.ReceiptPath || replacement.RebatchOf != first.ReceiptPath || len(replacement.Sources) != 3 || len(replacement.RebatchedCandidates) != 1 || replacement.RebatchedCandidates[0] != first.Candidate {
		t.Fatalf("replacement receipt = %+v", replacement)
	}
	if contains, err := isMergeAncestor(context.Background(), replacement.Candidate.Worktree, first.Candidate.SHA, replacement.Candidate.SHA); err != nil || !contains {
		t.Fatalf("replacement does not retain original candidate DAG: contains=%t err=%v", contains, err)
	}
	if current, err := os.ReadFile(first.ReceiptPath); err != nil || !bytes.Equal(current, originalReceipt) {
		t.Fatalf("original receipt changed: err=%v\nwant=%s\ngot=%s", err, originalReceipt, current)
	}
	ack, err := readPreparedWorktreeMergeRebatch(rebatchPath(first.ReceiptPath), first)
	if err != nil || ack.ReplacementReceiptPath != replacement.ReceiptPath || ack.Replacement != replacement.Candidate {
		t.Fatalf("rebatch acknowledgement = %+v err=%v", ack, err)
	}
	active, err := activeWorktreeMergeLaneReceipt(context.Background(), fixture.githubDir, filepath.Dir(first.ReceiptPath), first.Lane)
	if err != nil || active == nil || active.ReceiptPath != replacement.ReceiptPath {
		t.Fatalf("active lane after rebatch = %+v err=%v", active, err)
	}
	if _, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: first.ReceiptPath}); err == nil || !strings.Contains(err.Error(), "rebatched") {
		t.Fatalf("old receipt replay = %v, want rebatched refusal", err)
	}
	if err := validateRebatchedWorktreeMergeCleanup(context.Background(), fixture.githubDir, replacement); err == nil || !strings.Contains(err.Error(), "no remote landing") {
		t.Fatalf("pre-landing old-candidate cleanup eligibility = %v", err)
	}
	runEngineGit(t, fixture.canonical, "merge", "--no-ff", "--no-edit", replacement.Candidate.SHA)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	replacement.LandingSHA = strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	if err := persistWorktreeMergeReceipt(replacement); err != nil {
		t.Fatal(err)
	}
	if err := validateRebatchedWorktreeMergeCleanup(context.Background(), fixture.githubDir, replacement); err != nil {
		t.Fatalf("post-landing old-candidate cleanup eligibility = %v", err)
	}
	// The original candidate is deliberately not an ancestor of the advanced
	// first source. The replacement candidate carries the original candidate in
	// its DAG, so WB cleanup can prove absorption at the replacement landing.
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_TARGET_SHA", replacement.LandingSHA)
	oldCandidateCleanup, err := worktrees.Cleanup(context.Background(), worktrees.CleanupOptions{
		ProjectsRoot: fixture.githubDir, Task: first.Candidate.Task, Base: replacement.Target, ExactRepository: replacement.Repository,
		AbsorbedBy: replacement.LandingSHA, Apply: true, DeleteRemote: true, OlderThan: 0, Workers: 1,
	})
	if err != nil || len(oldCandidateCleanup.Results) != 1 || !oldCandidateCleanup.Results[0].Applied {
		t.Fatalf("old non-ancestor candidate cleanup = %+v err=%v", oldCandidateCleanup, err)
	}
}

func TestE2EPrepareWorktreeMergeRefusesClosedOrDriftedChecksFailedReceipt(t *testing.T) {
	for _, test := range []struct {
		name        string
		prState     string
		merged      bool
		driftRemote bool
		driftTarget bool
		badReceipt  bool
		status      WorktreeMergeStatus
	}{
		{name: "closed pull request", prState: "closed"},
		{name: "merged pull request", prState: "closed", merged: true},
		{name: "candidate ref drift", driftRemote: true},
		{name: "target divergence", driftTarget: true},
		{name: "receipt candidate mismatch", badReceipt: true},
		{name: "published pending candidate ref drift", status: WorktreeMergePublished, driftRemote: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newEngineFixture(t)
			firstSource := createMergeSource(t, fixture, "refuse-first", "feature/refuse-first", "first.txt", "first\n")
			secondSource := createMergeSource(t, fixture, "refuse-second", "feature/refuse-second", "second.txt", "second\n")
			first, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{firstSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
			if err != nil {
				t.Fatal(err)
			}
			runEngineGit(t, first.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/"+first.Candidate.Branch)
			status := test.status
			if status == "" {
				status = WorktreeMergeChecksFailed
			}
			first.Phase, first.Status, first.PullRequest, first.PublishedCandidateSHA = WorktreeMergePhaseLand, status, "41", first.Candidate.SHA
			if test.badReceipt {
				first.PublishedCandidateSHA = first.TargetSHA
			}
			if err := persistWorktreeMergeReceipt(first); err != nil {
				t.Fatal(err)
			}
			installWorktreeMergeDirectGH(t)
			t.Setenv("WB_TEST_CANDIDATE_SHA", first.Candidate.SHA)
			if test.prState != "" {
				t.Setenv("WB_TEST_PR_STATE", test.prState)
			}
			if test.merged {
				t.Setenv("WB_TEST_PR_MERGED", "true")
			}
			if test.driftRemote {
				secondHead := strings.TrimSpace(runEngineGit(t, secondSource.WorktreeDir, "rev-parse", "HEAD"))
				runEngineGit(t, first.Candidate.Worktree, "push", "--force", "origin", secondHead+":refs/heads/"+first.Candidate.Branch)
			}
			if test.driftTarget {
				diverged := strings.TrimSpace(runEngineGit(t, fixture.canonical, "commit-tree", "HEAD^{tree}", "-m", "test: unrelated target root"))
				runEngineGit(t, fixture.canonical, "push", "--force", "origin", diverged+":refs/heads/main")
			}
			_, err = PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{firstSource.WorktreeDir, secondSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", RebatchReceipt: first.ReceiptPath})
			if err == nil {
				t.Fatal("unsafe checks-failed rebatch was accepted")
			}
		})
	}
}

func TestE2EPrepareWorktreeMergeRefreshesPublishedCandidateAfterChecksFail(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "published-refresh-source", "feature/published-refresh", "first.txt", "first\n")
	first, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, first.Candidate.Worktree, "push", "origin", first.Candidate.SHA+":refs/heads/"+first.Candidate.Branch)
	first.Status = WorktreeMergeChecksFailed
	first.Phase = WorktreeMergePhaseLand
	first.PullRequest = "https://example.test/acme/app/pull/29"
	first.PublishedCandidateSHA = first.Candidate.SHA
	first.Route = WorktreeMergeRouteDecision{Requested: WorktreeMergeRouteAuto, Route: WorktreeMergeRoutePullRequest}
	first.PreviousTargetSHA = first.TargetSHA
	first.Cleanup = true
	first.OnFailure = "revert"
	if err := persistWorktreeMergeReceipt(first); err != nil {
		t.Fatal(err)
	}

	writeEngineFile(t, filepath.Join(source.WorktreeDir, "repair.txt"), "repair\n")
	runEngineGit(t, source.WorktreeDir, "add", "repair.txt")
	runEngineGit(t, source.WorktreeDir, "commit", "-m", "fix: repair failed checks")
	advancedSource := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))

	refreshed, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.ID != first.ID || refreshed.PullRequest != first.PullRequest || refreshed.PublishedCandidateSHA != first.Candidate.SHA {
		t.Fatalf("published refresh lost lane or PR identity: first=%+v refreshed=%+v", first, refreshed)
	}
	if refreshed.Sources[0].SHA != advancedSource || refreshed.Candidate.SHA == first.Candidate.SHA {
		t.Fatalf("published refresh did not advance exact source/candidate: %+v", refreshed)
	}
	if refreshed.Route != first.Route || refreshed.PreviousTargetSHA != first.PreviousTargetSHA || !refreshed.Cleanup || refreshed.OnFailure != "revert" {
		t.Fatalf("published refresh lost landing intent: %+v", refreshed)
	}
	if got := strings.TrimSpace(runEngineGit(t, refreshed.Candidate.Worktree, "ls-remote", "origin", "refs/heads/"+refreshed.Candidate.Branch)); !strings.HasPrefix(got, first.Candidate.SHA+"\t") {
		t.Fatalf("prepare rewrote published branch instead of retaining old exact head: %q", got)
	}
	installWorktreeMergePublishedRepairGH(t)
	t.Setenv("WB_TEST_PUBLISHED_SHA", first.Candidate.SHA)
	if landing, merged, err := pullRequestLandingReceipt(context.Background(), refreshed, WorktreeMergeLandOptions{Timeout: time.Second}); err != nil || merged || landing != "" {
		t.Fatalf("open PR at recorded predecessor was not accepted for additive repair: landing=%q merged=%t err=%v", landing, merged, err)
	}
	refreshed.Status = WorktreeMergeValidationFailed
	if err := persistWorktreeMergeReceipt(refreshed); err != nil {
		t.Fatal(err)
	}
	retried, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if retried.Candidate.SHA != refreshed.Candidate.SHA || retried.PullRequest != refreshed.PullRequest || retried.PublishedCandidateSHA != refreshed.PublishedCandidateSHA {
		t.Fatalf("same-source validation retry lost exact candidate or published PR identity: refreshed=%+v retried=%+v", refreshed, retried)
	}
}

func TestE2EPrepareWorktreeMergeConflictPreservesEverySource(t *testing.T) {
	fixture := newEngineFixture(t)
	sourceA := createMergeSource(t, fixture, "conflict-source-a", "feature/a", "shared.txt", "a\n")
	sourceB := createMergeSource(t, fixture, "conflict-source-b", "feature/b", "shared.txt", "b\n")
	sourceAHead := strings.TrimSpace(runEngineGit(t, sourceA.WorktreeDir, "rev-parse", "HEAD"))
	sourceBHead := strings.TrimSpace(runEngineGit(t, sourceB.WorktreeDir, "rev-parse", "HEAD"))

	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir,
		Sources:      []string{sourceA.WorktreeDir, sourceB.WorktreeDir},
		Target:       "main",
		Model:        "test-model",
		AgentRuntime: "test",
	})
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("conflicting prepare error = %v, receipt=%+v", err, receipt)
	}
	if receipt.Status != WorktreeMergeConflict || receipt.ResumeArgs == nil {
		t.Fatalf("conflict receipt = %+v", receipt)
	}
	if got := strings.TrimSpace(runEngineGit(t, sourceA.WorktreeDir, "rev-parse", "HEAD")); got != sourceAHead {
		t.Fatalf("source A changed from %s to %s", sourceAHead, got)
	}
	if got := strings.TrimSpace(runEngineGit(t, sourceB.WorktreeDir, "rev-parse", "HEAD")); got != sourceBHead {
		t.Fatalf("source B changed from %s to %s", sourceBHead, got)
	}
	status := runEngineGit(t, receipt.Candidate.Worktree, "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		t.Fatalf("candidate retained conflict state: %q", status)
	}
}

// TestE2ELandWorktreeMergeResumeValidationStillFailingStaysBlocked proves the
// other half of the guard: when the re-run validation fails again, the
// receipt stays prepare/validation_failed and nothing reaches the remote —
// resume must not silently downgrade a still-broken candidate into a push.
func TestE2ELandWorktreeMergeResumeValidationStillFailingStaysBlocked(t *testing.T) {
	fixture := newEngineFixture(t)
	writeEngineGoModule(t, fixture.canonical, "package app\n")
	runEngineGit(t, fixture.canonical, "add", "go.mod", "app.go")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: add Go validation fixture")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	source := createMergeSource(t, fixture, "still-failing-source", "feature/still-failing", "candidate.go", "package app\n\nfunc Candidate() { missingCandidate }\n")

	failed, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err == nil || failed.Status != WorktreeMergeValidationFailed || failed.Candidate.SHA == "" || failed.Phase != WorktreeMergePhasePrepare {
		t.Fatalf("initial validation failure = receipt %+v err %v", failed, err)
	}
	remoteBefore := strings.TrimSpace(runEngineGit(t, failed.Candidate.Worktree, "ls-remote", fixture.repository.CloneURL, "refs/heads/"+failed.Candidate.Branch))
	if remoteBefore != "" {
		t.Fatalf("candidate branch already present on origin before resume: %q", remoteBefore)
	}

	resumed, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: failed.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err == nil {
		t.Fatalf("resume of a still-failing candidate was not refused: receipt=%+v", resumed)
	}
	if resumed.Phase != WorktreeMergePhasePrepare || resumed.Status != WorktreeMergeValidationFailed {
		t.Fatalf("still-failing resume receipt = %+v, want prepare/validation_failed", resumed)
	}
	remoteAfter := strings.TrimSpace(runEngineGit(t, failed.Candidate.Worktree, "ls-remote", fixture.repository.CloneURL, "refs/heads/"+failed.Candidate.Branch))
	if remoteAfter != "" {
		t.Fatalf("still-failing resume pushed the candidate branch: %q", remoteAfter)
	}
	stored, readErr := readWorktreeMergeReceipt(failed.ReceiptPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if stored.Phase != WorktreeMergePhasePrepare || stored.Status != WorktreeMergeValidationFailed || stored.PullRequest != "" {
		t.Fatalf("persisted still-failing receipt = %+v", stored)
	}
}

func TestE2EWorktreeMergePushRunsExactHookOnceBeforeOpeningPushConnection(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "push-gate-source", "feature/push-gate", "push.txt", "push\n")
	head := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	hooksDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "pre-push.log")
	hook := filepath.Join(hooksDir, "pre-push")
	if err := testenv.WriteExecutableFile(hook, []byte("#!/bin/sh\nset -eu\nprintf 'call %s %s\\n' \"$1\" \"$2\" >>\"$WB_TEST_PUSH_LOG\"\ncat >>\"$WB_TEST_PUSH_LOG\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_PUSH_LOG", logPath)
	runEngineGit(t, source.WorktreeDir, "config", "core.hooksPath", hooksDir)
	remoteRef := "refs/heads/gated-candidate"
	gate, err := runWorktreeMergePrePushGate(context.Background(), source.WorktreeDir, head, remoteRef, 5*time.Second, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := pushWorktreeMergeRef(context.Background(), source.WorktreeDir, head, remoteRef, false, 5*time.Second, 0); err != nil {
		t.Fatal(err)
	}
	logContents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logContents)
	if strings.Count(logText, "call origin ") != 1 || !strings.Contains(logText, "refs/heads/feature/push-gate "+head+" "+remoteRef+" "+strings.Repeat("0", 40)) {
		t.Fatalf("pre-push hook did not receive one exact update: %q", logText)
	}
	if gate.Status != "passed" || gate.LocalSHA != head || gate.RemoteRef != remoteRef || gate.PreviousRemoteSHA != strings.Repeat("0", 40) {
		t.Fatalf("push gate receipt = %+v", gate)
	}
	if got := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "ls-remote", "origin", remoteRef)); !strings.HasPrefix(got, head+"\t") {
		t.Fatalf("no-verify transport did not publish exact gated head: %q", got)
	}
}

func TestE2EResumeWorktreeMergeAcceptsPostLandingTargetDescendant(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "post-land-descendant-source", "feature/post-land-descendant", "candidate.txt", "candidate\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_TARGET_SHA", receipt.Candidate.SHA)
	landed, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	writeEngineFile(t, filepath.Join(fixture.canonical, "release.txt"), "automatic release\n")
	runEngineGit(t, fixture.canonical, "add", "release.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "chore: automatic release")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	descendant := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	landed.Status = WorktreeMergePostTargetCIFailed
	landed.Checks = githubchecks.PullRequestWaitResult{Status: githubchecks.PullRequestWaitFailed, Head: landed.LandingSHA}
	landed.CanonicalSync = ""
	if err := persistWorktreeMergeReceipt(landed); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)

	resumed, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: landed.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != WorktreeMergeLanded || resumed.LandingSHA != landed.LandingSHA || resumed.Checks.ObservedTargetHead != descendant || !resumed.Checks.TargetContainsHead {
		t.Fatalf("descendant post-land resume = %+v, want landing %s and target %s", resumed, landed.LandingSHA, descendant)
	}
}

func TestE2ELandWorktreeMergeResumesAfterSquashPRMergedBeforeReceiptPersisted(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "squash-resume-source", "feature/squash-resume", "squash.txt", "squash\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	tree := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", receipt.Candidate.SHA+"^{tree}"))
	serverLanding := strings.TrimSpace(runEngineGit(t, fixture.canonical, "commit-tree", tree, "-p", receipt.TargetSHA, "-m", "squash candidate"))
	runEngineGit(t, fixture.canonical, "push", "origin", serverLanding+":refs/heads/main")
	receipt.PullRequest = "https://example.test/acme/app/pull/17"
	receipt.Route = WorktreeMergeRouteDecision{Requested: WorktreeMergeRouteAuto, Route: WorktreeMergeRoutePullRequest}
	receipt.PreviousTargetSHA = receipt.TargetSHA
	receipt.Checks = githubchecks.PullRequestWaitResult{Status: githubchecks.PullRequestWaitPassed, PullRequest: receipt.PullRequest, Head: receipt.Candidate.SHA}
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	installWorktreeMergeMergedPRGH(t)
	t.Setenv("WB_TEST_CANDIDATE_SHA", receipt.Candidate.SHA)
	t.Setenv("WB_TEST_TARGET_SHA", serverLanding)
	landed, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if landed.Status != WorktreeMergeLanded || landed.LandingSHA != serverLanding || landed.PreviousTargetSHA != receipt.TargetSHA {
		t.Fatalf("resumed squash receipt = %+v", landed)
	}
	if landed.Checks.Status != githubchecks.PullRequestWaitPassed || landed.Checks.PullRequest != "" || landed.Checks.ObservedTargetHead != serverLanding {
		t.Fatalf("resumed squash receipt did not replace candidate checks with target checks: %+v", landed.Checks)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); got != serverLanding {
		t.Fatalf("canonical target = %s, want resumed server landing %s", got, serverLanding)
	}
}

func TestE2ELandWorktreeMergeRebasesUnpublishedCandidateOntoAdvancedTarget(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "rebase-source", "feature/rebase", "feature.txt", "feature\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	preparedCandidate := receipt.Candidate.SHA
	writeEngineFile(t, filepath.Join(fixture.canonical, "target.txt"), "target advanced\n")
	runEngineGit(t, fixture.canonical, "add", "target.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "feat: advance target")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	advancedTarget := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))

	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
	landed, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Cleanup: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if landed.Rebase == nil || landed.Rebase.CandidateBefore != preparedCandidate || landed.Rebase.TargetBefore != receipt.TargetSHA ||
		landed.Rebase.TargetAfter != advancedTarget || landed.Rebase.CandidateAfter != landed.Candidate.SHA {
		t.Fatalf("rebase receipt = %+v, landing = %+v", landed.Rebase, landed)
	}
	if landed.Candidate.SHA == preparedCandidate {
		t.Fatal("candidate SHA did not change after target rebase")
	}
	if landed.Status != WorktreeMergeComplete {
		t.Fatalf("rebased landing did not complete cleanup: %+v", landed)
	}
	for _, name := range []string{"feature.txt", "target.txt"} {
		if _, err := os.Stat(filepath.Join(fixture.canonical, name)); err != nil {
			t.Fatalf("landed canonical target lacks %s: %v", name, err)
		}
	}
	if _, statErr := os.Stat(source.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("rebased source worktree remains after cleanup: %v", statErr)
	}
}
