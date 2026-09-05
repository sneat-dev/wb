package orchestrate

import (
	"bytes"
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

func TestPrepareWorktreeMergeRefusesValidationFailedReceiptAfterSourceAdvanceWithoutMutation(t *testing.T) {
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
	specFailing := func(detail string) quality.VerificationReport {
		return quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{{
			Language: "specscore", Module: ".", Check: quality.CheckSpec, Command: "specscore spec lint", Status: quality.StatusFailed, Detail: detail,
		}}}
	}
	nodeFailing := func(detail string) quality.VerificationReport {
		return quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{{
			Language: "node", Module: "frontend", Check: quality.CheckBuild, Command: "pnpm run build", Status: quality.StatusFailed, Detail: detail,
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
		{name: "coverage failure subset with changed shard placement", baseline: failing("WB coverage failure index:\n- [github.com/sneat-dev/wb/internal/worktrees shard 2/8] TestStable\n- [unsharded packages] TestRemoved\nWB coverage raw output\nbaseline output"), candidate: failing("WB coverage failure index:\n- [github.com/sneat-dev/wb/internal/worktrees shard 6/8] TestStable\nWB coverage raw output\ncandidate output")},
		{name: "coverage failure introduces test", baseline: failing("WB coverage failure index:\n- [github.com/sneat-dev/wb/internal/worktrees shard 2/8] TestStable\nWB coverage raw output\nbaseline output"), candidate: failing("WB coverage failure index:\n- [github.com/sneat-dev/wb/internal/worktrees shard 6/8] TestStable\n- [unsharded packages] TestNew\nWB coverage raw output\ncandidate output"), wantError: true},
		{name: "same Nx failure at different quoted snapshot paths", baseline: nodeFailing(`Could not find Nx modules at "/private/var/folders/aa/wb-worktree-merge-target-123/tree/frontend"`), candidate: nodeFailing(`Could not find Nx modules at "/private/var/folders/bb/wb-worktree-merge-target-456/tree/frontend"`)},
		{name: "different Nx failure remains different", baseline: nodeFailing(`Could not find Nx modules at "/private/var/folders/aa/wb-worktree-merge-target-123/tree/frontend"`), candidate: nodeFailing(`Could not find Nx modules at "/private/var/folders/bb/wb-worktree-merge-target-456/tree/frontend"; install dependencies first`), wantError: true},
		{name: "changed failure", baseline: failing("undefined: missing"), candidate: failing("undefined: other"), wantError: true},
		{name: "specscore environment-only baseline extra finding permits candidate", baseline: specFailing("specscore.yaml:0 studio-toolbar: requires project host/org/repo\nspec/features/x.md:12 missing-owner: owner is required"), candidate: specFailing("specscore.yaml:0 studio-toolbar: requires project host/org/repo")},
		{name: "specscore new identity", baseline: specFailing("specscore.yaml:0 studio-toolbar: requires project host/org/repo"), candidate: specFailing("specscore.yaml:0 studio-toolbar: requires project host/org/repo\nspec/features/x.md:12 missing-owner: owner is required"), wantError: true},
		{name: "specscore same identity changed detail", baseline: specFailing("specscore.yaml:0 studio-toolbar: requires project host/org/repo"), candidate: specFailing("specscore.yaml:0 studio-toolbar: now requires a configured remote")},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := worktreeMergeValidationRegression(test.baseline, test.candidate)
			if (err != nil) != test.wantError {
				t.Fatalf("regression error = %v, want error=%t", err, test.wantError)
			}
		})
	}
}

func TestWorktreeMergeValidationRegressionIgnoresCoverageShardPackagePlacement(t *testing.T) {
	detail := "WB coverage failure index:\n- [github.com/sneat-dev/wb/internal/worktrees shard 2/8] TestStable\nWB coverage raw output\noutput"
	entry := func(command string) quality.VerificationReport {
		return quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{{
			Language: "go", Module: ".", Check: quality.CheckTest, Command: command, Status: quality.StatusFailed, Detail: detail,
		}}}
	}
	baseline := entry("go test -coverprofile … ./... (8 process-isolated shards for ./internal/worktrees)")
	candidate := entry("go test -coverprofile … ./... (8 process-isolated shards for ./cmd/wb,./internal/worktrees)")
	if err := worktreeMergeValidationRegression(baseline, candidate); err != nil {
		t.Fatalf("coverage shard package placement changed failure identity: %v", err)
	}
	candidate.Results[0].Command = "go test -race ./..."
	if err := worktreeMergeValidationRegression(baseline, candidate); err == nil {
		t.Fatal("semantic coverage command change was accepted")
	}
}

