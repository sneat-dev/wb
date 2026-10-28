//go:build e2e

package orchestrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/quality"
)

// TestE2EPublishedPrepareRefreshConflictPreservesRecordedLocalRepairAndRefusesItsLoss
// is the recorded-candidate root-cause regression. A validated local repair Q
// descends the published predecessor P but was never published. A later prepare
// refresh fails while integrating an advanced source S: the receipt must still
// name Q, never replace it with P. A resolution containing P+Q+S may recover; a
// replacement head that keeps P+S but drops the Q-only repair must refuse
// without mutating the candidate or publication identity. The same refusal has
// to hold for the legacy empty-candidate receipt when Q is still recorded as
// its own validated revision.
//
//nolint:paralleltest // the scripted provider changes PATH/XDG and the private lane is shared
func TestE2EPublishedPrepareRefreshConflictPreservesRecordedLocalRepairAndRefusesItsLoss(t *testing.T) {
	fixture := newEngineFixture(t)
	sourceA := createMergeSource(t, fixture, "recorded-repair-a", "feature/recorded-repair-a", "shared.txt", "a\n")
	sourceB := createMergeSource(t, fixture, "recorded-repair-b", "feature/recorded-repair-b", "b-other.txt", "b\n")
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

	// A validated local repair Q descends P and adds work that was never
	// published. The prior receipt therefore records Q as its candidate while
	// the pull request and remote branch still stand at P.
	writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "local-repair.txt"), "validated local repair, not yet published\n")
	runEngineGit(t, receipt.Candidate.Worktree, "add", "local-repair.txt")
	runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "fix: validated local repair that is not yet published")
	repair := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))
	validated := receipt
	validated.Candidate.SHA = repair
	validated.Validation, validated.BaselineValidation = quality.VerificationReport{}, quality.VerificationReport{}
	validated.ValidationIdentity, validated.ValidationDeferral = nil, nil
	if err := validateWorktreeMergeCandidate(context.Background(), &validated, 30*time.Second, 0, 0, 0, nil); err != nil {
		t.Fatalf("validate the local repair: %v", err)
	}
	if validated.Validation.Status != quality.StatusPassed || validated.Validation.Revision != repair {
		t.Fatalf("local repair validation=%+v", validated.Validation)
	}
	receipt.Validation, receipt.BaselineValidation = validated.Validation, validated.BaselineValidation
	receipt.ValidationIdentity, receipt.ValidationDeferral = validated.ValidationIdentity, validated.ValidationDeferral
	receipt.Candidate.SHA = repair
	receipt.Phase, receipt.Status = WorktreeMergePhasePrepare, WorktreeMergePrepared
	receipt.PullRequest, receipt.PublishedCandidateSHA = "https://example.test/acme/app/pull/41", published
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}

	// The advanced source S makes the refresh conflict.
	writeEngineFile(t, filepath.Join(sourceA.WorktreeDir, "shared.txt"), "a-advanced\n")
	runEngineGit(t, sourceA.WorktreeDir, "add", "shared.txt")
	runEngineGit(t, sourceA.WorktreeDir, "commit", "-m", "feat: advance the conflicting recorded-repair source")
	advanced := strings.TrimSpace(runEngineGit(t, sourceA.WorktreeDir, "rev-parse", "HEAD"))
	failed, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{sourceA.WorktreeDir, sourceB.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err == nil || failed.Status != WorktreeMergeConflict || failed.Phase != WorktreeMergePhasePrepare || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("failed recorded-repair refresh=%+v error=%v", failed, err)
	}
	// Root cause: the refresh keeps Q. Recording P here would erase the
	// recorded intent and let a P+S resolution silently drop the repair.
	if failed.Candidate.SHA != repair || failed.PublishedCandidateSHA != published ||
		failed.PullRequest != receipt.PullRequest || failed.Sources[0].SHA != advanced {
		t.Fatalf("failed recorded-repair refresh lost Q: %+v", failed)
	}
	stored, err := readWorktreeMergeReceipt(receipt.ReceiptPath)
	if err != nil || stored.Candidate.SHA != repair || stored.PublishedCandidateSHA != published ||
		stored.PullRequest != receipt.PullRequest || stored.Status != WorktreeMergeConflict || stored.Sources[0].SHA != advanced {
		t.Fatalf("durable failed recorded-repair refresh=%+v error=%v", stored, err)
	}
	if head := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD")); head != repair {
		t.Fatalf("failed refresh left the candidate at %s, want preserved repair %s", head, repair)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+receipt.Candidate.Branch)); remote != published {
		t.Fatalf("failed refresh moved the published branch: %s", remote)
	}

	gh := installWorktreeMergeEngineGH(t, fixture, published, receipt.Candidate.Branch)
	options := wmEngineLandOptions(fixture, receipt.ReceiptPath)
	options.StopBeforeMerge = true
	options.ValidateLocally = true

	// A replacement head containing P+S+target but not Q must refuse.
	dropped := resolvePublishedRepairMerge(t, receipt.Candidate.Worktree, published, advanced, "a-advanced\nb\n")
	refused, refuseErr := ResumeWorktreeMerge(context.Background(), options)
	if refuseErr == nil || !strings.Contains(refuseErr.Error(), "does not contain required immutable root") || !strings.Contains(refuseErr.Error(), repair) {
		t.Fatalf("repair-dropping resume=%+v error=%v", refused, refuseErr)
	}
	if refused.Candidate.SHA != repair || refused.PublishedCandidateSHA != published || refused.PullRequest != gh.pr || refused.LandingSHA != "" {
		t.Fatalf("repair-dropping refusal changed identity: %+v", refused)
	}
	if head := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD")); head != dropped {
		t.Fatalf("refusal moved the replacement head from %s to %s", dropped, head)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+receipt.Candidate.Branch)); remote != published {
		t.Fatalf("repair-dropping refusal moved the published branch: %s", remote)
	}

	// The legacy shape records no candidate SHA at all, but the same repair is
	// still available through its validated revision. It must be preserved and
	// proved too, not waived because the candidate field is empty.
	legacy, err := readWorktreeMergeReceipt(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	legacy.Candidate.SHA = ""
	legacy.Validation = quality.VerificationReport{Repository: legacy.Repository, Path: "git:" + repair, Revision: repair, Status: quality.StatusPassed, WorkspaceClean: true}
	if err := persistWorktreeMergeReceipt(legacy); err != nil {
		t.Fatal(err)
	}
	legacyRefused, legacyErr := ResumeWorktreeMerge(context.Background(), options)
	if legacyErr == nil || !strings.Contains(legacyErr.Error(), "does not contain required immutable root") || !strings.Contains(legacyErr.Error(), repair) {
		t.Fatalf("legacy repair-dropping resume=%+v error=%v", legacyRefused, legacyErr)
	}
	if legacyRefused.Candidate.SHA != "" || legacyRefused.PublishedCandidateSHA != published || legacyRefused.PullRequest != gh.pr || legacyRefused.LandingSHA != "" {
		t.Fatalf("legacy repair-dropping refusal changed identity: %+v", legacyRefused)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+receipt.Candidate.Branch)); remote != published {
		t.Fatalf("legacy refusal moved the published branch: %s", remote)
	}

	// A clean resolution H that keeps P and Q and integrates S may recover and
	// publish by fast-forward only.
	resolved := resolvePublishedRepairMerge(t, receipt.Candidate.Worktree, repair, advanced, "a-advanced\nb\n")
	publishedResume, err := ResumeWorktreeMerge(context.Background(), options)
	if err != nil || publishedResume.Status != WorktreeMergePublished || publishedResume.Candidate.SHA != resolved ||
		publishedResume.PublishedCandidateSHA != resolved || publishedResume.PullRequest != gh.pr || publishedResume.Validation.Revision != resolved {
		t.Fatalf("recorded-repair resolution resume=%+v error=%v", publishedResume, err)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+receipt.Candidate.Branch)); remote != resolved {
		t.Fatalf("resolved published branch=%s, want %s", remote, resolved)
	}
	for _, root := range []string{published, repair, advanced, receipt.TargetSHA} {
		assertNativeAncestor(t, fixture.repository.CloneURL, root, resolved)
	}
}

