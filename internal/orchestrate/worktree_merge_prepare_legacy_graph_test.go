//go:build e2e

package orchestrate

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/runner"
)

// TestPrepareLegacyPublishedAdvanceRefusesHeadThatReturnedToCapturedIdentity is
// the held-proof identity race: the shared proof reads a different clean head H1
// that still contains every old recorded root while H0 - the captured identity
// and the only head carrying the candidate-only repair - is omitted, and the
// physical head returns to H0 before the exact guard. The physical check alone
// would pass while the proof's own re-derived candidate identity stayed H1.
// Actual normal Prepare must refuse before any durable mutation or publication.
//
//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestE2EPrepareLegacyPublishedAdvanceRefusesHeadThatReturnedToCapturedIdentity(t *testing.T) {
	fixture := newLegacyPublishedAdvanceUnrecordedRepairFixture(t)
	gh := installWorktreeMergeEngineGH(t, fixture.engineFixture, fixture.published, fixture.receipt.Candidate.Branch)
	before := fixture.receipt
	var alternate string
	// The locked re-read is the last cutpoint before the held proof, so the
	// replacement is in place while that proof resolves the candidate.
	reads := 0
	read := func(path string) (WorktreeMergeReceipt, error) {
		receipt, err := readWorktreeMergeReceipt(path)
		if err != nil || path != before.ReceiptPath {
			return receipt, err
		}
		reads++
		if reads == 1 {
			alternate = replaceLegacyPublishedCandidateHead(t, fixture)
		}
		return receipt, nil
	}
	// The fourth exact head read is the held bridge proof's own candidate read
	// (continuation, pre-lock proof, locked continuation, then this one). It
	// observes H1 while the worktree is returned to H0 immediately afterwards,
	// so only the re-derived identity can distinguish the two heads.
	run := &legacyCutpointRunner{
		Runner: runner.New(), dir: before.Candidate.Worktree,
		headRead: 4,
		observed: func() string { return alternate },
		move:     func() { runEngineGit(t, before.Candidate.Worktree, "reset", "--hard", fixture.resolvedHead) },
	}
	refused, err := prepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{fixture.source}, Target: "main", Model: "test-model", AgentRuntime: "test", run: run,
	}, read, persistWorktreeMergeReceipt)
	if err == nil || !strings.Contains(err.Error(), "re-derived receipt candidate") ||
		!strings.Contains(err.Error(), fixture.resolvedHead) {
		t.Fatalf("held-proof identity race=%+v error=%v", refused, err)
	}
	if reads != 1 || alternate == "" || !run.triggered {
		t.Fatalf("held-proof cutpoints reads=%d alternate=%q triggered=%v", reads, alternate, run.triggered)
	}
	if head := strings.TrimSpace(runEngineGit(t, before.Candidate.Worktree, "rev-parse", "HEAD")); head != fixture.resolvedHead {
		t.Fatalf("refusal left the candidate head at %s, want the captured %s", head, fixture.resolvedHead)
	}
	assertLegacyPrepareRefusalLeftPublicationAlone(t, fixture, gh, before, fixture.resolvedHead)
	stored, readErr := readWorktreeMergeReceipt(before.ReceiptPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	assertLegacyPublicationIdentityUnchanged(t, refused, before, stored)
	if refused.Status != WorktreeMergeConflict || refused.Candidate.SHA != "" ||
		stored.Status != WorktreeMergeConflict || stored.Candidate.SHA != "" {
		t.Fatalf("held-proof identity refusal recorded an unproven candidate: refused=%+v stored=%+v", refused, stored)
	}
	// No durable mutation at all: the refusal happens before the preparing
	// receipt is constructed, so the stored receipt is the one that was read.
	if !stored.UpdatedAt.Equal(before.UpdatedAt) || stored.Failure != before.Failure ||
		len(stored.SourceRefreshes) != len(before.SourceRefreshes) ||
		stored.Sources[0].SHA != before.Sources[0].SHA || stored.LandingSHA != "" {
		t.Fatalf("held-proof identity refusal rewrote the durable receipt: before=%+v stored=%+v", before, stored)
	}
}

