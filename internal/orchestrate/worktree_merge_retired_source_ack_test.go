package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// retiredSourceFailure is a directly landed receipt whose post-target CI
// failed, whose source checkout an applied WB cleanup then retired, while the
// merger's own candidate checkout survives. A later target commit stands in
// for the forward fix.
type retiredSourceFailure struct {
	fixture    engineFixture
	receipt    WorktreeMergeReceipt
	superseder string
}

func newRetiredSourceFailure(t *testing.T, advanceSource, supersede bool) retiredSourceFailure {
	t.Helper()
	fixture := newEngineFixture(t)
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
	source := createMergeSource(t, fixture, "retired-ack-source", "feature/retired-ack", "retired.txt", "retired\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", receipt.Candidate.SHA+":refs/heads/main")
	runEngineGit(t, fixture.canonical, "pull", "--ff-only", "origin", "main")
	receipt.Phase = WorktreeMergePhaseLand
	receipt.Status = WorktreeMergePostTargetCIFailed
	receipt.LandingSHA = receipt.Candidate.SHA
	receipt.Checks = githubchecks.PullRequestWaitResult{Status: githubchecks.PullRequestWaitFailed, Head: receipt.LandingSHA, Reason: "inherited target check failed"}
	receipt.Failure = "post-target CI failed"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	if advanceSource {
		runEngineGit(t, source.WorktreeDir, "commit", "--allow-empty", "-m", "later source work")
		runEngineGit(t, source.WorktreeDir, "push", "origin", source.Branch)
		runEngineGit(t, fixture.canonical, "fetch", "origin", source.Branch)
		runEngineGit(t, fixture.canonical, "merge", "--no-ff", "--no-edit", "origin/"+source.Branch)
		runEngineGit(t, fixture.canonical, "push", "origin", "main")
	}
	outcome, err := worktrees.Cleanup(context.Background(), worktrees.CleanupOptions{
		ProjectsRoot: fixture.githubDir, Task: receipt.Sources[0].Task, Base: "main",
		Apply: true, DeleteRemote: true, OlderThan: 0, Workers: 1,
	})
	if err != nil || len(outcome.Results) != 1 || !outcome.Results[0].Applied {
		t.Fatalf("retire source: %+v, error %v", outcome, err)
	}
	if _, err := os.Stat(receipt.Candidate.Worktree); err != nil {
		t.Fatalf("candidate checkout must survive: %v", err)
	}
	failure := retiredSourceFailure{fixture: fixture, receipt: receipt}
	if supersede {
		runEngineGit(t, fixture.canonical, "commit", "--allow-empty", "-m", "fix: forward repair of inherited CI failure")
		runEngineGit(t, fixture.canonical, "push", "origin", "main")
		failure.superseder = strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	}
	return failure
}

func (f retiredSourceFailure) options(observe targetChecksObserver) WorktreeMergeLandedFailureAcknowledgementOptions {
	return WorktreeMergeLandedFailureAcknowledgementOptions{
		ProjectsRoot: f.fixture.githubDir, Receipt: f.receipt.ReceiptPath, Apply: true,
		Actor: "reviewer", Reason: "landing is in the target and a later head passed CI", observeTargetChecks: observe,
	}
}

func checksResult(status githubchecks.PullRequestWaitStatus) targetChecksObserver {
	return func(_ context.Context, _, _, head string) (githubchecks.PullRequestWaitResult, error) {
		return githubchecks.PullRequestWaitResult{Status: status, Head: head, Reason: "observed " + string(status)}, nil
	}
}

func (f retiredSourceFailure) acknowledge(t *testing.T, options WorktreeMergeLandedFailureAcknowledgementOptions, run runner.Runner) (WorktreeMergeLandedFailureAcknowledgement, error) {
	t.Helper()
	if run == nil {
		run = defaultRunner
	}
	return acknowledgeLandedMergeFailure(context.Background(), options, run, readWorktreeMergeReceipt, persistLandedFailureAcknowledgement)
}

func requireNoAcknowledgement(t *testing.T, receipt WorktreeMergeReceipt) {
	t.Helper()
	if _, err := os.Stat(landedFailureAcknowledgementPath(receipt.ReceiptPath)); !os.IsNotExist(err) {
		t.Fatalf("refusal wrote an acknowledgement: %v", err)
	}
}