// TestE2EPublishedPrepareConflictRecoveryResumesAfterPlanResolutionFailure is
// the retry-trap regression for the published recovery. The proof records the
// clean resolved head H in memory before the route/validation plan is resolved,
// so a plan-resolution failure must not persist H as a prepare conflict: this
// recovery only re-runs for an empty or source-missing candidate, nothing else
// would re-validate H, and the publish guard would refuse the stale repair
// validation forever. The failure must leave the recorded repair Q, the
// published predecessor P, the open pull request, the remote branch, and the
// target exactly as recorded. Removing only that plan failure then resumes
// through the actual entry point, validates exact H, and publishes H by
// fast-forward onto the same existing pull request.
//
//nolint:paralleltest // the scripted provider changes PATH/XDG and the private lane is shared
func TestE2EPublishedPrepareConflictRecoveryResumesAfterPlanResolutionFailure(t *testing.T) {
	fixture, receipt, published, repair, advanced := newPublishedPrepareRepairConflictFixture(t)
	resolved := resolvePublishedRepairMerge(t, receipt.Candidate.Worktree, repair, advanced, "a-advanced\nb\n")
	gh := installWorktreeMergeEngineGH(t, fixture, published, receipt.Candidate.Branch)
	target := receipt.TargetSHA

	options := wmEngineLandOptions(fixture, receipt.ReceiptPath)
	options.Route, options.ValidateLocally = WorktreeMergeRouteDirect, true
	failed, planErr := ResumeWorktreeMerge(context.Background(), options)
	if planErr == nil || !strings.Contains(planErr.Error(), "direct route is not authoritatively permitted") {
		t.Fatalf("published recovery plan refusal=%+v error=%v", failed, planErr)
	}
	// The failure is planning, not candidate evidence: Q, P, the pull request,
	// the target, and the validated repair record all stay exactly as the failed
	// refresh recorded them, and H is never recorded as a conflicted candidate.
	if failed.Status != WorktreeMergeConflict || failed.Candidate.SHA != repair ||
		failed.PublishedCandidateSHA != published || failed.PullRequest != gh.pr ||
		failed.TargetSHA != target || failed.LandingSHA != "" || failed.Validation.Revision != repair {
		t.Fatalf("plan refusal changed recorded identity: %+v", failed)
	}
	stored, err := readWorktreeMergeReceipt(receipt.ReceiptPath)
	if err != nil || stored.Status != WorktreeMergeConflict || stored.Candidate.SHA != repair ||
		stored.PublishedCandidateSHA != published || stored.PullRequest != gh.pr ||
		stored.TargetSHA != target || stored.LandingSHA != "" {
		t.Fatalf("durable plan refusal=%+v error=%v", stored, err)
	}
	if head := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD")); head != resolved {
		t.Fatalf("plan refusal moved the clean resolution from %s to %s", resolved, head)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+receipt.Candidate.Branch)); remote != published {
		t.Fatalf("plan refusal pushed the candidate branch to %s, want %s", remote, published)
	}
	if live := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); live != target {
		t.Fatalf("plan refusal moved the target to %s, want %s", live, target)
	}
	if log := gh.ghLog(t); strings.Contains(log, "pr create") || strings.Contains(log, "pulls/41/merge") || strings.Contains(log, "update-branch") {
		t.Fatalf("plan refusal performed a pull-request write:\n%s", log)
	}

	// Removing only the plan failure (the route this target's own policy
	// permits) re-proves the publication, validates exact H, and publishes it
	// by fast-forward onto the same open pull request.
	options.Route, options.StopBeforeMerge = WorktreeMergeRoutePullRequest, true
	publishedResume, err := ResumeWorktreeMerge(context.Background(), options)
	if err != nil || publishedResume.Status != WorktreeMergePublished || publishedResume.Candidate.SHA != resolved ||
		publishedResume.PublishedCandidateSHA != resolved || publishedResume.PullRequest != gh.pr ||
		publishedResume.LandingSHA != "" || publishedResume.Failure != "" ||
		publishedResume.Validation.Revision != resolved || publishedResume.Validation.Status != quality.StatusPassed {
		t.Fatalf("resumed published recovery=%+v error=%v", publishedResume, err)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+receipt.Candidate.Branch)); remote != resolved {
		t.Fatalf("published recovery branch=%s, want %s", remote, resolved)
	}
	if live := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); live != target {
		t.Fatalf("published recovery moved the target: %s", live)
	}
	assertNativeAncestor(t, fixture.repository.CloneURL, published, resolved)
	assertNativeAncestor(t, fixture.repository.CloneURL, repair, resolved)
	stored, err = readWorktreeMergeReceipt(receipt.ReceiptPath)
	if err != nil || stored.Status != WorktreeMergePublished || stored.Candidate.SHA != resolved ||
		stored.PublishedCandidateSHA != resolved || stored.PullRequest != gh.pr || stored.Validation.Revision != resolved {
		t.Fatalf("durable published recovery=%+v error=%v", stored, err)
	}
}