// TestPrepareLegacyPublishedAdvanceRefusesSourceCheckpointThatLostTheMerge
// replaces the candidate head with H0 again after the advanced source was
// actually merged. H0 still descends the captured identity, so only the accepted
// graph can tell that this checkpoint dropped the source it claims to have
// accepted. Prepare must refuse and keep the last proven checkpoint.
//
//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestE2EPrepareLegacyPublishedAdvanceRefusesSourceCheckpointThatLostTheMerge(t *testing.T) {
	fixture := newLegacyPublishedAdvanceFixture(t)
	gh := installWorktreeMergeEngineGH(t, fixture.engineFixture, fixture.published, fixture.receipt.Candidate.Branch)
	before := fixture.receipt
	run := &legacyCutpointRunner{
		Runner: runner.New(), dir: before.Candidate.Worktree,
		afterMerge: fixture.currentSource,
		move:       func() { runEngineGit(t, before.Candidate.Worktree, "reset", "--hard", fixture.resolvedHead) },
	}
	failed, err := prepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{fixture.source}, Target: "main", Model: "test-model", AgentRuntime: "test", run: run,
	}, readWorktreeMergeReceipt, persistWorktreeMergeReceipt)
	if err == nil || !strings.Contains(err.Error(), "does not retain "+fixture.currentSource) {
		t.Fatalf("lost source checkpoint=%+v error=%v", failed, err)
	}
	if !run.triggered {
		t.Fatal("the source checkpoint cutpoint never replaced the merged head")
	}
	if failed.Status != WorktreeMergeConflict || failed.Candidate.SHA != fixture.resolvedHead ||
		failed.PublishedCandidateSHA != fixture.published || failed.PullRequest != gh.pr ||
		failed.TargetSHA != before.TargetSHA || failed.LandingSHA != "" ||
		len(failed.Sources) != 1 || failed.Sources[0].SHA != fixture.currentSource || failed.Sources[0].Merged ||
		len(failed.SourceRefreshes) != 2 ||
		failed.SourceRefreshes[0].Sources[0].SHA != fixture.initialSource ||
		failed.SourceRefreshes[1].Sources[0].SHA != fixture.oldSource {
		t.Fatalf("lost source checkpoint recorded an unproven candidate: %+v", failed)
	}
	stored, readErr := readWorktreeMergeReceipt(before.ReceiptPath)
	if readErr != nil || stored.Status != WorktreeMergeConflict || stored.Candidate.SHA != fixture.resolvedHead ||
		stored.PublishedCandidateSHA != fixture.published || stored.PullRequest != gh.pr ||
		stored.TargetSHA != before.TargetSHA || stored.Sources[0].Merged || len(stored.SourceRefreshes) != 2 {
		t.Fatalf("durable lost source checkpoint=%+v error=%v", stored, readErr)
	}
	if head := strings.TrimSpace(runEngineGit(t, before.Candidate.Worktree, "rev-parse", "HEAD")); head != fixture.resolvedHead {
		t.Fatalf("lost source checkpoint left the candidate head at %s", head)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+before.Candidate.Branch)); remote != fixture.published {
		t.Fatalf("lost source checkpoint published %s, want %s", remote, fixture.published)
	}
	if live := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); live != before.TargetSHA {
		t.Fatalf("lost source checkpoint moved the target to %s", live)
	}
	if log := gh.ghLog(t); strings.Contains(log, "pr create") || strings.Contains(log, "pulls/41/merge") || strings.Contains(log, "update-branch") {
		t.Fatalf("lost source checkpoint performed a pull-request write:\n%s", log)
	}
}

