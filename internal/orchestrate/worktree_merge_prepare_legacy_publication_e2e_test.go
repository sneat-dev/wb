//go:build e2e

package orchestrate

import (
	"context"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/quality"
)

// TestE2EPrepareLegacyPublishedAdvancePlanFailureResumesNormalJourney covers the
// bridge's pre-validation planning error. The normal prepare has already
// captured H and integrated the advanced source S-prime into H-prime when the
// route/validation plan cannot be resolved. That failure is not candidate
// evidence: the durable receipt must stay a truthful, resumable preparing
// receipt at exact H-prime (never a conflict, which no published recovery
// re-proves once the candidate contains every source), keep the captured H in
// its ancestry, and publish nothing. Removing only that planning failure lets
// the same ordinary resume validate exact H-prime and land it through the same
// existing pull request onto the real target.
//
//nolint:paralleltest // the scripted provider changes PATH/XDG and the private lane is shared
func TestE2EPrepareLegacyPublishedAdvancePlanFailureResumesNormalJourney(t *testing.T) {
	fixture := newLegacyPublishedAdvanceFixture(t)
	gh := installWorktreeMergeEngineGH(t, fixture.engineFixture, fixture.published, fixture.receipt.Candidate.Branch)
	// The receipt already owns this open publication: the provider must report
	// it as existing, so a replacement `pr create` is detectable.
	gh.writeState(t, "pr-created", "1")
	target := fixture.receipt.TargetSHA

	failed, planErr := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{fixture.source}, Target: "main",
		Model: "test-model", AgentRuntime: "test", Route: WorktreeMergeRouteDirect, ValidateLocally: true,
	})
	if planErr == nil || !strings.Contains(planErr.Error(), "direct route is not authoritatively permitted") {
		t.Fatalf("legacy plan refusal=%+v error=%v", failed, planErr)
	}
	if failed.Status != WorktreeMergePreparing || failed.Candidate.SHA == "" || failed.Candidate.SHA == fixture.resolvedHead {
		t.Fatalf("legacy plan refusal recorded an unproven candidate: %+v", failed)
	}
	head := failed.Candidate.SHA
	for _, root := range []string{fixture.resolvedHead, fixture.published, fixture.initialSource, fixture.oldSource, fixture.currentSource, target} {
		assertNativeAncestor(t, failed.Candidate.Worktree, root, head)
	}
	if failed.PublishedCandidateSHA != fixture.published || failed.PullRequest != gh.pr ||
		failed.TargetSHA != target || failed.LandingSHA != "" || failed.Validation.Status == quality.StatusPassed {
		t.Fatalf("legacy plan refusal claimed validation or lost publication: %+v", failed)
	}
	if failed.Sources[0].SHA != fixture.currentSource || len(failed.SourceRefreshes) != 2 ||
		failed.SourceRefreshes[0].Sources[0].SHA != fixture.initialSource ||
		failed.SourceRefreshes[1].Sources[0].SHA != fixture.oldSource {
		t.Fatalf("legacy plan refusal lost source history: %+v", failed)
	}
	stored, readErr := readWorktreeMergeReceipt(failed.ReceiptPath)
	if readErr != nil || stored.Status != WorktreeMergePreparing || stored.Candidate.SHA != head ||
		stored.PublishedCandidateSHA != fixture.published || stored.PullRequest != gh.pr ||
		stored.TargetSHA != target || stored.LandingSHA != "" ||
		stored.Sources[0].SHA != fixture.currentSource || len(stored.SourceRefreshes) != 2 {
		t.Fatalf("durable legacy plan refusal=%+v error=%v", stored, readErr)
	}
	if physical := strings.TrimSpace(runEngineGit(t, failed.Candidate.Worktree, "rev-parse", "HEAD")); physical != head {
		t.Fatalf("legacy plan refusal moved the integrated head from %s to %s", head, physical)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+failed.Candidate.Branch)); remote != fixture.published {
		t.Fatalf("legacy plan refusal published %s, want %s", remote, fixture.published)
	}
	if live := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); live != target {
		t.Fatalf("legacy plan refusal moved the target to %s, want %s", live, target)
	}
	if log := gh.ghLog(t); strings.Contains(log, "pr create") || strings.Contains(log, "pulls/41/merge") || strings.Contains(log, "update-branch") {
		t.Fatalf("legacy plan refusal performed a pull-request write:\n%s", log)
	}

	// Removing only the planning failure lets the ordinary resume validate the
	// exact integrated head and land it through the same pull request.
	options := wmEngineLandOptions(fixture.engineFixture, failed.ReceiptPath)
	options.ValidateLocally = true
	options.StopBeforeMerge = false
	landed, err := ResumeWorktreeMerge(context.Background(), options)
	if err != nil {
		t.Fatalf("actual normal resume failed: receipt=%+v error=%v", landed, err)
	}
	if landed.Status != WorktreeMergeLanded || landed.Candidate.SHA != head || landed.LandingSHA != head ||
		landed.PublishedCandidateSHA != head || landed.PullRequest != gh.pr || landed.Failure != "" ||
		landed.Validation.Revision != head || landed.Validation.Status != quality.StatusPassed {
		t.Fatalf("resumed legacy plan failure receipt=%+v", landed)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+failed.Candidate.Branch)); remote != head {
		t.Fatalf("resume published branch=%s, want landed head %s", remote, head)
	}
	if live := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); live != head {
		t.Fatalf("resume remote target=%s, want landed head %s", live, head)
	}
	for _, root := range []string{fixture.resolvedHead, fixture.published, fixture.initialSource, fixture.oldSource, fixture.currentSource, target} {
		assertNativeAncestor(t, fixture.repository.CloneURL, root, head)
	}
	log := gh.ghLog(t)
	if strings.Contains(log, "pr create") ||
		!strings.Contains(log, "api --method PUT repos/acme/app/pulls/41/merge") || !strings.Contains(log, "sha="+head) {
		t.Fatalf("normal resume did not merge the same existing pull request:\n%s", log)
	}
	stored, readErr = readWorktreeMergeReceipt(failed.ReceiptPath)
	if readErr != nil || stored.Status != WorktreeMergeLanded || stored.Candidate.SHA != head ||
		stored.PublishedCandidateSHA != head || stored.PullRequest != gh.pr || stored.LandingSHA != head ||
		stored.Validation.Revision != head || stored.TargetSHA != target {
		t.Fatalf("durable resumed receipt=%+v error=%v", stored, readErr)
	}
}

