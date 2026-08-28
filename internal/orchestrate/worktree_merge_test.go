package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestPrepareWorktreeMergeCreatesIsolatedConsumableCandidate(t *testing.T) {
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

// The target can already be red. A source which does not change that failure
// must still prepare: target failures are diagnostics, not candidate blockers.
func TestPrepareWorktreeMergeAllowsUnchangedFailingTargetValidation(t *testing.T) {
	fixture := newEngineFixture(t)
	writeEngineGoModule(t, fixture.canonical, "package app\n\nfunc Broken() { missingBaseline }\n")
	runEngineGit(t, fixture.canonical, "add", "go.mod", "app.go")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: seed failing target validation")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")

	source := createMergeSource(t, fixture, "unchanged-baseline-source", "feature/unchanged-baseline", "note.txt", "source is unrelated\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatalf("unchanged failing target validation blocked prepare: receipt=%+v err=%v", receipt, err)
	}
	if receipt.Status != WorktreeMergePrepared {
		t.Fatalf("receipt = %+v, want prepared", receipt)
	}
	if receipt.BaselineValidation.Status != quality.StatusFailed || receipt.Validation.Status != quality.StatusFailed {
		t.Fatalf("baseline/candidate validation = %+v / %+v, want matching failures", receipt.BaselineValidation, receipt.Validation)
	}
	persisted, readErr := readWorktreeMergeReceipt(receipt.ReceiptPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if persisted.BaselineValidation.Revision != receipt.TargetSHA || persisted.Validation.Revision != receipt.Candidate.SHA {
		t.Fatalf("durable validation revisions = baseline %+v candidate %+v", persisted.BaselineValidation, persisted.Validation)
	}
}

func TestPrepareWorktreeMergeRecordsPassingTargetAndCandidateValidation(t *testing.T) {
	fixture := newEngineFixture(t)
	writeEngineGoModule(t, fixture.canonical, "package app\n\nfunc Value() int { return 1 }\n")
	runEngineGit(t, fixture.canonical, "add", "go.mod", "app.go")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: seed passing target validation")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	source := createMergeSource(t, fixture, "passing-baseline-source", "feature/passing-baseline", "note.txt", "source is unrelated\n")

	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.BaselineValidation.Status != quality.StatusPassed || receipt.Validation.Status != quality.StatusPassed ||
		receipt.BaselineValidation.Revision != receipt.TargetSHA || receipt.Validation.Revision != receipt.Candidate.SHA {
		t.Fatalf("validation receipt = %+v", receipt)
	}
}

func TestPrepareWorktreeMergeRejectsChangedCandidateFailureBeyondTargetBaseline(t *testing.T) {
	fixture := newEngineFixture(t)
	writeEngineGoModule(t, fixture.canonical, "package app\n\nfunc Broken() { missingBaseline }\n")
	runEngineGit(t, fixture.canonical, "add", "go.mod", "app.go")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: seed failing target validation")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	source := createMergeSource(t, fixture, "changed-baseline-source", "feature/changed-baseline", "candidate.go", "package app\n\nfunc Candidate() { missingCandidate }\n")

	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err == nil || !strings.Contains(err.Error(), "introduced or changed failure") || receipt.Status != WorktreeMergeValidationFailed {
		t.Fatalf("changed candidate failure = receipt %+v err %v", receipt, err)
	}
	if receipt.BaselineValidation.Status != quality.StatusFailed || receipt.Validation.Status != quality.StatusFailed ||
		!strings.Contains(receipt.Failure, "introduced or changed failure") {
		t.Fatalf("failed validation receipt = %+v", receipt)
	}
}

func TestLandWorktreeMergeAllowsUnchangedFailingAdvancedTargetValidation(t *testing.T) {
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

func TestWorktreeMergeValidationRegressionMatchesOnlyEquivalentBaselineFailures(t *testing.T) {
	failing := func(detail string) quality.VerificationReport {
		return quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{{
			Language: "go", Module: ".", Check: quality.CheckTest, Command: "go test ./...", Status: quality.StatusFailed, Detail: detail,
		}}}
	}
	for _, test := range []struct {
		name      string
		baseline  quality.VerificationReport
		candidate quality.VerificationReport
		wantError bool
	}{
		{name: "passing target and candidate", baseline: quality.VerificationReport{Status: quality.StatusPassed}, candidate: quality.VerificationReport{Status: quality.StatusPassed}},
		{name: "same failure at different snapshot paths", baseline: failing("/tmp/target/app.go:3: undefined: missing"), candidate: failing("/tmp/candidate/app.go:3: undefined: missing")},
		{name: "changed failure", baseline: failing("undefined: missing"), candidate: failing("undefined: other"), wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := worktreeMergeValidationRegression(test.baseline, test.candidate)
			if (err != nil) != test.wantError {
				t.Fatalf("regression error = %v, want error=%t", err, test.wantError)
			}
		})
	}
}