func TestWorktreeMergeValidationRegressionMatchesContactusVolatileBuildOutput(t *testing.T) {
	nodeFailing := func(detail string) quality.VerificationReport {
		return quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{{
			Language: "node", Module: "landings", Check: quality.CheckBuild, Command: "pnpm run build", Status: quality.StatusFailed, Detail: detail,
		}}}
	}
	baseline := nodeFailing(`$ astro build && pnpm run build:app && node scripts/assemble-app.mjs
03:53:52 [types] Generated 51ms
03:53:52 [build] output: "static"
03:53:52 [build] directory: /private/var/folders/c6/pty228l52dx19k5xfxjz1ztr0000gn/
… output truncated; final 750 bytes:
03:53:53 [vite] ✓ built in 508ms
03:53:53 ✓ Completed in 13ms.
03:53:53 [build] ✓ Completed in 540ms.
03:53:53 [node] 2 page(s) built in 611ms
$ cd ../frontend && npx nx build contactus-app --base-href=/

 NX   Could not find Nx modules at "/private/var/folders/c6/pty228l52dx19k5xfxjz1ztr0000gn/T/wb-worktree-merge-target-515900289/tree/frontend".

Have you run npm/yarn install?

[ELIFECYCLE] Command failed with exit code 1.`)
	candidate := nodeFailing(`$ astro build && pnpm run build:app && node scripts/assemble-app.mjs
03:53:43 [types] Generated 49ms
03:53:43 [build] output: "static"
03:53:43 [build] directory: /Users/alex/.wb/worktrees/merge-sneat-co-contactus-main
… output truncated; final 750 bytes:
03:53:44 [vite] ✓ built in 505ms
03:53:44 ✓ Completed in 13ms.
03:53:44 [build] ✓ Completed in 537ms.
03:53:44 [node] 2 page(s) built in 606ms
$ cd ../frontend && npx nx build contactus-app --base-href=/

 NX   Could not find Nx modules at "/Users/alex/.wb/worktrees/merge-sneat-co-contactus-main-355e0d554d15-c46e04b0fe6b/sneat-co/contactus/frontend".

Have you run npm/yarn install?

[ELIFECYCLE] Command failed with exit code 1.`)
	if err := worktreeMergeValidationRegression(baseline, candidate); err != nil {
		t.Fatalf("receipt-shaped volatile output should be equivalent: %v", err)
	}
}

