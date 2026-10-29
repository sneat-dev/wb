//go:build e2e

package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// legacyPublishedAdvanceFixture is the exact legacy shape the prepare bridge
// owns, built from real private Git: the published predecessor P is open in a
// pull request, the receipt still records the old source snapshot S, and the
// clean managed candidate HEAD H already resolved P+S+target but does not
// contain the strictly advanced current source S-prime.
type legacyPublishedAdvanceFixture struct {
	engineFixture
	receipt         WorktreeMergeReceipt
	source          string
	published       string
	candidateBranch string
	initialSource   string
	oldSource       string
	resolvedHead    string
	currentSource   string
}

func newLegacyPublishedAdvanceFixture(t *testing.T) legacyPublishedAdvanceFixture {
	t.Helper()
	return newLegacyPublishedAdvanceFixtureFrom(t, newEngineFixture(t))
}

func newLegacyPublishedAdvanceFixtureFrom(t *testing.T, fixture engineFixture) legacyPublishedAdvanceFixture {
	t.Helper()
	source := createMergeSource(t, fixture, "legacy-published-source", "feature/legacy-published", "source.txt", "source\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	published := receipt.Candidate.SHA
	initialSource := receipt.Sources[0]
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", published+":refs/heads/"+receipt.Candidate.Branch)
	// A candidate-only repair keeps H off the source's own first-parent line,
	// so the advanced source can never accidentally contain the resolved head.
	writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "local-repair.txt"), "local repair\n")
	runEngineGit(t, receipt.Candidate.Worktree, "add", "local-repair.txt")
	runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "fix: local repair recorded by the published candidate")
	repair := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))

	// S is the receipted old source snapshot. H merges it into the repaired
	// candidate and is the clean resolution a failed prepare refresh left in
	// the managed candidate.
	writeEngineFile(t, filepath.Join(source.WorktreeDir, "advance-old.txt"), "old advance\n")
	runEngineGit(t, source.WorktreeDir, "add", "advance-old.txt")
	runEngineGit(t, source.WorktreeDir, "commit", "-m", "feat: advance the legacy published source")
	oldSource := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	runEngineGit(t, receipt.Candidate.Worktree, "merge", "--no-edit", oldSource)
	resolvedHead := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))

	receipt.Phase, receipt.Status = WorktreeMergePhasePrepare, WorktreeMergeConflict
	receipt.Sources[0].SHA = oldSource
	receipt.Candidate.SHA = ""
	receipt.PullRequest = "https://example.test/acme/app/pull/41"
	receipt.PublishedCandidateSHA = published
	receipt.Validation = quality.VerificationReport{Repository: receipt.Repository, Path: "git:" + repair, Revision: repair, Status: quality.StatusPassed, WorkspaceClean: true}
	receipt.SourceRefreshes = []WorktreeMergeSourceRefresh{{RecordedAt: receipt.CreatedAt, Sources: []WorktreeMergeSource{initialSource}}}
	receipt.Failure = "prepare refresh failed before recording the resolved head"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}

	// S-prime descends S but is not contained by H: ordinary prepare must
	// integrate it into H rather than demand the source contain H.
	writeEngineFile(t, filepath.Join(source.WorktreeDir, "advance-prime.txt"), "prime advance\n")
	runEngineGit(t, source.WorktreeDir, "add", "advance-prime.txt")
	runEngineGit(t, source.WorktreeDir, "commit", "-m", "feat: advance the legacy published source again")
	currentSource := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	if got := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "merge-base", resolvedHead, currentSource)); got == resolvedHead {
		t.Fatalf("fixture current source contains the resolved candidate head")
	}

	return legacyPublishedAdvanceFixture{
		engineFixture: fixture, receipt: receipt, source: source.WorktreeDir, published: published,
		candidateBranch: receipt.Candidate.Branch,
		initialSource:   initialSource.SHA, oldSource: oldSource, resolvedHead: resolvedHead, currentSource: currentSource,
	}
}

func prepareLegacyPublishedAdvance(t *testing.T, fixture legacyPublishedAdvanceFixture) (WorktreeMergeReceipt, error) {
	t.Helper()
	return PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{fixture.source}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
}