// TestPrepareLegacyPublishedAdvanceRefusesLaterSourceCheckpointThatDroppedEarlierSource
// drives a real two-source batch: source A is accepted into H_A, then the
// later source-B integration head is moved to one containing H0 and B but
// dropping A and H_A. The batch must refuse instead of recording B over a head
// that silently lost A, and must retain H_A plus A's accepted flag.
//
//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestE2EPrepareLegacyPublishedAdvanceRefusesLaterSourceCheckpointThatDroppedEarlierSource(t *testing.T) {
	fixture := newLegacyPublishedTwoSourceAdvanceFixture(t)
	gh := installWorktreeMergeEngineGH(t, fixture.engineFixture, fixture.published, fixture.receipt.Candidate.Branch)
	before := fixture.receipt
	// The B-only head is a real clean resolution of H0 with source B that never
	// contains A's advanced commit.
	runEngineGit(t, before.Candidate.Worktree, "reset", "--hard", fixture.resolvedHead)
	runEngineGit(t, before.Candidate.Worktree, "merge", "--no-edit", fixture.currentSourceB)
	droppedHead := strings.TrimSpace(runEngineGit(t, before.Candidate.Worktree, "rev-parse", "HEAD"))
	runEngineGit(t, before.Candidate.Worktree, "reset", "--hard", fixture.resolvedHead)
	if droppedHead == fixture.resolvedHead || droppedHead == fixture.currentSourceA {
		t.Fatalf("dropped-head fixture resolved %q", droppedHead)
	}
	assertNativeAncestor(t, before.Candidate.Worktree, fixture.resolvedHead, droppedHead)
	assertNativeAncestor(t, before.Candidate.Worktree, fixture.currentSourceB, droppedHead)

	var acceptedA, integratedA string
	save := func(receipt WorktreeMergeReceipt) error {
		if integratedA == "" && receipt.Status == WorktreeMergePreparing && len(receipt.Sources) == 2 &&
			receipt.Sources[0].Merged && !receipt.Sources[1].Merged {
			acceptedA, integratedA = receipt.Sources[0].SHA, receipt.Candidate.SHA
		}
		return persistWorktreeMergeReceipt(receipt)
	}
	run := &legacyCutpointRunner{
		Runner: runner.New(), dir: before.Candidate.Worktree,
		afterMerge: fixture.currentSourceB,
		move:       func() { runEngineGit(t, before.Candidate.Worktree, "reset", "--hard", droppedHead) },
	}
	failed, err := prepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir,
		Sources:      []string{fixture.sourceA, fixture.sourceB},
		Target:       "main", Model: "test-model", AgentRuntime: "test", run: run,
	}, readWorktreeMergeReceipt, save)
	if err == nil || !strings.Contains(err.Error(), "does not retain "+integratedA) {
		t.Fatalf("dropped earlier source=%+v error=%v accepted=%q integrated=%q", failed, err, acceptedA, integratedA)
	}
	if !run.triggered || acceptedA != fixture.currentSourceA || integratedA == "" || integratedA == fixture.resolvedHead {
		t.Fatalf("dropped earlier source cutpoints triggered=%v accepted=%q integrated=%q", run.triggered, acceptedA, integratedA)
	}
	if got := strings.TrimSpace(runEngineGit(t, before.Candidate.Worktree, "merge-base", integratedA, droppedHead)); got == integratedA {
		t.Fatalf("dropped head %s still contains the accepted head %s", droppedHead, integratedA)
	}
	if failed.Status != WorktreeMergeConflict || failed.Candidate.SHA != integratedA ||
		failed.PublishedCandidateSHA != fixture.published || failed.PullRequest != gh.pr ||
		failed.TargetSHA != before.TargetSHA || failed.LandingSHA != "" ||
		len(failed.Sources) != 2 || !failed.Sources[0].Merged || failed.Sources[1].Merged ||
		failed.Sources[0].SHA != fixture.currentSourceA || failed.Sources[1].SHA != fixture.currentSourceB {
		t.Fatalf("dropped earlier source lost the accepted checkpoint: %+v", failed)
	}
	if len(failed.SourceRefreshes) != 2 ||
		len(failed.SourceRefreshes[0].Sources) != 2 ||
		failed.SourceRefreshes[0].Sources[0].SHA != fixture.initialSourceA ||
		failed.SourceRefreshes[0].Sources[1].SHA != fixture.initialSourceB ||
		failed.SourceRefreshes[1].Sources[0].SHA != fixture.oldSourceA ||
		failed.SourceRefreshes[1].Sources[1].SHA != fixture.oldSourceB {
		t.Fatalf("dropped earlier source lost the old source history: %+v", failed.SourceRefreshes)
	}
	stored, readErr := readWorktreeMergeReceipt(before.ReceiptPath)
	if readErr != nil || stored.Status != WorktreeMergeConflict || stored.Candidate.SHA != integratedA ||
		stored.PublishedCandidateSHA != fixture.published || stored.PullRequest != gh.pr ||
		stored.TargetSHA != before.TargetSHA || !stored.Sources[0].Merged || stored.Sources[1].Merged ||
		len(stored.SourceRefreshes) != 2 {
		t.Fatalf("durable dropped earlier source=%+v error=%v", stored, readErr)
	}
	if head := strings.TrimSpace(runEngineGit(t, before.Candidate.Worktree, "rev-parse", "HEAD")); head != droppedHead {
		t.Fatalf("dropped earlier source moved the candidate head to %s, want %s", head, droppedHead)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+before.Candidate.Branch)); remote != fixture.published {
		t.Fatalf("dropped earlier source published %s, want %s", remote, fixture.published)
	}
	if live := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); live != before.TargetSHA {
		t.Fatalf("dropped earlier source moved the target to %s", live)
	}
	if log := gh.ghLog(t); strings.Contains(log, "pr create") || strings.Contains(log, "pulls/41/merge") || strings.Contains(log, "update-branch") {
		t.Fatalf("dropped earlier source performed a pull-request write:\n%s", log)
	}
}