func TestLandWorktreeMergeDirectWalksExactRemoteJourney(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "direct-source", "feature/direct", "direct.txt", "direct\n")
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
	if landed.Status != WorktreeMergeLanded || landed.Route.Route != WorktreeMergeRouteDirect || landed.LandingSHA != receipt.Candidate.SHA {
		t.Fatalf("landing receipt = %+v", landed)
	}
	if landed.PushGate == nil || landed.PushGate.Status != "passed" || landed.PushGate.RemoteRef != "refs/heads/main" || landed.PushGate.LocalSHA != receipt.Candidate.SHA {
		t.Fatalf("direct landing omitted exact pre-push gate evidence: %+v", landed.PushGate)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); got != landed.LandingSHA {
		t.Fatalf("canonical target = %s, want exact remote landing %s", got, landed.LandingSHA)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "ls-remote", "origin", "refs/heads/main")); !strings.HasPrefix(got, landed.LandingSHA+"\t") {
		t.Fatalf("remote target = %q, want %s", got, landed.LandingSHA)
	}
	reverted, err := PrepareWorktreeMergeRevert(context.Background(), fixture.githubDir, landed.ReceiptPath, time.Second, 0)
	if err != nil {
		t.Fatal(err)
	}
	if reverted.Phase != WorktreeMergePhaseRevert || reverted.Status != WorktreeMergePrepared || reverted.Candidate.SHA == landed.Candidate.SHA {
		t.Fatalf("forward revert receipt = %+v", reverted)
	}
	if _, err := os.Stat(filepath.Join(reverted.Candidate.Worktree, "direct.txt")); !os.IsNotExist(err) {
		t.Fatalf("forward revert candidate retained landed file: %v", err)
	}
	t.Setenv("WB_TEST_TARGET_SHA", reverted.Candidate.SHA)
	revertLanded, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: reverted.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if revertLanded.Status != WorktreeMergeLanded || revertLanded.RevertOf == nil || revertLanded.RevertOf.LandingSHA != landed.LandingSHA {
		t.Fatalf("forward revert landing receipt = %+v", revertLanded)
	}
	if _, err := os.Stat(filepath.Join(fixture.canonical, "direct.txt")); !os.IsNotExist(err) {
		t.Fatalf("forward revert did not remove landed file from canonical target: %v", err)
	}
}

func TestWorktreeMergePushRunsExactHookOnceBeforeOpeningPushConnection(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "push-gate-source", "feature/push-gate", "push.txt", "push\n")
	head := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	hooksDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "pre-push.log")
	hook := filepath.Join(hooksDir, "pre-push")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nset -eu\nprintf 'call %s %s\\n' \"$1\" \"$2\" >>\"$WB_TEST_PUSH_LOG\"\ncat >>\"$WB_TEST_PUSH_LOG\"\n"), 0o755); err != nil {
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

func TestLandWorktreeMergeCleanupTerminalizesExactRepositoryAssets(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "cleanup-source", "feature/cleanup", "cleanup.txt", "cleanup\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	candidateWorktree := receipt.Candidate.Worktree
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_TARGET_SHA", receipt.Candidate.SHA)
	landed, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto, Cleanup: true,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if landed.Status != WorktreeMergeComplete || len(landed.CleanedTasks) != 2 || len(landed.CleanupReports) != 2 {
		t.Fatalf("terminal cleanup receipt = %+v", landed)
	}
	for _, path := range []string{source.WorktreeDir, candidateWorktree} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("cleaned worktree still exists at %s: %v", path, statErr)
		}
	}
}