// TestPrepareLegacyPublishedAdvanceIntegratesAdvancedSourceIntoResolvedHead
// drives the actual normal PrepareWorktreeMerge entry point over the example
// shape: H is not rewound and the current source is not required to contain it.
// The proven resolved head must be integrated with S-prime, validated, and
// persisted without publishing anything or replacing the open pull request.
//
//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestE2EPrepareLegacyPublishedAdvanceIntegratesAdvancedSourceIntoResolvedHead(t *testing.T) {
	fixture := newLegacyPublishedAdvanceFixture(t)
	gh := installWorktreeMergeEngineGH(t, fixture.engineFixture, fixture.published, fixture.receipt.Candidate.Branch)
	target := fixture.receipt.TargetSHA

	prepared, err := prepareLegacyPublishedAdvance(t, fixture)
	if err != nil {
		t.Fatalf("actual normal prepare failed: receipt=%+v error=%v", prepared, err)
	}
	if prepared.Status != WorktreeMergePrepared || prepared.Phase != WorktreeMergePhasePrepare ||
		prepared.Candidate.SHA == "" || prepared.Candidate.SHA == fixture.resolvedHead ||
		prepared.PublishedCandidateSHA != fixture.published || prepared.PullRequest != gh.pr ||
		prepared.TargetSHA != target || prepared.LandingSHA != "" || prepared.Failure != "" {
		t.Fatalf("prepared legacy published receipt=%+v", prepared)
	}
	head := prepared.Candidate.SHA
	if got := strings.TrimSpace(runEngineGit(t, prepared.Candidate.Worktree, "rev-parse", "HEAD")); got != head {
		t.Fatalf("prepared candidate head=%s, want recorded %s", got, head)
	}
	for _, root := range []string{fixture.published, fixture.resolvedHead, fixture.oldSource, fixture.currentSource, target} {
		assertNativeAncestor(t, prepared.Candidate.Worktree, root, head)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+prepared.Candidate.Branch)); got != fixture.published {
		t.Fatalf("prepare published the candidate branch to %s, want unchanged predecessor %s", got, fixture.published)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); got != target {
		t.Fatalf("prepare moved the target to %s, want %s", got, target)
	}
	if log := gh.ghLog(t); strings.Contains(log, "pr create") || strings.Contains(log, "pulls/41/merge") || strings.Contains(log, "update-branch") {
		t.Fatalf("prepare performed a pull-request write:\n%s", log)
	}
	stored, err := readWorktreeMergeReceipt(fixture.receipt.ReceiptPath)
	if err != nil || stored.Status != WorktreeMergePrepared || stored.Candidate.SHA != head ||
		stored.PublishedCandidateSHA != fixture.published || stored.PullRequest != gh.pr || stored.TargetSHA != target ||
		len(stored.Sources) != 1 || stored.Sources[0].SHA != fixture.currentSource ||
		len(stored.SourceRefreshes) != 2 ||
		stored.SourceRefreshes[0].Sources[0].SHA != fixture.initialSource ||
		stored.SourceRefreshes[1].Sources[0].SHA != fixture.oldSource {
		t.Fatalf("durable prepared receipt=%+v error=%v", stored, err)
	}
}

// TestResumeLegacyPublishedAdvanceStillRefusesExactSourceDrift keeps the
// exact-source resume owner unchanged: an advanced source is never permission
// to record H there, and the advanced-source bridge belongs to normal prepare
// only.
//
//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestE2EResumeLegacyPublishedAdvanceStillRefusesExactSourceDrift(t *testing.T) {
	fixture := newLegacyPublishedAdvanceFixture(t)
	gh := installWorktreeMergeEngineGH(t, fixture.engineFixture, fixture.published, fixture.receipt.Candidate.Branch)
	before := fixture.receipt

	options := wmEngineLandOptions(fixture.engineFixture, fixture.receipt.ReceiptPath)
	options.StopBeforeMerge = true
	options.ValidateLocally = true
	refused, err := ResumeWorktreeMerge(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "advanced from "+fixture.oldSource+" to "+fixture.currentSource) {
		t.Fatalf("advanced-source resume=%+v error=%v", refused, err)
	}
	if refused.Candidate.SHA != "" || refused.PublishedCandidateSHA != fixture.published ||
		refused.PullRequest != gh.pr || refused.Status != WorktreeMergeConflict || refused.LandingSHA != "" ||
		len(refused.Sources) != 1 || refused.Sources[0].SHA != fixture.oldSource {
		t.Fatalf("advanced-source refusal mutated candidate/publication identity: %+v", refused)
	}
	stored, readErr := readWorktreeMergeReceipt(before.ReceiptPath)
	if readErr != nil || stored.Candidate.SHA != "" || stored.PublishedCandidateSHA != fixture.published ||
		stored.PullRequest != gh.pr || stored.Status != WorktreeMergeConflict ||
		stored.Sources[0].SHA != fixture.oldSource || stored.TargetSHA != before.TargetSHA {
		t.Fatalf("durable advanced-source refusal=%+v error=%v", stored, readErr)
	}
	if head := strings.TrimSpace(runEngineGit(t, fixture.receipt.Candidate.Worktree, "rev-parse", "HEAD")); head != fixture.resolvedHead {
		t.Fatalf("advanced-source refusal moved the resolved head from %s to %s", fixture.resolvedHead, head)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+before.Candidate.Branch)); remote != fixture.published {
		t.Fatalf("advanced-source refusal moved the published branch to %s, want %s", remote, fixture.published)
	}
}

