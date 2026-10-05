package orchestrate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubchecks"

	"github.com/sneat-dev/wb/internal/githubobserver/testfixture"

	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestReportWorktreeMergeQualityProgressPreservesShardProgress(t *testing.T) {
	t.Parallel()
	var events []progress.Event
	reporter := reportWorktreeMergeQualityProgress(func(event progress.Event) {
		events = append(events, event)
	})
	reporter(quality.Progress{
		Check: quality.CheckTest, Command: "go test", Detail: "internal/worktrees shard 2/8",
		State: quality.ProgressStarted, Completed: 3, Total: 17,
	})
	reporter(quality.Progress{
		Check: quality.CheckTest, Command: "go test", Detail: "internal/worktrees shard 2/8 attempt 2 failed; retrying",
		State: quality.ProgressRetrying, Status: quality.StatusFailed, Attempts: 2,
		Completed: 3, Total: 17,
	})
	reporter(quality.Progress{
		Check: quality.CheckTest, Command: "go test", Detail: "internal/worktrees shard 2/8",
		State: quality.ProgressCompleted, Status: quality.StatusPassed, Attempts: 2,
		Completed: 4, Total: 17,
	})

	if len(events) != 3 {
		t.Fatalf("progress events = %#v, want 3", events)
	}
	started, retrying, completed := events[0], events[1], events[2]
	if started.Phase != "validate_candidate" || started.State != progress.Started || started.Completed != 3 || started.Total != 17 {
		t.Fatalf("started progress = %#v", started)
	}
	if started.Detail != "test: go test: internal/worktrees shard 2/8" {
		t.Fatalf("started detail = %q", started.Detail)
	}
	if retrying.State != progress.Running || retrying.Completed != 3 || retrying.Total != 17 {
		t.Fatalf("retrying progress = %#v", retrying)
	}
	if retrying.Detail != "test: go test: internal/worktrees shard 2/8 attempt 2 failed; retrying: attempt 2: failed" {
		t.Fatalf("retrying detail = %q", retrying.Detail)
	}
	if completed.State != progress.Completed || completed.Completed != 4 || completed.Total != 17 {
		t.Fatalf("completed progress = %#v", completed)
	}
	if completed.Detail != "test: go test: internal/worktrees shard 2/8: attempt 2: passed" {
		t.Fatalf("completed detail = %q", completed.Detail)
	}
}

