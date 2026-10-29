//go:build e2e

package orchestrate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/runner"
)

// newLegacyPublishedAdvanceUnrecordedRepairFixture builds the same legacy
// shape, but the published candidate's candidate-only repair is deliberately
// unreceipted: no validation, deferral, identity, refresh, or target record
// names it. Anything that reaches the resolved head H therefore has to be the
// exact captured identity, because no ancestry/root proof can insist on it.
func newLegacyPublishedAdvanceUnrecordedRepairFixture(t *testing.T) legacyPublishedAdvanceFixture {
	t.Helper()
	fixture := newLegacyPublishedAdvanceFixture(t)
	fixture.receipt.Validation = quality.VerificationReport{}
	if err := persistWorktreeMergeReceipt(fixture.receipt); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// replaceLegacyPublishedCandidateHead rewinds the managed candidate worktree to
// a different clean head that keeps every root the durable receipt records - the
// published predecessor, the recorded old source snapshot, the earlier source
// refresh, and the target - while dropping the captured head and the
// candidate-only repair only that head carries.
func replaceLegacyPublishedCandidateHead(t *testing.T, fixture legacyPublishedAdvanceFixture) string {
	t.Helper()
	worktree := fixture.receipt.Candidate.Worktree
	runEngineGit(t, worktree, "reset", "--hard", fixture.published)
	runEngineGit(t, worktree, "merge", "--no-edit", fixture.oldSource)
	alternate := strings.TrimSpace(runEngineGit(t, worktree, "rev-parse", "HEAD"))
	if alternate == "" || alternate == fixture.resolvedHead {
		t.Fatalf("alternate candidate head=%q did not replace captured %s", alternate, fixture.resolvedHead)
	}
	for _, root := range []string{fixture.published, fixture.oldSource, fixture.initialSource, fixture.receipt.TargetSHA} {
		assertNativeAncestor(t, worktree, root, alternate)
	}
	if got := strings.TrimSpace(runEngineGit(t, worktree, "merge-base", fixture.resolvedHead, alternate)); got == fixture.resolvedHead {
		t.Fatalf("alternate candidate %s still contains the captured head %s", alternate, fixture.resolvedHead)
	}
	if status := strings.TrimSpace(runEngineGit(t, worktree, "status", "--porcelain=v1")); status != "" {
		t.Fatalf("alternate candidate worktree is dirty: %s", status)
	}
	return alternate
}

// assertLegacyPrepareRefusalLeftPublicationAlone holds the shared refusal
// contract for the captured-head races: same durable receipt identity, same
// resolved head, same open publication, unmoved target, and no pull-request
// write of any kind.
func assertLegacyPrepareRefusalLeftPublicationAlone(t *testing.T, fixture legacyPublishedAdvanceFixture, gh *wmEngineGH, before WorktreeMergeReceipt, wantHead string) {
	t.Helper()
	stored, readErr := readWorktreeMergeReceipt(before.ReceiptPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	assertLegacyPublicationIdentityUnchanged(t, stored, before, stored)
	if head := strings.TrimSpace(runEngineGit(t, before.Candidate.Worktree, "rev-parse", "HEAD")); head != wantHead {
		t.Fatalf("refusal moved the candidate head from %s to %s", wantHead, head)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+before.Candidate.Branch)); remote != fixture.published {
		t.Fatalf("refusal pushed the published branch to %s, want %s", remote, fixture.published)
	}
	if live := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); live != before.TargetSHA {
		t.Fatalf("refusal moved the target to %s, want %s", live, before.TargetSHA)
	}
	if log := gh.ghLog(t); strings.Contains(log, "pr create") || strings.Contains(log, "pulls/41/merge") || strings.Contains(log, "update-branch") {
		t.Fatalf("refusal performed a pull-request write:\n%s", log)
	}
}