// TestPrepareLegacyPublishedUnchangedSourceKeepsExactResumeOwner proves the
// new advanced-source permission is not granted to an unchanged source: normal
// prepare still refuses the occupied legacy lane, and exact-source resume
// remains the only owner that may publish the already resolved head.
//
//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestE2EPrepareLegacyPublishedUnchangedSourceKeepsExactResumeOwner(t *testing.T) {
	fixture, receipt, published, head := newPublishedPrepareConflictFixture(t)
	gh := installWorktreeMergeEngineGH(t, fixture, published, receipt.Candidate.Branch)

	refused, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{receipt.Sources[0].Worktree}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err == nil || !strings.Contains(err.Error(), "still owned by non-terminal receipt") || refused.Candidate.SHA != "" {
		t.Fatalf("unchanged-source prepare=%+v error=%v", refused, err)
	}
	stored, readErr := readWorktreeMergeReceipt(receipt.ReceiptPath)
	if readErr != nil || stored.Candidate.SHA != "" || stored.Status != WorktreeMergeConflict ||
		stored.PublishedCandidateSHA != published || stored.Sources[0].SHA != receipt.Sources[0].SHA {
		t.Fatalf("unchanged-source prepare mutated the receipt=%+v error=%v", stored, readErr)
	}

	options := wmEngineLandOptions(fixture, receipt.ReceiptPath)
	options.StopBeforeMerge = true
	options.ValidateLocally = true
	resumed, err := ResumeWorktreeMerge(context.Background(), options)
	if err != nil || resumed.Status != WorktreeMergePublished || resumed.Candidate.SHA != head ||
		resumed.PublishedCandidateSHA != head || resumed.PullRequest != gh.pr {
		t.Fatalf("unchanged-source exact resume=%+v error=%v", resumed, err)
	}
}

func assertLegacyPublicationIdentityUnchanged(t *testing.T, receipt WorktreeMergeReceipt, before WorktreeMergeReceipt, durable WorktreeMergeReceipt) {
	t.Helper()
	if receipt.Candidate.SHA != before.Candidate.SHA || receipt.PublishedCandidateSHA != before.PublishedCandidateSHA ||
		receipt.PullRequest != before.PullRequest || receipt.TargetSHA != before.TargetSHA ||
		len(receipt.Sources) != len(before.Sources) {
		t.Fatalf("refusal mutated in-memory publication identity: %+v", receipt)
	}
	if durable.Candidate.SHA != before.Candidate.SHA || durable.PublishedCandidateSHA != before.PublishedCandidateSHA ||
		durable.PullRequest != before.PullRequest || durable.TargetSHA != before.TargetSHA ||
		durable.Status != before.Status || len(durable.Sources) != len(before.Sources) {
		t.Fatalf("refusal mutated durable publication identity: %+v", durable)
	}
	for index := range before.Sources {
		if durable.Sources[index].SHA != before.Sources[index].SHA {
			t.Fatalf("refusal mutated durable source %d: %+v", index, durable.Sources[index])
		}
	}
}

// unrelatedLegacyRoot returns a real commit that is not an ancestor of the
// fixture's resolved candidate head.
func unrelatedLegacyRoot(t *testing.T, fixture engineFixture, name string) string {
	t.Helper()
	source := createMergeSource(t, fixture, name, "feature/"+name, name+".txt", name+"\n")
	return strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
}