func TestLandWorktreeMergeRebasesUnpublishedCandidateOntoAdvancedTarget(t *testing.T) {
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

func TestLandWorktreeMergeRebaseConflictAbortsWithoutChangingSources(t *testing.T) {
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
		Cleanup: true, OnFailure: "revert", Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
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
	if !failed.Cleanup || !persisted.Cleanup || persisted.Route.Requested != WorktreeMergeRouteDirect || persisted.OnFailure != "revert" {
		t.Fatalf("landing intent was not durable across interruption: returned=%+v persisted=%+v", failed, persisted)
	}
	resume := strings.Join(persisted.ResumeArgs, " ")
	for _, required := range []string{"--route direct", "--cleanup", "--on-failure revert"} {
		if !strings.Contains(resume, required) {
			t.Fatalf("resume args %q lost %q", resume, required)
		}
	}
	bareResume := WorktreeMergeLandOptions{Route: WorktreeMergeRouteAuto, OnFailure: "stop"}
	if retainWorktreeMergeLandIntent(&persisted, &bareResume) {
		t.Fatal("bare resume unexpectedly changed already-durable landing intent")
	}
	if bareResume.Route != WorktreeMergeRouteDirect || !bareResume.Cleanup || bareResume.OnFailure != "revert" {
		t.Fatalf("bare resume did not restore durable landing intent: %+v", bareResume)
	}
}

func TestLandWorktreeMergeRefusesToRewritePublishedCandidateForTargetDrift(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "published-drift-source", "feature/published-drift", "published.txt", "candidate\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.PullRequest = "https://example.test/acme/app/pull/23"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	writeEngineFile(t, filepath.Join(fixture.canonical, "advanced.txt"), "target\n")
	runEngineGit(t, fixture.canonical, "add", "advanced.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "feat: advance published target")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	installWorktreeMergeOpenPRGH(t)
	t.Setenv("WB_TEST_CANDIDATE_SHA", receipt.Candidate.SHA)

	failed, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "refusing to rewrite the published branch without force-push") || failed.Status != WorktreeMergeConflict {
		t.Fatalf("published target drift receipt=%+v err=%v", failed, err)
	}
	if got := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD")); got != receipt.Candidate.SHA {
		t.Fatalf("published candidate was rewritten: got %s want %s", got, receipt.Candidate.SHA)
	}
}

func TestLandWorktreeMergeResumesAfterSquashPRMergedBeforeReceiptPersisted(t *testing.T) {
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
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); got != serverLanding {
		t.Fatalf("canonical target = %s, want resumed server landing %s", got, serverLanding)
	}
}

func TestPrepareWorktreeMergeConflictPreservesEverySource(t *testing.T) {
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

func TestPrepareWorktreeMergeUsesRemoteDefaultInsteadOfAssumingMain(t *testing.T) {
	fixture := newEngineFixtureOnBranch(t, "trunk")
	source := createMergeSourceOnBase(t, fixture, "default-target-source", "feature/default-target", "trunk", "trunk.txt", "trunk\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Target != "trunk" || receipt.TargetSHA != strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "origin/trunk")) {
		t.Fatalf("default target receipt = %+v", receipt)
	}
}

func TestPrepareWorktreeMergeKeepsOneExclusiveActiveTargetLane(t *testing.T) {
	fixture := newEngineFixture(t)
	sourceA := createMergeSource(t, fixture, "lane-source-a", "feature/lane-a", "lane-a.txt", "a\n")
	sourceB := createMergeSource(t, fixture, "lane-source-b", "feature/lane-b", "lane-b.txt", "b\n")
	first, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{sourceA.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{sourceB.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err == nil || !strings.Contains(err.Error(), "still owned by non-terminal receipt") {
		t.Fatalf("second active lane prepare = receipt %+v err %v", blocked, err)
	}
	if blocked.ReceiptPath != first.ReceiptPath || blocked.Candidate.Worktree != first.Candidate.Worktree {
		t.Fatalf("lane blocker did not return its exact owner: first=%+v blocked=%+v", first, blocked)
	}
}

func TestPrepareWorktreeMergeRefreshesUnpublishedCandidateWhenSourceAdvances(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "refresh-source", "feature/refresh", "first.txt", "first\n")
	first, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	writeEngineFile(t, filepath.Join(source.WorktreeDir, "second.txt"), "second\n")
	runEngineGit(t, source.WorktreeDir, "add", "second.txt")
	runEngineGit(t, source.WorktreeDir, "commit", "-m", "feat: advance prepared source")
	advancedSource := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))

	refreshed, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.ID != first.ID || refreshed.ReceiptPath != first.ReceiptPath || refreshed.Candidate.Worktree != first.Candidate.Worktree {
		t.Fatalf("source advance created a competing candidate: first=%+v refreshed=%+v", first, refreshed)
	}
	if len(refreshed.SourceRefreshes) != 1 || refreshed.SourceRefreshes[0].Sources[0].SHA != first.Sources[0].SHA {
		t.Fatalf("source refresh audit = %+v", refreshed.SourceRefreshes)
	}
	if refreshed.Sources[0].SHA != advancedSource || refreshed.Candidate.SHA == first.Candidate.SHA {
		t.Fatalf("refreshed exact heads = source %s candidate %s", refreshed.Sources[0].SHA, refreshed.Candidate.SHA)
	}
	if _, err := os.Stat(filepath.Join(refreshed.Candidate.Worktree, "second.txt")); err != nil {
		t.Fatalf("refreshed candidate lacks advanced source content: %v", err)
	}
}