// TestPrepareLegacyPublishedAdvanceRefusesReplacedCandidateHeadUnderTheLaneLock
// is the captured-identity race: between the pre-lock proof and the held-lock
// re-read the managed candidate is replaced by a different clean H1 that still
// contains every root the durable receipt records, so the shared 938 proof
// alone would adopt it. Ordinary prepare must refuse the exact drift and leave
// the durable receipt, the externally changed head, the publication, and the
// target untouched.
//
//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestE2EPrepareLegacyPublishedAdvanceRefusesReplacedCandidateHeadUnderTheLaneLock(t *testing.T) {
	fixture := newLegacyPublishedAdvanceUnrecordedRepairFixture(t)
	gh := installWorktreeMergeEngineGH(t, fixture.engineFixture, fixture.published, fixture.receipt.Candidate.Branch)
	before := fixture.receipt
	var alternate string
	reads := 0
	read := func(path string) (WorktreeMergeReceipt, error) {
		receipt, err := readWorktreeMergeReceipt(path)
		if err != nil || path != before.ReceiptPath {
			return receipt, err
		}
		reads++
		if reads == 1 {
			// The lane owner reads the recorded receipt directly while it
			// decides the continuation, so the first read through this hook of
			// that recorded path is the locked re-read: the replacement lands
			// strictly after the pre-lock bridge captured H.
			alternate = replaceLegacyPublishedCandidateHead(t, fixture)
		}
		return receipt, nil
	}
	refused, err := prepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{fixture.source}, Target: "main", Model: "test-model", AgentRuntime: "test",
	}, read, persistWorktreeMergeReceipt)
	if err == nil || !strings.Contains(err.Error(), "drifted from captured "+fixture.resolvedHead) {
		t.Fatalf("replaced-candidate prepare=%+v error=%v", refused, err)
	}
	if reads != 1 {
		t.Fatalf("locked re-read ran %d times, want exactly the post-lock read", reads)
	}
	if alternate == "" {
		t.Fatal("the locked re-read never replaced the candidate head")
	}
	assertLegacyPrepareRefusalLeftPublicationAlone(t, fixture, gh, before, alternate)
	stored, readErr := readWorktreeMergeReceipt(before.ReceiptPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	assertLegacyPublicationIdentityUnchanged(t, refused, before, stored)
	if refused.Status != WorktreeMergeConflict {
		t.Fatalf("replaced-candidate refusal changed the returned receipt: %+v", refused)
	}
}

// TestPrepareLegacyPublishedAdvanceRefusesCompetingContinuationForCapturedCandidate
// keeps the captured identity even when the held re-read no longer satisfies
// the bridge's own shape: another owner recorded a post-target CI failure and a
// landing between the pre-lock proof and the lock, so prepare would otherwise
// take the post-target repair continuation and build a new candidate that can
// never retain H.
//
//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestE2EPrepareLegacyPublishedAdvanceRefusesCompetingContinuationForCapturedCandidate(t *testing.T) {
	fixture := newLegacyPublishedAdvanceFixture(t)
	gh := installWorktreeMergeEngineGH(t, fixture.engineFixture, fixture.published, fixture.receipt.Candidate.Branch)
	before := fixture.receipt
	reads := 0
	read := func(path string) (WorktreeMergeReceipt, error) {
		receipt, err := readWorktreeMergeReceipt(path)
		if err != nil || path != before.ReceiptPath {
			return receipt, err
		}
		reads++
		if reads == 1 {
			receipt.Phase, receipt.Status = WorktreeMergePhasePrepare, WorktreeMergePostTargetCIFailed
			receipt.Candidate.SHA = fixture.resolvedHead
			receipt.LandingSHA = fixture.published
		}
		return receipt, nil
	}
	refused, err := prepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{fixture.source}, Target: "main", Model: "test-model", AgentRuntime: "test",
	}, read, persistWorktreeMergeReceipt)
	if err == nil || !strings.Contains(err.Error(), "cannot continue as a post-target repair") {
		t.Fatalf("competing-continuation prepare=%+v error=%v", refused, err)
	}
	if reads != 1 {
		t.Fatalf("locked re-read ran %d times, want exactly the post-lock read", reads)
	}
	assertLegacyPrepareRefusalLeftPublicationAlone(t, fixture, gh, before, fixture.resolvedHead)
}