// TestPrepareLegacyPublishedAdvanceMergeFailureKeepsResolvedHeadAndIntent
// proves the bridge captures the proven H before integration: a conflicting
// S-prime must leave a truthful conflict receipt naming H, the new source, and
// the old refresh history, with the publication, remote predecessor, and
// target untouched. A repeated attempt must not erase that intent.
//
//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestE2EPrepareLegacyPublishedAdvanceMergeFailureKeepsResolvedHeadAndIntent(t *testing.T) {
	fixture := newLegacyPublishedAdvanceFixture(t)
	gh := installWorktreeMergeEngineGH(t, fixture.engineFixture, fixture.published, fixture.receipt.Candidate.Branch)
	target := fixture.receipt.TargetSHA
	writeEngineFile(t, filepath.Join(fixture.source, "local-repair.txt"), "source-side repair that conflicts\n")
	runEngineGit(t, fixture.source, "add", "local-repair.txt")
	runEngineGit(t, fixture.source, "commit", "-m", "feat: conflict with the resolved local repair")
	conflictingSource := strings.TrimSpace(runEngineGit(t, fixture.source, "rev-parse", "HEAD"))

	failed, err := prepareLegacyPublishedAdvance(t, fixture)
	if err == nil || !strings.Contains(err.Error(), "merge conflict while integrating") {
		t.Fatalf("conflicting legacy prepare=%+v error=%v", failed, err)
	}
	if failed.Status != WorktreeMergeConflict || failed.Candidate.SHA != fixture.resolvedHead ||
		failed.PublishedCandidateSHA != fixture.published || failed.PullRequest != gh.pr ||
		failed.TargetSHA != target || failed.LandingSHA != "" ||
		len(failed.Sources) != 1 || failed.Sources[0].SHA != conflictingSource {
		t.Fatalf("merge failure lost the proven resolved head: %+v", failed)
	}
	if len(failed.SourceRefreshes) != 2 ||
		failed.SourceRefreshes[0].Sources[0].SHA != fixture.initialSource ||
		failed.SourceRefreshes[1].Sources[0].SHA != fixture.oldSource {
		t.Fatalf("merge failure lost source history: %+v", failed.SourceRefreshes)
	}
	if head := strings.TrimSpace(runEngineGit(t, failed.Candidate.Worktree, "rev-parse", "HEAD")); head != fixture.resolvedHead {
		t.Fatalf("merge failure moved the resolved head from %s to %s", fixture.resolvedHead, head)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+fixture.candidateBranch)); remote != fixture.published {
		t.Fatalf("merge failure moved the published branch to %s, want %s", remote, fixture.published)
	}
	if live := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); live != target {
		t.Fatalf("merge failure moved the target to %s, want %s", live, target)
	}
	before := failed
	repeated, repeatErr := prepareLegacyPublishedAdvance(t, fixture)
	if repeatErr == nil {
		t.Fatalf("repeated merge failure unexpectedly succeeded: %+v", repeated)
	}
	stored, readErr := readWorktreeMergeReceipt(failed.ReceiptPath)
	if readErr != nil || stored.Status != WorktreeMergeConflict || stored.Candidate.SHA != before.Candidate.SHA ||
		stored.PublishedCandidateSHA != fixture.published || stored.PullRequest != gh.pr ||
		stored.Sources[0].SHA != conflictingSource || len(stored.SourceRefreshes) != 2 {
		t.Fatalf("repeated merge failure erased intent=%+v error=%v", stored, readErr)
	}
	if log := gh.ghLog(t); strings.Contains(log, "pr create") || strings.Contains(log, "pulls/41/merge") || strings.Contains(log, "update-branch") {
		t.Fatalf("merge failure performed a pull-request write:\n%s", log)
	}
}