// TestE2EOrdinaryPreparingPlanFailureResumesNormalJourney is the ordinary
// preparing validation-block retry regression. A managed exact preparing
// candidate H already contains the recorded repair Q, every current source, and
// the recorded target, while the predecessor/repair validation is still
// recorded at Q. A route/validation-plan failure on the first resume is
// pre-validation planning, not candidate evidence: the stored status must stay
// preparing (never conflict), because a conflict for a head that already
// contains every source has no recovery path that re-proves it, and the
// publication guard would then refuse the stale Q validation forever.
//
// Removing only that planning failure (the route this target's policy permits)
// must let the SAME ordinary resume validate exact H and land it through the
// existing pull request onto the real target. StopBeforeMerge stays false
// explicitly: the optional publish-only guard must not mask a stale-validation
// defect by stopping before the actual merge.
//
//nolint:paralleltest // the scripted provider changes PATH/XDG and the private lane is shared
func TestE2EOrdinaryPreparingPlanFailureResumesNormalJourney(t *testing.T) {
	fixture, receipt, published, repair, advanced := newPublishedPrepareRepairConflictFixture(t)
	resolved := resolvePublishedRepairMerge(t, receipt.Candidate.Worktree, repair, advanced, "a-advanced\nb\n")
	gh := installWorktreeMergeEngineGH(t, fixture, published, receipt.Candidate.Branch)
	// The receipt already owns this open publication, so the provider must
	// report it as existing: adopting it is the only correct continuation and
	// any `pr create` would be a replacement pull request.
	gh.writeState(t, "pr-created", "1")
	target := receipt.TargetSHA

	// The durable ordinary preparing shape: exact guarded H recorded as the
	// interrupted candidate, the open pull request still at P, and the prior
	// validation still recorded at Q.
	receipt.Phase, receipt.Status = WorktreeMergePhasePrepare, WorktreeMergePreparing
	receipt.Candidate.SHA = resolved
	receipt.Failure = ""
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}

	options := wmEngineLandOptions(fixture, receipt.ReceiptPath)
	options.Route, options.ValidateLocally = WorktreeMergeRouteDirect, true
	options.StopBeforeMerge = false
	failed, planErr := ResumeWorktreeMerge(context.Background(), options)
	if planErr == nil || !strings.Contains(planErr.Error(), "direct route is not authoritatively permitted") {
		t.Fatalf("preparing plan refusal=%+v error=%v", failed, planErr)
	}
	// The failure stays a preparing receipt: the exact head, the recorded
	// repair validation, the publication, and the target are unchanged, and
	// nothing was pushed or written to the pull request.
	if failed.Status != WorktreeMergePreparing || failed.Candidate.SHA != resolved ||
		failed.PublishedCandidateSHA != published || failed.PullRequest != gh.pr ||
		failed.TargetSHA != target || failed.LandingSHA != "" || failed.Validation.Revision != repair {
		t.Fatalf("preparing plan refusal changed recorded identity: %+v", failed)
	}
	stored, err := readWorktreeMergeReceipt(receipt.ReceiptPath)
	if err != nil || stored.Status != WorktreeMergePreparing || stored.Candidate.SHA != resolved ||
		stored.PublishedCandidateSHA != published || stored.PullRequest != gh.pr ||
		stored.TargetSHA != target || stored.LandingSHA != "" || stored.Validation.Revision != repair {
		t.Fatalf("durable preparing plan refusal=%+v error=%v", stored, err)
	}
	if head := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD")); head != resolved {
		t.Fatalf("preparing plan refusal moved the guarded head from %s to %s", resolved, head)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+receipt.Candidate.Branch)); remote != published {
		t.Fatalf("preparing plan refusal pushed the candidate branch to %s, want %s", remote, published)
	}
	if live := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); live != target {
		t.Fatalf("preparing plan refusal moved the target to %s, want %s", live, target)
	}
	if log := gh.ghLog(t); strings.Contains(log, "pr create") || strings.Contains(log, "pulls/41/merge") || strings.Contains(log, "update-branch") {
		t.Fatalf("preparing plan refusal performed a pull-request write:\n%s", log)
	}

	// Removing only the planning failure lets the same ordinary resume adopt
	// the existing pull request, validate exact H, fast-forward the published
	// branch, and merge H onto the real target.
	options.Route = WorktreeMergeRoutePullRequest
	options.StopBeforeMerge = false
	landed, err := ResumeWorktreeMerge(context.Background(), options)
	if err != nil || landed.Status != WorktreeMergeLanded || landed.Candidate.SHA != resolved ||
		landed.PublishedCandidateSHA != resolved || landed.PullRequest != gh.pr ||
		landed.LandingSHA != resolved || landed.Failure != "" ||
		landed.Validation.Revision != resolved || landed.Validation.Status != quality.StatusPassed {
		t.Fatalf("normal preparing resume=%+v error=%v", landed, err)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+receipt.Candidate.Branch)); remote != resolved {
		t.Fatalf("normal resume published branch=%s, want %s", remote, resolved)
	}
	if live := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); live != resolved {
		t.Fatalf("normal resume landed target=%s, want %s", live, resolved)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); got != resolved {
		t.Fatalf("normal resume canonical target=%s, want %s", got, resolved)
	}
	assertNativeAncestor(t, fixture.repository.CloneURL, published, resolved)
	if log := gh.ghLog(t); strings.Contains(log, "pr create") ||
		!strings.Contains(log, "api --method PUT repos/acme/app/pulls/41/merge") || !strings.Contains(log, "sha="+resolved) {
		t.Fatalf("normal resume did not merge the same existing pull request:\n%s", log)
	}
	stored, err = readWorktreeMergeReceipt(receipt.ReceiptPath)
	if err != nil || stored.Status != WorktreeMergeLanded || stored.Candidate.SHA != resolved ||
		stored.PublishedCandidateSHA != resolved || stored.PullRequest != gh.pr ||
		stored.LandingSHA != resolved || stored.Validation.Revision != resolved {
		t.Fatalf("durable normal resume=%+v error=%v", stored, err)
	}
}

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