func TestWorktreeMergePRTitlePreservesConventionalReleaseIntent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		subjects []string
		sources  int
		want     string
	}{
		{name: "single commit", subjects: []string{"fix(worktree): retain exact receipt"}, sources: 1, want: "fix(worktree): retain exact receipt"},
		{name: "single source uses original purpose", subjects: []string{"fix: address review", "Merge remote-tracking branch 'origin/main'", "fix: reconcile source pull requests"}, sources: 1, want: "fix: reconcile source pull requests"},
		{name: "feature summarizes related changes", subjects: []string{"fix: repair cleanup", "feat(worktree): add mechanical merge", "Merge branch 'main'"}, sources: 2, want: "feat: add mechanical merge and 1 related change"},
		{name: "breaking marker is retained", subjects: []string{"feat!: replace merge receipt schema", "fix: repair cleanup"}, sources: 2, want: "feat!: replace merge receipt schema and 1 related change"},
		{name: "fix wins over metadata", subjects: []string{"docs: explain merge", "fix(ci): retain release signal"}, sources: 2, want: "fix: retain release signal and 1 related change"},
		{name: "untyped fallback remains releasable", subjects: []string{"Merge branch 'one'", "Update generated files"}, sources: 2, want: "fix: apply 2 related changes"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := worktreeMergePRTitle(test.subjects, test.sources); got != test.want {
				t.Fatalf("title = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWorktreeMergeCheckProgressReportsObservableWait(t *testing.T) {
	t.Parallel()
	var events []progress.Event
	reporter := func(event progress.Event) { events = append(events, event) }
	reportWorktreeMergeCheckProgress(reporter, "candidate_checks")(githubchecks.PullRequestWaitProgress{
		Observation: 3,
		Result: githubchecks.PullRequestWaitResult{
			Status: githubchecks.PullRequestWaitPending,
			Reason: "observed GitHub checks are still pending",
			Checks: []githubchecks.RemoteCheck{{Name: "build", Bucket: "pass"}, {Name: "test", Bucket: "pending"}},
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

func TestWorktreeMergeValidationTimeoutsPreserveExplicitPreparePolicy(t *testing.T) {
	t.Parallel()
	if got := worktreeMergeValidationTimeouts(0, 0); got != nil {
		t.Fatalf("zero limits = %+v, want omitted legacy receipt field", got)
	}
	stored := worktreeMergeValidationTimeouts(12*time.Minute, 5*time.Minute)
	check, shard := receiptWorktreeMergeValidationTimeouts(WorktreeMergeReceipt{ValidationTimeouts: stored})
	if check != 12*time.Minute || shard != 5*time.Minute {
		t.Fatalf("stored limits = %s/%s, want 12m/5m", check, shard)
	}
	check, shard = receiptWorktreeMergeValidationTimeouts(WorktreeMergeReceipt{})
	if check != 0 || shard != 0 {
		t.Fatalf("legacy receipt limits = %s/%s, want zero", check, shard)
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
//
//nolint:paralleltest // t.Setenv confines the validation cache to this real-Go fixture.
func TestPrepareWorktreeMergeAllowsUnchangedFailingTargetValidation(t *testing.T) {
	t.Setenv("WB_VALIDATION_CACHE", filepath.Join(t.TempDir(), "cache"))
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

func TestWorktreeMergeLintEvidenceIgnoresCachedTestFailure(t *testing.T) {
	t.Parallel()
	report := worktreeMergeLintEvidence(quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{
		{Check: quality.CheckLint, Status: quality.StatusPassed},
		{Check: quality.CheckTest, Status: quality.StatusFailed, Detail: "target test failure"},
	}})
	if report.Status != quality.StatusPassed || len(report.Results) != 1 || report.Results[0].Check != quality.CheckLint {
		t.Fatalf("full cache lint projection = %+v", report)
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

//nolint:paralleltest // t.Setenv confines the validation cache to this real-Go fixture.
func TestPrepareWorktreeMergeRejectsChangedCandidateFailureBeyondTargetBaseline(t *testing.T) {
	t.Setenv("WB_VALIDATION_CACHE", filepath.Join(t.TempDir(), "cache"))
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

func TestValidationCacheValidatorSHAsTrackSpecscoreExecutable(t *testing.T) {
	bin := t.TempDir()
	specscore := filepath.Join(bin, "specscore")
	write := func(contents string) {
		t.Helper()
		if err := testenv.WriteExecutableFile(specscore, []byte(contents), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("#!/bin/sh\nprintf '%s\\n' one\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	first := validationCacheValidatorSHAs([]quality.Check{quality.CheckSpec})
	if len(first) != 1 || len(first["specscore"]) != 64 {
		t.Fatalf("first validator SHA = %#v", first)
	}
	write("#!/bin/sh\nprintf '%s\\n' two\n")
	second := validationCacheValidatorSHAs([]quality.Check{quality.CheckSpec})
	if first["specscore"] == second["specscore"] {
		t.Fatalf("validator SHA did not change after executable replacement: %q", first["specscore"])
	}
	if got := validationCacheValidatorSHAs([]quality.Check{quality.CheckLint}); got != nil {
		t.Fatalf("non-spec checks unexpectedly fingerprinted specscore: %#v", got)
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

func TestResumeWorktreeMergeAdoptsExistingExactHeadPullRequest(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "existing-pr-source", "feature/existing-pr", "existing-pr.txt", "candidate\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	installWorktreeMergePublishOnlyPRGH(t)
	t.Setenv("WB_TEST_CANDIDATE_SHA", receipt.Candidate.SHA)
	t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
	t.Setenv("WB_TEST_EXISTING_PR_JSON", fmt.Sprintf(
		`[{"html_url":"https://example.test/acme/app/pull/41","state":"open","head":{"ref":%q,"sha":%q,"repo":{"full_name":"acme/app"}},"base":{"ref":"main"}}]`,
		receipt.Candidate.Branch, receipt.Candidate.SHA))
	logPath := filepath.Join(t.TempDir(), "gh.log")
	t.Setenv("WB_TEST_GH_LOG", logPath)
	var events []progress.Event

	published, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest,
		StopBeforeMerge: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
		Progress: func(event progress.Event) { events = append(events, event) },
	})
	if err != nil {
		t.Fatalf("adopt exact-head pull request: receipt=%+v err=%v", published, err)
	}
	if published.Status != WorktreeMergePublished || published.PullRequest != "https://example.test/acme/app/pull/41" ||
		published.PublishedCandidateSHA != receipt.Candidate.SHA {
		t.Fatalf("adopted handoff receipt = %+v", published)
	}
	logContents, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	logText := string(logContents)
	if !strings.Contains(logText, "api --paginate repos/acme/app/commits/"+receipt.Candidate.SHA+"/pulls") ||
		!strings.Contains(logText, "pr view https://example.test/acme/app/pull/41") {
		t.Fatalf("existing pull request was not authoritatively discovered and verified:\n%s", logText)
	}
	for _, forbidden := range []string{"pr create", "pr list"} {
		if strings.Contains(logText, forbidden) {
			t.Fatalf("exact pull-request adoption invoked %q:\n%s", forbidden, logText)
		}
	}
	foundAdoption := false
	for _, event := range events {
		if event.Phase == "adopt_pull_request" && event.State == progress.Completed && event.Detail == published.PullRequest {
			foundAdoption = true
		}
	}
	if !foundAdoption {
		t.Fatalf("progress omitted exact pull-request adoption: %+v", events)
	}
	persisted, readErr := readWorktreeMergeReceipt(receipt.ReceiptPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if persisted.Status != WorktreeMergePublished || persisted.PullRequest != published.PullRequest ||
		persisted.PublishedCandidateSHA != receipt.Candidate.SHA {
		t.Fatalf("persisted adopted handoff = %+v", persisted)
	}
}

func TestPreparedValidationReuseAllowsPassedReceiptWithoutBaselineAndNonGoWorktree(t *testing.T) {
	t.Parallel()
	candidate := t.TempDir()
	receipt := WorktreeMergeReceipt{
		Status:    WorktreeMergePrepared,
		TargetSHA: "target", Candidate: WorktreeMergeCandidate{SHA: "candidate", Worktree: candidate},
		Validation: quality.VerificationReport{Revision: "candidate", WorkspaceClean: true, Status: quality.StatusPassed},
	}
	identity, fingerprintable := worktreeMergeValidationIdentity(receipt)
	if !fingerprintable {
		t.Fatal("non-Go worktree identity was not fingerprintable")
	}
	receipt.ValidationIdentity = &identity
	reusable, err := preparedValidationStillValidContext(context.Background(), receipt, worktreeMergeValidationPlan{}, 0, 0, 0)
	if err != nil || !reusable {
		t.Fatalf("non-Go passed receipt was not reusable without baseline: reusable=%t err=%v", reusable, err)
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
	landed.Checks = githubchecks.PullRequestWaitResult{Status: githubchecks.PullRequestWaitFailed, Head: landed.LandingSHA}
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
	receipt.Failure = "obsolete cleanup refusal"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	landed, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto, Cleanup: true,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if landed.Status != WorktreeMergeComplete || landed.Failure != "" || len(landed.CleanedTasks) != 2 || len(landed.CleanupReports) != 2 {
		t.Fatalf("terminal cleanup receipt = %+v", landed)
	}
	for _, path := range []string{source.WorktreeDir, candidateWorktree} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("cleaned worktree still exists at %s: %v", path, statErr)
		}
	}
}

func TestLandWorktreeMergeRevalidatesInterruptedPreparingCandidate(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "interrupted-prepare-source", "feature/interrupted-prepare", "change.txt", "change\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status = WorktreeMergePreparing
	receipt.ValidationTimeouts = worktreeMergeValidationTimeouts(12*time.Minute, 5*time.Minute)
	receipt.Validation = quality.VerificationReport{}
	receipt.BaselineValidation = quality.VerificationReport{}
	receipt.ValidationIdentity = nil
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_TARGET_SHA", receipt.Candidate.SHA)
	landed, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
		PrepareTimeout: time.Minute, ShardAttemptTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, readErr := readWorktreeMergeReceipt(receipt.ReceiptPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	check, shard := receiptWorktreeMergeValidationTimeouts(stored)
	if check != 12*time.Minute || shard != 30*time.Second {
		t.Fatalf("resume lost stored or overridden limits: check=%s shard=%s", check, shard)
	}
	if landed.Validation.Status != quality.StatusPassed || landed.Validation.Revision != receipt.Candidate.SHA {
		t.Fatalf("interrupted candidate was published without exact validation: %+v", landed.Validation)
	}
}

// TestLandWorktreeMergeRevalidatesValidationFailedReceiptThenPublishes covers
// the fix for the 2026-09-07 incident: a receipt whose status is exactly
// prepare/validation_failed had no branch in LandWorktreeMerge that re-ran
// validation, so resume pushed the unvalidated candidate straight to a pull
// request. Resume must now revalidate the exact candidate SHA before any
// publish, honoring stored timeouts unless the caller overrides them.
func TestLandWorktreeMergeRevalidatesValidationFailedReceiptThenPublishes(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "resume-validation-failed-source", "feature/resume-validation-failed", "change.txt", "change\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the exact defective receipt shape: phase=prepare,
	// status=validation_failed, with the candidate SHA already recorded but
	// its recorded validation stale/failed and unfingerprinted.
	receipt.Status = WorktreeMergeValidationFailed
	receipt.Failure = "candidate check failed"
	receipt.ValidationTimeouts = worktreeMergeValidationTimeouts(12*time.Minute, 5*time.Minute)
	receipt.Validation = quality.VerificationReport{Status: quality.StatusFailed, Revision: receipt.Candidate.SHA}
	receipt.BaselineValidation = quality.VerificationReport{}
	receipt.ValidationIdentity = nil
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_TARGET_SHA", receipt.Candidate.SHA)

	var events []progress.Event
	landed, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
		PrepareTimeout: time.Minute, ShardAttemptTimeout: 30 * time.Second,
		Progress: func(event progress.Event) { events = append(events, event) },
	})
	if err != nil {
		t.Fatalf("resume of a re-passing validation_failed receipt was refused: receipt=%+v err=%v", landed, err)
	}
	if landed.Status != WorktreeMergeLanded || landed.Validation.Status != quality.StatusPassed || landed.Validation.Revision != receipt.Candidate.SHA {
		t.Fatalf("landed receipt after revalidation = %+v", landed)
	}
	foundRevalidate := false
	for _, event := range events {
		if event.Phase == "revalidate_candidate" && event.State == progress.Started {
			foundRevalidate = true
		}
	}
	if !foundRevalidate {
		t.Fatalf("resume did not report re-running validation for the validation_failed receipt: %+v", events)
	}
	stored, readErr := readWorktreeMergeReceipt(receipt.ReceiptPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	// The explicit ShardAttemptTimeout override wins; the omitted CheckTimeout
	// inherits the value the receipt already had stored — the same rule
	// interrupted-preparing resume already follows.
	check, shard := receiptWorktreeMergeValidationTimeouts(stored)
	if check != 12*time.Minute || shard != 30*time.Second {
		t.Fatalf("resume lost stored or overridden validation limits: check=%s shard=%s", check, shard)
	}
	if stored.Status != WorktreeMergeLanded || stored.ValidationIdentity == nil || stored.ValidationIdentity.CandidateSHA != receipt.Candidate.SHA {
		t.Fatalf("persisted receipt after revalidated publish = %+v", stored)
	}
}

// TestRequireWorktreeMergePublishedValidationRefusesUnvalidatedCandidate unit
// tests the single guard every publish and landing transition calls through,
// including the founder-approved carve-out for an already-published
// candidate advancing atop its own open pull request.
func TestRequireWorktreeMergePublishedValidationRefusesUnvalidatedCandidate(t *testing.T) {
	t.Parallel()
	base := WorktreeMergeReceipt{
		ReceiptPath: "/tmp/receipt.json",
		Status:      WorktreeMergePrepared,
		Candidate:   WorktreeMergeCandidate{SHA: "cafef00d"},
		Validation:  quality.VerificationReport{Status: quality.StatusPassed, Revision: "cafef00d"},
		ValidationIdentity: &WorktreeMergeValidationIdentity{
			CandidateSHA: "cafef00d",
		},
	}
	if err := requireWorktreeMergePublishedValidationContext(context.Background(), base, worktreeMergeValidationPlan{}, 0, 0, 0); err != nil {
		t.Fatalf("validated exact candidate was refused: %v", err)
	}

	failedStatus := base
	failedStatus.Status = WorktreeMergeValidationFailed
	if err := requireWorktreeMergePublishedValidationContext(context.Background(), failedStatus, worktreeMergeValidationPlan{}, 0, 0, 0); err == nil ||
		!strings.Contains(err.Error(), "cafef00d") || !strings.Contains(err.Error(), "wb worktree merge resume /tmp/receipt.json") {
		t.Fatalf("validation_failed receipt status was not refused with a resume hint: %v", err)
	}

	mismatchedIdentity := base
	identity := *base.ValidationIdentity
	identity.CandidateSHA = "deadbeef"
	mismatchedIdentity.ValidationIdentity = &identity
	if err := requireWorktreeMergePublishedValidationContext(context.Background(), mismatchedIdentity, worktreeMergeValidationPlan{}, 0, 0, 0); err == nil {
		t.Fatal("candidate SHA identity mismatch was not refused")
	}

	staleRevision := base
	staleRevision.Validation.Revision = "deadbeef"
	if err := requireWorktreeMergePublishedValidationContext(context.Background(), staleRevision, worktreeMergeValidationPlan{}, 0, 0, 0); err == nil {
		t.Fatal("validation recorded against a different revision was not refused")
	}

	missingIdentity := base
	missingIdentity.ValidationIdentity = nil
	if err := requireWorktreeMergePublishedValidationContext(context.Background(), missingIdentity, worktreeMergeValidationPlan{}, 0, 0, 0); err == nil {
		t.Fatal("missing validation identity was not refused")
	}

	// The already-published-at-this-exact-SHA carve-out bypasses the check
	// only when the receipt's own status has left validation_failed; it does
	// NOT run for a receipt whose status is still validation_failed even if
	// PublishedCandidateSHA happens to equal the candidate SHA.
	publishedAtCurrentSHA := base
	publishedAtCurrentSHA.PullRequest = "https://example.test/acme/app/pull/1"
	publishedAtCurrentSHA.PublishedCandidateSHA = "cafef00d"
	if err := requireWorktreeMergePublishedValidationContext(context.Background(), publishedAtCurrentSHA, worktreeMergeValidationPlan{}, 0, 0, 0); err != nil {
		t.Fatalf("already-published candidate at its exact validated SHA was incorrectly refused: %v", err)
	}
	publishedAtCurrentSHAButFailed := failedStatus
	publishedAtCurrentSHAButFailed.PullRequest = "https://example.test/acme/app/pull/1"
	publishedAtCurrentSHAButFailed.PublishedCandidateSHA = "cafef00d"
	if err := requireWorktreeMergePublishedValidationContext(context.Background(), publishedAtCurrentSHAButFailed, worktreeMergeValidationPlan{}, 0, 0, 0); err == nil {
		t.Fatal("published-at-current-SHA carve-out was applied despite a validation_failed status")
	}

	// This is the 2026-09-07 incident shape: an already-published PR whose
	// PublishedCandidateSHA names an OLD head, while Candidate.SHA has since
	// advanced to a new, unvalidated (here: validation_failed) SHA. Any
	// status must be refused — this is exactly the carve-out that used to
	// let an unvalidated advanced candidate through untouched.
	publishedAdvance := failedStatus
	publishedAdvance.PullRequest = "https://example.test/acme/app/pull/445"
	publishedAdvance.PublishedCandidateSHA = "183b0a7"
	if err := requireWorktreeMergePublishedValidationContext(context.Background(), publishedAdvance, worktreeMergeValidationPlan{}, 0, 0, 0); err == nil {
		t.Fatal("advanced candidate past an old PublishedCandidateSHA was not refused")
	}
	// An advanced candidate whose exact SHA HAS been locally re-validated
	// (Validation.Revision and the recorded identity both name it) is not
	// blocked by the mismatched PublishedCandidateSHA — the mismatch only
	// disables the no-revalidation-needed carve-out, it is not itself a
	// second, independent refusal reason. Proving that exact SHA is what the
	// land-phase re-validation block in LandWorktreeMerge does before this
	// guard runs.
	advancedAndRevalidated := base
	advancedAndRevalidated.PullRequest = "https://example.test/acme/app/pull/445"
	advancedAndRevalidated.PublishedCandidateSHA = "183b0a7"
	if err := requireWorktreeMergePublishedValidationContext(context.Background(), advancedAndRevalidated, worktreeMergeValidationPlan{}, 0, 0, 0); err != nil {
		t.Fatalf("advanced candidate that was re-validated at its exact SHA was incorrectly refused: %v", err)
	}
}

// TestLandWorktreeMergeRefusesPublishWhenValidationIdentityMismatchesCandidate
// exercises the guard through the real publish/land path with a hand-built
// receipt: a prepared receipt whose validation identity no longer names the
// exact candidate SHA must be refused before any remote ref is touched.
func TestLandWorktreeMergeRefusesPublishWhenValidationIdentityMismatchesCandidate(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "identity-mismatch-source", "feature/identity-mismatch", "change.txt", "change\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != WorktreeMergePrepared || receipt.ValidationIdentity == nil {
		t.Fatalf("prepared fixture receipt = %+v", receipt)
	}
	mismatched := *receipt.ValidationIdentity
	mismatched.CandidateSHA = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	receipt.ValidationIdentity = &mismatched
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_TARGET_SHA", receipt.Candidate.SHA)

	refused, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "wb worktree merge resume") {
		t.Fatalf("identity-mismatched candidate was not refused: receipt=%+v err=%v", refused, err)
	}
	if refused.Status != WorktreeMergeConflict {
		t.Fatalf("identity-mismatched refusal receipt = %+v", refused)
	}
	remoteAfter := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "ls-remote", fixture.repository.CloneURL, "refs/heads/"+receipt.Candidate.Branch))
	if remoteAfter != "" {
		t.Fatalf("identity-mismatched candidate was pushed: %q", remoteAfter)
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func TestResumeWorktreeMergeCompletesAlreadyTerminalizedCleanup(t *testing.T) {
	fixture, source, landed, claims := landedTerminalCleanupFixture(t)
	claimBytes := map[string][]byte{}
	for task, claimPath := range claims {
		contents, err := os.ReadFile(claimPath)
		if err != nil {
			t.Fatal(err)
		}
		claimBytes[task] = contents
	}
	externallyTerminalizeMergeCleanup(t, fixture, &landed)
	terminalBytes := terminalWorkLogBytes(t, claims)

	resumed, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: landed.ReceiptPath, Cleanup: true, Route: WorktreeMergeRouteAuto,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != WorktreeMergeComplete || !resumed.Cleanup || strings.Join(resumed.CleanedTasks, ",") != strings.Join(sortedUniqueMergeTasks(landed), ",") {
		t.Fatalf("terminalized cleanup resume = %+v", resumed)
	}
	for task, claimPath := range claims {
		if got, err := os.ReadFile(claimPath); err != nil || string(got) != string(claimBytes[task]) {
			t.Fatalf("immutable claim %s changed: err=%v", task, err)
		}
	}
	assertTerminalWorkLogBytes(t, claims, terminalBytes)
	if _, err := os.Stat(source.WorktreeDir); !os.IsNotExist(err) {
		t.Fatalf("source worktree survived external cleanup: %v", err)
	}
}

func TestAcknowledgeMissingCleanupRecoversOnlyAfterExactAssetsAreGone(t *testing.T) {
	fixture, _, landed, claims := landedTerminalCleanupFixture(t)
	intent := WorktreeMergeLandOptions{Route: WorktreeMergeRouteAuto, Cleanup: true, OnFailure: "stop"}
	retainWorktreeMergeLandIntent(&landed, &intent)
	if err := persistWorktreeMergeReceipt(landed); err != nil {
		t.Fatal(err)
	}
	if _, err := AcknowledgeMissingWorktreeMergeCleanup(context.Background(), WorktreeMergeMissingCleanupAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: landed.ReceiptPath,
	}); err == nil || (!strings.Contains(err.Error(), "worktree") && !strings.Contains(err.Error(), "partially terminalized")) {
		t.Fatalf("live asset acknowledgement = %v, want worktree refusal", err)
	}

	externallyTerminalizeMergeCleanup(t, fixture, &landed)
	missingTask := landed.Sources[0].Task
	if err := os.Remove(terminalWorkLogPath(claims[missingTask])); err != nil {
		t.Fatal(err)
	}
	ack, err := AcknowledgeMissingWorktreeMergeCleanup(context.Background(), WorktreeMergeMissingCleanupAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: landed.ReceiptPath, Apply: true,
		Actor: "reviewer", Reason: "legacy cleanup removed assets before terminal evidence was retained",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ack.Status != "missing_cleanup_acknowledged" || ack.ReceiptSHA256 == "" || len(ack.Assets) != 2 {
		t.Fatalf("missing cleanup acknowledgement = %+v", ack)
	}
	candidateTerminalPath := terminalWorkLogPath(claims[landed.Candidate.Task])
	candidateTerminal, err := os.ReadFile(candidateTerminalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidateTerminalPath, []byte(strings.Replace(string(candidateTerminal), landed.Candidate.SHA, strings.Repeat("f", len(landed.Candidate.SHA)), 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: landed.ReceiptPath, Cleanup: true, Route: WorktreeMergeRouteAuto,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	}); err == nil || !strings.Contains(err.Error(), "does not exactly corroborate") {
		t.Fatalf("mismatched retained terminal resume = %v, want refusal", err)
	}
	if err := os.WriteFile(candidateTerminalPath, candidateTerminal, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(landed.Candidate.Worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: landed.ReceiptPath, Cleanup: true, Route: WorktreeMergeRouteAuto,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	}); err == nil || (!strings.Contains(err.Error(), "worktree") && !strings.Contains(err.Error(), "partially terminalized")) {
		t.Fatalf("reappeared worktree resume = %v, want refusal", err)
	}
	if err := os.Remove(landed.Candidate.Worktree); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, fixture.canonical, "branch", landed.Sources[0].Branch, landed.Sources[0].SHA)
	if _, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: landed.ReceiptPath, Cleanup: true, Route: WorktreeMergeRouteAuto,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	}); err == nil || !strings.Contains(err.Error(), "local branch") {
		t.Fatalf("reappeared branch resume = %v, want refusal", err)
	}
	runEngineGit(t, fixture.canonical, "branch", "-D", landed.Sources[0].Branch)
	if err := os.WriteFile(filepath.Join(fixture.canonical, "target-advance.txt"), []byte("advance\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, fixture.canonical, "add", "target-advance.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "target advances after cleanup acknowledgement")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	replayed, err := AcknowledgeMissingWorktreeMergeCleanup(context.Background(), WorktreeMergeMissingCleanupAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: landed.ReceiptPath, Apply: true,
		Actor: "reviewer", Reason: "legacy cleanup removed assets before terminal evidence was retained",
	})
	if err != nil || replayed.ID != ack.ID || replayed.CurrentTargetSHA != ack.CurrentTargetSHA {
		t.Fatalf("acknowledgement replay after target advance = %+v err=%v", replayed, err)
	}
	resumed, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: landed.ReceiptPath, Cleanup: true, Route: WorktreeMergeRouteAuto,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		afterHash, _ := worktreeMergeReceiptSHA256(landed.ReceiptPath)
		t.Fatalf("%v (receipt hash before=%s after=%s)", err, ack.ReceiptSHA256, afterHash)
	}
	if resumed.Status != WorktreeMergeComplete || strings.Join(resumed.CleanedTasks, ",") != strings.Join(sortedUniqueMergeTasks(landed), ",") {
		t.Fatalf("acknowledged cleanup resume = %+v", resumed)
	}
	next := createMergeSource(t, fixture, "after-missing-cleanup", "feature/after-missing-cleanup", "after.txt", "after\n")
	if prepared, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{next.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	}); err != nil || prepared.Status != WorktreeMergePrepared {
		t.Fatalf("prepare after acknowledged cleanup = %+v err=%v", prepared, err)
	}
}

func TestMissingCleanupAcknowledgementFailsClosedAfterReceiptTamper(t *testing.T) {
	fixture, _, landed, claims := landedTerminalCleanupFixture(t)
	intent := WorktreeMergeLandOptions{Route: WorktreeMergeRouteAuto, Cleanup: true, OnFailure: "stop"}
	retainWorktreeMergeLandIntent(&landed, &intent)
	if err := persistWorktreeMergeReceipt(landed); err != nil {
		t.Fatal(err)
	}
	externallyTerminalizeMergeCleanup(t, fixture, &landed)
	if err := os.Remove(terminalWorkLogPath(claims[landed.Sources[0].Task])); err != nil {
		t.Fatal(err)
	}
	if _, err := AcknowledgeMissingWorktreeMergeCleanup(context.Background(), WorktreeMergeMissingCleanupAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: landed.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "legacy evidence gap",
	}); err != nil {
		t.Fatal(err)
	}
	landed.Failure = "tampered after acknowledgement"
	if err := persistWorktreeMergeReceipt(landed); err != nil {
		t.Fatal(err)
	}
	if _, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: landed.ReceiptPath, Cleanup: true, Route: WorktreeMergeRouteAuto,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	}); err == nil || !strings.Contains(err.Error(), "invalid immutable evidence") {
		t.Fatalf("tampered receipt resume = %v", err)
	}
}

func TestMissingCleanupAcknowledgementReleasesLaneWithoutReceiptResume(t *testing.T) {
	fixture, _, landed, claims := landedTerminalCleanupFixture(t)
	intent := WorktreeMergeLandOptions{Route: WorktreeMergeRouteAuto, Cleanup: true, OnFailure: "stop"}
	retainWorktreeMergeLandIntent(&landed, &intent)
	if err := persistWorktreeMergeReceipt(landed); err != nil {
		t.Fatal(err)
	}
	externallyTerminalizeMergeCleanup(t, fixture, &landed)
	if err := os.Remove(terminalWorkLogPath(claims[landed.Sources[0].Task])); err != nil {
		t.Fatal(err)
	}
	if _, err := AcknowledgeMissingWorktreeMergeCleanup(context.Background(), WorktreeMergeMissingCleanupAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: landed.ReceiptPath, Apply: true,
		Actor: "reviewer", Reason: "legacy cleanup removed assets before terminal evidence was retained",
	}); err != nil {
		t.Fatal(err)
	}

	// The acknowledgement is terminal evidence in its own right. A fresh
	// prepare must not require a cosmetic resume that rewrites the old receipt.
	next := createMergeSource(t, fixture, "after-ack-without-resume", "feature/after-ack-without-resume", "next.txt", "next\n")
	prepared, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{next.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil || prepared.Status != WorktreeMergePrepared {
		t.Fatalf("prepare after acknowledgement without receipt resume = %+v err=%v", prepared, err)
	}
}

func TestMissingCleanupAcknowledgementRefusesRemoteTargetRewind(t *testing.T) {
	fixture, _, landed, claims := landedTerminalCleanupFixture(t)
	intent := WorktreeMergeLandOptions{Route: WorktreeMergeRouteAuto, Cleanup: true, OnFailure: "stop"}
	retainWorktreeMergeLandIntent(&landed, &intent)
	if err := persistWorktreeMergeReceipt(landed); err != nil {
		t.Fatal(err)
	}
	externallyTerminalizeMergeCleanup(t, fixture, &landed)
	if err := os.Remove(terminalWorkLogPath(claims[landed.Sources[0].Task])); err != nil {
		t.Fatal(err)
	}
	if _, err := AcknowledgeMissingWorktreeMergeCleanup(context.Background(), WorktreeMergeMissingCleanupAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: landed.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "legacy evidence gap",
	}); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, fixture.canonical, "push", "--force", "origin", landed.TargetSHA+":main")
	if _, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: landed.ReceiptPath, Cleanup: true, Route: WorktreeMergeRouteAuto,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	}); err == nil || (!strings.Contains(err.Error(), "does not contain receipted landing") && !strings.Contains(err.Error(), "no longer contains acknowledged target")) {
		t.Fatalf("rewound target resume = %v, want ancestry refusal", err)
	}
}

func TestResumeWorktreeMergeRefusesIncompleteTerminalizedCleanupEvidence(t *testing.T) {
	tests := []struct {
		name          string
		breakEvidence func(t *testing.T, fixture engineFixture, landed WorktreeMergeReceipt, claims map[string]string)
		want          string
	}{
		{
			name: "missing terminal", want: "read removed terminal Work Log",
			breakEvidence: func(t *testing.T, _ engineFixture, landed WorktreeMergeReceipt, claims map[string]string) {
				if err := os.Remove(terminalWorkLogPath(claims[landed.Candidate.Task])); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "mismatched terminal", want: "does not exactly corroborate",
			breakEvidence: func(t *testing.T, _ engineFixture, landed WorktreeMergeReceipt, claims map[string]string) {
				path := terminalWorkLogPath(claims[landed.Candidate.Task])
				contents, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(strings.Replace(string(contents), landed.Candidate.SHA, strings.Repeat("f", len(landed.Candidate.SHA)), 1)), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "tampered immutable claim digest", want: "claim digest mismatch",
			breakEvidence: func(t *testing.T, _ engineFixture, landed WorktreeMergeReceipt, claims map[string]string) {
				path := claims[landed.Candidate.Task]
				contents, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				claimID := strings.TrimSuffix(filepath.Base(path), ".json")
				if err := os.WriteFile(path, []byte(strings.Replace(string(contents), claimID, strings.Repeat("0", len(claimID)), 1)), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "missing sealed outbox", want: "read immutable terminal outbox",
			breakEvidence: func(t *testing.T, _ engineFixture, landed WorktreeMergeReceipt, claims map[string]string) {
				if err := os.Remove(sealedTerminalOutboxPath(claims[landed.Candidate.Task])); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "mismatched sealed outbox", want: "outbox does not corroborate",
			breakEvidence: func(t *testing.T, _ engineFixture, landed WorktreeMergeReceipt, claims map[string]string) {
				path := sealedTerminalOutboxPath(claims[landed.Candidate.Task])
				contents, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(strings.Replace(string(contents), landed.Candidate.SHA, strings.Repeat("f", len(landed.Candidate.SHA)), 1)), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "local branch remains", want: "local branch",
			breakEvidence: func(t *testing.T, fixture engineFixture, landed WorktreeMergeReceipt, _ map[string]string) {
				runEngineGit(t, fixture.canonical, "branch", landed.Candidate.Branch, landed.Candidate.SHA)
			},
		},
		{
			name: "remote branch remains", want: "remote branch",
			breakEvidence: func(t *testing.T, fixture engineFixture, landed WorktreeMergeReceipt, _ map[string]string) {
				runEngineGit(t, fixture.canonical, "push", "origin", landed.Candidate.SHA+":refs/heads/"+landed.Candidate.Branch)
			},
		},
	}
	fixture, _, landed, claims := landedTerminalCleanupFixture(t)
	externallyTerminalizeMergeCleanup(t, fixture, &landed)
	type fileSnapshot struct {
		contents []byte
		mode     os.FileMode
	}
	snapshots := map[string]fileSnapshot{}
	for _, claim := range claims {
		for _, path := range []string{claim, terminalWorkLogPath(claim), sealedTerminalOutboxPath(claim)} {
			contents, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			snapshots[path] = fileSnapshot{contents: contents, mode: info.Mode().Perm()}
		}
	}
	receiptBytes, err := os.ReadFile(landed.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	receiptInfo, err := os.Stat(landed.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	expectations, err := terminalWorkLogExpectations(landed)
	if err != nil {
		t.Fatal(err)
	}
	canonicalHead := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	remoteTarget := strings.TrimSpace(runEngineGit(t, fixture.canonical, "ls-remote", "--heads", "origin", "refs/heads/"+landed.Target))
	verifyAuthority := func(ctx context.Context, affected string, localPresent, remotePresent bool) error {
		for path, snapshot := range snapshots {
			if path == affected {
				continue
			}
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			if !bytes.Equal(contents, snapshot.contents) || info.Mode().Perm() != snapshot.mode {
				return fmt.Errorf("unaffected native custody changed: %s", path)
			}
		}
		head, _, err := runCommand(ctx, defaultRunner, 0, 0, fixture.canonical, "git", "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		if strings.TrimSpace(head) != canonicalHead {
			return fmt.Errorf("canonical target changed: %s", head)
		}
		remote, _, err := runCommand(ctx, defaultRunner, 0, 0, fixture.canonical, "git", "ls-remote", "--heads", "origin", "refs/heads/"+landed.Target)
		if err != nil {
			return err
		}
		if strings.TrimSpace(remote) != remoteTarget {
			return fmt.Errorf("remote target changed: %s", remote)
		}
		for _, expectation := range expectations {
			if _, err := os.Lstat(expectation.Worktree); !os.IsNotExist(err) {
				return fmt.Errorf("terminal worktree no longer absent: %s: %v", expectation.Worktree, err)
			}
			local, _, err := runCommand(ctx, defaultRunner, 0, 0, fixture.canonical, "git", "branch", "--list", "--format=%(refname:short)", expectation.Branch)
			if err != nil {
				return err
			}
			wantLocal := ""
			if localPresent && expectation.Task == landed.Candidate.Task {
				wantLocal = expectation.Branch
			}
			if strings.TrimSpace(local) != wantLocal {
				return fmt.Errorf("unexpected local cleanup ref %s: %s", expectation.Branch, local)
			}
			remote, _, err := runCommand(ctx, defaultRunner, 0, 0, fixture.canonical, "git", "ls-remote", "--heads", "origin", "refs/heads/"+expectation.Branch)
			if err != nil {
				return err
			}
			wantRemote := ""
			if remotePresent && expectation.Task == landed.Candidate.Task {
				wantRemote = landed.Candidate.SHA + "\trefs/heads/" + expectation.Branch
			}
			if strings.TrimSpace(remote) != wantRemote {
				return fmt.Errorf("unexpected remote cleanup ref %s: %s", expectation.Branch, remote)
			}
		}
		return nil
	}
	verifyBaseline := func(ctx context.Context) error {
		if err := verifyAuthority(ctx, "", false, false); err != nil {
			return err
		}
		contents, err := os.ReadFile(landed.ReceiptPath)
		if err != nil {
			return err
		}
		info, err := os.Stat(landed.ReceiptPath)
		if err != nil {
			return err
		}
		if !bytes.Equal(contents, receiptBytes) || info.Mode().Perm() != receiptInfo.Mode().Perm() {
			return fmt.Errorf("full native receipt baseline changed")
		}
		if err := worktrees.ValidateRemovedTerminalWorkLogs(fixture.githubDir, expectations); err != nil {
			return err
		}
		if err := requireTerminalCleanupBranchesAbsent(ctx, fixture.githubDir, landed, expectations, 0, 0); err != nil {
			return err
		}
		lock, err := AcquireOperationLock(fixture.githubDir, landed.Lane, true)
		if err != nil {
			return fmt.Errorf("resume did not release native operation lock: %w", err)
		}
		return lock.Release()
	}
	if err := verifyBaseline(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, test := range tests {
		restored := false
		ok := t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := verifyBaseline(ctx); err != nil {
				t.Fatal(err)
			}
			affected := ""
			switch test.name {
			case "missing terminal", "mismatched terminal":
				affected = terminalWorkLogPath(claims[landed.Candidate.Task])
			case "tampered immutable claim digest":
				affected = claims[landed.Candidate.Task]
			case "missing sealed outbox", "mismatched sealed outbox":
				affected = sealedTerminalOutboxPath(claims[landed.Candidate.Task])
			}
			localRef := test.name == "local branch remains"
			remoteRef := test.name == "remote branch remains"
			restore := func(ctx context.Context) error {
				if affected != "" {
					snapshot := snapshots[affected]
					if err := os.WriteFile(affected, snapshot.contents, snapshot.mode); err != nil {
						return err
					}
					if err := os.Chmod(affected, snapshot.mode); err != nil {
						return err
					}
				}
				if localRef {
					current, _, err := runCommand(ctx, defaultRunner, 0, 0, fixture.canonical, "git", "for-each-ref", "--format=%(objectname)", "refs/heads/"+landed.Candidate.Branch)
					if err != nil {
						return err
					}
					if strings.TrimSpace(current) != "" && strings.TrimSpace(current) != landed.Candidate.SHA {
						return fmt.Errorf("refuse restoring unexpected local row ref: %s", current)
					}
					if _, _, err := runCommand(ctx, defaultRunner, 0, 0, fixture.canonical, "git", "update-ref", "-d", "refs/heads/"+landed.Candidate.Branch); err != nil {
						return err
					}
				}
				if remoteRef {
					current, _, err := runCommand(ctx, defaultRunner, 0, 0, fixture.canonical, "git", "ls-remote", "--heads", "origin", "refs/heads/"+landed.Candidate.Branch)
					if err != nil {
						return err
					}
					if strings.TrimSpace(current) != "" {
						if strings.TrimSpace(current) != landed.Candidate.SHA+"\trefs/heads/"+landed.Candidate.Branch {
							return fmt.Errorf("refuse restoring unexpected remote row ref: %s", current)
						}
						if _, _, err := runCommand(ctx, defaultRunner, 0, 0, fixture.canonical, "git", "push", "origin", ":refs/heads/"+landed.Candidate.Branch); err != nil {
							return err
						}
					}
				}
				if err := os.WriteFile(landed.ReceiptPath, receiptBytes, receiptInfo.Mode().Perm()); err != nil {
					return err
				}
				if err := os.Chmod(landed.ReceiptPath, receiptInfo.Mode().Perm()); err != nil {
					return err
				}
				if err := verifyBaseline(ctx); err != nil {
					return err
				}
				restored = true
				return nil
			}
			t.Cleanup(func() {
				if restored {
					return
				}
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cleanupCancel()
				if err := restore(cleanupCtx); err != nil {
					t.Errorf("restore owned native baseline: %v", err)
				}
			})
			// Every original row recipe and refusal predicate below stays intact.
			test.breakEvidence(t, fixture, landed, claims)
			failed, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
				ProjectsRoot: fixture.githubDir, Receipt: landed.ReceiptPath, Cleanup: true, Route: WorktreeMergeRouteAuto,
				Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
			})
			unaffectedErr := verifyAuthority(ctx, affected, localRef, remoteRef)
			if restoreErr := restore(ctx); restoreErr != nil {
				t.Fatalf("restore shared native fixture: %v", restoreErr)
			}
			if unaffectedErr != nil {
				t.Fatalf("refusal changed unaffected authority before restoration: %v", unaffectedErr)
			}
			if err == nil || !strings.Contains(err.Error(), test.want) || failed.Status == WorktreeMergeComplete {
				t.Fatalf("terminal cleanup refusal = %+v err=%v, want %q", failed, err, test.want)
			}
		})
		if !ok || !restored {
			t.Fatal("stop shared native baseline after failed row or incomplete restoration")
		}
	}
}

func landedTerminalCleanupFixture(t *testing.T) (engineFixture, worktrees.CreateResult, WorktreeMergeReceipt, map[string]string) {
	t.Helper()
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "terminal-cleanup-source", "feature/terminal-cleanup", "source.txt", "source\n")
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
	if err != nil || landed.Status != WorktreeMergeLanded {
		t.Fatalf("land terminal cleanup fixture = %+v err=%v", landed, err)
	}
	claims := map[string]string{}
	for _, expectation := range []struct {
		task, worktree string
	}{{landed.Candidate.Task, landed.Candidate.Worktree}, {landed.Sources[0].Task, landed.Sources[0].Worktree}} {
		view, viewErr := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: expectation.worktree})
		if viewErr != nil || view.Claim == nil {
			t.Fatalf("load %s Work Log claim: %+v err=%v", expectation.task, view, viewErr)
		}
		claims[expectation.task] = view.Claim.ClaimPath
	}
	return fixture, source, landed, claims
}

func externallyTerminalizeMergeCleanup(t *testing.T, fixture engineFixture, receipt *WorktreeMergeReceipt) {
	t.Helper()
	for _, task := range sortedUniqueMergeTasks(*receipt) {
		externallyTerminalizeTask(t, fixture, receipt, task)
	}
}

func externallyTerminalizeTask(t *testing.T, fixture engineFixture, receipt *WorktreeMergeReceipt, task string) {
	t.Helper()
	outcome, err := worktrees.Cleanup(context.Background(), worktrees.CleanupOptions{
		ProjectsRoot: fixture.githubDir, Task: task, Base: receipt.Target, ExactRepository: receipt.Repository,
		AbsorbedBy: receipt.LandingSHA, MergeReceiptProofs: worktreeMergeCleanupProofs(*receipt, task),
		Apply: true, DeleteRemote: true, OlderThan: 0, Workers: 1,
	})
	if err != nil {
		t.Fatalf("external cleanup task %s: %v", task, err)
	}
	if len(outcome.Results) != 1 || !outcome.Results[0].Applied {
		t.Fatalf("external cleanup task %s = %+v", task, outcome)
	}
}

func terminalWorkLogPath(claimPath string) string {
	return filepath.Join(filepath.Dir(filepath.Dir(claimPath)), "terminals", filepath.Base(claimPath))
}

func sealedTerminalOutboxPath(claimPath string) string {
	claimID := strings.TrimSuffix(filepath.Base(claimPath), ".json")
	run := filepath.Base(filepath.Dir(filepath.Dir(claimPath)))
	taskDir := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(claimPath))))
	return filepath.Join(taskDir, "outbox", run+"-"+claimID+"-sealed.json")
}

func terminalWorkLogBytes(t *testing.T, claims map[string]string) map[string][]byte {
	t.Helper()
	bytes := make(map[string][]byte, len(claims))
	for task, claimPath := range claims {
		contents, err := os.ReadFile(terminalWorkLogPath(claimPath))
		if err != nil {
			t.Fatal(err)
		}
		bytes[task] = contents
	}
	return bytes
}

func assertTerminalWorkLogBytes(t *testing.T, claims map[string]string, want map[string][]byte) {
	t.Helper()
	for task, claimPath := range claims {
		got, err := os.ReadFile(terminalWorkLogPath(claimPath))
		if err != nil || string(got) != string(want[task]) {
			t.Fatalf("immutable terminal %s changed: err=%v", task, err)
		}
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

// TestConflictCandidateAdvanceNeedsValidationToleratesTwoChainedUpdates
// proves red-team finding M-B: the earlier M2 fix only tolerated a SINGLE
// recorded server-side update-branch advance between a conflict-candidate
// acknowledgement's snapshot and the receipt's current target/candidate —
// a straight equality check against ack.AdvancedCandidateSHA, or a single
// TargetRefreshes hop, still failed after a SECOND update landed on top of
// the first. worktreeMergeTargetAdvanceRecorded/worktreeMergeCandidateAdvanceRecorded
// must walk the whole TargetRefreshes chain, however many hops it takes.
func TestConflictCandidateAdvanceNeedsValidationToleratesTwoChainedUpdates(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "mb-two-updates-source", "feature/mb-two-updates", "mb-two-updates.txt", "mb\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Take the conflict-candidate acknowledgement snapshot at the receipt's
	// state right after a conflict resolution was recorded — the ack's
	// AdvancedCandidateSHA is a manually resolved candidate distinct from
	// OriginalCandidate.SHA (readConflictCandidateAdvance's own immutable-
	// identity check requires this: the whole point of the record is that
	// the candidate advanced). The receipt itself is then at that same
	// resolved candidate, exactly as advanceResolvedConflictWorktreeMergeCandidate
	// would have left it, BEFORE either update-branch advance this test
	// chains on top.
	resolvedCandidateSHA := receipt.Candidate.SHA + "-resolved"
	receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	ackPath := conflictCandidateAdvancePath(receipt.ReceiptPath)
	ack := WorktreeMergeConflictCandidateAdvance{
		SchemaVersion: worktreeMergeConflictCandidateAdvanceSchemaVersion, Status: "conflict_candidate_advanced",
		ReceiptPath: receipt.ReceiptPath, AcknowledgementPath: ackPath, ReceiptSHA256: receiptHash,
		ReceiptID: receipt.ID, Lane: receipt.Lane, Repository: receipt.Repository, Target: receipt.Target,
		ReceiptTargetSHA: receipt.TargetSHA, CurrentTargetSHA: receipt.TargetSHA, OriginalCandidate: receipt.Candidate,
		AdvancedCandidateSHA: resolvedCandidateSHA, ClaimBaseSHA: "claim-base",
		Sources: append([]WorktreeMergeSource(nil), receipt.Sources...), RecordedAt: time.Now().UTC(),
	}
	ack.ID = conflictCandidateAdvanceID(ack)
	if err := persistConflictCandidateAdvance(ackPath, ack); err != nil {
		t.Fatal(err)
	}
	receipt.Candidate.SHA = resolvedCandidateSHA

	// Chain TWO recorded server-side update-branch advances onto the
	// receipt, each hop's "previous" being the prior hop's "new" — exactly
	// what two successive update-branch merges during one PR-land wait
	// record via TargetRefreshes.
	firstNewTarget := receipt.TargetSHA + "-t1"
	firstNewCandidate := receipt.Candidate.SHA + "-c1"
	secondNewTarget := receipt.TargetSHA + "-t2"
	secondNewCandidate := receipt.Candidate.SHA + "-c2"
	receipt.TargetRefreshes = append(receipt.TargetRefreshes,
		WorktreeMergeTargetRefresh{
			RecordedAt: time.Now().UTC(), PreviousTargetSHA: receipt.TargetSHA, NewTargetSHA: firstNewTarget,
			PreviousCandidateSHA: receipt.Candidate.SHA, NewCandidateSHA: firstNewCandidate,
		},
		WorktreeMergeTargetRefresh{
			RecordedAt: time.Now().UTC(), PreviousTargetSHA: firstNewTarget, NewTargetSHA: secondNewTarget,
			PreviousCandidateSHA: firstNewCandidate, NewCandidateSHA: secondNewCandidate,
		},
	)
	receipt.TargetSHA = secondNewTarget
	receipt.Candidate.SHA = secondNewCandidate
	// Isolate the target/candidate matching logic under test from the
	// unrelated "has a matching successful validation" branch this
	// function also takes when receipt.Status is WorktreeMergePrepared:
	// WorktreeMergePreparing makes it return the chain-match verdict
	// directly instead.
	receipt.Status = WorktreeMergePreparing

	needsValidation, err := conflictCandidateAdvanceNeedsValidation(receipt)
	if err != nil {
		t.Fatalf("two chained recorded update-branch advances were not tolerated: %v", err)
	}
	if !needsValidation {
		t.Fatalf("a still-preparing receipt with a matching acknowledgement should still need validation")
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

// TestActiveMergeLaneClaimReportsSourceBranchOfAnActiveReceipt covers the
// lesson merger-lane-branch-race: a main agent deciding whether to push to a
// candidate branch (a revert included) has no way to see that a merger lane
// has already claimed it for an in-flight batch. ActiveMergeLaneClaim answers
// "is anyone draining this?" before the push instead of after the merge.
func TestActiveMergeLaneClaimReportsSourceBranchOfAnActiveReceipt(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "claim-source", "feature/claimed", "claimed.txt", "a\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	claim, err := ActiveMergeLaneClaim(fixture.githubDir, fixture.repository.Slug, "feature/claimed")
	if err != nil {
		t.Fatal(err)
	}
	if claim == nil {
		t.Fatalf("expected an active merger-lane claim for %s, got none", "feature/claimed")
	}
	if claim.Lane != receipt.Lane || claim.Target != "main" || claim.ReceiptPath != receipt.ReceiptPath {
		t.Fatalf("claim = %+v, want lane=%s target=main receipt=%s", claim, receipt.Lane, receipt.ReceiptPath)
	}

	unclaimed, err := ActiveMergeLaneClaim(fixture.githubDir, fixture.repository.Slug, "feature/never-touched")
	if err != nil {
		t.Fatal(err)
	}
	if unclaimed != nil {
		t.Fatalf("expected no claim for an unrelated branch, got %+v", unclaimed)
	}
}

// TestActiveMergeLaneClaimIgnoresACompletedReceipt proves a landed-and-cleaned
// lane no longer reports the branch as claimed, so the check does not go on
// warning about work that already shipped.
func TestActiveMergeLaneClaimIgnoresACompletedReceipt(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "complete-source", "feature/completed", "completed.txt", "a\n")
	prepared, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_TARGET_SHA", prepared.Candidate.SHA)
	landed, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: prepared.ReceiptPath, Route: WorktreeMergeRouteAuto, Cleanup: true,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if landed.Status != WorktreeMergeComplete {
		t.Fatalf("expected a complete landing, got %+v", landed)
	}

	claim, err := ActiveMergeLaneClaim(fixture.githubDir, fixture.repository.Slug, "feature/completed")
	if err != nil {
		t.Fatal(err)
	}
	if claim != nil {
		t.Fatalf("expected no claim once the lane landed, got %+v", claim)
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
		CheckTimeout: 2 * time.Minute, ShardAttemptTimeout: time.Minute,
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
		ShardAttemptTimeout: 3 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.ID != first.ID || refreshed.ReceiptPath != first.ReceiptPath || refreshed.Candidate.Worktree != first.Candidate.Worktree {
		t.Fatalf("source advance created a competing candidate: first=%+v refreshed=%+v", first, refreshed)
	}
	check, shard := receiptWorktreeMergeValidationTimeouts(refreshed)
	if check != 2*time.Minute || shard != 3*time.Minute {
		t.Fatalf("refresh ignored explicit timeout or lost omitted limit: check=%s shard=%s", check, shard)
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

func TestPrepareWorktreeMergeRebatchesExactOpenChecksFailedReceipt(t *testing.T) {
	fixture := newEngineFixture(t)
	firstSource := createMergeSource(t, fixture, "checks-rebatch-first", "feature/checks-rebatch-first", "first.txt", "first\n")
	secondSource := createMergeSource(t, fixture, "checks-rebatch-second", "feature/checks-rebatch-second", "second.txt", "second\n")
	first, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{firstSource.WorktreeDir, secondSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, first.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/"+first.Candidate.Branch)
	first.Phase = WorktreeMergePhaseLand
	first.Status = WorktreeMergeChecksFailed
	first.PullRequest = "41"
	first.PublishedCandidateSHA = first.Candidate.SHA
	first.Failure = "strict required-check fence unavailable"
	if err := persistWorktreeMergeReceipt(first); err != nil {
		t.Fatal(err)
	}
	originalReceipt, err := os.ReadFile(first.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	thirdSource := createMergeSource(t, fixture, "checks-rebatch-third", "feature/checks-rebatch-third", "third.txt", "third\n")
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_CANDIDATE_SHA", first.Candidate.SHA)

	replacement, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{firstSource.WorktreeDir, secondSource.WorktreeDir, thirdSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", RebatchReceipt: first.ReceiptPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replacement.RebatchOf != first.ReceiptPath || len(replacement.RebatchedCandidates) != 1 || replacement.RebatchedCandidates[0] != first.Candidate {
		t.Fatalf("replacement receipt = %+v", replacement)
	}
	if current, err := os.ReadFile(first.ReceiptPath); err != nil || !bytes.Equal(current, originalReceipt) {
		t.Fatalf("checks-failed receipt changed: err=%v", err)
	}
}

// TestPrepareWorktreeMergeRebatchClosesSupersededPullRequest proves red-team
// finding M6: rebatching a published-unlanded candidate (one whose PR-land
// engine wait has already very likely armed GitHub auto-merge) must close
// that candidate's own pull request, so its armed auto-merge cannot land it
// alongside the replacement. Disarm-on-red stays forbidden — this is
// retirement of a superseded candidate, not a reaction to a red check.
func TestPrepareWorktreeMergeRebatchClosesSupersededPullRequest(t *testing.T) {
	fixture := newEngineFixture(t)
	firstSource := createMergeSource(t, fixture, "close-pr-rebatch-first", "feature/close-pr-rebatch-first", "first.txt", "first\n")
	secondSource := createMergeSource(t, fixture, "close-pr-rebatch-second", "feature/close-pr-rebatch-second", "second.txt", "second\n")
	first, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{firstSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, first.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/"+first.Candidate.Branch)
	// Published-unlanded with an armed-auto-merge-shaped status: the PR-land
	// engine arms auto-merge before it ever waits for checks, so reaching
	// checks_failed already implies the pull request's auto-merge is armed.
	first.Phase = WorktreeMergePhaseLand
	first.Status = WorktreeMergeChecksFailed
	first.PullRequest = "41"
	first.PublishedCandidateSHA = first.Candidate.SHA
	first.Failure = "strict required-check fence unavailable"
	first.AutoMergeArmed = true
	if err := persistWorktreeMergeReceipt(first); err != nil {
		t.Fatal(err)
	}
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_CANDIDATE_SHA", first.Candidate.SHA)
	closedLog := filepath.Join(t.TempDir(), "closed-pr.log")
	t.Setenv("WB_TEST_CLOSED_PR_LOG", closedLog)

	replacement, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{firstSource.WorktreeDir, secondSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", RebatchReceipt: first.ReceiptPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replacement.SupersededPullRequest != "41" {
		t.Fatalf("replacement receipt did not record the closed superseded pull request: %+v", replacement)
	}
	closedCalls, readErr := os.ReadFile(closedLog)
	if readErr != nil || !strings.Contains(string(closedCalls), "pulls/41") {
		t.Fatalf("superseded pull request was not closed: err=%v calls=%q", readErr, string(closedCalls))
	}
	rebatch, err := readPreparedWorktreeMergeRebatch(rebatchPath(first.ReceiptPath), first)
	if err != nil {
		t.Fatal(err)
	}
	if rebatch.ClosedPullRequest != "41" {
		t.Fatalf("rebatch acknowledgement did not record the closed pull request: %+v", rebatch)
	}

	// Resuming (recovering) the same rebatch must not attempt to close the
	// pull request a second time.
	if err := os.Remove(closedLog); err != nil {
		t.Fatal(err)
	}
	if err := ensurePreparedWorktreeMergeRebatch(context.Background(), &WorktreeMergePreparedRebatch{ReceiptPath: first.ReceiptPath}, &replacement, time.Sleep); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(closedLog); !os.IsNotExist(statErr) {
		t.Fatalf("re-ensuring an already-closed rebatch re-invoked the close call: statErr=%v", statErr)
	}
}

// TestPrepareWorktreeMergeRebatchRefusesWhenClosingTheSupersededPullRequestFails
// proves the other half of M6: the rebatch must refuse — never silently
// proceed with a live stale candidate — when closing its pull request fails.
func TestPrepareWorktreeMergeRebatchRefusesWhenClosingTheSupersededPullRequestFails(t *testing.T) {
	fixture := newEngineFixture(t)
	firstSource := createMergeSource(t, fixture, "close-pr-fail-first", "feature/close-pr-fail-first", "first.txt", "first\n")
	secondSource := createMergeSource(t, fixture, "close-pr-fail-second", "feature/close-pr-fail-second", "second.txt", "second\n")
	first, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{firstSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, first.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/"+first.Candidate.Branch)
	first.Phase = WorktreeMergePhaseLand
	first.Status = WorktreeMergeChecksFailed
	first.PullRequest = "41"
	first.PublishedCandidateSHA = first.Candidate.SHA
	first.Failure = "strict required-check fence unavailable"
	if err := persistWorktreeMergeReceipt(first); err != nil {
		t.Fatal(err)
	}
	originalReceipt, err := os.ReadFile(first.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_CANDIDATE_SHA", first.Candidate.SHA)
	t.Setenv("WB_TEST_CLOSE_PR_FAIL", "1")

	_, err = PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{firstSource.WorktreeDir, secondSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", RebatchReceipt: first.ReceiptPath,
	})
	if err == nil || !strings.Contains(err.Error(), "close superseded pull request") {
		t.Fatalf("rebatch with a failing pull-request closure = %v, want a refusal naming the closure", err)
	}
	if current, readErr := os.ReadFile(first.ReceiptPath); readErr != nil || !bytes.Equal(current, originalReceipt) {
		t.Fatalf("original receipt changed despite refused rebatch: err=%v", readErr)
	}
	if _, statErr := os.Stat(rebatchPath(first.ReceiptPath)); !os.IsNotExist(statErr) {
		t.Fatal("a refused rebatch left behind an acknowledgement")
	}
}

// TestPrepareWorktreeMergeRebatchRefusesWhenSupersededPullRequestWasMergedBeforeClose
// proves red-team finding M-C: GitHub's close call on a pull request that
// was merged before it reached the API succeeds without error — a merged
// pull request reports "closed" too, and PATCH state=closed on it is a
// harmless no-op, not a refusal. Closing must re-read the pull request and
// require both closed AND not merged; an already-merged superseded
// candidate must refuse the rebatch, naming the merged pull request,
// never proceed past an already-landed change.
func TestPrepareWorktreeMergeRebatchRefusesWhenSupersededPullRequestWasMergedBeforeClose(t *testing.T) {
	fixture := newEngineFixture(t)
	firstSource := createMergeSource(t, fixture, "merged-before-close-first", "feature/merged-before-close-first", "first.txt", "first\n")
	secondSource := createMergeSource(t, fixture, "merged-before-close-second", "feature/merged-before-close-second", "second.txt", "second\n")
	first, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{firstSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, first.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/"+first.Candidate.Branch)
	first.Phase = WorktreeMergePhaseLand
	first.Status = WorktreeMergeChecksFailed
	first.PullRequest = "41"
	first.PublishedCandidateSHA = first.Candidate.SHA
	first.Failure = "strict required-check fence unavailable"
	first.AutoMergeArmed = true
	if err := persistWorktreeMergeReceipt(first); err != nil {
		t.Fatal(err)
	}
	originalReceipt, err := os.ReadFile(first.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_CANDIDATE_SHA", first.Candidate.SHA)
	closedLog := filepath.Join(t.TempDir(), "closed-pr.log")
	t.Setenv("WB_TEST_CLOSED_PR_LOG", closedLog)
	// The pull request reads open and unmerged right up until the close
	// call itself: GitHub merges it in the window between the rebatch's
	// earlier checks and the close reaching the API. The close itself
	// still succeeds — exactly the harmless no-op GitHub reports for a
	// pull request that was merged before the close reached it — but the
	// post-close re-read this closes (M-C) must see it merged.
	t.Setenv("WB_TEST_MERGE_ON_CLOSE", "1")

	_, err = PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{firstSource.WorktreeDir, secondSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", RebatchReceipt: first.ReceiptPath,
	})
	if err == nil || !strings.Contains(err.Error(), "41") || !strings.Contains(err.Error(), "merged") {
		t.Fatalf("rebatch over a pull request merged before its close = %v, want a refusal naming pull request 41 as merged", err)
	}
	closedCalls, readErr := os.ReadFile(closedLog)
	if readErr != nil || !strings.Contains(string(closedCalls), "pulls/41") {
		t.Fatalf("close was never attempted before the post-close verification: err=%v calls=%q", readErr, string(closedCalls))
	}
	if current, readErr := os.ReadFile(first.ReceiptPath); readErr != nil || !bytes.Equal(current, originalReceipt) {
		t.Fatalf("original receipt changed despite a refused rebatch: err=%v", readErr)
	}
	if _, statErr := os.Stat(rebatchPath(first.ReceiptPath)); !os.IsNotExist(statErr) {
		t.Fatal("a refused rebatch left behind an acknowledgement")
	}
}

func TestPrepareWorktreeMergeRebatchesExactOpenPublishedPendingReceipt(t *testing.T) {
	fixture := newEngineFixture(t)
	firstSource := createMergeSource(t, fixture, "pending-rebatch-first", "feature/pending-rebatch-first", "first.txt", "first\n")
	secondSource := createMergeSource(t, fixture, "pending-rebatch-second", "feature/pending-rebatch-second", "second.txt", "second\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{firstSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/"+receipt.Candidate.Branch)
	receipt.Phase, receipt.Status, receipt.PullRequest, receipt.PublishedCandidateSHA = WorktreeMergePhaseLand, WorktreeMergePublished, "41", receipt.Candidate.SHA
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	originalReceipt, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_CANDIDATE_SHA", receipt.Candidate.SHA)
	replacement, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{firstSource.WorktreeDir, secondSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", RebatchReceipt: receipt.ReceiptPath})
	if err != nil || replacement.RebatchOf != receipt.ReceiptPath {
		t.Fatalf("published pending rebatch = %+v err=%v", replacement, err)
	}
	if current, readErr := os.ReadFile(receipt.ReceiptPath); readErr != nil || !bytes.Equal(current, originalReceipt) {
		t.Fatalf("published pending receipt changed: err=%v", readErr)
	}
}

func TestPrepareWorktreeMergeRefusesMalformedPreparedReceipt(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*WorktreeMergeReceipt)
	}{
		{name: "wrong phase", mutate: func(receipt *WorktreeMergeReceipt) { receipt.Phase = WorktreeMergePhaseLand }},
		{name: "published fields", mutate: func(receipt *WorktreeMergeReceipt) {
			receipt.PullRequest = "41"
			receipt.PublishedCandidateSHA = receipt.Candidate.SHA
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newEngineFixture(t)
			firstSource := createMergeSource(t, fixture, "malformed-prepared-first", "feature/malformed-prepared-first", "first.txt", "first\n")
			secondSource := createMergeSource(t, fixture, "malformed-prepared-second", "feature/malformed-prepared-second", "second.txt", "second\n")
			receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{firstSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&receipt)
			if err := persistWorktreeMergeReceipt(receipt); err != nil {
				t.Fatal(err)
			}
			_, err = PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{firstSource.WorktreeDir, secondSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", RebatchReceipt: receipt.ReceiptPath})
			if err == nil {
				t.Fatal("malformed prepared receipt was accepted")
			}
		})
	}
}

func TestActiveLaneReceiptSkipsUnusablePreparedRebatchSidecar(t *testing.T) {
	fixture := newEngineFixture(t)
	staleSource := createMergeSource(t, fixture, "stale-sidecar-source", "feature/stale-sidecar", "stale.txt", "stale\n")
	stale, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{staleSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	sidecar := rebatchPath(stale.ReceiptPath)
	if err := os.WriteFile(sidecar, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := hasPreparedWorktreeMergeRebatch(stale); err == nil {
		t.Fatal("planted sidecar must fail authentication")
	}
	if _, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: stale.ReceiptPath}); err == nil ||
		(!strings.Contains(err.Error(), "invalid immutable identity") && !strings.Contains(err.Error(), "decode prepared rebatch")) {
		t.Fatalf("land of the receipt that owns the sidecar = %v, want sidecar authentication failure", err)
	}
	otherSource := createMergeSource(t, fixture, "other-lane-source", "feature/other-lane", "other.txt", "other\n")
	prepared, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{otherSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatalf("prepare for a different source on the same lane: %v", err)
	}
	if prepared.ReceiptPath == stale.ReceiptPath {
		t.Fatalf("new prepare reused the stale receipt %s", stale.ReceiptPath)
	}
	active, err := activeWorktreeMergeLaneReceipt(context.Background(), fixture.githubDir, filepath.Dir(stale.ReceiptPath), stale.Lane)
	if err != nil {
		t.Fatalf("lane scan aborted on the unusable sidecar: %v", err)
	}
	if active == nil || active.ReceiptPath != prepared.ReceiptPath {
		t.Fatalf("active lane after skipping unusable sidecar = %+v", active)
	}
}

// TestActiveLaneReceiptSkipsValidPreparedRebatchSidecar pins
// activeWorktreeMergeLaneReceipt's `continue` under `if rebatched` (#748):
// a lane receipt carrying a genuinely valid (not merely unusable, as above)
// `.prepared.rebatched.ack.json` sidecar must be skipped by the lane scan
// on every run, not only when directory-listing order happens to reach it
// before its replacement. The replacement receipt's filename is derived
// from a hash of its (superseded) source set, so it sorts unpredictably
// relative to the original's -- #748's own coverage flake was exactly
// that: on a run where the replacement happened to list first, the scan
// returned it as active without ever visiting (and thus never covering
// the rebatched-skip continue on) the original. Passing the replacement's
// own path as `except` removes that non-determinism: the scan then has
// only the original to consider, so it must reach and cover line 4550
// every time, and the assertion on hasPreparedWorktreeMergeRebatch
// isolates the rebatched-sidecar reason from every other reason
// activeWorktreeMergeLaneReceipt might otherwise skip a receipt for.
func TestActiveLaneReceiptSkipsValidPreparedRebatchSidecar(t *testing.T) {
	mergeLaneRebatchJourney(t, nil, nil)
}

func TestPrepareWorktreeMergeRebatchRefusesSourceRemovalTargetDriftAndDirtyEvidence(t *testing.T) {
	newPrepared := func(t *testing.T) (engineFixture, worktrees.CreateResult, WorktreeMergeReceipt) {
		t.Helper()
		fixture := newEngineFixture(t)
		source := createMergeSource(t, fixture, "rebatch-negative", "feature/rebatch-negative", "source.txt", "source\n")
		receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
		if err != nil {
			t.Fatal(err)
		}
		return fixture, source, receipt
	}
	t.Run("source removal", func(t *testing.T) {
		fixture, source, receipt := newPrepared(t)
		_, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", RebatchReceipt: receipt.ReceiptPath})
		if err == nil || !strings.Contains(err.Error(), "must add") {
			t.Fatalf("source removal error = %v", err)
		}
	})
	t.Run("target drift", func(t *testing.T) {
		fixture, source, receipt := newPrepared(t)
		second := createMergeSource(t, fixture, "rebatch-drift-extra", "feature/rebatch-drift-extra", "extra.txt", "extra\n")
		writeEngineFile(t, filepath.Join(fixture.canonical, "target.txt"), "target\n")
		runEngineGit(t, fixture.canonical, "add", "target.txt")
		runEngineGit(t, fixture.canonical, "commit", "-m", "test: drift rebatch target")
		runEngineGit(t, fixture.canonical, "push", "origin", "main")
		_, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir, second.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", RebatchReceipt: receipt.ReceiptPath})
		if err == nil || !strings.Contains(err.Error(), "target drift") {
			t.Fatalf("target drift error = %v", err)
		}
	})
	t.Run("non descendant replacement ref", func(t *testing.T) {
		fixture, _, receipt := newPrepared(t)
		extra := createMergeSource(t, fixture, "rebatch-non-descendant-extra", "feature/rebatch-non-descendant-extra", "extra.txt", "extra\n")
		extraSources, _, _, err := inspectWorktreeMergeSources(context.Background(), fixture.githubDir, []string{extra.WorktreeDir}, "main")
		if err != nil {
			t.Fatal(err)
		}
		nonDescendant := receipt.Sources[0]
		nonDescendant.SHA = receipt.TargetSHA
		_, err = validatePreparedWorktreeMergeRebatch(context.Background(), fixture.githubDir, receipt.ReceiptPath, "acme/app", "main", append([]WorktreeMergeSource{nonDescendant}, extraSources...))
		if err == nil || !strings.Contains(err.Error(), "not a descendant") {
			t.Fatalf("non-descendant replacement error = %v", err)
		}
	})
	t.Run("duplicate source ref", func(t *testing.T) {
		fixture, _, receipt := newPrepared(t)
		extra := createMergeSource(t, fixture, "rebatch-duplicate-extra", "feature/rebatch-duplicate-extra", "extra.txt", "extra\n")
		extraSources, _, _, err := inspectWorktreeMergeSources(context.Background(), fixture.githubDir, []string{extra.WorktreeDir}, "main")
		if err != nil {
			t.Fatal(err)
		}
		duplicate := receipt.Sources[0]
		_, err = validatePreparedWorktreeMergeRebatch(context.Background(), fixture.githubDir, receipt.ReceiptPath, "acme/app", "main", append(append([]WorktreeMergeSource{}, receipt.Sources[0], duplicate), extraSources...))
		if err == nil || !strings.Contains(err.Error(), "supplied more than once") {
			t.Fatalf("duplicate source ref error = %v", err)
		}
	})
	t.Run("dirty candidate", func(t *testing.T) {
		fixture, source, receipt := newPrepared(t)
		second := createMergeSource(t, fixture, "rebatch-dirty-extra", "feature/rebatch-dirty-extra", "extra.txt", "extra\n")
		writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "dirty.txt"), "dirty\n")
		_, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir, second.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", RebatchReceipt: receipt.ReceiptPath})
		if err == nil || !strings.Contains(err.Error(), "candidate is not clean") {
			t.Fatalf("dirty candidate error = %v", err)
		}
	})
	t.Run("dirty source", func(t *testing.T) {
		fixture, source, receipt := newPrepared(t)
		second := createMergeSource(t, fixture, "rebatch-dirty-source-extra", "feature/rebatch-dirty-source-extra", "extra.txt", "extra\n")
		writeEngineFile(t, filepath.Join(second.WorktreeDir, "dirty.txt"), "dirty\n")
		_, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir, second.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", RebatchReceipt: receipt.ReceiptPath})
		if err == nil || !strings.Contains(err.Error(), "worktree is dirty") {
			t.Fatalf("dirty source error = %v", err)
		}
	})
}

func TestPrepareWorktreeMergeRebatchRetryCompletesAcknowledgementAfterPostReceiptWriteFailure(t *testing.T) {
	fixture := newEngineFixture(t)
	firstSource := createMergeSource(t, fixture, "rebatch-retry-first", "feature/rebatch-retry-first", "first.txt", "first\n")
	first, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{firstSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err != nil {
		t.Fatal(err)
	}
	originalReceipt, err := os.ReadFile(first.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	secondSource := createMergeSource(t, fixture, "rebatch-retry-second", "feature/rebatch-retry-second", "second.txt", "second\n")
	previousPersist := persistPreparedWorktreeMergeRebatchForPrepare
	persistPreparedWorktreeMergeRebatchForPrepare = func(string, WorktreeMergePreparedRebatch) error { return os.ErrPermission }
	defer func() { persistPreparedWorktreeMergeRebatchForPrepare = previousPersist }()
	partial, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{firstSource.WorktreeDir, secondSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", RebatchReceipt: first.ReceiptPath,
	})
	if err == nil || partial.Status != WorktreeMergePrepared || partial.RebatchOf != first.ReceiptPath {
		t.Fatalf("post-receipt acknowledgement failure = receipt %+v err=%v", partial, err)
	}
	if _, statErr := os.Stat(rebatchPath(first.ReceiptPath)); !os.IsNotExist(statErr) {
		t.Fatalf("acknowledgement exists after injected write failure: %v", statErr)
	}
	if current, err := os.ReadFile(first.ReceiptPath); err != nil || !bytes.Equal(current, originalReceipt) {
		t.Fatalf("original receipt changed by failed rebatch acknowledgement: err=%v", err)
	}
	persistPreparedWorktreeMergeRebatchForPrepare = previousPersist
	recovered, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{firstSource.WorktreeDir, secondSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", RebatchReceipt: first.ReceiptPath,
	})
	if err != nil || recovered.ReceiptPath != partial.ReceiptPath || recovered.Candidate != partial.Candidate {
		t.Fatalf("rebatch acknowledgement recovery = receipt %+v err=%v", recovered, err)
	}
	if _, err := readPreparedWorktreeMergeRebatch(rebatchPath(first.ReceiptPath), first); err != nil {
		t.Fatalf("recovered acknowledgement = %v", err)
	}
	active, err := activeWorktreeMergeLaneReceipt(context.Background(), fixture.githubDir, filepath.Dir(first.ReceiptPath), first.Lane)
	if err != nil || active == nil || active.ReceiptPath != recovered.ReceiptPath {
		t.Fatalf("active lane after acknowledgement recovery = %+v err=%v", active, err)
	}
}

func TestResolveWorktreeMergeAutoRouteUsesDirectOnlyForAuthoritativelyUnprotectedTarget(t *testing.T) {
	for _, test := range []struct {
		name       string
		branchJSON string
		rulesJSON  string
		want       WorktreeMergeRoute
	}{
		{name: "unprotected", branchJSON: `{"protected":false,"protection":{}}`, rulesJSON: `[]`, want: WorktreeMergeRouteDirect},
		{name: "classic pull request", branchJSON: `{"protected":true,"protection":{"required_pull_request_reviews":{}}}`, rulesJSON: `[]`, want: WorktreeMergeRoutePullRequest},
		{name: "ruleset pull request", branchJSON: `{"protected":true,"protection":{}}`, rulesJSON: `[{"type":"pull_request","ruleset_id":7,"ruleset_source_type":"Repository","ruleset_source":"acme/app"}]`, want: WorktreeMergeRoutePullRequest},
		{name: "incomplete branch policy", branchJSON: `{}`, rulesJSON: `[]`, want: WorktreeMergeRoutePullRequest},
		{name: "incomplete rules policy", branchJSON: `{"protected":false,"protection":{}}`, rulesJSON: `{}`, want: WorktreeMergeRoutePullRequest},
		{name: "merge queue unsupported", branchJSON: `{"protected":true,"protection":{}}`, rulesJSON: `[{"type":"merge_queue","ruleset_id":9,"ruleset_source_type":"Repository","ruleset_source":"acme/app"}]`, want: WorktreeMergeRouteUnsupported},
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
		"  'api repos/acme/app/rules/branches/main?per_page=100 --include'|'api repos/acme/app/rules/branches/main?per_page=100') printf '%s\\n' \"$WB_TEST_RULES_JSON\" ;;\n" +
		"  *) echo \"unexpected gh command: $*\" >&2; exit 2 ;;\n" +
		"esac\n"
	if err := testenv.WriteExecutableFile(script, []byte(testfixture.WithEmptyActionsRuns(body)), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_BRANCH_JSON", branchJSON)
	t.Setenv("WB_TEST_RULES_JSON", rulesJSON)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
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
  'api repos/acme/app/rules/branches/main?per_page=100 --include'|'api repos/acme/app/rules/branches/main?per_page=100') printf '%s\n' '[]' ;;
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
  'api --paginate repos/acme/app/commits/'*'/pulls'|'api repos/acme/app/commits/'*'/pulls?per_page=100 --include') printf '%s\n' '[]' ;;
  'api repos/acme/app/pulls/'*' --include'|'api repos/acme/app/pulls/'*)
    state="${WB_TEST_PR_STATE:-open}"
    if [ -f "${WB_TEST_PR_STATE_FILE:-/nonexistent}" ]; then state="$(cat "$WB_TEST_PR_STATE_FILE")"; fi
    merged="${WB_TEST_PR_MERGED:-false}"
    if [ -f "${WB_TEST_PR_MERGED_FILE:-/nonexistent}" ]; then merged="$(cat "$WB_TEST_PR_MERGED_FILE")"; fi
    # WB_TEST_VERIFY_READ_FAIL_ONCE simulates one transient GitHub read
    # failure on M5's post-close verification re-read only (state is
    # already "closed" by the time that read happens; the pre-close
    # validatePublishedUnlandedRebatch read still sees "open" and must not
    # be disturbed). The marker file is consumed so only the first such
    # read fails; the in-process retry's next attempt succeeds normally.
    if [ -n "${WB_TEST_VERIFY_READ_FAIL_ONCE:-}" ] && [ "$state" = "closed" ] && [ -f "$WB_TEST_VERIFY_READ_FAIL_ONCE" ]; then
      rm -f "$WB_TEST_VERIFY_READ_FAIL_ONCE"
      echo "gh: connection reset by peer" >&2
      exit 1
    fi
    printf '{"number":41,"state":"%s","merged":%s,"draft":false,"title":"candidate","head":{"ref":"candidate","sha":"%s","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}\n' "$state" "$merged" "$WB_TEST_CANDIDATE_SHA" ;;
  'api --method PATCH repos/acme/app/pulls/'*' -f state=closed')
    if [ -n "${WB_TEST_CLOSE_PR_FAIL:-}" ]; then
      echo '{"message":"Validation Failed"}' >&2
      exit 1
    fi
    printf '%s\n' "$*" >>"${WB_TEST_CLOSED_PR_LOG:-/dev/null}"
    # Closing genuinely flips the pull request's observed state for every
    # later read in this test - a re-read that still saw "open" after a
    # successful close would be a fixture lie, not a faithful GitHub
    # simulation - UNLESS the test itself already pinned WB_TEST_PR_STATE
    # to simulate a pull request that was merged (and thus already
    # reports "closed") before this close call ever reached it.
    if [ -z "${WB_TEST_PR_STATE:-}" ]; then
      printf 'closed' >"${WB_TEST_PR_STATE_FILE:?WB_TEST_PR_STATE_FILE must be set}"
    fi
    # WB_TEST_MERGE_ON_CLOSE simulates GitHub merging the pull request in
    # the window between this rebatch's earlier open-and-unmerged read and
    # this close call reaching the API: the close itself still succeeds
    # (a merged pull request reports "closed" too), but it never actually
    # retired an open candidate - the post-close verification this closes
    # (red-team finding M-C) must catch it on its OWN re-read, not here.
    if [ -n "${WB_TEST_MERGE_ON_CLOSE:-}" ]; then
      printf 'true' >"${WB_TEST_PR_MERGED_FILE:?WB_TEST_PR_MERGED_FILE must be set}"
    fi
    printf '{"number":41,"state":"closed"}\n' ;;
  *'/check-runs?per_page=100 --include'|*'/check-runs?per_page=100') printf '%s\n' '{"total_count":0,"check_runs":[]}' ;;
  *'/status?per_page=100 --include'|*'/status?per_page=100') printf '%s\n' '{"total_count":0,"statuses":[]}' ;;
  *) echo "unexpected gh command: $*" >&2; exit 2 ;;
esac
`
	if err := testenv.WriteExecutableFile(script, []byte(testfixture.WithEmptyActionsRuns(body)), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("WB_TEST_PR_STATE_FILE", filepath.Join(t.TempDir(), "pr-state"))
	t.Setenv("WB_TEST_PR_MERGED_FILE", filepath.Join(t.TempDir(), "pr-merged"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func installWorktreeMergePublishOnlyPRGH(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := filepath.Join(bin, "gh")
	body := `#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$WB_TEST_GH_LOG"
state_file="${WB_TEST_PR_STATE_FILE:-$WB_TEST_GH_LOG.state}"
state="OPEN"
if [ -f "$state_file" ]; then state="$(cat "$state_file")"; fi
case "$*" in
  'api repos/acme/app/branches/main --include'|'api repos/acme/app/branches/main') printf '%s\n' '{"protected":true,"protection":{"required_pull_request_reviews":{}}}' ;;
  'api repos/acme/app/rules/branches/main?per_page=100 --include'|'api repos/acme/app/rules/branches/main?per_page=100') printf '%s\n' '[]' ;;
  'pr list --head '*' --base main --state open --json url --jq .[0].url') printf '\n' ;;
  'pr create --base main --head '* ) printf '%s\n' 'https://example.test/acme/app/pull/41' ;;
  'pr view https://example.test/acme/app/pull/41 --repo acme/app --json state,headRefOid,baseRefName')
    printf '{"state":"%s","headRefOid":"%s","baseRefName":"main"}\n' "$state" "$WB_TEST_CANDIDATE_SHA" ;;
  'pr view https://example.test/acme/app/pull/41 --repo acme/app --json headRefOid,baseRefName')
    printf '{"headRefOid":"%s","baseRefName":"main"}\n' "$WB_TEST_CANDIDATE_SHA" ;;
  'pr view https://example.test/acme/app/pull/41 --repo acme/app --json state,mergedAt,mergeCommit,headRefOid,baseRefName')
    if [ "$state" = MERGED ]; then
      printf '{"state":"MERGED","mergedAt":"2026-09-01T00:00:00Z","headRefOid":"%s","baseRefName":"main","mergeCommit":{"oid":"%s"}}\n' "$WB_TEST_CANDIDATE_SHA" "$WB_TEST_CANDIDATE_SHA"
    else
      printf '{"state":"OPEN","mergedAt":"","headRefOid":"%s","baseRefName":"main","mergeCommit":{"oid":""}}\n' "$WB_TEST_CANDIDATE_SHA"
    fi ;;
  'api repos/acme/app --include'|'api repos/acme/app') printf '%s\n' '{"allow_merge_commit":true,"allow_squash_merge":false,"allow_rebase_merge":false}' ;;
  'pr merge https://example.test/acme/app/pull/41 --match-head-commit '*' --merge')
    git --git-dir="$WB_TEST_REMOTE" update-ref refs/heads/main "$WB_TEST_CANDIDATE_SHA"
    printf 'MERGED\n' >"$state_file" ;;
  'api repos/acme/app/pulls/'*' --include'|'api repos/acme/app/pulls/'*)
    printf '{"number":41,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"%s","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}\n' "$WB_TEST_CANDIDATE_SHA" ;;
  *'/check-runs?per_page=100 --include'|*'/check-runs?per_page=100') printf '%s\n' '{"total_count":0,"check_runs":[]}' ;;
  *'/status?per_page=100 --include'|*'/status?per_page=100') printf '%s\n' '{"total_count":0,"statuses":[]}' ;;
  'api repos/acme/app/git/ref/heads/main --include'|'api repos/acme/app/git/ref/heads/main') printf '{"object":{"sha":"%s"}}\n' "$(git --git-dir="$WB_TEST_REMOTE" rev-parse refs/heads/main)" ;;
  'api repos/acme/app/compare/'*'...'* )
    pair="${2#*compare/}"
    base="${pair%%...*}"
    candidate="${pair#*...}"
    merge_base="$(git --git-dir="$WB_TEST_REMOTE" merge-base "$base" "$candidate")"
    if git --git-dir="$WB_TEST_REMOTE" merge-base --is-ancestor "$base" "$candidate"; then status="ahead"; else status="diverged"; fi
    printf '{"status":"%s","base_commit":{"sha":"%s"},"merge_base_commit":{"sha":"%s"}}\n' "$status" "$base" "$merge_base" ;;
  'api --paginate repos/acme/app/commits/'*'/pulls') printf '%s\n' "${WB_TEST_EXISTING_PR_JSON:-[]}" ;;
  *) echo "unexpected gh command: $*" >&2; exit 2 ;;
esac
`
	if err := testenv.WriteExecutableFile(script, []byte(testfixture.WithEmptyActionsRuns(body)), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}