// TestPrepareLegacyPublishedAdvanceValidationFailureRetainsHPrimeAndIntent
// proves the bridge persists the integrated H-prime before validation: a real
// validation failure must keep the exact head, H, the advanced source, every
// old source snapshot, the same open publication, and an unmoved target.
//
//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestE2EPrepareLegacyPublishedAdvanceValidationFailureRetainsHPrimeAndIntent(t *testing.T) {
	fixture := newEngineFixture(t)
	writeEngineGoModule(t, fixture.canonical, "package app\n")
	runEngineGit(t, fixture.canonical, "add", "go.mod", "app.go")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: native validation baseline")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	advance := newLegacyPublishedAdvanceFixtureFrom(t, fixture)
	gh := installWorktreeMergeEngineGH(t, advance.engineFixture, advance.published, advance.receipt.Candidate.Branch)
	target := advance.receipt.TargetSHA

	writeEngineFile(t, filepath.Join(advance.source, "failing.go"), "package app\n\nfunc Broken() { missingLegacySymbol }\n")
	runEngineGit(t, advance.source, "add", "failing.go")
	runEngineGit(t, advance.source, "commit", "-m", "feat: add a real validation failure")
	advanced := strings.TrimSpace(runEngineGit(t, advance.source, "rev-parse", "HEAD"))

	failed, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: advance.githubDir, Sources: []string{advance.source}, Target: "main",
		Model: "test-model", AgentRuntime: "test", ValidateLocally: true,
	})
	if err == nil || failed.Status != WorktreeMergeValidationFailed || failed.Candidate.SHA == "" {
		t.Fatalf("failing legacy prepare=%+v error=%v", failed, err)
	}
	head := failed.Candidate.SHA
	if head == advance.resolvedHead {
		t.Fatalf("validation failure did not record the integrated H-prime: %+v", failed)
	}
	for _, root := range []string{advance.resolvedHead, advance.published, advance.oldSource, advance.currentSource, advanced, target} {
		assertNativeAncestor(t, failed.Candidate.Worktree, root, head)
	}
	if failed.PublishedCandidateSHA != advance.published || failed.PullRequest != gh.pr ||
		failed.TargetSHA != target || failed.LandingSHA != "" ||
		failed.Validation.Revision != head || failed.Validation.Status != quality.StatusFailed {
		t.Fatalf("validation failure lost publication or head evidence: %+v", failed)
	}
	if len(failed.SourceRefreshes) != 2 ||
		failed.SourceRefreshes[0].Sources[0].SHA != advance.initialSource ||
		failed.SourceRefreshes[1].Sources[0].SHA != advance.oldSource {
		t.Fatalf("validation failure lost source history: %+v", failed.SourceRefreshes)
	}
	if remote := strings.TrimSpace(runEngineGit(t, advance.repository.CloneURL, "rev-parse", "refs/heads/"+advance.candidateBranch)); remote != advance.published {
		t.Fatalf("validation failure moved the published branch to %s, want %s", remote, advance.published)
	}
	if live := strings.TrimSpace(runEngineGit(t, advance.repository.CloneURL, "rev-parse", "refs/heads/main")); live != target {
		t.Fatalf("validation failure moved the target to %s, want %s", live, target)
	}
	if log := gh.ghLog(t); strings.Contains(log, "pr create") || strings.Contains(log, "pulls/41/merge") || strings.Contains(log, "update-branch") {
		t.Fatalf("validation failure performed a pull-request write:\n%s", log)
	}

	stored, readErr := readWorktreeMergeReceipt(failed.ReceiptPath)
	if readErr != nil || stored.Status != WorktreeMergeValidationFailed || stored.Candidate.SHA != head ||
		stored.PublishedCandidateSHA != advance.published || stored.PullRequest != gh.pr ||
		stored.Sources[0].SHA != advanced || len(stored.SourceRefreshes) != 2 {
		t.Fatalf("durable validation failure=%+v error=%v", stored, readErr)
	}
	repeated, repeatedErr := prepareLegacyPublishedAdvance(t, advance)
	if repeatedErr == nil && repeated.Candidate.SHA != head {
		t.Fatalf("repeated validation attempt replaced the recorded head: %+v", repeated)
	}
	stored, readErr = readWorktreeMergeReceipt(failed.ReceiptPath)
	if readErr != nil || stored.Status != WorktreeMergeValidationFailed || stored.Candidate.SHA != head ||
		stored.PublishedCandidateSHA != advance.published || stored.Sources[0].SHA != advanced {
		t.Fatalf("repeated validation attempt erased intent=%+v error=%v", stored, readErr)
	}
}