func TestPrepareWorktreeMergeRefreshesPublishedCandidateAfterChecksFail(t *testing.T) {
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

func TestPrepareWorktreeMergeCarriesForwardRepairAfterTargetCIFailure(t *testing.T) {
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
	first.Checks = PullRequestWaitResult{Status: PullRequestWaitFailed, Repository: "acme/app", Target: "main", Head: landing, Reason: "target test failed"}
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

func TestPrepareWorktreeMergeCarriesForwardRepairAfterLandedCleanupPending(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "landed-forward-repair-source", "feature/landed-forward-repair", "first.txt", "first\n")
	first, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce the original receipt faithfully: the isolated candidate did not
	// have node_modules and therefore recorded local validation failure, but its
	// direct landing later passed exact target CI and was synchronized.
	runEngineGit(t, fixture.canonical, "merge", "--ff-only", first.Candidate.SHA)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	landing := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	first.Phase = WorktreeMergePhaseLand
	first.Status = WorktreeMergeLanded
	first.Route = WorktreeMergeRouteDecision{Requested: WorktreeMergeRouteAuto, Route: WorktreeMergeRouteDirect}
	first.PreviousTargetSHA = first.TargetSHA
	first.LandingSHA = landing
	first.CanonicalSync = "fast_forwarded"
	first.Validation = quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{{
		Language: "node", Module: ".", Check: "lint", Command: "pnpm run lint", Status: quality.StatusFailed,
		Detail: "sh: nx: command not found; node_modules missing",
	}}}
	first.Checks = PullRequestWaitResult{Status: PullRequestWaitPassed, Repository: "acme/app", Target: "main", Head: landing, ObservedHead: landing, ObservedTargetHead: landing}
	first.Failure = "candidate validation failed"
	if err := persistWorktreeMergeReceipt(first); err != nil {
		t.Fatal(err)
	}

	writeEngineFile(t, filepath.Join(source.WorktreeDir, "release-plan.txt"), "0.27.5\n")
	runEngineGit(t, source.WorktreeDir, "add", "release-plan.txt")
	runEngineGit(t, source.WorktreeDir, "commit", "-m", "chore(release): plan provider repair")
	advancedSource := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))

	repaired, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatalf("landed receipt did not prepare its additive forward repair: receipt=%+v err=%v", repaired, err)
	}
	if repaired.ID != first.ID || repaired.ReceiptPath != first.ReceiptPath || repaired.Candidate.Worktree != first.Candidate.Worktree {
		t.Fatalf("forward repair abandoned its audited lane: first=%+v repaired=%+v", first, repaired)
	}
	if repaired.Status != WorktreeMergePrepared || repaired.TargetSHA != landing || repaired.Sources[0].SHA != advancedSource {
		t.Fatalf("forward repair exact target/source/status = %+v", repaired)
	}
	if len(repaired.ForwardRepairs) != 1 || repaired.ForwardRepairs[0].Status != WorktreeMergeLanded ||
		repaired.ForwardRepairs[0].LandingSHA != landing || repaired.ForwardRepairs[0].CandidateSHA != first.Candidate.SHA {
		t.Fatalf("forward repair did not retain the landed receipt history: %+v", repaired.ForwardRepairs)
	}
	history := repaired.ForwardRepairs[0]
	if history.Validation.Status != quality.StatusFailed || history.Checks.Status != PullRequestWaitPassed ||
		len(history.Sources) != 1 || history.Sources[0].SHA != first.Sources[0].SHA {
		t.Fatalf("forward repair lost prior local-validation or exact-target-CI history: %+v", history)
	}
	for _, ancestor := range []string{landing, advancedSource} {
		contains, ancestorErr := isMergeAncestor(context.Background(), repaired.Candidate.Worktree, ancestor, repaired.Candidate.SHA)
		if ancestorErr != nil || !contains {
			t.Fatalf("repair candidate %s does not contain %s: %v", repaired.Candidate.SHA, ancestor, ancestorErr)
		}
	}
}