// TestPrepareLegacyPublishedAdvanceRefusesHeadMoveBeforePreparingSave moves the
// managed candidate to a clean replacement while the native candidate receipt
// is being persisted, after the held proof: the preparing receipt must not
// carry the integration forward as if the replacement were still H.
//
//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestE2EPrepareLegacyPublishedAdvanceRefusesHeadMoveBeforePreparingSave(t *testing.T) {
	fixture := newLegacyPublishedAdvanceFixture(t)
	gh := installWorktreeMergeEngineGH(t, fixture.engineFixture, fixture.published, fixture.receipt.Candidate.Branch)
	before := fixture.receipt
	var alternate string
	save := func(receipt WorktreeMergeReceipt) error {
		if alternate == "" && receipt.Status == WorktreeMergePreparing && receipt.Candidate.SHA == fixture.resolvedHead {
			alternate = replaceLegacyPublishedCandidateHead(t, fixture)
		}
		return persistWorktreeMergeReceipt(receipt)
	}
	failed, err := prepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{fixture.source}, Target: "main", Model: "test-model", AgentRuntime: "test",
	}, readWorktreeMergeReceipt, save)
	if err == nil || !strings.Contains(err.Error(), "drifted from captured "+fixture.resolvedHead) {
		t.Fatalf("preparing-save head move=%+v error=%v", failed, err)
	}
	if alternate == "" {
		t.Fatal("the preparing save cutpoint never replaced the candidate head")
	}
	if failed.Status != WorktreeMergeConflict || failed.Candidate.SHA != fixture.resolvedHead ||
		failed.PublishedCandidateSHA != fixture.published || failed.PullRequest != gh.pr ||
		failed.TargetSHA != before.TargetSHA || failed.LandingSHA != "" ||
		len(failed.Sources) != 1 || failed.Sources[0].SHA != fixture.currentSource || len(failed.SourceRefreshes) != 2 {
		t.Fatalf("preparing-save refusal lost the last proven checkpoint: %+v", failed)
	}
	stored, readErr := readWorktreeMergeReceipt(before.ReceiptPath)
	if readErr != nil || stored.Status != WorktreeMergeConflict || stored.Candidate.SHA != fixture.resolvedHead ||
		stored.PublishedCandidateSHA != fixture.published || stored.PullRequest != gh.pr ||
		stored.Sources[0].SHA != fixture.currentSource || len(stored.SourceRefreshes) != 2 {
		t.Fatalf("durable preparing-save refusal=%+v error=%v", stored, readErr)
	}
	if head := strings.TrimSpace(runEngineGit(t, before.Candidate.Worktree, "rev-parse", "HEAD")); head != alternate {
		t.Fatalf("preparing-save refusal moved the replaced head from %s to %s", alternate, head)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+before.Candidate.Branch)); remote != fixture.published {
		t.Fatalf("preparing-save refusal published %s, want %s", remote, fixture.published)
	}
	if log := gh.ghLog(t); strings.Contains(log, "pr create") || strings.Contains(log, "pulls/41/merge") || strings.Contains(log, "update-branch") {
		t.Fatalf("preparing-save refusal performed a pull-request write:\n%s", log)
	}
}