// new normal-prepare entry point over every evidence class the shared 938
// proof owns: source custody and ancestry, candidate/canonical/claim custody,
// target and hosted pull-request identity, and every recorded root. A refusal
// must leave the receipt, the resolved head, and the publication untouched.
//
// TestRecoverLegacyPublishedPrepareAdvanceAdmitsOnlyTheAdvancedEmptyShapeUnit
// pins the bridge's own admission boundary and its locked source re-read at the
// shared proof seam: nil, non-prepare-conflict, and recorded-candidate receipts
// never enter, while a dirty current source is refused before H is adopted.
//
//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestE2ERecoverLegacyPublishedPrepareAdvanceAdmitsOnlyTheAdvancedEmptyShapeUnit(t *testing.T) {
	fixture := newLegacyPublishedAdvanceFixture(t)
	current := []WorktreeMergeSource{{Task: fixture.receipt.Sources[0].Task, Worktree: fixture.source, Branch: fixture.receipt.Sources[0].Branch, SHA: fixture.currentSource}}

	if changed, err := recoverLegacyPublishedPrepareAdvanceWithRunner(t.Context(), runner.New(), fixture.githubDir, nil, current, 0, 0); changed || err != nil {
		t.Fatalf("nil receipt changed=%v error=%v", changed, err)
	}
	landPhase := fixture.receipt
	landPhase.Phase = WorktreeMergePhaseLand
	if changed, err := recoverLegacyPublishedPrepareAdvanceWithRunner(t.Context(), runner.New(), fixture.githubDir, &landPhase, current, 0, 0); changed || err != nil {
		t.Fatalf("land-phase receipt changed=%v error=%v", changed, err)
	}
	recorded := fixture.receipt
	recorded.Candidate.SHA = fixture.resolvedHead
	before := recorded
	if changed, err := recoverLegacyPublishedPrepareAdvanceWithRunner(t.Context(), runner.New(), fixture.githubDir, &recorded, current, 0, 0); changed || err != nil || recorded.Candidate.SHA != before.Candidate.SHA {
		t.Fatalf("recorded-candidate receipt changed=%v error=%v receipt=%+v", changed, err, recorded)
	}

	dirty := fixture.receipt
	writeEngineFile(t, filepath.Join(fixture.source, "dirty.txt"), "dirty\n")
	beforeDirty := dirty
	changed, err := recoverLegacyPublishedPrepareAdvanceWithRunner(t.Context(), runner.New(), fixture.githubDir, &dirty, current, 0, 0)
	if changed || err == nil || !strings.Contains(err.Error(), "worktree is dirty") || dirty.Candidate.SHA != "" {
		t.Fatalf("dirty current source changed=%v error=%v receipt=%+v", changed, err, dirty)
	}
	if dirty.Candidate.SHA != beforeDirty.Candidate.SHA || dirty.Status != beforeDirty.Status || dirty.Failure != beforeDirty.Failure {
		t.Fatalf("dirty current source mutated the receipt: %+v", dirty)
	}

	sentinel := errors.New("controlled source ancestry read refused")
	run := conflictNegativeRunner{Runner: runner.New(), dir: fixture.source, argv: "merge-base " + fixture.oldSource + " " + fixture.currentSource, err: sentinel}
	negative := fixture.receipt
	beforeNegative := negative
	changed, err = recoverLegacyPublishedPrepareAdvanceWithRunner(t.Context(), run, fixture.githubDir, &negative, current, 0, 0)
	if changed || !errors.Is(err, sentinel) || negative.Candidate.SHA != beforeNegative.Candidate.SHA {
		t.Fatalf("negative ancestry changed=%v error=%v receipt=%+v", changed, err, negative)
	}
}

// TestPrepareLegacyPublishedAdvanceReProvesUnderTheLaneLock covers the
// post-lock re-proof: a receipt that changes between the pre-lock decision and
// the locked re-read must be refused before any source is integrated.
//
//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestE2EPrepareLegacyPublishedAdvanceReProvesUnderTheLaneLock(t *testing.T) {
	fixture := newLegacyPublishedAdvanceFixture(t)
	gh := installWorktreeMergeEngineGH(t, fixture.engineFixture, fixture.published, fixture.receipt.Candidate.Branch)
	receiptPath := fixture.receipt.ReceiptPath
	unrelated := unrelatedLegacyRoot(t, fixture.engineFixture, "post-lock-unrelated-root")
	mutated := false
	read := func(path string) (WorktreeMergeReceipt, error) {
		receipt, err := readWorktreeMergeReceipt(path)
		if err == nil && path == receiptPath && !mutated {
			// Only the locked re-read sees a publication identity that the
			// earlier pre-lock proof could not have accepted.
			mutated = true
			receipt.PublishedCandidateSHA = unrelated
		}
		return receipt, err
	}
	_, err := prepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{fixture.source}, Target: "main", Model: "test-model", AgentRuntime: "test",
	}, read, persistWorktreeMergeReceipt)
	if err == nil || !strings.Contains(err.Error(), "does not contain required immutable root") {
		t.Fatalf("post-lock re-proof error=%v", err)
	}
	if !mutated {
		t.Fatal("post-lock re-read was never exercised")
	}
	stored, readErr := readWorktreeMergeReceipt(receiptPath)
	if readErr != nil || stored.Candidate.SHA != "" || stored.Status != WorktreeMergeConflict ||
		stored.PublishedCandidateSHA != fixture.published || stored.Sources[0].SHA != fixture.oldSource {
		t.Fatalf("post-lock refusal mutated the durable receipt=%+v error=%v", stored, readErr)
	}
	if head := strings.TrimSpace(runEngineGit(t, fixture.receipt.Candidate.Worktree, "rev-parse", "HEAD")); head != fixture.resolvedHead {
		t.Fatalf("post-lock refusal moved the resolved head from %s to %s", fixture.resolvedHead, head)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+fixture.candidateBranch)); remote != fixture.published {
		t.Fatalf("post-lock refusal moved the published branch to %s, want %s", remote, fixture.published)
	}
	if log := gh.ghLog(t); strings.Contains(log, "pr create") || strings.Contains(log, "pulls/41/merge") || strings.Contains(log, "update-branch") {
		t.Fatalf("post-lock refusal performed a pull-request write:\n%s", log)
	}
}

