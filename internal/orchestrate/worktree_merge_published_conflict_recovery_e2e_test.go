//go:build e2e

package orchestrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/quality"
)

// TestE2EPublishedPrepareConflictRecoveryResumesExistingPullRequest drives the
// legacy empty-SHA published prepare conflict through the ordinary
// ResumeWorktreeMerge entry point: fresh private-Git and hosted proofs, the
// ordinary validation, and a fast-forward-only publication onto the existing
// open pull request branch.
//
//nolint:paralleltest // the scripted provider changes PATH/XDG and the private lane is shared
func TestE2EPublishedPrepareConflictRecoveryResumesExistingPullRequest(t *testing.T) {
	fixture, receipt, published, head := newPublishedPrepareConflictFixture(t)
	gh := installWorktreeMergeEngineGH(t, fixture, published, receipt.Candidate.Branch)

	var phases []string
	options := wmEngineLandOptions(fixture, receipt.ReceiptPath)
	options.StopBeforeMerge = true
	options.ValidateLocally = true
	options.Progress = func(event progress.Event) {
		phases = append(phases, event.Phase+":"+string(event.State))
	}
	resumed, err := ResumeWorktreeMerge(context.Background(), options)
	if err != nil {
		t.Fatalf("actual published prepare resume failed: receipt=%+v error=%v", resumed, err)
	}
	if resumed.Status != WorktreeMergePublished || resumed.Candidate.SHA != head || resumed.PublishedCandidateSHA != head ||
		resumed.PullRequest != gh.pr || resumed.LandingSHA != "" || resumed.Failure != "" {
		t.Fatalf("resumed published receipt=%+v", resumed)
	}
	if resumed.Validation.Revision != head || resumed.Validation.Status != quality.StatusPassed {
		t.Fatalf("published resume did not validate the refreshed head: %+v", resumed.Validation)
	}
	wantPhases := []string{"recover_candidate:", "validate_preserved_candidate:", "publish_candidate:"}
	for _, want := range wantPhases {
		found := false
		for _, phase := range phases {
			if strings.HasPrefix(phase, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("resume did not execute %s: %q", want, phases)
		}
	}
	// The existing pull request branch advanced strictly by fast-forward from
	// the recorded publication; no force push and no new target commit exist.
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+receipt.Candidate.Branch)); remote != head {
		t.Fatalf("live candidate branch=%s, want refreshed head %s", remote, head)
	}
	assertNativeAncestor(t, fixture.repository.CloneURL, published, head)
	if target := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); target != receipt.TargetSHA {
		t.Fatalf("published handoff changed the target: %s", target)
	}
	stored, err := readWorktreeMergeReceipt(receipt.ReceiptPath)
	if err != nil || stored.Status != WorktreeMergePublished || stored.Candidate.SHA != head || stored.PublishedCandidateSHA != head ||
		stored.PullRequest != gh.pr || stored.Validation.Revision != head {
		t.Fatalf("durable published receipt=%+v error=%v", stored, err)
	}
}

// TestE2EPublishedPrepareConflictRecoveryValidationFailureRetainsIdentityAndRetry
// proves a failed ordinary validation after the legacy proof keeps the exact
// publication identity, records the truthful unvalidated head, never touches
// the remote branch, and re-proves/retries that same head on the next resume.
//
//nolint:paralleltest // the scripted provider changes PATH/XDG and the private lane is shared
func TestE2EPublishedPrepareConflictRecoveryValidationFailureRetainsIdentityAndRetry(t *testing.T) {
	fixture := newEngineFixture(t)
	writeEngineGoModule(t, fixture.canonical, "package app\n")
	runEngineGit(t, fixture.canonical, "add", "go.mod", "app.go")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: native published validation baseline")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	source := createMergeSource(t, fixture, "published-failing-source", "feature/published-failing", "source.txt", "source\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	published := receipt.Candidate.SHA
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", published+":refs/heads/"+receipt.Candidate.Branch)
	writeEngineFile(t, filepath.Join(source.WorktreeDir, "failing.go"), "package app\n\nfunc Broken() { missingRefreshedSymbol }\n")
	runEngineGit(t, source.WorktreeDir, "add", "failing.go")
	runEngineGit(t, source.WorktreeDir, "commit", "-m", "feat: advance with a real validation failure")
	advanced := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	runEngineGit(t, receipt.Candidate.Worktree, "merge", "--no-edit", advanced)
	head := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))
	receipt.Phase, receipt.Status = WorktreeMergePhasePrepare, WorktreeMergeConflict
	receipt.Sources[0].SHA = advanced
	receipt.Candidate.SHA = ""
	receipt.PullRequest = "https://example.test/acme/app/pull/41"
	receipt.PublishedCandidateSHA = published
	receipt.Failure = "prepare refresh failed before recording the refreshed head"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	gh := installWorktreeMergeEngineGH(t, fixture, published, receipt.Candidate.Branch)
	options := wmEngineLandOptions(fixture, receipt.ReceiptPath)
	options.StopBeforeMerge = true
	options.ValidateLocally = true

	for attempt := 1; attempt <= 2; attempt++ {
		failed, runErr := ResumeWorktreeMerge(context.Background(), options)
		if runErr == nil || failed.Status != WorktreeMergeValidationFailed || failed.Candidate.SHA != head ||
			failed.PublishedCandidateSHA != published || failed.PullRequest != gh.pr ||
			failed.Validation.Revision != head || failed.Validation.Status != quality.StatusFailed || failed.LandingSHA != "" {
			t.Fatalf("validation failure attempt %d receipt=%+v error=%v", attempt, failed, runErr)
		}
		stored, readErr := readWorktreeMergeReceipt(receipt.ReceiptPath)
		if readErr != nil || stored.Status != WorktreeMergeValidationFailed || stored.Candidate.SHA != head ||
			stored.PublishedCandidateSHA != published || stored.PullRequest != gh.pr {
			t.Fatalf("durable validation failure attempt %d=%+v error=%v", attempt, stored, readErr)
		}
		if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+receipt.Candidate.Branch)); remote != published {
			t.Fatalf("validation failure attempt %d moved the published branch: %s", attempt, remote)
		}
		if target := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); target != receipt.TargetSHA {
			t.Fatalf("validation failure attempt %d changed the target: %s", attempt, target)
		}
	}
}