// TestPrepareLegacyPublishedAdvanceRefusesIntegratedHeadWithoutCapturedCandidate
// moves the managed candidate during source integration. The resulting
// integration head omits H, so nothing may record it as the integrated
// candidate: the receipt keeps the last head this operation proved and the
// publication stays where it was.
//
//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestE2EPrepareLegacyPublishedAdvanceRefusesIntegratedHeadWithoutCapturedCandidate(t *testing.T) {
	fixture := newLegacyPublishedAdvanceFixture(t)
	gh := installWorktreeMergeEngineGH(t, fixture.engineFixture, fixture.published, fixture.receipt.Candidate.Branch)
	before := fixture.receipt
	var alternate string
	run := &legacyHeadMoveRunner{
		Runner: runner.New(),
		dir:    before.Candidate.Worktree,
		argv:   "merge-base " + fixture.currentSource + " HEAD",
		move:   func() { alternate = replaceLegacyPublishedCandidateHead(t, fixture) },
	}
	failed, err := prepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{fixture.source}, Target: "main", Model: "test-model", AgentRuntime: "test", run: run,
	}, readWorktreeMergeReceipt, persistWorktreeMergeReceipt)
	if err == nil || !strings.Contains(err.Error(), "does not retain captured legacy published prepare candidate "+fixture.resolvedHead) {
		t.Fatalf("integration head move=%+v error=%v", failed, err)
	}
	if alternate == "" {
		t.Fatal("the source integration cutpoint never replaced the candidate head")
	}
	if failed.Status != WorktreeMergeConflict || failed.Candidate.SHA != fixture.resolvedHead ||
		failed.PublishedCandidateSHA != fixture.published || failed.PullRequest != gh.pr ||
		failed.TargetSHA != before.TargetSHA || failed.Sources[0].Merged {
		t.Fatalf("integration refusal recorded an unproven candidate: %+v", failed)
	}
	stored, readErr := readWorktreeMergeReceipt(before.ReceiptPath)
	if readErr != nil || stored.Status != WorktreeMergeConflict || stored.Candidate.SHA != fixture.resolvedHead ||
		stored.PublishedCandidateSHA != fixture.published || stored.Sources[0].Merged {
		t.Fatalf("durable integration refusal=%+v error=%v", stored, readErr)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+before.Candidate.Branch)); remote != fixture.published {
		t.Fatalf("integration refusal published %s, want %s", remote, fixture.published)
	}
	if log := gh.ghLog(t); strings.Contains(log, "pr create") || strings.Contains(log, "pulls/41/merge") || strings.Contains(log, "update-branch") {
		t.Fatalf("integration refusal performed a pull-request write:\n%s", log)
	}
}

// TestPrepareLegacyPublishedAdvanceRefusesHeadMoveBeforePreparedRecord moves the
// managed candidate while the integrated checkpoint is persisted, after the
// last integration proof. The prepared record must not adopt the replacement:
// the receipt keeps the integrated H-prime it proved and nothing is published.
//
//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestE2EPrepareLegacyPublishedAdvanceRefusesHeadMoveBeforePreparedRecord(t *testing.T) {
	fixture := newLegacyPublishedAdvanceFixture(t)
	gh := installWorktreeMergeEngineGH(t, fixture.engineFixture, fixture.published, fixture.receipt.Candidate.Branch)
	before := fixture.receipt
	var alternate, integrated string
	save := func(receipt WorktreeMergeReceipt) error {
		if alternate == "" && receipt.Status == WorktreeMergePreparing && receipt.Sources[0].Merged && receipt.Candidate.SHA != fixture.resolvedHead {
			integrated = receipt.Candidate.SHA
			alternate = replaceLegacyPublishedCandidateHead(t, fixture)
		}
		return persistWorktreeMergeReceipt(receipt)
	}
	failed, err := prepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{fixture.source}, Target: "main", Model: "test-model", AgentRuntime: "test",
	}, readWorktreeMergeReceipt, save)
	if err == nil || !strings.Contains(err.Error(), "does not retain captured legacy published prepare candidate "+fixture.resolvedHead) {
		t.Fatalf("prepared-record head move=%+v error=%v", failed, err)
	}
	if alternate == "" || integrated == "" {
		t.Fatalf("prepared-record cutpoint integrated=%q alternate=%q", integrated, alternate)
	}
	assertNativeAncestor(t, before.Candidate.Worktree, fixture.resolvedHead, integrated)
	if failed.Status != WorktreeMergeConflict || failed.Candidate.SHA != integrated ||
		failed.PublishedCandidateSHA != fixture.published || failed.PullRequest != gh.pr ||
		failed.TargetSHA != before.TargetSHA || failed.LandingSHA != "" {
		t.Fatalf("prepared-record refusal lost the last proven checkpoint: %+v", failed)
	}
	stored, readErr := readWorktreeMergeReceipt(before.ReceiptPath)
	if readErr != nil || stored.Status != WorktreeMergeConflict || stored.Candidate.SHA != integrated ||
		stored.PublishedCandidateSHA != fixture.published || stored.PullRequest != gh.pr {
		t.Fatalf("durable prepared-record refusal=%+v error=%v", stored, readErr)
	}
	if head := strings.TrimSpace(runEngineGit(t, before.Candidate.Worktree, "rev-parse", "HEAD")); head != alternate {
		t.Fatalf("prepared-record refusal moved the replaced head from %s to %s", alternate, head)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+before.Candidate.Branch)); remote != fixture.published {
		t.Fatalf("prepared-record refusal published %s, want %s", remote, fixture.published)
	}
	if log := gh.ghLog(t); strings.Contains(log, "pr create") || strings.Contains(log, "pulls/41/merge") || strings.Contains(log, "update-branch") {
		t.Fatalf("prepared-record refusal performed a pull-request write:\n%s", log)
	}
}