func TestResumeWorktreeMergeAdvancesLandedCleanupPendingSource(t *testing.T) {
	fixture, source, first := prepareLandedCleanupPendingMerge(t, "resume-landed-forward-repair", "feature/resume-landed-forward-repair")
	writeEngineFile(t, filepath.Join(source.WorktreeDir, "release-plan.txt"), "0.27.5\n")
	runEngineGit(t, source.WorktreeDir, "add", "release-plan.txt")
	runEngineGit(t, source.WorktreeDir, "commit", "-m", "chore(release): plan provider repair")
	advancedSource := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_REMOTE", strings.TrimSpace(runEngineGit(t, fixture.canonical, "remote", "get-url", "origin")))

	resumed, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: first.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("resume did not prepare and land the additive forward repair: receipt=%+v err=%v", resumed, err)
	}
	if resumed.Status != WorktreeMergeLanded || resumed.Candidate.SHA == first.Candidate.SHA || resumed.Sources[0].SHA != advancedSource {
		t.Fatalf("resume re-reported the old candidate instead of landing the source advance: first=%+v resumed=%+v", first, resumed)
	}
	if len(resumed.ForwardRepairs) != 1 || resumed.ForwardRepairs[0].LandingSHA != first.LandingSHA {
		t.Fatalf("resume did not retain the previous landing history: %+v", resumed.ForwardRepairs)
	}
}

func TestResumeWorktreeMergeReobservesDescendantTargetAfterPostTargetCIDrift(t *testing.T) {
	fixture, source, first := prepareLandedCleanupPendingMerge(t, "resume-release-drift", "feature/resume-release-drift")
	first.Status = WorktreeMergePostTargetCIFailed
	first.Checks = PullRequestWaitResult{
		Status: PullRequestWaitFailed, Repository: "acme/app", Target: "main", Head: first.LandingSHA,
		ObservedHead: first.LandingSHA, ObservedTargetHead: first.LandingSHA, Reason: "target advanced while checks were observed",
	}
	first.Failure = "exact remote target advanced during post-target CI observation"
	first.Cleanup = true
	if err := persistWorktreeMergeReceipt(first); err != nil {
		t.Fatal(err)
	}

	writeEngineFile(t, filepath.Join(fixture.canonical, "release.txt"), "0.27.5\n")
	runEngineGit(t, fixture.canonical, "add", "release.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "chore(release): publish packages")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	releaseTarget := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_REMOTE", strings.TrimSpace(runEngineGit(t, fixture.canonical, "remote", "get-url", "origin")))

	resumed, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: first.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Cleanup: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("resume did not re-observe the exact descendant release target: receipt=%+v err=%v", resumed, err)
	}
	if resumed.Status != WorktreeMergeComplete || resumed.LandingSHA != releaseTarget || resumed.Checks.Status != PullRequestWaitPassed {
		t.Fatalf("release-target re-observation did not terminalize the receipt: %+v", resumed)
	}
	if len(resumed.ForwardRepairs) != 1 || resumed.ForwardRepairs[0].Status != WorktreeMergePostTargetCIFailed ||
		resumed.ForwardRepairs[0].LandingSHA != first.LandingSHA || resumed.ForwardRepairs[0].Failure != first.Failure {
		t.Fatalf("release-target re-observation lost the prior exact observation: %+v", resumed.ForwardRepairs)
	}
	if _, statErr := os.Stat(source.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("terminal receipt retained source worktree: %v", statErr)
	}
}