// TestPrepareLegacyPublishedAdvanceRefusesPreparedCheckpointThatLostIntegratedCandidate
// moves the candidate during the final prepared-head read, after the integrated
// H-prime was persisted. H0 is still the captured identity, so only the accepted
// graph can tell that the validated checkpoint dropped the integrated candidate
// and its source. Prepare must refuse and keep H-prime.
//
//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestE2EPrepareLegacyPublishedAdvanceRefusesPreparedCheckpointThatLostIntegratedCandidate(t *testing.T) {
	fixture := newLegacyPublishedAdvanceFixture(t)
	gh := installWorktreeMergeEngineGH(t, fixture.engineFixture, fixture.published, fixture.receipt.Candidate.Branch)
	before := fixture.receipt
	var integrated string
	save := func(receipt WorktreeMergeReceipt) error {
		if integrated == "" && receipt.Status == WorktreeMergePreparing && receipt.Sources[0].Merged {
			integrated = receipt.Candidate.SHA
		}
		return persistWorktreeMergeReceipt(receipt)
	}
	// The eighth exact head read is the prepared checkpoint: the held proof and
	// its guard read four, the post-save guard one, the source checkpoint one,
	// and this one. It observes H0 while the integrated H-prime is the last
	// proven candidate.
	run := &legacyCutpointRunner{
		Runner: runner.New(), dir: before.Candidate.Worktree,
		headRead: 8,
		move:     func() { runEngineGit(t, before.Candidate.Worktree, "reset", "--hard", fixture.resolvedHead) },
	}
	failed, err := prepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{fixture.source}, Target: "main", Model: "test-model", AgentRuntime: "test", run: run,
	}, readWorktreeMergeReceipt, save)
	if err == nil || !strings.Contains(err.Error(), "does not retain "+integrated) {
		t.Fatalf("prepared checkpoint move=%+v error=%v integrated=%q", failed, err, integrated)
	}
	if !run.triggered || integrated == "" || integrated == fixture.resolvedHead {
		t.Fatalf("prepared checkpoint cutpoints triggered=%v integrated=%q", run.triggered, integrated)
	}
	if failed.Status != WorktreeMergeConflict || failed.Candidate.SHA != integrated ||
		failed.PublishedCandidateSHA != fixture.published || failed.PullRequest != gh.pr ||
		failed.TargetSHA != before.TargetSHA || failed.LandingSHA != "" ||
		len(failed.SourceRefreshes) != 2 {
		t.Fatalf("prepared checkpoint lost the last proven candidate: %+v", failed)
	}
	stored, readErr := readWorktreeMergeReceipt(before.ReceiptPath)
	if readErr != nil || stored.Status != WorktreeMergeConflict || stored.Candidate.SHA != integrated ||
		stored.PublishedCandidateSHA != fixture.published || stored.PullRequest != gh.pr ||
		!stored.Sources[0].Merged || len(stored.SourceRefreshes) != 2 {
		t.Fatalf("durable prepared checkpoint=%+v error=%v", stored, readErr)
	}
	if head := strings.TrimSpace(runEngineGit(t, before.Candidate.Worktree, "rev-parse", "HEAD")); head != fixture.resolvedHead {
		t.Fatalf("prepared checkpoint left the candidate head at %s", head)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+before.Candidate.Branch)); remote != fixture.published {
		t.Fatalf("prepared checkpoint published %s, want %s", remote, fixture.published)
	}
	if log := gh.ghLog(t); strings.Contains(log, "pr create") || strings.Contains(log, "pulls/41/merge") || strings.Contains(log, "update-branch") {
		t.Fatalf("prepared checkpoint performed a pull-request write:\n%s", log)
	}
}