func TestWorktreeMergeValidationRegressionMatchesExactContactusTruncatedTail(t *testing.T) {
	nodeFailing := func(detail string) quality.VerificationReport {
		return quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{{
			Language: "node", Module: "landings", Check: quality.CheckBuild, Command: "pnpm run build", Status: quality.StatusFailed, Detail: detail,
		}}}
	}
	baseline := nodeFailing(`$ astro build && pnpm run build:app && node scripts/assemble-app.mjs
04:56:35 [types] Generated 58ms
04:56:35 [build] output: "static"
04:56:35 [build] mode: "static"
04:56:35 [build] directory: /private/var/folders/c6/pty228l52dx19k5xfxjz1ztr0000gn/
… output truncated; final 750 bytes:
 built in 591ms
04:56:36 [vite] ✓ built in 8ms
04:56:36 [build] Rearranging server assets...

 generating static routes
04:56:36   ├─ /en/privacy/index.html (+8ms)
04:56:36   ├─ /index.html (+4ms)
04:56:36 ✓ Completed in 21ms.

04:56:36 [build] ✓ Completed in 637ms.
04:56:36 [@astrojs/sitemap] ` + "\x60" + `sitemap-index.xml` + "\x60" + ` created at ` + "\x60" + `dist` + "\x60" + `
04:56:36 [build] 2 page(s) built in 719ms
04:56:36 [build] Complete!
$ cd ../frontend && npx nx build contactus-app --base-href=/

 NX   Could not find Nx modules at "/private/var/folders/c6/pty228l52dx19k5xfxjz1ztr0000gn/T/wb-worktree-merge-target-3002203795/tree/frontend".

Have you run npm/yarn install?

[ELIFECYCLE] Command failed with exit code 1.
[ELIFECYCLE] Command failed with exit code 1.`)
	candidate := nodeFailing(`$ astro build && pnpm run build:app && node scripts/assemble-app.mjs
04:56:25 [types] Generated 27ms
04:56:25 [build] output: "static"
04:56:25 [build] mode: "static"
04:56:25 [build] directory: /Users/alex/.wb/worktrees/merge-sneat-co-contactus-main
… output truncated; final 750 bytes:
ilt in 112ms
04:56:25 [vite] ✓ built in 9ms
04:56:25 [build] Rearranging server assets...

 generating static routes
04:56:25   ├─ /en/privacy/index.html (+8ms)
04:56:25   ├─ /index.html (+4ms)
04:56:25 ✓ Completed in 20ms.

04:56:25 [build] ✓ Completed in 158ms.
04:56:25 [@astrojs/sitemap] ` + "\x60" + `sitemap-index.xml` + "\x60" + ` created at ` + "\x60" + `dist` + "\x60" + `
04:56:25 [build] 2 page(s) built in 215ms
04:56:25 [build] Complete!
$ cd ../frontend && npx nx build contactus-app --base-href=/

 NX   Could not find Nx modules at "/Users/alex/.wb/worktrees/merge-sneat-co-contactus-main-355e0d554d15-c46e04b0fe6b/sneat-co/contactus/frontend".

Have you run npm/yarn install?

[ELIFECYCLE] Command failed with exit code 1.
[ELIFECYCLE] Command failed with exit code 1.`)
	if err := worktreeMergeValidationRegression(baseline, candidate); err != nil {
		t.Fatalf("exact Contactus receipt-shaped tail should be equivalent: %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(string) string
	}{
		{name: "different Nx error text", mutate: func(detail string) string {
			return strings.Replace(detail, "Could not find Nx modules", "Could not find Nx workspace", 1)
		}},
		{name: "different Nx error code", mutate: func(detail string) string {
			return strings.Replace(detail, "exit code 1", "exit code 2", 1)
		}},
		{name: "different Nx error number", mutate: func(detail string) string {
			return strings.Replace(detail, "2 page(s) built", "3 page(s) built", 1)
		}},
		{name: "different truncated timing verb", mutate: func(detail string) string {
			return strings.Replace(detail, " built in 591ms", "failed in 591ms", 1)
		}},
		{name: "extra Nx diagnostic", mutate: func(detail string) string {
			return detail + "\nNX diagnostic: install dependencies first"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if sameWorktreeMergeFailure(nodeFailing(baseline.Results[0].Detail).Results[0], nodeFailing(test.mutate(baseline.Results[0].Detail)).Results[0]) {
				t.Fatalf("normalized comparison erased %s", test.name)
			}
		})
	}
}