// TestPrepareLegacyPublishedAdvanceRefusesUnprovenCustodyAndRoots drives the
// new normal-prepare entry point over every evidence class the shared 938
// proof owns: source custody and ancestry, candidate/canonical/claim custody,
// target and hosted pull-request identity, and every recorded root. A refusal
// must leave the receipt, the resolved head, and the publication untouched.
//
//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestE2EPrepareLegacyPublishedAdvanceRefusesUnprovenCustodyAndRoots(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *legacyPublishedAdvanceFixture, *WorktreeMergeReceipt)
		want   string
	}{
		{"candidate worktree is dirty", func(t *testing.T, f *legacyPublishedAdvanceFixture, _ *WorktreeMergeReceipt) {
			writeEngineFile(t, filepath.Join(f.receipt.Candidate.Worktree, "dirty.txt"), "dirty\n")
		}, ""},
		{"candidate branch differs", func(_ *testing.T, _ *legacyPublishedAdvanceFixture, r *WorktreeMergeReceipt) {
			r.Candidate.Branch = "wb/other-candidate"
		}, "does not match WB Guard"},
		{"candidate canonical repository differs", func(_ *testing.T, _ *legacyPublishedAdvanceFixture, r *WorktreeMergeReceipt) {
			r.Repository = "acme/other"
		}, "canonical repository does not match"},
		{"candidate claim is inactive", func(t *testing.T, f *legacyPublishedAdvanceFixture, r *WorktreeMergeReceipt) {
			view, err := worktrees.LoadWorkLogView(t.Context(), worktrees.LoadWorkLogOptions{ProjectsRoot: f.githubDir, Worktree: r.Candidate.Worktree})
			if err != nil || view.Claim == nil || view.Claim.ClaimPath == "" {
				t.Fatalf("actual candidate claim=%+v error=%v", view.Claim, err)
			}
			raw, err := os.ReadFile(view.Claim.ClaimPath)
			if err != nil {
				t.Fatal(err)
			}
			var claim map[string]any
			if err := json.Unmarshal(raw, &claim); err != nil {
				t.Fatal(err)
			}
			claim["lifecycle"] = "terminal"
			inactive, err := json.Marshal(claim)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(view.Claim.ClaimPath, inactive, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "claim"},
		{"recorded target drifted", func(t *testing.T, f *legacyPublishedAdvanceFixture, _ *WorktreeMergeReceipt) {
			writeEngineFile(t, filepath.Join(f.canonical, "drift.txt"), "drift\n")
			runEngineGit(t, f.canonical, "add", "drift.txt")
			runEngineGit(t, f.canonical, "commit", "-m", "test: drift the recorded target")
			runEngineGit(t, f.canonical, "push", "origin", "main")
		}, "target drifted"},
		{"published predecessor root lost", func(t *testing.T, f *legacyPublishedAdvanceFixture, r *WorktreeMergeReceipt) {
			r.PublishedCandidateSHA = unrelatedLegacyRoot(t, f.engineFixture, "unrelated-published-root")
		}, "does not contain required immutable root"},
		{"validated repair root lost", func(t *testing.T, f *legacyPublishedAdvanceFixture, r *WorktreeMergeReceipt) {
			r.Validation.Revision = unrelatedLegacyRoot(t, f.engineFixture, "unrelated-validation-root")
		}, "does not contain required immutable root"},
		{"source refresh root lost", func(t *testing.T, f *legacyPublishedAdvanceFixture, r *WorktreeMergeReceipt) {
			root := unrelatedLegacyRoot(t, f.engineFixture, "unrelated-refresh-root")
			r.SourceRefreshes = append(r.SourceRefreshes, WorktreeMergeSourceRefresh{
				RecordedAt: r.CreatedAt,
				Sources:    []WorktreeMergeSource{{Task: "historical-source", Worktree: r.Sources[0].Worktree, Branch: r.Sources[0].Branch, SHA: root}},
			})
		}, "does not contain required immutable root"},
		{"recorded old source is not an ancestor", func(t *testing.T, f *legacyPublishedAdvanceFixture, r *WorktreeMergeReceipt) {
			r.Sources[0].SHA = unrelatedLegacyRoot(t, f.engineFixture, "unrelated-old-source")
		}, "still owned by non-terminal receipt"},
		{"recorded source custody differs", func(_ *testing.T, _ *legacyPublishedAdvanceFixture, r *WorktreeMergeReceipt) {
			r.Sources[0].Branch = "feature/other-custody"
		}, "still owned by non-terminal receipt"},
		{"published branch moved", func(t *testing.T, f *legacyPublishedAdvanceFixture, r *WorktreeMergeReceipt) {
			// The provider still reports the predecessor it last observed
			// while the private remote ref moved: the ref proof must refuse.
			t.Setenv("WB_TEST_PR_HEAD_SHA", f.published)
			runEngineGit(t, r.Candidate.Worktree, "push", "origin", f.resolvedHead+":refs/heads/"+r.Candidate.Branch)
		}, "candidate remote branch"},
	} {
		//nolint:paralleltest // Each case builds its own t.Setenv real-Git fixture in the shared private lane.
		t.Run(tc.name, func(t *testing.T) {
			fixture := newLegacyPublishedAdvanceFixture(t)
			gh := installWorktreeMergeEngineGH(t, fixture.engineFixture, fixture.published, fixture.receipt.Candidate.Branch)
			tc.mutate(t, &fixture, &fixture.receipt)
			if err := persistWorktreeMergeReceipt(fixture.receipt); err != nil {
				t.Fatal(err)
			}
			before := fixture.receipt
			prepared, err := prepareLegacyPublishedAdvance(t, fixture)
			if err == nil || (tc.want != "" && !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("unproven custody prepare=%+v error=%v want %q", prepared, err, tc.want)
			}
			stored, readErr := readWorktreeMergeReceipt(before.ReceiptPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			assertLegacyPublicationIdentityUnchanged(t, prepared, before, stored)
			if head := strings.TrimSpace(runEngineGit(t, before.Candidate.Worktree, "rev-parse", "HEAD")); head != fixture.resolvedHead {
				t.Fatalf("refusal moved the resolved head from %s to %s", fixture.resolvedHead, head)
			}
			if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+fixture.candidateBranch)); remote != fixture.published && tc.name != "published branch moved" {
				t.Fatalf("refusal moved the published branch to %s, want %s", remote, fixture.published)
			}
			if log := gh.ghLog(t); strings.Contains(log, "pr create") || strings.Contains(log, "pulls/41/merge") || strings.Contains(log, "update-branch") {
				t.Fatalf("refusal performed a pull-request write:\n%s", log)
			}
		})
	}
}