// TestPrepareLegacyPublishedAdvanceRefusesPreparedCleanDescendantBeyondValidatedHead
// commits a fresh clean descendant H2 of the integrated H-prime at the final
// prepared-head read, after validation already covered H-prime. H2 keeps the
// captured H, H-prime itself, the accepted source and every recorded root, so
// both ancestry guards admit it; only the exact validated identity can tell that
// recording H2 would stamp an unvalidated head as the candidate validation
// covered. Prepare must refuse and keep H-prime with its truthful validation.
//
//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestE2EPrepareLegacyPublishedAdvanceRefusesPreparedCleanDescendantBeyondValidatedHead(t *testing.T) {
	fixture := newLegacyPublishedAdvanceFixture(t)
	gh := installWorktreeMergeEngineGH(t, fixture.engineFixture, fixture.published, fixture.receipt.Candidate.Branch)
	before := fixture.receipt
	var integrated, descendant string
	save := func(receipt WorktreeMergeReceipt) error {
		if integrated == "" && receipt.Status == WorktreeMergePreparing && receipt.Sources[0].Merged {
			integrated = receipt.Candidate.SHA
		}
		return persistWorktreeMergeReceipt(receipt)
	}
	// The eighth exact head read is the prepared checkpoint, after validation.
	run := &legacyCutpointRunner{
		Runner: runner.New(), dir: before.Candidate.Worktree, headRead: 8,
		move: func() {
			writeEngineFile(t, filepath.Join(before.Candidate.Worktree, "clean-descendant.txt"), "clean descendant\n")
			runEngineGit(t, before.Candidate.Worktree, "add", "clean-descendant.txt")
			runEngineGit(t, before.Candidate.Worktree, "commit", "-m", "test: clean descendant after validation")
			descendant = strings.TrimSpace(runEngineGit(t, before.Candidate.Worktree, "rev-parse", "HEAD"))
		},
	}
	failed, err := prepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{fixture.source}, Target: "main", Model: "test-model", AgentRuntime: "test", run: run,
	}, readWorktreeMergeReceipt, save)
	if err == nil || !strings.Contains(err.Error(), "is not the validated candidate "+integrated) {
		t.Fatalf("clean prepared descendant=%+v error=%v integrated=%q descendant=%q", failed, err, integrated, descendant)
	}
	if !run.triggered || integrated == "" || descendant == "" || descendant == integrated || descendant == fixture.resolvedHead {
		t.Fatalf("prepared descendant cutpoints triggered=%v integrated=%q descendant=%q", run.triggered, integrated, descendant)
	}
	// H2 is a strict clean descendant that dropped nothing: the captured H, the
	// validated H-prime, the accepted source and every recorded root remain.
	assertNativeAncestor(t, before.Candidate.Worktree, integrated, descendant)
	for _, root := range []string{fixture.published, fixture.resolvedHead, fixture.oldSource, fixture.currentSource, before.TargetSHA} {
		assertNativeAncestor(t, before.Candidate.Worktree, root, descendant)
	}
	if failed.Status != WorktreeMergeConflict || failed.Candidate.SHA != integrated ||
		failed.Validation.Revision != integrated || failed.Validation.Status != quality.StatusSkipped ||
		failed.PublishedCandidateSHA != fixture.published || failed.PullRequest != gh.pr ||
		failed.TargetSHA != before.TargetSHA || failed.LandingSHA != "" ||
		len(failed.Sources) != 1 || !failed.Sources[0].Merged || failed.Sources[0].SHA != fixture.currentSource ||
		len(failed.SourceRefreshes) != 2 {
		t.Fatalf("prepared descendant lost the validated identity: %+v", failed)
	}
	// This fixture's normal prepare defers local validation to the fenced
	// required-check policy, so the deferral is the identity that must keep
	// naming H-prime rather than the unvalidated descendant.
	if failed.ValidationDeferral == nil || failed.ValidationDeferral.CandidateSHA != integrated {
		t.Fatalf("prepared descendant relabelled the validation deferral: %+v", failed.ValidationDeferral)
	}
	stored, readErr := readWorktreeMergeReceipt(before.ReceiptPath)
	if readErr != nil || stored.Status != WorktreeMergeConflict || stored.Candidate.SHA != integrated ||
		stored.Validation.Revision != integrated || stored.PublishedCandidateSHA != fixture.published ||
		stored.PullRequest != gh.pr || stored.TargetSHA != before.TargetSHA || stored.LandingSHA != "" ||
		!stored.Sources[0].Merged || stored.Sources[0].SHA != fixture.currentSource || len(stored.SourceRefreshes) != 2 {
		t.Fatalf("durable prepared descendant=%+v error=%v", stored, readErr)
	}
	if head := strings.TrimSpace(runEngineGit(t, before.Candidate.Worktree, "rev-parse", "HEAD")); head != descendant {
		t.Fatalf("prepared descendant moved the candidate head to %s, want the untouched physical %s", head, descendant)
	}
	if status := strings.TrimSpace(runEngineGit(t, before.Candidate.Worktree, "status", "--porcelain=v1")); status != "" {
		t.Fatalf("prepared descendant left the candidate worktree dirty: %s", status)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+before.Candidate.Branch)); remote != fixture.published {
		t.Fatalf("prepared descendant published %s, want %s", remote, fixture.published)
	}
	if live := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); live != before.TargetSHA {
		t.Fatalf("prepared descendant moved the target to %s", live)
	}
	if log := gh.ghLog(t); strings.Contains(log, "pr create") || strings.Contains(log, "pulls/41/merge") || strings.Contains(log, "update-branch") {
		t.Fatalf("prepared descendant performed a pull-request write:\n%s", log)
	}
}