func TestResumeWorktreeMergeRefusesUnrelatedTargetAfterPostTargetCIDrift(t *testing.T) {
	fixture, _, first := prepareLandedCleanupPendingMerge(t, "resume-unrelated-drift", "feature/resume-unrelated-drift")
	first.Status = WorktreeMergePostTargetCIFailed
	first.Checks = PullRequestWaitResult{Status: PullRequestWaitFailed, Head: first.LandingSHA, Reason: "target drift"}
	first.Failure = "exact remote target advanced during post-target CI observation"
	if err := persistWorktreeMergeReceipt(first); err != nil {
		t.Fatal(err)
	}

	runEngineGit(t, fixture.canonical, "checkout", "--orphan", "unrelated-release")
	runEngineGit(t, fixture.canonical, "rm", "-rf", ".")
	writeEngineFile(t, filepath.Join(fixture.canonical, "unrelated.txt"), "unrelated\n")
	runEngineGit(t, fixture.canonical, "add", "unrelated.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "chore(release): unrelated history")
	runEngineGit(t, fixture.canonical, "push", "--force", "origin", "HEAD:main")
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_REMOTE", strings.TrimSpace(runEngineGit(t, fixture.canonical, "remote", "get-url", "origin")))

	blocked, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: first.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Cleanup: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "does not contain prior landing") {
		t.Fatalf("unrelated target drift was not refused: receipt=%+v err=%v", blocked, err)
	}
	if blocked.LandingSHA != first.LandingSHA || len(blocked.ForwardRepairs) != 0 || blocked.Status != WorktreeMergePostTargetCIFailed {
		t.Fatalf("unrelated target refusal mutated prior evidence: first=%+v blocked=%+v", first, blocked)
	}
}

func TestPrepareWorktreeMergeRefusesLandedForwardRepairAfterTargetDrift(t *testing.T) {
	fixture, source, first := prepareLandedCleanupPendingMerge(t, "landed-target-drift", "feature/landed-target-drift")
	writeEngineFile(t, filepath.Join(source.WorktreeDir, "repair.txt"), "repair\n")
	runEngineGit(t, source.WorktreeDir, "add", "repair.txt")
	runEngineGit(t, source.WorktreeDir, "commit", "-m", "fix: additive repair")
	writeEngineFile(t, filepath.Join(fixture.canonical, "target-drift.txt"), "new target work\n")
	runEngineGit(t, fixture.canonical, "add", "target-drift.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "feat: target drift")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")

	blocked, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err == nil || !strings.Contains(err.Error(), "drifted from landed target") {
		t.Fatalf("target-drift repair = receipt %+v err %v", blocked, err)
	}
	if blocked.Candidate.SHA != first.Candidate.SHA || blocked.Sources[0].SHA != first.Sources[0].SHA {
		t.Fatalf("target-drift refusal mutated the prior receipt: first=%+v blocked=%+v", first, blocked)
	}
}

func TestPrepareWorktreeMergeRefusesNonDescendantLandedForwardRepair(t *testing.T) {
	fixture, source, first := prepareLandedCleanupPendingMerge(t, "landed-non-descendant", "feature/landed-non-descendant")
	runEngineGit(t, source.WorktreeDir, "reset", "--hard", first.TargetSHA)
	writeEngineFile(t, filepath.Join(source.WorktreeDir, "replacement.txt"), "different history\n")
	runEngineGit(t, source.WorktreeDir, "add", "replacement.txt")
	runEngineGit(t, source.WorktreeDir, "commit", "-m", "fix: non-descendant replacement")

	blocked, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err == nil || !strings.Contains(err.Error(), "still owned by non-terminal receipt") {
		t.Fatalf("non-descendant repair = receipt %+v err %v", blocked, err)
	}
	if blocked.Candidate.SHA != first.Candidate.SHA || blocked.Sources[0].SHA != first.Sources[0].SHA {
		t.Fatalf("non-descendant refusal mutated the prior receipt: first=%+v blocked=%+v", first, blocked)
	}
}