// TestPrepareLegacyPublishedAdvanceRefusesUnprovenPullRequestIdentity keeps
// the hosted proof exact through the new entry point: the pull request must be
// open and unmerged at the recorded repository, base, head branch, and head
// SHA, and the private remote predecessor must still match it.
//
//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestE2EPrepareLegacyPublishedAdvanceRefusesUnprovenPullRequestIdentity(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
		env  map[string]string
	}{
		{"closed pull request", "not open", map[string]string{"WB_TEST_PR_STATE": "CLOSED"}},
		{"merged pull request", "not open", map[string]string{"WB_TEST_PR_MERGED": "true"}},
		{"wrong base", "does not match exact repository", map[string]string{"WB_TEST_PR_BASE_REF": "release"}},
		{"wrong head branch", "does not match exact repository", map[string]string{"WB_TEST_PR_HEAD_REF": "wb/other-head"}},
		{"wrong head SHA", "does not match exact repository", map[string]string{"WB_TEST_PR_HEAD_SHA": strings.Repeat("a", 40)}},
		{"wrong head repository", "does not match exact repository", map[string]string{"WB_TEST_PR_HEAD_REPO": "acme/other"}},
		{"wrong base repository", "does not match exact repository", map[string]string{"WB_TEST_PR_BASE_REPO": "acme/other"}},
	} {
		//nolint:paralleltest // Each case builds its own t.Setenv real-Git fixture in the shared private lane.
		t.Run(tc.name, func(t *testing.T) {
			fixture := newLegacyPublishedAdvanceFixture(t)
			gh := installWorktreeMergeEngineGH(t, fixture.engineFixture, fixture.published, fixture.receipt.Candidate.Branch)
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			before := fixture.receipt
			prepared, err := prepareLegacyPublishedAdvance(t, fixture)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unproven pull request prepare=%+v error=%v want %q", prepared, err, tc.want)
			}
			stored, readErr := readWorktreeMergeReceipt(before.ReceiptPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			assertLegacyPublicationIdentityUnchanged(t, prepared, before, stored)
			if head := strings.TrimSpace(runEngineGit(t, before.Candidate.Worktree, "rev-parse", "HEAD")); head != fixture.resolvedHead {
				t.Fatalf("refusal moved the resolved head from %s to %s", fixture.resolvedHead, head)
			}
			if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+before.Candidate.Branch)); remote != fixture.published {
				t.Fatalf("refusal moved the published branch to %s, want %s", remote, fixture.published)
			}
			if log := gh.ghLog(t); strings.Contains(log, "pr create") || strings.Contains(log, "pulls/41/merge") || strings.Contains(log, "update-branch") {
				t.Fatalf("refusal performed a pull-request write:\n%s", log)
			}
		})
	}
}
