package orchestrate

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestWorktreeMergePRTitlePreservesConventionalReleaseIntent(t *testing.T) {
	tests := []struct {
		name     string
		subjects []string
		want     string
	}{
		{name: "single commit", subjects: []string{"fix(worktree): retain exact receipt"}, want: "fix(worktree): retain exact receipt"},
		{name: "feature wins over fixes", subjects: []string{"fix: repair cleanup", "feat(worktree): add mechanical merge", "Merge branch 'main'"}, want: "feat: merge 2 worktree candidates into main"},
		{name: "breaking marker is retained", subjects: []string{"feat!: replace merge receipt schema", "fix: repair cleanup"}, want: "feat!: merge 2 worktree candidates into main"},
		{name: "fix wins over metadata", subjects: []string{"docs: explain merge", "fix(ci): retain release signal"}, want: "fix: merge 2 worktree candidates into main"},
		{name: "untyped fallback remains releasable", subjects: []string{"Merge branch 'one'", "Update generated files"}, want: "fix: merge 2 worktree candidates into main"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := worktreeMergePRTitle(test.subjects, 2, "main"); got != test.want {
				t.Fatalf("title = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWorktreeMergeCheckProgressReportsObservableWait(t *testing.T) {
	var events []progress.Event
	reporter := func(event progress.Event) { events = append(events, event) }
	reportWorktreeMergeCheckProgress(reporter, "candidate_checks")(PullRequestWaitProgress{
		Observation: 3,
		Result: PullRequestWaitResult{
			Status: PullRequestWaitPending,
			Reason: "observed GitHub checks are still pending",
			Checks: []RemoteCheck{{Name: "build", Bucket: "pass"}, {Name: "test", Bucket: "pending"}},
		},
		NextPoll: 30 * time.Second,
	})
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	event := events[0]
	if event.Operation != "worktree_merge" || event.Phase != "candidate_checks" || event.State != progress.Waiting {
		t.Fatalf("event = %+v", event)
	}
	for _, want := range []string{"poll 3", "1 passed", "1 pending", "next poll in 30s"} {
		if !strings.Contains(event.Detail, want) {
			t.Errorf("detail %q is missing %q", event.Detail, want)
		}
	}
}

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

func TestInspectWorktreeMergeSourcesPreservesWorkLogLoadError(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "unreadable-source", "feature/unreadable-source", "source.txt", "source\n")
	prompts := filepath.Join(source.WorktreeDir, ".wb", "local", "prompts")
	if err := os.RemoveAll(prompts); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prompts, []byte("not a prompt directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, _, err := inspectWorktreeMergeSources(context.Background(), fixture.githubDir, []string{source.WorktreeDir}, "main")
	if err == nil {
		t.Fatal("inspectWorktreeMergeSources unexpectedly accepted a source with an unreadable Work Log")
	}
	if !strings.Contains(err.Error(), "load Work Log for source") || strings.Contains(err.Error(), "no authoritative active Work Log claim") {
		t.Fatalf("source Work Log error = %v", err)
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

func TestPrepareWorktreeMergeSkipsUnneededPassingTargetValidation(t *testing.T) {
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
	if receipt.BaselineValidation.Status != quality.StatusSkipped || receipt.Validation.Status != quality.StatusPassed ||
		receipt.BaselineValidation.Revision != receipt.TargetSHA || receipt.Validation.Revision != receipt.Candidate.SHA {
		t.Fatalf("validation receipt = %+v", receipt)
	}
	if len(receipt.BaselineValidation.Results) != 1 || !strings.Contains(receipt.BaselineValidation.Results[0].Detail, "not needed") {
		t.Fatalf("lazy baseline reason missing: %+v", receipt.BaselineValidation)
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

func TestResumeWorktreeMergeAcceptsPostLandingTargetDescendant(t *testing.T) {
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
	landed.Checks = PullRequestWaitResult{Status: PullRequestWaitFailed, Head: landed.LandingSHA}
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

func TestResumeWorktreeMergeRefusesPostLandingTargetWithoutLanding(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "post-land-diverged-source", "feature/post-land-diverged", "candidate.txt", "candidate\n")
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
	unrelated := strings.TrimSpace(runEngineGit(t, fixture.canonical, "commit-tree", receipt.TargetSHA+"^{tree}", "-p", receipt.TargetSHA, "-m", "rewrite target without landing"))
	runEngineGit(t, fixture.canonical, "update-ref", "refs/heads/main", unrelated, landed.LandingSHA)
	runEngineGit(t, fixture.canonical, "push", "--force", "origin", "main")
	landed.Status = WorktreeMergePostTargetCIFailed
	landed.Checks = PullRequestWaitResult{Status: PullRequestWaitFailed, Head: landed.LandingSHA}
	landed.CanonicalSync = ""
	if err := persistWorktreeMergeReceipt(landed); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)

	failed, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: landed.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "does not contain exact landed head") || failed.Status != WorktreeMergePostTargetCIFailed {
		t.Fatalf("non-descendant post-land resume = %+v err=%v", failed, err)
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

func TestLandWorktreeMergeResumeCompleteCleanupClearsStaleFailure(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "resume-complete-source", "feature/resume-complete", "cleanup.txt", "cleanup\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_TARGET_SHA", receipt.Candidate.SHA)
	landed, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto, Cleanup: true,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	historicalFirst := writeFailedCleanupReport(t, landed.Sources[0].Task, landed.Repository, time.Now().UTC().Add(-2*time.Hour))
	historicalSecond := writeFailedCleanupReport(t, landed.Sources[0].Task, landed.Repository, time.Now().UTC().Add(-time.Hour))
	landed.CleanupReports = append([]string{historicalFirst, historicalSecond}, landed.CleanupReports...)
	landed.Failure = "cleanup task was previously refused"
	if err := persistWorktreeMergeReceipt(landed); err != nil {
		t.Fatal(err)
	}

	resumed, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto, Cleanup: true,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != WorktreeMergeComplete || resumed.Failure != "" {
		t.Fatalf("resumed terminal receipt = %+v", resumed)
	}
	persisted, err := readWorktreeMergeReceipt(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Failure != "" {
		t.Fatalf("stale terminal failure was persisted: %+v", persisted)
	}
}

func writeFailedCleanupReport(t *testing.T, task, repository string, generatedAt time.Time) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cleanup.json")
	report := map[string]any{
		"generated_at":  generatedAt,
		"phase":         "applied",
		"task":          task,
		"apply":         true,
		"delete_remote": true,
		"results": []map[string]any{{
			"task": task, "repository": repository, "applied": false,
			"worktree_gone": false, "branch_deleted": false, "reason": "prior receipt proof was incomplete",
		}},
	}
	contents, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNormalizeCompletedWorktreeMergeReceiptPreservesFailureWithoutTerminalEvidence(t *testing.T) {
	receipt := WorktreeMergeReceipt{
		Status: WorktreeMergeComplete, Cleanup: true, Failure: "cleanup task remains unapplied", ReceiptPath: "receipt.json",
		Candidate: WorktreeMergeCandidate{Task: "candidate"},
		Sources:   []WorktreeMergeSource{{Task: "source"}},
		CleanedTasks: []string{
			"candidate",
		},
		CleanupReports: []string{"candidate-cleanup.json"},
	}
	if err := normalizeCompletedWorktreeMergeReceipt(&receipt); err == nil || !strings.Contains(err.Error(), "cleanup evidence is incomplete") {
		t.Fatalf("incomplete terminal receipt error = %v", err)
	}
	if receipt.Failure != "cleanup task remains unapplied" {
		t.Fatalf("incomplete receipt failure was cleared: %+v", receipt)
	}
}

func TestNormalizeCompletedWorktreeMergeReceiptPreservesFailureForDuplicateTask(t *testing.T) {
	receipt := WorktreeMergeReceipt{
		Status: WorktreeMergeComplete, Cleanup: true, Failure: "cleanup task remains unapplied", ReceiptPath: "receipt.json",
		Candidate: WorktreeMergeCandidate{Task: "candidate"}, Sources: []WorktreeMergeSource{{Task: "source"}},
		CleanedTasks:   []string{"candidate", "candidate"},
		CleanupReports: []string{"candidate-cleanup.json", "source-cleanup.json"},
	}
	if err := normalizeCompletedWorktreeMergeReceipt(&receipt); err == nil || !strings.Contains(err.Error(), "cleaned task identities are inconsistent") {
		t.Fatalf("duplicate cleanup identity error = %v", err)
	}
	if receipt.Failure != "cleanup task remains unapplied" {
		t.Fatalf("duplicate receipt failure was cleared: %+v", receipt)
	}
}

func TestValidateTerminalCleanupReportsRejectsMalformedSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cleanup.json")
	contents, err := json.Marshal(map[string]any{"generated_at": time.Now().UTC(), "phase": "applied"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	err = worktrees.ValidateTerminalCleanupReports([]string{path}, "acme/app", []string{"source"})
	if err == nil || !strings.Contains(err.Error(), "inconsistent applied schema") {
		t.Fatalf("malformed cleanup report error = %v", err)
	}
}

func TestValidateTerminalCleanupReportsAcceptsHistoricalPartialProgress(t *testing.T) {
	task := "source"
	repository := "acme/app"
	historical := writeCleanupReportFixture(t, task, repository, time.Now().UTC().Add(-time.Hour), false, true, false, "remote branch was retired before the interrupted worktree removal")
	completed := writeCleanupReportFixture(t, task, repository, time.Now().UTC(), true, true, true, "")
	if err := worktrees.ValidateTerminalCleanupReports([]string{historical, completed}, repository, []string{task}); err != nil {
		t.Fatalf("historical partial cleanup report was rejected: %v", err)
	}
	impossible := writeCleanupReportFixture(t, task, repository, time.Now().UTC(), false, true, true, "cleanup failed after both terminal assets were removed")
	if err := worktrees.ValidateTerminalCleanupReports([]string{impossible}, repository, []string{task}); err == nil || !strings.Contains(err.Error(), "inconsistent failed cleanup evidence") {
		t.Fatalf("impossible failed cleanup report error = %v", err)
	}
}

func writeCleanupReportFixture(t *testing.T, task, repository string, generatedAt time.Time, applied, worktreeGone, branchDeleted bool, reason string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cleanup.json")
	report := map[string]any{
		"generated_at":  generatedAt,
		"phase":         "applied",
		"task":          task,
		"apply":         true,
		"delete_remote": true,
		"results": []map[string]any{{
			"task": task, "repository": repository, "applied": applied,
			"worktree_gone": worktreeGone, "branch_deleted": branchDeleted, "reason": reason,
		}},
	}
	contents, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCleanupWorktreeMergeAssetsTerminalizesSourceWithReceiptProvenSquashLanding(t *testing.T) {
	fixture, source, receipt, landing := squashLandedMergeReceipt(t)
	installWorktreeMergeDirectGH(t)

	if err := cleanupWorktreeMergeAssets(context.Background(), fixture.githubDir, &receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source.WorktreeDir); !os.IsNotExist(err) {
		t.Fatalf("receipt-proven source worktree still exists: %v", err)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "refs/remotes/origin/main")); got != landing {
		t.Fatalf("exact fetched target = %s, want landing %s", got, landing)
	}
	if len(receipt.CleanedTasks) != 2 || receipt.CleanedTasks[1] != receipt.Sources[0].Task {
		t.Fatalf("cleaned tasks = %#v", receipt.CleanedTasks)
	}
}

func TestCleanupWorktreeMergeReceiptProofRefusesBrokenLinks(t *testing.T) {
	tests := []struct {
		name         string
		breakReceipt func(*WorktreeMergeReceipt, string)
		want         string
	}{
		{name: "source identity", breakReceipt: func(receipt *WorktreeMergeReceipt, _ string) { receipt.Sources[0].Branch = "feature/advanced" }, want: "source identity no longer matches"},
		{name: "source candidate ancestry", breakReceipt: func(receipt *WorktreeMergeReceipt, base string) { receipt.Candidate.SHA = base }, want: "is not an ancestor of candidate"},
		{name: "candidate landing tree", breakReceipt: func(receipt *WorktreeMergeReceipt, base string) { receipt.LandingSHA = base }, want: "does not equal landing tree"},
		{name: "landing target containment", breakReceipt: func(receipt *WorktreeMergeReceipt, _ string) { receipt.LandingSHA = receipt.Candidate.SHA }, want: "is not contained in the exact fetched target"},
		{name: "receipt identity", breakReceipt: func(receipt *WorktreeMergeReceipt, _ string) { receipt.Sources[0].SHA = "not-a-sha" }, want: "receipt has invalid source SHA"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, source, receipt, _ := squashLandedMergeReceipt(t)
			base := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", receipt.TargetSHA))
			test.breakReceipt(&receipt, base)
			installWorktreeMergeDirectGH(t)

			err := cleanupWorktreeMergeAssets(context.Background(), fixture.githubDir, &receipt)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("cleanup error = %v, want %q", err, test.want)
			}
			if _, statErr := os.Stat(source.WorktreeDir); statErr != nil {
				t.Fatalf("refused source worktree was removed: %v", statErr)
			}
		})
	}
}

func squashLandedMergeReceipt(t *testing.T) (engineFixture, worktrees.CreateResult, WorktreeMergeReceipt, string) {
	t.Helper()
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "squash-cleanup-source", "feature/squash-cleanup", "dependency.txt", "source\n")
	runEngineGit(t, source.WorktreeDir, "push", "origin", source.Branch)
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	writeEngineFile(t, filepath.Join(fixture.canonical, "dependency.txt"), "target\n")
	runEngineGit(t, fixture.canonical, "add", "dependency.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "advance target into source conflict")
	target := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, fixture.canonical, "push", "origin", "main")

	resolved := filepath.Join(t.TempDir(), "resolved")
	runEngineGit(t, filepath.Dir(resolved), "clone", fixture.repository.CloneURL, resolved)
	runEngineGit(t, resolved, "config", "user.name", "WB Test")
	runEngineGit(t, resolved, "config", "user.email", "wb@example.test")
	writeEngineFile(t, filepath.Join(resolved, "dependency.txt"), "resolved\n")
	runEngineGit(t, resolved, "add", "dependency.txt")
	runEngineGit(t, resolved, "commit", "-m", "resolve candidate conflict")
	runEngineGit(t, fixture.canonical, "fetch", resolved, "HEAD")
	tree := strings.TrimSpace(runEngineGit(t, resolved, "rev-parse", "HEAD^{tree}"))
	candidate := strings.TrimSpace(runEngineGit(t, fixture.canonical, "commit-tree", tree, "-p", target, "-p", receipt.Sources[0].SHA, "-m", "integration candidate"))
	landing := strings.TrimSpace(runEngineGit(t, fixture.canonical, "commit-tree", tree, "-p", target, "-m", "squash candidate landing"))
	runEngineGit(t, fixture.canonical, "update-ref", "refs/heads/main", landing, target)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	receipt.Candidate.SHA = candidate
	receipt.LandingSHA = landing
	receipt.CleanedTasks = []string{receipt.Candidate.Task}
	return fixture, source, receipt, landing
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
		Cleanup: true, OnFailure: "revert", Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond, ProgressRequested: true,
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
	for _, required := range []string{"--route direct", "--cleanup", "--progress", "--on-failure revert"} {
		if !strings.Contains(resume, required) {
			t.Fatalf("resume args %q lost %q", resume, required)
		}
	}
	bareResume := WorktreeMergeLandOptions{Route: WorktreeMergeRouteAuto, OnFailure: "stop"}
	if retainWorktreeMergeLandIntent(&persisted, &bareResume) {
		t.Fatal("bare resume unexpectedly changed already-durable landing intent")
	}
	if bareResume.Route != WorktreeMergeRouteDirect || !bareResume.Cleanup || bareResume.OnFailure != "revert" || !bareResume.ProgressRequested {
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

func TestResumeWorktreeMergeRecoversResolvedConflictWithEmptyCandidateSHA(t *testing.T) {
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

func TestResumeWorktreeMergeRefusesEmptyCandidateFromUnrelatedWorktree(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "unrelated-candidate-source", "feature/unrelated-candidate", "TECH-STACK.md", "source\n")
	writeEngineFile(t, filepath.Join(fixture.canonical, "TECH-STACK.md"), "target\n")
	runEngineGit(t, fixture.canonical, "add", "TECH-STACK.md")
	runEngineGit(t, fixture.canonical, "commit", "-m", "advance target into add/add conflict")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err == nil || receipt.Candidate.SHA != "" {
		t.Fatalf("conflicting prepare receipt=%+v err=%v", receipt, err)
	}
	receipt.Candidate.Worktree = source.WorktreeDir
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}

	_, err = ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("unrelated candidate recovery error = %v", err)
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
		"  'api repos/acme/app/branches/main --include'|'api repos/acme/app/branches/main') printf '%s\\n' \"$WB_TEST_BRANCH_JSON\" ;;\n" +
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
  'api repos/acme/app/branches/main --include'|'api repos/acme/app/branches/main') printf '%s\n' '{"protected":false,"protection":{}}' ;;
  'api --paginate --slurp repos/acme/app/rules/branches/main?per_page=100') printf '%s\n' '[[]]' ;;
  'api repos/acme/app/git/ref/heads/main --include'|'api repos/acme/app/git/ref/heads/main')
    target_sha="${WB_TEST_TARGET_SHA:-}"
    if [ -n "${WB_TEST_REMOTE:-}" ]; then target_sha="$(git --git-dir="$WB_TEST_REMOTE" rev-parse refs/heads/main)"; fi
    printf '{"object":{"sha":"%s"}}\n' "$target_sha" ;;
  'api repos/acme/app/compare/'*'...'*)
    pair="${2#*compare/}"
    base="${pair%%...*}"
    candidate="${pair#*...}"
    merge_base="$(git --git-dir="$WB_TEST_REMOTE" merge-base "$base" "$candidate")"
    if git --git-dir="$WB_TEST_REMOTE" merge-base --is-ancestor "$base" "$candidate"; then
      status="ahead"
      if [ "$base" = "$candidate" ]; then status="identical"; fi
    elif git --git-dir="$WB_TEST_REMOTE" merge-base --is-ancestor "$candidate" "$base"; then
      status="behind"
    else
      status="diverged"
    fi
    printf '{"status":"%s","base_commit":{"sha":"%s"},"merge_base_commit":{"sha":"%s"}}\n' "$status" "$base" "$merge_base" ;;
  'api --paginate repos/acme/app/commits/'*'/pulls') printf '%s\n' '[]' ;;
  *'/check-runs?per_page=100 --include'|*'/check-runs?per_page=100') printf '%s\n' '{"total_count":0,"check_runs":[]}' ;;
  *'/status?per_page=100 --include'|*'/status?per_page=100') printf '%s\n' '{"total_count":0,"statuses":[]}' ;;
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
  'api repos/acme/app/branches/main --include'|'api repos/acme/app/branches/main') printf '%s\n' '{"protected":false,"protection":{}}' ;;
  'api --paginate --slurp repos/acme/app/rules/branches/main?per_page=100') printf '%s\n' '[[]]' ;;
  'api repos/acme/app/git/ref/heads/main --include'|'api repos/acme/app/git/ref/heads/main') printf '{"object":{"sha":"%s"}}\n' "$WB_TEST_TARGET_SHA" ;;
  *'/check-runs?per_page=100 --include'|*'/check-runs?per_page=100') printf '%s\n' '{"total_count":0,"check_runs":[]}' ;;
  *'/status?per_page=100 --include'|*'/status?per_page=100') printf '%s\n' '{"total_count":0,"statuses":[]}' ;;
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