// TestE2EPublishedPrepareRefreshConflictPreservesPublishedIdentityAndRecovers
// is the original-cause regression: a real refresh of an already published
// candidate fails on a source merge conflict. The receipt must retain the
// published predecessor as its candidate identity (never an empty snapshot),
// refuse an unresolved resume without touching the remote, and then recover
// the human-resolved clean descendant through the same guarded path.
//
//nolint:paralleltest // the scripted provider changes PATH/XDG and the private lane is shared
func TestE2EPublishedPrepareRefreshConflictPreservesPublishedIdentityAndRecovers(t *testing.T) {
	fixture := newEngineFixture(t)
	sourceA := createMergeSource(t, fixture, "refresh-conflict-a", "feature/refresh-conflict-a", "shared.txt", "a\n")
	sourceB := createMergeSource(t, fixture, "refresh-conflict-b", "feature/refresh-conflict-b", "b-other.txt", "b\n")
	runEngineGit(t, sourceB.WorktreeDir, "merge", "--no-edit", sourceA.Branch)
	writeEngineFile(t, filepath.Join(sourceB.WorktreeDir, "shared.txt"), "b\n")
	runEngineGit(t, sourceB.WorktreeDir, "add", "shared.txt")
	runEngineGit(t, sourceB.WorktreeDir, "commit", "-m", "feat: change the shared file on the descendant source")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{sourceA.WorktreeDir, sourceB.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	published := receipt.Candidate.SHA
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", published+":refs/heads/"+receipt.Candidate.Branch)
	receipt.Phase, receipt.Status = WorktreeMergePhasePrepare, WorktreeMergePublished
	receipt.PullRequest, receipt.PublishedCandidateSHA = "https://example.test/acme/app/pull/41", published
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	writeEngineFile(t, filepath.Join(sourceA.WorktreeDir, "shared.txt"), "a-advanced\n")
	runEngineGit(t, sourceA.WorktreeDir, "add", "shared.txt")
	runEngineGit(t, sourceA.WorktreeDir, "commit", "-m", "feat: advance the conflicting published source")
	advanced := strings.TrimSpace(runEngineGit(t, sourceA.WorktreeDir, "rev-parse", "HEAD"))

	failed, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{sourceA.WorktreeDir, sourceB.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err == nil || failed.Status != WorktreeMergeConflict || failed.Phase != WorktreeMergePhasePrepare || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("failed published refresh=%+v error=%v", failed, err)
	}
	if failed.Candidate.SHA != published || failed.PublishedCandidateSHA != published || failed.PullRequest != receipt.PullRequest || failed.Sources[0].SHA != advanced {
		t.Fatalf("failed published refresh lost proved identity: %+v", failed)
	}
	stored, err := readWorktreeMergeReceipt(receipt.ReceiptPath)
	if err != nil || stored.Candidate.SHA != published || stored.PublishedCandidateSHA != published || stored.PullRequest != receipt.PullRequest || stored.Status != WorktreeMergeConflict {
		t.Fatalf("durable failed refresh=%+v error=%v", stored, err)
	}
	if head := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD")); head != published {
		t.Fatalf("failed refresh left the candidate at %s, want the preserved publication %s", head, published)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+receipt.Candidate.Branch)); remote != published {
		t.Fatalf("failed refresh moved the published branch: %s", remote)
	}

	gh := installWorktreeMergeEngineGH(t, fixture, published, receipt.Candidate.Branch)
	options := wmEngineLandOptions(fixture, receipt.ReceiptPath)
	options.StopBeforeMerge = true
	options.ValidateLocally = true

	// An unresolved resume must refuse: the preserved publication alone is
	// never permission to republish a candidate that lacks the advanced source.
	refused, refuseErr := ResumeWorktreeMerge(context.Background(), options)
	if refuseErr == nil || !strings.Contains(refuseErr.Error(), "does not contain required immutable root") {
		t.Fatalf("unresolved published refresh resume=%+v error=%v", refused, refuseErr)
	}
	if refused.Candidate.SHA != published || refused.PublishedCandidateSHA != published || refused.PullRequest != gh.pr {
		t.Fatalf("unresolved refusal changed publication identity: %+v", refused)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+receipt.Candidate.Branch)); remote != published {
		t.Fatalf("unresolved refusal moved the published branch: %s", remote)
	}

	// Resolve the conflict in the preserved candidate worktree exactly as a
	// human would, then let ordinary resume prove and publish the clean head.
	if _, _, mergeErr := runCommand(context.Background(), defaultRunner, 0, 0, receipt.Candidate.Worktree, "git", "merge", "--no-edit", advanced); mergeErr == nil {
		t.Fatal("human resolution fixture did not conflict")
	}
	writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "shared.txt"), "a-advanced\nb\n")
	runEngineGit(t, receipt.Candidate.Worktree, "add", "shared.txt")
	runEngineGit(t, receipt.Candidate.Worktree, "commit", "--no-edit")
	resolved := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))

	publishedResume, err := ResumeWorktreeMerge(context.Background(), options)
	if err != nil || publishedResume.Status != WorktreeMergePublished || publishedResume.Candidate.SHA != resolved || publishedResume.PublishedCandidateSHA != resolved ||
		publishedResume.PullRequest != gh.pr || publishedResume.Validation.Revision != resolved {
		t.Fatalf("resolved published refresh resume=%+v error=%v", publishedResume, err)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+receipt.Candidate.Branch)); remote != resolved {
		t.Fatalf("resolved published refresh branch=%s, want %s", remote, resolved)
	}
	assertNativeAncestor(t, fixture.repository.CloneURL, published, resolved)
}