// TestPrepareLegacyPublishedAdvanceRefusalSurvivesGuardReadFailures keeps the
// captured-identity guards non-inventive: when the private-Git read that proves
// the captured head or its retention fails, prepare refuses with that error and
// leaves the durable receipt and the resolved head alone.
//
//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestE2EPrepareLegacyPublishedAdvanceRefusalSurvivesGuardReadFailures(t *testing.T) {
	sentinel := errors.New("controlled captured-head read refused")
	//nolint:paralleltest // Each case builds its own t.Setenv real-Git fixture in the shared private lane.
	t.Run("captured head read", func(t *testing.T) {
		fixture := newLegacyPublishedAdvanceFixture(t)
		gh := installWorktreeMergeEngineGH(t, fixture.engineFixture, fixture.published, fixture.receipt.Candidate.Branch)
		before := fixture.receipt
		refused, err := prepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
			ProjectsRoot: fixture.githubDir, Sources: []string{fixture.source}, Target: "main", Model: "test-model", AgentRuntime: "test",
			// The fifth exact head read is the captured-identity guard: the
			// continuation check reads twice and each bridge proof reads once.
			run: &legacyReadCountRunner{Runner: runner.New(), dir: before.Candidate.Worktree, argv: "rev-parse --verify HEAD^{commit}", failAt: 5, err: sentinel},
		}, readWorktreeMergeReceipt, persistWorktreeMergeReceipt)
		if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "read captured legacy published prepare candidate") {
			t.Fatalf("captured-head read failure=%+v error=%v", refused, err)
		}
		assertLegacyPrepareRefusalLeftPublicationAlone(t, fixture, gh, before, fixture.resolvedHead)
	})
	//nolint:paralleltest // Each case builds its own t.Setenv real-Git fixture in the shared private lane.
	t.Run("captured retention read", func(t *testing.T) {
		fixture := newLegacyPublishedAdvanceFixture(t)
		gh := installWorktreeMergeEngineGH(t, fixture.engineFixture, fixture.published, fixture.receipt.Candidate.Branch)
		before := fixture.receipt
		failed, err := prepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
			ProjectsRoot: fixture.githubDir, Sources: []string{fixture.source}, Target: "main", Model: "test-model", AgentRuntime: "test",
			run: &legacyHeadMoveRunner{
				Runner: runner.New(), dir: before.Candidate.Worktree, argv: "merge-base " + fixture.resolvedHead, prefix: true, err: sentinel,
			},
		}, readWorktreeMergeReceipt, persistWorktreeMergeReceipt)
		if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "prove integrated candidate retains the captured legacy published prepare candidate") {
			t.Fatalf("captured-retention read failure=%+v error=%v", failed, err)
		}
		// The integration guard runs after the source was merged into H, so the
		// durable receipt keeps the last candidate this operation proved (H)
		// and records no integrated head, publication, or target change.
		if failed.Status != WorktreeMergeConflict || failed.Candidate.SHA != fixture.resolvedHead ||
			failed.PublishedCandidateSHA != fixture.published || failed.PullRequest != gh.pr ||
			failed.TargetSHA != before.TargetSHA || failed.Sources[0].Merged {
			t.Fatalf("captured-retention read failure recorded an unproven candidate: %+v", failed)
		}
		stored, readErr := readWorktreeMergeReceipt(before.ReceiptPath)
		if readErr != nil || stored.Status != WorktreeMergeConflict || stored.Candidate.SHA != fixture.resolvedHead ||
			stored.PublishedCandidateSHA != fixture.published || stored.Sources[0].Merged ||
			len(stored.SourceRefreshes) != 2 {
			t.Fatalf("durable captured-retention read failure=%+v error=%v", stored, readErr)
		}
		if head := strings.TrimSpace(runEngineGit(t, before.Candidate.Worktree, "rev-parse", "HEAD")); head == "" {
			t.Fatal("captured-retention read failure left the candidate without a head")
		}
		if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+before.Candidate.Branch)); remote != fixture.published {
			t.Fatalf("captured-retention read failure published %s, want %s", remote, fixture.published)
		}
		if log := gh.ghLog(t); strings.Contains(log, "pr create") || strings.Contains(log, "pulls/41/merge") || strings.Contains(log, "update-branch") {
			t.Fatalf("captured-retention read failure performed a pull-request write:\n%s", log)
		}
	})
}