// TestPrepareLegacyPublishedAdvanceRefusesIntegratedGraphReadFailure keeps the
// accepted-graph guards non-inventive: when the private-Git read that proves one
// accepted source root fails, prepare refuses with that error and keeps the last
// proven checkpoint plus the publication.
//
//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestE2EPrepareLegacyPublishedAdvanceRefusesIntegratedGraphReadFailure(t *testing.T) {
	fixture := newLegacyPublishedAdvanceFixture(t)
	gh := installWorktreeMergeEngineGH(t, fixture.engineFixture, fixture.published, fixture.receipt.Candidate.Branch)
	before := fixture.receipt
	sentinel := errors.New("controlled integrated graph read refused")
	run := &legacyGraphReadFailRunner{Runner: runner.New(), dir: before.Candidate.Worktree, ancestor: fixture.currentSource, err: sentinel}
	failed, err := prepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{fixture.source}, Target: "main", Model: "test-model", AgentRuntime: "test", run: run,
	}, readWorktreeMergeReceipt, persistWorktreeMergeReceipt)
	if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "prove integrated candidate retains "+fixture.currentSource) {
		t.Fatalf("integrated graph read failure=%+v error=%v", failed, err)
	}
	if !run.failed {
		t.Fatal("the integrated graph ancestry read was never exercised")
	}
	if failed.Status != WorktreeMergeConflict || failed.Candidate.SHA != fixture.resolvedHead ||
		failed.PublishedCandidateSHA != fixture.published || failed.PullRequest != gh.pr ||
		failed.TargetSHA != before.TargetSHA || failed.Sources[0].Merged {
		t.Fatalf("integrated graph read failure recorded an unproven candidate: %+v", failed)
	}
	stored, readErr := readWorktreeMergeReceipt(before.ReceiptPath)
	if readErr != nil || stored.Status != WorktreeMergeConflict || stored.Candidate.SHA != fixture.resolvedHead ||
		stored.PublishedCandidateSHA != fixture.published || stored.Sources[0].Merged || len(stored.SourceRefreshes) != 2 {
		t.Fatalf("durable integrated graph read failure=%+v error=%v", stored, readErr)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+before.Candidate.Branch)); remote != fixture.published {
		t.Fatalf("integrated graph read failure published %s, want %s", remote, fixture.published)
	}
	if log := gh.ghLog(t); strings.Contains(log, "pr create") || strings.Contains(log, "pulls/41/merge") || strings.Contains(log, "update-branch") {
		t.Fatalf("integrated graph read failure performed a pull-request write:\n%s", log)
	}
}