func prepareLandedCleanupPendingMerge(t *testing.T, task, branch string) (engineFixture, worktrees.CreateResult, WorktreeMergeReceipt) {
	t.Helper()
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, task, branch, "first.txt", "first\n")
	first, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, fixture.canonical, "merge", "--ff-only", first.Candidate.SHA)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	landing := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	first.Phase = WorktreeMergePhaseLand
	first.Status = WorktreeMergeLanded
	first.Route = WorktreeMergeRouteDecision{Requested: WorktreeMergeRouteAuto, Route: WorktreeMergeRouteDirect}
	first.PreviousTargetSHA = first.TargetSHA
	first.LandingSHA = landing
	first.CanonicalSync = "fast_forwarded"
	first.Checks = PullRequestWaitResult{Status: PullRequestWaitPassed, Repository: "acme/app", Target: "main", Head: landing, ObservedHead: landing, ObservedTargetHead: landing}
	if err := persistWorktreeMergeReceipt(first); err != nil {
		t.Fatal(err)
	}
	return fixture, source, first
}

func TestResolveWorktreeMergeAutoRouteUsesDirectOnlyForAuthoritativelyUnprotectedTarget(t *testing.T) {
	for _, test := range []struct {
		name       string
		branchJSON string
		rulesJSON  string
		want       WorktreeMergeRoute
	}{
		{name: "unprotected", branchJSON: `{"protected":false,"protection":{}}`, rulesJSON: `[[]]`, want: WorktreeMergeRouteDirect},
		{name: "classic pull request", branchJSON: `{"protected":true,"protection":{"required_pull_request_reviews":{}}}`, rulesJSON: `[[]]`, want: WorktreeMergeRoutePullRequest},
		{name: "ruleset pull request", branchJSON: `{"protected":true,"protection":{}}`, rulesJSON: `[[{"type":"pull_request","ruleset_id":7,"ruleset_source_type":"Repository","ruleset_source":"acme/app"}]]`, want: WorktreeMergeRoutePullRequest},
		{name: "incomplete branch policy", branchJSON: `{}`, rulesJSON: `[[]]`, want: WorktreeMergeRoutePullRequest},
		{name: "incomplete rules policy", branchJSON: `{"protected":false,"protection":{}}`, rulesJSON: `{}`, want: WorktreeMergeRoutePullRequest},
		{name: "merge queue unsupported", branchJSON: `{"protected":true,"protection":{}}`, rulesJSON: `[[{"type":"merge_queue","ruleset_id":9,"ruleset_source_type":"Repository","ruleset_source":"acme/app"}]]`, want: WorktreeMergeRouteUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			installWorktreeMergeGH(t, test.branchJSON, test.rulesJSON)
			decision, err := ResolveWorktreeMergeRoute(context.Background(), "acme/app", "main", WorktreeMergeRouteAuto)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Route != test.want {
				t.Fatalf("decision = %+v, want %s", decision, test.want)
			}
		})
	}
}

func createMergeSource(t *testing.T, fixture engineFixture, task, branch, name, contents string) worktrees.CreateResult {
	return createMergeSourceOnBase(t, fixture, task, branch, "main", name, contents)
}

func writeEngineGoModule(t *testing.T, root, source string) {
	t.Helper()
	writeEngineFile(t, filepath.Join(root, "go.mod"), "module example.com/mergefixture\n\ngo 1.22\n")
	writeEngineFile(t, filepath.Join(root, "app.go"), source)
}