// legacyHeadMoveRunner mutates the fixture's private Git state (or returns a
// controlled error) when it observes one exact or prefix-matched command, then
// delegates every other command to the real runner.
type legacyHeadMoveRunner struct {
	runner.Runner
	dir, argv string
	prefix    bool
	move      func()
	err       error
	moved     bool
}

func (r *legacyHeadMoveRunner) RunOpts(ctx context.Context, dir string, options runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if !r.moved && name == "git" && dir == r.dir {
		joined := strings.Join(args, " ")
		if joined == r.argv || (r.prefix && strings.HasPrefix(joined, r.argv)) {
			r.moved = true
			if r.err != nil {
				return runner.Result{}, r.err
			}
			if r.move != nil {
				r.move()
			}
		}
	}
	return r.Runner.RunOpts(ctx, dir, options, name, args...)
}

// legacyReadCountRunner fails the Nth matching command, letting a test target a
// read that happens only after the bridge's own identical read.
type legacyReadCountRunner struct {
	runner.Runner
	dir, argv string
	failAt    int
	seen      int
	err       error
}

func (r *legacyReadCountRunner) RunOpts(ctx context.Context, dir string, options runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if name == "git" && dir == r.dir && strings.Join(args, " ") == r.argv {
		r.seen++
		if r.seen == r.failAt {
			return runner.Result{}, r.err
		}
	}
	return r.Runner.RunOpts(ctx, dir, options, name, args...)
}

// TestPrepareOrdinaryPlanFailureStaysAConflict keeps the untouched ordinary
// prepare plan-failure contract: only the captured legacy published bridge keeps
// a resumable preparing receipt, and a plain candidate still records its
// pre-validation planning error as a conflict.
//
//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestE2EPrepareOrdinaryPlanFailureStaysAConflict(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "ordinary-plan-source", "feature/ordinary-plan", "ordinary-plan.txt", "ordinary\n")
	failed, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main",
		Model: "test-model", AgentRuntime: "test", Route: WorktreeMergeRouteDirect, ValidateLocally: true,
	})
	if err == nil || !strings.Contains(err.Error(), "direct route is not authoritatively permitted") {
		t.Fatalf("ordinary plan failure=%+v error=%v", failed, err)
	}
	if failed.Status != WorktreeMergeConflict || failed.Candidate.SHA == "" || failed.PullRequest != "" {
		t.Fatalf("ordinary plan failure=%+v", failed)
	}
	stored, readErr := readWorktreeMergeReceipt(failed.ReceiptPath)
	if readErr != nil || stored.Status != WorktreeMergeConflict || stored.Candidate.SHA != failed.Candidate.SHA {
		t.Fatalf("durable ordinary plan failure=%+v error=%v", stored, readErr)
	}
	if len(stored.Sources) != 1 {
		t.Fatalf("ordinary plan failure recorded %d sources", len(stored.Sources))
	}
	if head := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD")); head != stored.Sources[0].SHA {
		t.Fatalf("ordinary plan failure moved the source head to %s, want recorded %s", head, stored.Sources[0].SHA)
	}
}