// legacyGraphReadFailRunner fails the accepted-source ancestry read the
// integrated-graph guard performs against the checkpoint head (never the
// pre-merge ancestor check, which names HEAD literally).
type legacyGraphReadFailRunner struct {
	runner.Runner
	dir, ancestor string
	err           error
	failed        bool
}

func (r *legacyGraphReadFailRunner) RunOpts(ctx context.Context, dir string, options runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if name == "git" && dir == r.dir && len(args) == 3 && args[0] == "merge-base" &&
		args[1] == r.ancestor && args[2] != "HEAD" {
		r.failed = true
		return runner.Result{}, r.err
	}
	return r.Runner.RunOpts(ctx, dir, options, name, args...)
}

// legacyCutpointRunner rewrites the managed candidate at one exact cutpoint
// inside ordinary prepare. Either it triggers on the first candidate head read
// after a named merge (a source checkpoint) or on the Nth exact head read (the
// held bridge proof or the prepared checkpoint), and can both return a synthetic
// observed head and move the worktree, so a proof and the physical guard can
// disagree. Every other command delegates to the real runner.
type legacyCutpointRunner struct {
	runner.Runner
	dir        string
	afterMerge string
	headRead   int
	seen       int
	pending    bool
	observed   func() string
	move       func()
	triggered  bool
}

func (r *legacyCutpointRunner) RunOpts(ctx context.Context, dir string, options runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if name == "git" && dir == r.dir {
		joined := strings.Join(args, " ")
		if r.afterMerge != "" && joined == "merge --no-edit "+r.afterMerge {
			r.pending = true
		} else if joined == "rev-parse --verify HEAD^{commit}" {
			r.seen++
			fire := r.pending || (r.afterMerge == "" && r.seen == r.headRead)
			if fire {
				r.pending = false
				r.triggered = true
				if r.move != nil {
					r.move()
				}
				if r.observed != nil {
					observed := r.observed() + "\n"
					return runner.Result{Stdout: observed, CombinedOutput: observed}, nil
				}
			}
		}
	}
	return r.Runner.RunOpts(ctx, dir, options, name, args...)
}