// TestE2EPrepareLegacyPublishedAdvanceThenNormalResumeLandsSamePullRequest is
// the positive real-Git journey for the prepare-only legacy bridge: the normal
// prepare entry point recovers H, integrates the strictly advanced source
// S-prime into H-prime, and publishes nothing; the ordinary normal resume then
// validates exact H-prime and lands it through the same open pull request,
// fast-forwarding the published branch and the real target while retaining
// every recorded predecessor.
//
//nolint:paralleltest // the scripted provider changes PATH/XDG and the private lane is shared
func TestE2EPrepareLegacyPublishedAdvanceThenNormalResumeLandsSamePullRequest(t *testing.T) {
	fixture := newLegacyPublishedAdvanceFixture(t)
	gh := installWorktreeMergeEngineGH(t, fixture.engineFixture, fixture.published, fixture.receipt.Candidate.Branch)
	// The receipt already owns this open publication: the provider must report
	// it as existing, so any replacement `pr create` is detected rather than
	// hidden by a fresh fixture PR.
	gh.writeState(t, "pr-created", "1")
	target := fixture.receipt.TargetSHA

	prepared, err := prepareLegacyPublishedAdvance(t, fixture)
	if err != nil {
		t.Fatalf("actual normal prepare failed: receipt=%+v error=%v", prepared, err)
	}
	if prepared.Status != WorktreeMergePrepared || prepared.Candidate.SHA == "" ||
		prepared.PublishedCandidateSHA != fixture.published || prepared.PullRequest != gh.pr {
		t.Fatalf("normal prepare receipt=%+v", prepared)
	}
	head := prepared.Candidate.SHA
	for _, root := range []string{fixture.published, fixture.resolvedHead, fixture.initialSource, fixture.oldSource, fixture.currentSource, target} {
		assertNativeAncestor(t, prepared.Candidate.Worktree, root, head)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+prepared.Candidate.Branch)); got != fixture.published {
		t.Fatalf("prepare moved the published branch to %s, want %s", got, fixture.published)
	}
	prepareLog := gh.ghLog(t)
	if strings.Contains(prepareLog, "pr create") || strings.Contains(prepareLog, "pulls/41/merge") || strings.Contains(prepareLog, "update-branch") {
		t.Fatalf("prepare performed a pull-request write:\n%s", prepareLog)
	}

	options := wmEngineLandOptions(fixture.engineFixture, prepared.ReceiptPath)
	options.ValidateLocally = true
	options.StopBeforeMerge = false
	landed, err := ResumeWorktreeMerge(context.Background(), options)
	if err != nil {
		t.Fatalf("actual normal resume failed: receipt=%+v error=%v", landed, err)
	}
	if landed.Status != WorktreeMergeLanded || landed.Candidate.SHA != head || landed.LandingSHA != head ||
		landed.PublishedCandidateSHA != head || landed.PullRequest != gh.pr || landed.Failure != "" ||
		landed.Validation.Revision != head || landed.Validation.Status != quality.StatusPassed {
		t.Fatalf("resumed legacy published receipt=%+v", landed)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+prepared.Candidate.Branch)); got != head {
		t.Fatalf("published branch=%s, want landed head %s", got, head)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); got != head {
		t.Fatalf("remote target=%s, want landed head %s", got, head)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); got != head {
		t.Fatalf("canonical target=%s, want landed head %s", got, head)
	}
	for _, root := range []string{fixture.published, fixture.resolvedHead, fixture.initialSource, fixture.oldSource, fixture.currentSource, target} {
		assertNativeAncestor(t, fixture.repository.CloneURL, root, head)
	}
	log := gh.ghLog(t)
	if strings.Contains(log, "pr create") ||
		!strings.Contains(log, "api --method PUT repos/acme/app/pulls/41/merge") || !strings.Contains(log, "sha="+head) {
		t.Fatalf("normal resume did not merge the same existing pull request:\n%s", log)
	}
	stored, err := readWorktreeMergeReceipt(prepared.ReceiptPath)
	if err != nil || stored.Status != WorktreeMergeLanded || stored.Candidate.SHA != head ||
		stored.PublishedCandidateSHA != head || stored.PullRequest != gh.pr || stored.LandingSHA != head ||
		stored.Validation.Revision != head || stored.TargetSHA != target {
		t.Fatalf("durable landed receipt=%+v error=%v", stored, err)
	}
}