func TestNormalizeWorktreeMergeFailureDetailTruncatedTimingIsFailClosed(t *testing.T) {
	const marker = "… output truncated; final 750 bytes:\n"
	for _, test := range []struct {
		name string
		in   string
		want string
	}{
		{name: "partial built", in: marker + "ilt in 112ms", want: strings.TrimSpace(marker) + " built in <duration>"},
		{name: "partial completed", in: marker + "pleted in 112ms", want: strings.TrimSpace(marker) + " completed in <duration>"},
		{name: "unknown timing phrase", in: marker + "error in 112ms", want: strings.TrimSpace(marker) + " error in 112ms"},
		{name: "semantic partial line", in: marker + "or in 112ms", want: strings.TrimSpace(marker) + " or in 112ms"},
		{name: "marker without tail line", in: marker, want: strings.TrimSpace(marker)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeWorktreeMergeFailureDetail(test.in); got != test.want {
				t.Fatalf("normalized detail = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWorktreeMergeValidationRegressionMatchesYardiusEnvironmentFailures(t *testing.T) {
	nodeFailing := func(detail string) quality.VerificationEntry {
		return quality.VerificationEntry{Language: "node", Module: "landings", Check: quality.CheckBuild, Command: "pnpm run build", Status: quality.StatusFailed, Detail: detail}
	}
	specFailing := func(detail string) quality.VerificationEntry {
		return quality.VerificationEntry{Language: "specscore", Module: "", Check: quality.CheckSpec, Command: "", Status: quality.StatusFailed, Detail: detail}
	}
	baseline := quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{
		nodeFailing(`$ astro build && pnpm run build:app && node scripts/assemble-app.mjs
03:54:32 [types] Generated 48ms
03:54:32 [build] directory: /private/var/folders/c6/pty228l52dx19k5xfxjz1ztr0000gn/
… output truncated; final 750 bytes:
$ cd .. && npx nx build yardius-app --base-href=/

 NX   Could not find Nx modules at "/private/var/folders/c6/pty228l52dx19k5xfxjz1ztr0000gn/T/wb-worktree-merge-target-786210721/tree".

Have you run npm/yarn install?

[ELIFECYCLE] Command failed with exit code 1.`),
		specFailing(`SpecScore config "/private/var/folders/c6/pty228l52dx19k5xfxjz1ztr0000gn/T/wb-worktree-merge-target-786210721/tree/specscore.yaml" requires root "/private/var/folders/c6/pty228l52dx19k5xfxjz1ztr0000gn/T/wb-worktree-merge-target-786210721/tree/spec", but the root is missing`),
	}}
	candidate := quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{
		nodeFailing(`$ astro build && pnpm run build:app && node scripts/assemble-app.mjs
03:54:25 [types] Generated 46ms
03:54:25 [build] directory: /Users/alex/.wb/worktrees/merge-sneat-co-yardius-main-b3d61f4f34c9-220bcc8b0858/sneat-co/yardius
… output truncated; final 750 bytes:
$ cd .. && npx nx build yardius-app --base-href=/

 NX   Could not find Nx modules at "/Users/alex/.wb/worktrees/merge-sneat-co-yardius-main-b3d61f4f34c9-220bcc8b0858/sneat-co/yardius".

Have you run npm/yarn install?

[ELIFECYCLE] Command failed with exit code 1.`),
		specFailing(`SpecScore config "/Users/alex/.wb/worktrees/merge-sneat-co-yardius-main-b3d61f4f34c9-220bcc8b0858/sneat-co/yardius/specscore.yaml" requires root "/Users/alex/.wb/worktrees/merge-sneat-co-yardius-main-b3d61f4f34c9-220bcc8b0858/sneat-co/yardius/spec", but the root is missing`),
	}}
	if err := worktreeMergeValidationRegression(baseline, candidate); err != nil {
		t.Fatalf("receipt-shaped environment failures should be equivalent: %v", err)
	}
}

func TestNormalizeWorktreeMergeFailureDetailPreservesBehaviorAndSemanticNumbers(t *testing.T) {
	baseline := `03:53:52 [types] Generated 51ms at /private/var/folders/c6/target/tree/frontend".`
	candidate := `03:53:43 [types] Generated 49ms at /Users/alex/.wb/worktrees/candidate/tree/frontend".`
	if got, want := normalizeWorktreeMergeFailureDetail(baseline), normalizeWorktreeMergeFailureDetail(candidate); got != want {
		t.Fatalf("timestamp/duration/path-only difference normalized to %q and %q", got, want)
	}
	if got, want := normalizeWorktreeMergeFailureDetail(`Nx modules at "/private/var/folders/c6/target/tree".`), `Nx modules at "<workspace>".`; got != want {
		t.Fatalf("absolute path terminal punctuation normalization = %q, want %q", got, want)
	}
	for _, test := range []struct {
		name      string
		baseline  string
		candidate string
	}{
		{name: "semantic duration", baseline: "command timed out after 30s", candidate: "command timed out after 60s"},
		{name: "embedded timestamp", baseline: "error identity recorded at 03:53:43", candidate: "error identity recorded at 03:53:44"},
		{name: "line-leading semantic timestamp", baseline: "03:53:43 error identity", candidate: "03:53:44 error identity"},
		{name: "error code", baseline: "command failed with exit code 1", candidate: "command failed with exit code 2"},
		{name: "semantic number", baseline: "2 page(s) built", candidate: "3 page(s) built"},
		{name: "error text", baseline: "Could not find Nx modules", candidate: "Could not find Nx workspace"},
		{name: "added diagnostic", baseline: "Nx modules missing", candidate: "Nx modules missing; install dependencies first"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if left, right := normalizeWorktreeMergeFailureDetail(test.baseline), normalizeWorktreeMergeFailureDetail(test.candidate); left == right {
				t.Fatalf("normalized comparison erased %s: %q", test.name, left)
			}
		})
	}
}

func TestVerifyWorktreeMergeTargetProvidesCandidateOriginRemoteContext(t *testing.T) {
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
	if err := os.WriteFile(specscore, []byte("#!/bin/sh\nset -eu\nif [ \"$1 $2\" != \"spec lint\" ]; then exit 2; fi\ngit remote get-url origin >\"$WB_TEST_BASELINE_ORIGIN\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_BASELINE_ORIGIN", observedOrigin)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	report, err := verifyWorktreeMergeTarget(context.Background(), fixture.repository.Slug, fixture.canonical, target, 0, 0)
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

func TestResumeWorktreeMergeStopBeforeMergePublishesAndPreservesExactPRHandoff(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "published-stop-source", "feature/published-stop", "published-stop.txt", "published stop\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	installWorktreeMergePublishOnlyPRGH(t)
	t.Setenv("WB_TEST_CANDIDATE_SHA", receipt.Candidate.SHA)
	t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
	logPath := filepath.Join(t.TempDir(), "gh.log")
	t.Setenv("WB_TEST_GH_LOG", logPath)

	published, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest,
		StopBeforeMerge: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("publish-only resume failed: receipt=%+v err=%v", published, err)
	}
	if published.Status != WorktreeMergePublished || published.PullRequest != "https://example.test/acme/app/pull/41" ||
		published.PublishedCandidateSHA != receipt.Candidate.SHA || published.LandingSHA != "" || published.Checks.Status != "" {
		t.Fatalf("published handoff receipt = %+v", published)
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

func TestResumeWorktreeMergeStopBeforeMergeRefusesTargetOrSourceDrift(t *testing.T) {
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
			name: "partial terminal cleanup", want: "only partially terminalized",
			breakEvidence: func(t *testing.T, fixture engineFixture, landed WorktreeMergeReceipt, _ map[string]string) {
				externallyTerminalizeTask(t, fixture, &landed, landed.Candidate.Task)
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
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, _, landed, claims := landedTerminalCleanupFixture(t)
			if test.name == "partial terminal cleanup" {
				test.breakEvidence(t, fixture, landed, claims)
			} else {
				externallyTerminalizeMergeCleanup(t, fixture, &landed)
				test.breakEvidence(t, fixture, landed, claims)
			}
			failed, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
				ProjectsRoot: fixture.githubDir, Receipt: landed.ReceiptPath, Cleanup: true, Route: WorktreeMergeRouteAuto,
				Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
			})
			if err == nil || !strings.Contains(err.Error(), test.want) || failed.Status == WorktreeMergeComplete {
				t.Fatalf("terminal cleanup refusal = %+v err=%v, want %q", failed, err, test.want)
			}
		})
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

func TestResumeWorktreeMergeAdvancesResolvedConflictCandidateDescendant(t *testing.T) {
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

func TestAdvanceResolvedConflictCandidateRefusesUnsafeEvidence(t *testing.T) {
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

// A candidate may be created from one immutable base, then have a target-drift
// conflict resolved manually. The receipt records the newer target snapshot,
// but the Work Log must retain its original claim base. Resume may normalize
// that proven state without replaying the already-integrated source.
func TestResumeWorktreeMergeRecoversResolvedTargetDriftConflictWithHistoricalClaimBase(t *testing.T) {
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

func TestPrepareWorktreeMergeRebatchesPreparedReceiptAdditivelyAndPreservesOldEvidence(t *testing.T) {
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
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
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
  'api --paginate repos/acme/app/commits/'*'/pulls') printf '%s\n' '[]' ;;
  'api repos/acme/app/pulls/'*' --include'|'api repos/acme/app/pulls/'*)
    printf '{"number":41,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"%s","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}\n' "$WB_TEST_CANDIDATE_SHA" ;;
  *'/check-runs?per_page=100 --include'|*'/check-runs?per_page=100') printf '%s\n' '{"total_count":0,"check_runs":[]}' ;;
  *'/status?per_page=100 --include'|*'/status?per_page=100') printf '%s\n' '{"total_count":0,"statuses":[]}' ;;
  *) echo "unexpected gh command: $*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
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
  'api repos/acme/app/rules/branches/main?per_page=100 --include'|'api repos/acme/app/rules/branches/main?per_page=100') printf '%s\n' '[]' ;;
  'api repos/acme/app/git/ref/heads/main --include'|'api repos/acme/app/git/ref/heads/main') printf '{"object":{"sha":"%s"}}\n' "$WB_TEST_TARGET_SHA" ;;
  'api repos/acme/app/pulls/'*' --include'|'api repos/acme/app/pulls/'*)
    printf '{"number":41,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"%s","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}\n' "$WB_TEST_CANDIDATE_SHA" ;;
  *'/check-runs?per_page=100 --include'|*'/check-runs?per_page=100') printf '%s\n' '{"total_count":0,"check_runs":[]}' ;;
  *'/status?per_page=100 --include'|*'/status?per_page=100') printf '%s\n' '{"total_count":0,"statuses":[]}' ;;
  *) echo "unexpected gh command: $*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
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
	t.Setenv("XDG_STATE_HOME", t.TempDir())
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
  'api --paginate repos/acme/app/commits/'*'/pulls') printf '%s\n' '[]' ;;
  *) echo "unexpected gh command: $*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
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
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}