// legacyPublishedTwoSourceFixture is the same legacy published shape over two
// independent sources, so a batch can accept one source and then lose it in a
// later checkpoint.
type legacyPublishedTwoSourceFixture struct {
	engineFixture
	receipt                        WorktreeMergeReceipt
	sourceA, sourceB               string
	published                      string
	initialSourceA, initialSourceB string
	oldSourceA, oldSourceB         string
	currentSourceA, currentSourceB string
	resolvedHead                   string
}

func newLegacyPublishedTwoSourceAdvanceFixture(t *testing.T) legacyPublishedTwoSourceFixture {
	t.Helper()
	fixture := newEngineFixture(t)
	sourceA := createMergeSource(t, fixture, "legacy-graph-source-a", "feature/legacy-graph-a", "graph-a.txt", "graph a\n")
	sourceB := createMergeSource(t, fixture, "legacy-graph-source-b", "feature/legacy-graph-b", "graph-b.txt", "graph b\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{sourceA.WorktreeDir, sourceB.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	published := receipt.Candidate.SHA
	initial := append([]WorktreeMergeSource(nil), receipt.Sources...)
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", published+":refs/heads/"+receipt.Candidate.Branch)
	writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "local-repair.txt"), "local repair\n")
	runEngineGit(t, receipt.Candidate.Worktree, "add", "local-repair.txt")
	runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "fix: local repair recorded by the published candidate")
	repair := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))

	oldA := advanceLegacyGraphSource(t, sourceA.WorktreeDir, "graph-a-old.txt")
	oldB := advanceLegacyGraphSource(t, sourceB.WorktreeDir, "graph-b-old.txt")
	runEngineGit(t, receipt.Candidate.Worktree, "merge", "--no-edit", oldA)
	runEngineGit(t, receipt.Candidate.Worktree, "merge", "--no-edit", oldB)
	resolvedHead := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))

	receipt.Phase, receipt.Status = WorktreeMergePhasePrepare, WorktreeMergeConflict
	receipt.Sources[0].SHA = oldA
	receipt.Sources[1].SHA = oldB
	receipt.Candidate.SHA = ""
	receipt.PullRequest = "https://example.test/acme/app/pull/41"
	receipt.PublishedCandidateSHA = published
	receipt.Validation = quality.VerificationReport{Repository: receipt.Repository, Path: "git:" + repair, Revision: repair, Status: quality.StatusPassed, WorkspaceClean: true}
	receipt.SourceRefreshes = []WorktreeMergeSourceRefresh{{RecordedAt: receipt.CreatedAt, Sources: initial}}
	receipt.Failure = "prepare refresh failed before recording the resolved head"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}

	currentA := advanceLegacyGraphSource(t, sourceA.WorktreeDir, "graph-a-prime.txt")
	currentB := advanceLegacyGraphSource(t, sourceB.WorktreeDir, "graph-b-prime.txt")
	if got := strings.TrimSpace(runEngineGit(t, sourceA.WorktreeDir, "merge-base", resolvedHead, currentA)); got == resolvedHead {
		t.Fatalf("fixture source A contains the resolved candidate head")
	}
	if got := strings.TrimSpace(runEngineGit(t, sourceB.WorktreeDir, "merge-base", resolvedHead, currentB)); got == resolvedHead {
		t.Fatalf("fixture source B contains the resolved candidate head")
	}
	return legacyPublishedTwoSourceFixture{
		engineFixture: fixture, receipt: receipt,
		sourceA: sourceA.WorktreeDir, sourceB: sourceB.WorktreeDir, published: published,
		initialSourceA: initial[0].SHA, initialSourceB: initial[1].SHA,
		oldSourceA: oldA, oldSourceB: oldB,
		currentSourceA: currentA, currentSourceB: currentB,
		resolvedHead: resolvedHead,
	}
}

func advanceLegacyGraphSource(t *testing.T, worktree, name string) string {
	t.Helper()
	writeEngineFile(t, filepath.Join(worktree, name), name+"\n")
	runEngineGit(t, worktree, "add", name)
	runEngineGit(t, worktree, "commit", "-m", "feat: add "+name)
	return strings.TrimSpace(runEngineGit(t, worktree, "rev-parse", "HEAD"))
}