//nolint:paralleltest // Cleanup reads the fake gh through process environment.
func TestAcknowledgeRetiredSourcePostTargetFailureReleasesLaneWhenSupersededByGreen(t *testing.T) {
	failure := newRetiredSourceFailure(t, false, true)
	before, err := os.ReadFile(failure.receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	released, err := mergeReceiptReleasedByFailureEvidence(context.Background(), failure.fixture.githubDir, failure.receipt)
	if err != nil || released {
		t.Fatalf("lane released before acknowledgement: %v %v", released, err)
	}

	ack, err := failure.acknowledge(t, failure.options(checksResult(githubchecks.PullRequestWaitPassed)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if ack.CurrentTargetSHA != failure.superseder || ack.SupersedingTargetChecks == nil || ack.SupersedingTargetChecks.Head != failure.superseder ||
		ack.SupersedingTargetChecks.Status != githubchecks.PullRequestWaitPassed || len(ack.RetiredSources) != 1 {
		t.Fatalf("acknowledgement evidence = %+v", ack)
	}
	retired := ack.RetiredSources[0]
	if retired.SHA != failure.receipt.Sources[0].SHA || retired.CleanupHeadSHA != retired.SHA || retired.CleanupReportPath == "" || retired.ClaimBaseSHA == "" {
		t.Fatalf("retired source evidence = %+v", retired)
	}
	if ack.Actor != "reviewer" || ack.Reason == "" {
		t.Fatalf("acknowledgement lost who and why: %+v", ack)
	}
	if _, err := readLandedFailureAcknowledgement(ack.AcknowledgementPath, failure.receipt); err != nil {
		t.Fatalf("persisted acknowledgement does not authenticate: %v", err)
	}
	released, err = mergeReceiptReleasedByFailureEvidence(context.Background(), failure.fixture.githubDir, failure.receipt)
	if err != nil || !released {
		t.Fatalf("lane still held after acknowledgement: %v %v", released, err)
	}
	after, err := os.ReadFile(failure.receipt.ReceiptPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("historical receipt changed: %v", err)
	}
	replay, err := failure.acknowledge(t, failure.options(checksResult(githubchecks.PullRequestWaitPassed)), nil)
	if err != nil || replay.ID != ack.ID {
		t.Fatalf("replay = %+v, %v", replay, err)
	}

	// A persisted record whose superseding head is not green no longer
	// releases the lane.
	tampered := ack
	tampered.SupersedingTargetChecks = &WorktreeMergeSupersedingTargetChecks{Head: ack.CurrentTargetSHA, Status: githubchecks.PullRequestWaitPending, ObservedAt: time.Now()}
	if err := os.Remove(ack.AcknowledgementPath); err != nil {
		t.Fatal(err)
	}
	if err := persistLandedFailureAcknowledgement(ack.AcknowledgementPath, tampered); err != nil {
		t.Fatal(err)
	}
	if _, err := readLandedFailureAcknowledgement(ack.AcknowledgementPath, failure.receipt); err == nil || !strings.Contains(err.Error(), "incomplete retired-source evidence") {
		t.Fatalf("tampered acknowledgement error = %v", err)
	}
}

//nolint:paralleltest // Cleanup reads the fake gh through process environment.
func TestAcknowledgeRetiredSourceAcceptsCleanedDescendantContainedInTarget(t *testing.T) {
	failure := newRetiredSourceFailure(t, true, false)
	ack, err := failure.acknowledge(t, failure.options(checksResult(githubchecks.PullRequestWaitPassed)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if proof := ack.RetiredSources[0]; proof.CleanupHeadSHA == proof.SHA {
		t.Fatalf("cleaned descendant head not recorded: %+v", proof)
	}
}

//nolint:paralleltest // Cleanup reads the fake gh through process environment.
func TestAcknowledgeRetiredSourceRefusesWithoutGreenSupersedingHead(t *testing.T) {
	failure := newRetiredSourceFailure(t, false, true)
	for name, observe := range map[string]targetChecksObserver{
		"pending": checksResult(githubchecks.PullRequestWaitPending),
		"failed":  checksResult(githubchecks.PullRequestWaitFailed),
		"read error": func(context.Context, string, string, string) (githubchecks.PullRequestWaitResult, error) {
			return githubchecks.PullRequestWaitResult{}, errors.New("checks unavailable")
		},
		"other head": func(context.Context, string, string, string) (githubchecks.PullRequestWaitResult, error) {
			return githubchecks.PullRequestWaitResult{Status: githubchecks.PullRequestWaitPassed, Head: strings.Repeat("f", 40)}, nil
		},
	} {
		if _, err := failure.acknowledge(t, failure.options(observe), nil); err == nil {
			t.Fatalf("%s: accepted", name)
		} else if name == "pending" && !strings.Contains(err.Error(), "wb ci wait") {
			t.Fatalf("pending refusal does not name the wait: %v", err)
		}
	}
	requireNoAcknowledgement(t, failure.receipt)
}

//nolint:paralleltest // Cleanup reads the fake gh through process environment.
func TestAcknowledgeRetiredSourceRefusesWhileTargetIsStillTheFailedLanding(t *testing.T) {
	failure := newRetiredSourceFailure(t, false, false)
	_, err := failure.acknowledge(t, failure.options(checksResult(githubchecks.PullRequestWaitPassed)), nil)
	if err == nil || !strings.Contains(err.Error(), "is still the failed landing") {
		t.Fatalf("error = %v", err)
	}
	requireNoAcknowledgement(t, failure.receipt)

	// A source path that cannot be inspected is neither present nor retired.
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	failure.receipt.Sources[0].Worktree = filepath.Join(file, "child")
	if err := persistWorktreeMergeReceipt(failure.receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := failure.acknowledge(t, failure.options(checksResult(githubchecks.PullRequestWaitPassed)), nil); err == nil || !strings.Contains(err.Error(), "inspect receipted source") {
		t.Fatalf("uninspectable source error = %v", err)
	}
}

// ancestryFaultRunner answers chosen merge-base questions as not contained,
// or fails them, and runs every other command for real.
type ancestryFaultRunner struct {
	runner.Runner
	ancestor, descendant string
	fail                 bool
}

func (r ancestryFaultRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if name == "git" && len(args) == 3 && args[0] == "merge-base" && (r.ancestor == "" || args[1] == r.ancestor) && (r.descendant == "" || args[2] == r.descendant) {
		if r.fail {
			return runner.Result{ExitCode: 2}, errors.New("object store unavailable")
		}
		return runner.Result{Stdout: strings.Repeat("0", 40) + "\n"}, nil
	}
	return r.Runner.RunOpts(ctx, dir, opts, name, args...)
}

//nolint:paralleltest // Cleanup reads the fake gh through process environment.
func TestAcknowledgeRetiredSourceRefusesUnprovenContainment(t *testing.T) {
	failure := newRetiredSourceFailure(t, true, true)
	receipt, source := failure.receipt, failure.receipt.Sources[0]
	gitDir := receipt.Candidate.Worktree
	proof, err := proveRetiredLandedFailureSource(context.Background(), defaultRunner, worktrees.ReadRemovedTerminalWorkLogClaimBase, failure.fixture.githubDir, gitDir, receipt, source)
	if err != nil {
		t.Fatal(err)
	}
	cleaned := proof.CleanupHeadSHA

	// End to end: the cleaned head is not in the current target.
	notInTarget := ancestryFaultRunner{Runner: defaultRunner, ancestor: cleaned, descendant: failure.superseder}
	if _, err := failure.acknowledge(t, failure.options(checksResult(githubchecks.PullRequestWaitPassed)), notInTarget); err == nil || !strings.Contains(err.Error(), "does not contain cleaned head") {
		t.Fatalf("not-in-target error = %v", err)
	}
	requireNoAcknowledgement(t, receipt)

	for name, run := range map[string]ancestryFaultRunner{
		"cleaned head not a descendant":  {ancestor: source.SHA, descendant: cleaned},
		"descendant ancestry unreadable": {ancestor: source.SHA, descendant: cleaned, fail: true},
		"source lacks claim base":        {ancestor: proof.ClaimBaseSHA, descendant: source.SHA},
		"claim base ancestry unreadable": {ancestor: proof.ClaimBaseSHA, descendant: source.SHA, fail: true},
	} {
		run.Runner = defaultRunner
		if _, err := proveRetiredLandedFailureSource(context.Background(), run, worktrees.ReadRemovedTerminalWorkLogClaimBase, failure.fixture.githubDir, gitDir, receipt, source); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	unsealed := func(string, worktrees.TerminalWorkLogExpectation) (string, error) {
		return "", errors.New("terminal seal missing")
	}
	if _, err := proveRetiredLandedFailureSource(context.Background(), defaultRunner, unsealed, failure.fixture.githubDir, gitDir, receipt, source); err == nil || !strings.Contains(err.Error(), "terminal seal missing") {
		t.Fatalf("unsealed Work Log error = %v", err)
	}
	unreadable := ancestryFaultRunner{Runner: defaultRunner, ancestor: cleaned, descendant: failure.superseder, fail: true}
	if _, err := proveRetiredSourcesSuperseded(context.Background(), unreadable, checksResult(githubchecks.PullRequestWaitPassed), gitDir, receipt, []WorktreeMergeCleanedSourceProof{proof}, failure.superseder); err == nil {
		t.Fatal("unreadable target containment accepted")
	}
}

func TestAcknowledgeRetiredSourceRefusesWithoutCleanupRecord(t *testing.T) {
	t.Parallel()
	fixture := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, fixture, "unrecorded-source", "feature/unrecorded", "unrecorded.txt", "unrecorded\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", receipt.Candidate.SHA+":refs/heads/main")
	runEngineGit(t, fixture.canonical, "pull", "--ff-only", "origin", "main")
	receipt.Phase, receipt.Status, receipt.LandingSHA = WorktreeMergePhaseLand, WorktreeMergePostTargetCIFailed, receipt.Candidate.SHA
	receipt.Checks = githubchecks.PullRequestWaitResult{Status: githubchecks.PullRequestWaitFailed, Head: receipt.LandingSHA}
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	// Removed by hand, not by WB cleanup: absence alone is never evidence.
	runEngineGit(t, fixture.canonical, "worktree", "remove", "--force", source.WorktreeDir)
	failure := retiredSourceFailure{fixture: fixture, receipt: receipt}
	_, err = failure.acknowledge(t, failure.options(checksResult(githubchecks.PullRequestWaitPassed)), nil)
	if err == nil || !strings.Contains(err.Error(), "no exact applied WB cleanup record") {
		t.Fatalf("error = %v", err)
	}
	requireNoAcknowledgement(t, receipt)
}

func TestRetiredSourceProofRequiresCompleteIdentityAndInspectableCheckout(t *testing.T) {
	t.Parallel()
	receipt := WorktreeMergeReceipt{Status: WorktreeMergePostTargetCIFailed, Repository: "acme/app", Target: "main"}
	if _, err := proveRetiredLandedFailureSource(context.Background(), defaultRunner, worktrees.ReadRemovedTerminalWorkLogClaimBase, t.TempDir(), t.TempDir(), receipt, WorktreeMergeSource{Task: "t"}); err == nil {
		t.Fatal("incomplete identity accepted")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := sourceCheckoutRetired(receipt, WorktreeMergeSource{Worktree: filepath.Join(file, "child")}); err == nil {
		t.Fatal("uninspectable checkout treated as present or absent")
	}
	if retired, err := sourceCheckoutRetired(WorktreeMergeReceipt{Status: WorktreeMergeValidationFailed}, WorktreeMergeSource{Worktree: "/missing"}); err != nil || retired {
		t.Fatalf("non post-target receipt used retired path: %v %v", retired, err)
	}
}

func TestSupersedingTargetChecksObserverRefusesIncompleteIdentityWithoutGitHub(t *testing.T) {
	t.Parallel()
	if _, err := observeSupersedingTargetChecks(context.Background(), "", "", ""); err == nil {
		t.Fatal("incomplete identity observed")
	}
}

func TestRetiredSourceEvidenceMustMatchItsReceipt(t *testing.T) {
	t.Parallel()
	landing, current := strings.Repeat("a", 40), strings.Repeat("b", 40)
	source := WorktreeMergeSource{Task: "s", Worktree: "/w", Branch: "b", SHA: landing}
	receipt := WorktreeMergeReceipt{Status: WorktreeMergePostTargetCIFailed, LandingSHA: landing, Sources: []WorktreeMergeSource{source}}
	valid := WorktreeMergeLandedFailureAcknowledgement{
		CurrentTargetSHA: current, Actor: "a", Reason: "r", RecordedAt: time.Now(),
		RetiredSources: []WorktreeMergeCleanedSourceProof{{
			Task: "s", Worktree: "/w", Branch: "b", SHA: landing, CleanupReportPath: "/r", CleanupReportSHA256: strings.Repeat("c", 64),
			CleanupHeadSHA: landing, ClaimBaseSHA: landing,
		}},
		SupersedingTargetChecks: &WorktreeMergeSupersedingTargetChecks{Head: current, Status: githubchecks.PullRequestWaitPassed, ObservedAt: time.Now()},
	}
	if err := validateRetiredSourceEvidence(valid, receipt); err != nil {
		t.Fatal(err)
	}
	if err := validateRetiredSourceEvidence(WorktreeMergeLandedFailureAcknowledgement{}, receipt); err != nil {
		t.Fatal(err)
	}
	noChecks := valid
	noChecks.SupersedingTargetChecks = nil
	duplicated := valid
	duplicated.RetiredSources = append(append([]WorktreeMergeCleanedSourceProof(nil), valid.RetiredSources...), valid.RetiredSources...)
	for name, ack := range map[string]WorktreeMergeLandedFailureAcknowledgement{"no checks": noChecks, "duplicate source": duplicated} {
		if err := validateRetiredSourceEvidence(ack, receipt); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
