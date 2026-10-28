//go:build e2e

package orchestrate

import (
	"context"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/quality"
)

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