func createMergeSourceOnBase(t *testing.T, fixture engineFixture, task, branch, base, name, contents string) worktrees.CreateResult {
	t.Helper()
	prompt := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(prompt, []byte("prepare merge fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := worktrees.Create(context.Background(), []string{fixture.repository.Slug}, worktrees.CreateOptions{
		ProjectsRoot: fixture.githubDir,
		Operation:    task,
		Branch:       branch,
		BranchChosen: true,
		Base:         base,
		WorkLog: worktrees.WorkLogOptions{
			EffortID: task, RunID: task + "-run", Initiator: "test", AgentID: task,
			AgentRuntime: "test", Model: "test-model", OriginalPrompt: prompt, RequireOriginalPrompt: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := created[0]
	writeEngineFile(t, filepath.Join(result.WorktreeDir, name), contents)
	runEngineGit(t, result.WorktreeDir, "add", name)
	runEngineGit(t, result.WorktreeDir, "commit", "-m", "feat: add "+name)
	return result
}

func installWorktreeMergeGH(t *testing.T, branchJSON, rulesJSON string) {
	t.Helper()
	bin := t.TempDir()
	script := filepath.Join(bin, "gh")
	body := "#!/bin/sh\nset -eu\n" +
		"case \"$*\" in\n" +
		"  'api repos/acme/app/branches/main') printf '%s\\n' \"$WB_TEST_BRANCH_JSON\" ;;\n" +
		"  'api --paginate --slurp repos/acme/app/rules/branches/main?per_page=100') printf '%s\\n' \"$WB_TEST_RULES_JSON\" ;;\n" +
		"  *) echo \"unexpected gh command: $*\" >&2; exit 2 ;;\n" +
		"esac\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_BRANCH_JSON", branchJSON)
	t.Setenv("WB_TEST_RULES_JSON", rulesJSON)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func installWorktreeMergeDirectGH(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := filepath.Join(bin, "gh")
	body := `#!/bin/sh
set -eu
case "$*" in
  'api repos/acme/app/branches/main') printf '%s\n' '{"protected":false,"protection":{}}' ;;
  'api --paginate --slurp repos/acme/app/rules/branches/main?per_page=100') printf '%s\n' '[[]]' ;;
  'api repos/acme/app/git/ref/heads/main')
    target_sha="${WB_TEST_TARGET_SHA:-}"
    if [ -n "${WB_TEST_REMOTE:-}" ]; then target_sha="$(git --git-dir="$WB_TEST_REMOTE" rev-parse refs/heads/main)"; fi
    printf '{"object":{"sha":"%s"}}\n' "$target_sha" ;;
  'api --paginate repos/acme/app/commits/'*'/pulls') printf '%s\n' '[]' ;;
  *'/check-runs?per_page=100') printf '%s\n' '{"total_count":0,"check_runs":[]}' ;;
  *'/status?per_page=100') printf '%s\n' '{"total_count":0,"statuses":[]}' ;;
  *) echo "unexpected gh command: $*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func installWorktreeMergeMergedPRGH(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := filepath.Join(bin, "gh")
	body := `#!/bin/sh
set -eu
case "$*" in
  'pr view https://example.test/acme/app/pull/17 --repo acme/app --json state,mergedAt,mergeCommit,headRefOid,baseRefName')
    printf '{"state":"MERGED","mergedAt":"2026-08-27T00:00:00Z","headRefOid":"%s","baseRefName":"main","mergeCommit":{"oid":"%s"}}\n' "$WB_TEST_CANDIDATE_SHA" "$WB_TEST_TARGET_SHA" ;;
  'api repos/acme/app/branches/main') printf '%s\n' '{"protected":false,"protection":{}}' ;;
  'api --paginate --slurp repos/acme/app/rules/branches/main?per_page=100') printf '%s\n' '[[]]' ;;
  'api repos/acme/app/git/ref/heads/main') printf '{"object":{"sha":"%s"}}\n' "$WB_TEST_TARGET_SHA" ;;
  *'/check-runs?per_page=100') printf '%s\n' '{"total_count":0,"check_runs":[]}' ;;
  *'/status?per_page=100') printf '%s\n' '{"total_count":0,"statuses":[]}' ;;
  *) echo "unexpected gh command: $*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func installWorktreeMergeOpenPRGH(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := filepath.Join(bin, "gh")
	body := `#!/bin/sh
set -eu
case "$*" in
  'pr view https://example.test/acme/app/pull/23 --repo acme/app --json state,mergedAt,mergeCommit,headRefOid,baseRefName')
    printf '{"state":"OPEN","mergedAt":"","headRefOid":"%s","baseRefName":"main","mergeCommit":{"oid":""}}\n' "$WB_TEST_CANDIDATE_SHA" ;;
  *) echo "unexpected gh command: $*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func installWorktreeMergePublishedRepairGH(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := filepath.Join(bin, "gh")
	body := `#!/bin/sh
set -eu
case "$*" in
  'pr view https://example.test/acme/app/pull/29 --repo acme/app --json state,mergedAt,mergeCommit,headRefOid,baseRefName')
    printf '{"state":"OPEN","mergedAt":"","headRefOid":"%s","baseRefName":"main","mergeCommit":{"oid":""}}\n' "$WB_TEST_PUBLISHED_SHA" ;;
  *) echo "unexpected gh command: $*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}