// TestE2EPublishedPrepareConflictRecoveryLandsExistingPullRequestAndCleansUp
// keeps the ordinary landing and cleanup journey intact after the legacy
// proof: the existing pull request is adopted at the refreshed exact head,
// server checks/merge run, the canonical clone fast-forwards, and every
// managed asset is terminalized through the normal cleanup owner.
//
//nolint:paralleltest // the scripted provider changes PATH/XDG and the private lane is shared
func TestE2EPublishedPrepareConflictRecoveryLandsExistingPullRequestAndCleansUp(t *testing.T) {
	fixture, receipt, published, head := newPublishedPrepareConflictFixture(t)
	gh := installWorktreeMergeEngineGH(t, fixture, published, receipt.Candidate.Branch)
	existing := fmt.Sprintf(`[{"number":41,"html_url":%q,"state":"open","head":{"sha":%q,"ref":%q,"repo":{"full_name":"acme/app"}},"base":{"ref":"main","repo":{"full_name":"acme/app"}}}]`,
		gh.pr, head, receipt.Candidate.Branch)
	if err := os.Setenv("WB_TEST_EXISTING_PR_HEAD", head); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("WB_TEST_EXISTING_PR_HEAD") })
	if err := os.Setenv("WB_TEST_EXISTING_PR_JSON", existing); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("WB_TEST_EXISTING_PR_JSON") })

	options := wmEngineLandOptions(fixture, receipt.ReceiptPath)
	options.Cleanup = true
	options.ValidateLocally = true
	landed, err := ResumeWorktreeMerge(context.Background(), options)
	if err != nil {
		t.Fatalf("actual published prepare landing failed: receipt=%+v error=%v", landed, err)
	}
	if landed.Status != WorktreeMergeComplete || landed.Candidate.SHA != head || landed.LandingSHA != head ||
		landed.PullRequest != gh.pr || landed.PublishedCandidateSHA != head || landed.Failure != "" {
		t.Fatalf("landed published receipt=%+v", landed)
	}
	if len(landed.CleanedTasks) == 0 || landed.CanonicalSync == "" {
		t.Fatalf("landed published receipt did not sync or clean: %+v", landed)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); got != head {
		t.Fatalf("canonical target=%s, want landed head %s", got, head)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); remote != head {
		t.Fatalf("landed target=%s, want %s", remote, head)
	}
	if _, err := os.Stat(filepath.Join(receipt.Candidate.Worktree, ".git")); !os.IsNotExist(err) {
		t.Fatalf("managed candidate worktree survived cleanup: %v", err)
	}
}
