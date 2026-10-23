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
	"time"

	"github.com/sneat-dev/wb/internal/runner"
)

// targetAbsorbedFaultRunner delegates to native Git and fails only the one
// git subcommand under test.
type targetAbsorbedFaultRunner struct {
	runner.Runner
	failSubcommand string
}

func (r targetAbsorbedFaultRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if name == "git" && len(args) > 0 && args[0] == r.failSubcommand {
		return runner.Result{ExitCode: 1}, errors.New("injected " + r.failSubcommand + " failure")
	}
	return r.Runner.RunOpts(ctx, dir, opts, name, args...)
}

// targetAbsorbedFixture prepares one real candidate on an explicitly rooted
// fixture and returns it still `prepared`, the state that blocked
// datatug-core main on 2026-10-22.
func targetAbsorbedFixture(t *testing.T) (engineFixture, WorktreeMergeReceipt) {
	t.Helper()
	fixture := newExplicitRootEngineFixture(t)
	source := createMergeSourceOnBase(t, fixture, "absorbed-first", "wb/absorbed/first", "main", "first.txt", "first\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main",
		Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != WorktreeMergePrepared {
		t.Fatalf("fixture receipt status = %s, want prepared", receipt.Status)
	}
	return fixture, receipt
}

// landCandidateOutsideReceipt publishes the receipt's candidate to the remote
// target the way a separately merged pull request does, leaving the receipt
// itself untouched.
func landCandidateOutsideReceipt(t *testing.T, fixture engineFixture, receipt WorktreeMergeReceipt) {
	t.Helper()
	runEngineGit(t, fixture.canonical, "push", "origin", receipt.Candidate.SHA+":refs/heads/main")
}

func targetAbsorbedAcknowledgements(t *testing.T, receipt WorktreeMergeReceipt) []string {
	t.Helper()
	matches, err := filepath.Glob(receipt.ReceiptPath + ".*" + worktreeMergeTargetAbsorbedAcknowledgementSuffix)
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func TestE2EPrepareReleasesLaneHeldByPreparedReceiptWhoseCandidateAlreadyLanded(t *testing.T) {
	t.Parallel()
	fixture, held := targetAbsorbedFixture(t)
	before, err := os.ReadFile(held.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	second := createMergeSourceOnBase(t, fixture, "absorbed-second", "wb/absorbed/second", "main", "second.txt", "second\n")
	prepareSecond := func() (WorktreeMergeReceipt, error) {
		return PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
			ProjectsRoot: fixture.githubDir, Sources: []string{second.WorktreeDir}, Target: "main",
			Model: "test-model", AgentRuntime: "test",
		})
	}
	if _, err := prepareSecond(); err == nil || !strings.Contains(err.Error(), "still owned by non-terminal receipt") {
		t.Fatalf("prepare while unlanded candidate holds the lane = %v, want refusal", err)
	}
	if acks := targetAbsorbedAcknowledgements(t, held); len(acks) != 0 {
		t.Fatalf("unlanded receipt was acknowledged: %v", acks)
	}
	if claim, err := ActiveMergeLaneClaim(fixture.githubDir, held.Repository, held.Sources[0].Branch); err != nil || claim == nil {
		t.Fatalf("branch claim of an unlanded receipt = %+v, %v; want the held receipt", claim, err)
	}

	landCandidateOutsideReceipt(t, fixture, held)
	next, err := prepareSecond()
	if err != nil {
		t.Fatalf("prepare after the held candidate landed = %v, want the lane released", err)
	}
	if next.ReceiptPath == held.ReceiptPath || next.Status != WorktreeMergePrepared {
		t.Fatalf("second receipt = %+v", next)
	}
	after, err := os.ReadFile(held.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("the historical receipt was rewritten")
	}
	acks := targetAbsorbedAcknowledgements(t, held)
	if len(acks) != 1 {
		t.Fatalf("acknowledgements = %v, want exactly one", acks)
	}
	ack, path, err := readWorktreeMergeTargetAbsorbedAcknowledgement(held)
	if err != nil || path != acks[0] {
		t.Fatalf("read acknowledgement = %v at %s", err, path)
	}
	if ack.Actor != WorktreeMergeTargetAbsorbedScanActor || ack.TargetSHA != held.Candidate.SHA ||
		ack.ReceiptStatus != WorktreeMergePrepared || !strings.Contains(ack.Reason, "nothing is left to land") {
		t.Fatalf("acknowledgement = %+v", ack)
	}
	for _, source := range held.Sources {
		if !strings.Contains(strings.Join(ack.ProvedCommits, " "), source.SHA) {
			t.Fatalf("source %s missing from proved commits %v", source.SHA, ack.ProvedCommits)
		}
	}
	if claim, err := ActiveMergeLaneClaim(fixture.githubDir, held.Repository, held.Sources[0].Branch); err != nil || claim != nil {
		t.Fatalf("branch claim of an absorbed receipt = %+v, %v; want none", claim, err)
	}
}

func TestE2ETargetAbsorbedAcknowledgementNeverReleasesAfterTamperOrReceiptRewrite(t *testing.T) {
	t.Parallel()
	fixture, held := targetAbsorbedFixture(t)
	landCandidateOutsideReceipt(t, fixture, held)
	if !worktreeMergeReceiptReleasedByTargetAbsorption(context.Background(), defaultRunner, fixture.githubDir, held) {
		t.Fatal("absorbed receipt was not released")
	}
	// An existing acknowledgement short-circuits a second proof.
	if !worktreeMergeReceiptReleasedByTargetAbsorption(context.Background(), defaultRunner, fixture.githubDir, held) {
		t.Fatal("recorded acknowledgement was not honored")
	}
	path := targetAbsorbedAcknowledgements(t, held)[0]
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(original), WorktreeMergeTargetAbsorbedScanActor, "someone else", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if worktreeMergeReceiptReleasedByTargetAbsorption(context.Background(), defaultRunner, fixture.githubDir, held) {
		t.Fatal("a tampered acknowledgement released the lane")
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readWorktreeMergeTargetAbsorbedAcknowledgement(held); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("malformed acknowledgement read = %v, want a decode error", err)
	}

	// A receipt rewritten after the proof is judged afresh: its new bytes
	// have no acknowledgement until a new proof records one.
	rewritten := held
	rewritten.UpdatedAt = rewritten.UpdatedAt.Add(time.Second)
	if err := persistWorktreeMergeReceipt(rewritten); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readWorktreeMergeTargetAbsorbedAcknowledgement(rewritten); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("acknowledgement for rewritten receipt = %v, want none", err)
	}
	if !worktreeMergeReceiptReleasedByTargetAbsorption(context.Background(), defaultRunner, fixture.githubDir, rewritten) {
		t.Fatal("rewritten absorbed receipt was not re-proved")
	}
	if got := len(targetAbsorbedAcknowledgements(t, held)); got != 2 {
		t.Fatalf("acknowledgements after rewrite = %d, want one per receipt revision", got)
	}
}

func TestE2ETargetAbsorptionKeepsBlockingWhenProofOrRecordFails(t *testing.T) {
	t.Parallel()
	fixture, held := targetAbsorbedFixture(t)
	ctx := context.Background()

	if worktreeMergeReceiptReleasedByTargetAbsorption(ctx, defaultRunner, fixture.githubDir, held) {
		t.Fatal("a receipt whose candidate is not on the target was released")
	}
	preparing := held
	preparing.Status = WorktreeMergePreparing
	if _, _, err := proveWorktreeMergeTargetAbsorbed(ctx, defaultRunner, fixture.githubDir, preparing, time.Now()); err == nil || !strings.Contains(err.Error(), "not eligible") {
		t.Fatalf("preparing proof = %v, want ineligible", err)
	}
	unrecorded := held
	unrecorded.Sources = append([]WorktreeMergeSource(nil), held.Sources...)
	unrecorded.Sources[0].SHA = ""
	if _, _, err := proveWorktreeMergeTargetAbsorbed(ctx, defaultRunner, fixture.githubDir, unrecorded, time.Now()); err == nil || !strings.Contains(err.Error(), "every source head") {
		t.Fatalf("unrecorded source proof = %v", err)
	}
	foreign := held
	foreign.Repository = "not-a-slug"
	if _, _, err := proveWorktreeMergeTargetAbsorbed(ctx, defaultRunner, fixture.githubDir, foreign, time.Now()); err == nil {
		t.Fatal("an unresolvable repository was proved")
	}
	missingTarget := held
	missingTarget.Target = "no-such-branch"
	if _, _, err := proveWorktreeMergeTargetAbsorbed(ctx, defaultRunner, fixture.githubDir, missingTarget, time.Now()); err == nil || !strings.Contains(err.Error(), "fetch exact remote target") {
		t.Fatalf("missing target proof = %v", err)
	}

	landCandidateOutsideReceipt(t, fixture, held)
	unknownObject := held
	unknownObject.PublishedCandidateSHA = strings.Repeat("e", 40)
	if _, _, err := proveWorktreeMergeTargetAbsorbed(ctx, defaultRunner, fixture.githubDir, unknownObject, time.Now()); err == nil {
		t.Fatal("an unknown commit was proved absorbed")
	}
	targetSHA, commits, err := proveWorktreeMergeTargetAbsorbed(ctx, defaultRunner, fixture.githubDir, held, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	changed := held
	changed.Status = WorktreeMergeChecksPending
	if _, _, err := recordWorktreeMergeTargetAbsorbed(changed, targetSHA, commits, "test", "test", time.Now()); err == nil || !strings.Contains(err.Error(), "changed while") {
		t.Fatalf("record against a changed receipt = %v", err)
	}
	gone := held
	gone.ReceiptPath = filepath.Join(t.TempDir(), "gone.json")
	if _, _, err := recordWorktreeMergeTargetAbsorbed(gone, targetSHA, commits, "test", "test", time.Now()); err == nil {
		t.Fatal("recorded against a missing receipt")
	}
	// A missing receipt reads as unacknowledged; its proof can never be
	// recorded, so it still never releases a lane.
	if _, _, err := readWorktreeMergeTargetAbsorbedAcknowledgement(gone); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("acknowledgement read for a missing receipt = %v, want not-exist", err)
	}
	unreadable := held
	unreadable.ReceiptPath = filepath.Join(t.TempDir(), "unreadable.json")
	if err := os.WriteFile(unreadable.ReceiptPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := recordWorktreeMergeTargetAbsorbed(unreadable, targetSHA, commits, "test", "test", time.Now()); err == nil {
		t.Fatal("recorded against an invalid receipt")
	}

	// A competing writer that published a valid acknowledgement first wins;
	// one that published garbage keeps the lane blocked.
	first, firstPath, err := recordWorktreeMergeTargetAbsorbed(held, targetSHA, commits, "first", "first writer", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	second, secondPath, err := recordWorktreeMergeTargetAbsorbed(held, targetSHA, commits, "second", "second writer", time.Now())
	if err != nil || second.ID != first.ID || secondPath != firstPath {
		t.Fatalf("second record = %+v at %s, %v; want the first writer's acknowledgement", second, secondPath, err)
	}
	if err := os.WriteFile(firstPath, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := recordWorktreeMergeTargetAbsorbed(held, targetSHA, commits, "third", "third writer", time.Now()); err == nil {
		t.Fatal("a garbage acknowledgement was accepted")
	}
	if worktreeMergeReceiptReleasedByTargetAbsorption(ctx, defaultRunner, fixture.githubDir, held) {
		t.Fatal("a garbage acknowledgement released the lane")
	}
}

func TestE2ETargetAbsorptionRecordFailsWhenReportDirectoryIsUnreadable(t *testing.T) {
	t.Parallel()
	fixture, held := targetAbsorbedFixture(t)
	landCandidateOutsideReceipt(t, fixture, held)
	directory := filepath.Dir(held.ReceiptPath)
	if err := os.Chmod(directory, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
	if worktreeMergeReceiptReleasedByTargetAbsorption(context.Background(), defaultRunner, fixture.githubDir, held) {
		t.Fatal("released without a recorded acknowledgement")
	}
}

func TestE2ESweepTargetAbsorbedReceiptsReportsUnwritableReportDirectory(t *testing.T) {
	t.Parallel()
	fixture, held := targetAbsorbedFixture(t)
	landCandidateOutsideReceipt(t, fixture, held)
	directory := filepath.Dir(held.ReceiptPath)
	if err := os.Chmod(directory, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
	sweep, err := SweepTargetAbsorbedWorktreeMergeReceipts(context.Background(), WorktreeMergeTargetAbsorbedSweepOptions{ProjectsRoot: fixture.githubDir, Apply: true, Actor: "operator", Reason: "drain absorbed lanes"})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range sweep.Results {
		if result.Receipt == held.ReceiptPath {
			if result.Outcome != WorktreeMergeTargetAbsorbedNotAbsorbed || result.Detail == "" {
				t.Fatalf("unwritable sweep = %+v, want a reported write failure", result)
			}
			return
		}
	}
	t.Fatalf("held receipt missing from sweep: %+v", sweep.Results)
}

func TestE2ETargetAbsorptionKeepsLaneWhileCandidateBranchHoldsUnlandedWork(t *testing.T) {
	t.Parallel()
	fixture, held := targetAbsorbedFixture(t)
	ctx := context.Background()
	landCandidateOutsideReceipt(t, fixture, held)
	writeEngineFile(t, filepath.Join(held.Candidate.Worktree, "follow-up.txt"), "follow-up\n")
	runEngineGit(t, held.Candidate.Worktree, "add", "follow-up.txt")
	runEngineGit(t, held.Candidate.Worktree, "commit", "-m", "feat: follow-up on the candidate")
	if _, _, err := proveWorktreeMergeTargetAbsorbed(ctx, defaultRunner, fixture.githubDir, held, time.Now()); err == nil || !strings.Contains(err.Error(), "is not in main") {
		t.Fatalf("proof with an advanced candidate branch = %v, want refusal", err)
	}
	runEngineGit(t, held.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/main")
	runEngineGit(t, held.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/"+held.Candidate.Branch)
	if _, _, err := proveWorktreeMergeTargetAbsorbed(ctx, defaultRunner, fixture.githubDir, held, time.Now()); err != nil {
		t.Fatalf("proof once the follow-up landed = %v", err)
	}
	for _, subcommand := range []string{"for-each-ref", "ls-remote"} {
		faulty := targetAbsorbedFaultRunner{Runner: defaultRunner, failSubcommand: subcommand}
		if _, _, err := proveWorktreeMergeTargetAbsorbed(ctx, faulty, fixture.githubDir, held, time.Now()); err == nil || !strings.Contains(err.Error(), "injected "+subcommand) {
			t.Fatalf("proof with failing %s = %v", subcommand, err)
		}
	}
}

func TestE2ESweepTargetAbsorbedReceiptsDryRunThenApply(t *testing.T) {
	t.Parallel()
	fixture, held := targetAbsorbedFixture(t)
	ctx := context.Background()
	if _, err := sweepTargetAbsorbedWorktreeMergeReceipts(ctx, defaultRunner, WorktreeMergeTargetAbsorbedSweepOptions{ProjectsRoot: " "}, time.Now); err == nil {
		t.Fatal("a sweep without a projects root ran")
	}
	reports := filepath.Dir(held.ReceiptPath)
	writeEngineFile(t, filepath.Join(reports, "merge-unreadable.json"), "{")
	if err := os.Mkdir(filepath.Join(reports, "merge-directory.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	outcomes := func(apply bool, receipts ...string) map[string]WorktreeMergeTargetAbsorbedSweepResult {
		t.Helper()
		sweep, err := SweepTargetAbsorbedWorktreeMergeReceipts(ctx, WorktreeMergeTargetAbsorbedSweepOptions{ProjectsRoot: fixture.githubDir, Receipts: receipts, Apply: apply, Actor: "operator", Reason: "drain absorbed lanes"})
		if err != nil {
			t.Fatal(err)
		}
		byReceipt := map[string]WorktreeMergeTargetAbsorbedSweepResult{}
		for _, result := range sweep.Results {
			byReceipt[filepath.Base(result.Receipt)] = result
		}
		return byReceipt
	}
	name := filepath.Base(held.ReceiptPath)
	if got := outcomes(false)[name]; got.Outcome != WorktreeMergeTargetAbsorbedNotAbsorbed || !strings.Contains(got.Detail, "is not in main") {
		t.Fatalf("unlanded sweep = %+v", got)
	}
	if got := outcomes(false)["merge-unreadable.json"]; got.Outcome != WorktreeMergeTargetAbsorbedNotAbsorbed || got.Detail == "" {
		t.Fatalf("unreadable sweep = %+v", got)
	}

	landCandidateOutsideReceipt(t, fixture, held)
	if got := outcomes(false)[name]; got.Outcome != WorktreeMergeTargetAbsorbedWouldRelease || got.TargetSHA != held.Candidate.SHA {
		t.Fatalf("dry-run sweep = %+v", got)
	}
	if acks := targetAbsorbedAcknowledgements(t, held); len(acks) != 0 {
		t.Fatalf("dry run wrote %v", acks)
	}
	applied := outcomes(true, held.ReceiptPath)[name]
	if applied.Outcome != WorktreeMergeTargetAbsorbedReleased || applied.AcknowledgementPath == "" {
		t.Fatalf("applied sweep = %+v", applied)
	}
	ack, _, err := readWorktreeMergeTargetAbsorbedAcknowledgement(held)
	if err != nil || ack.Actor != "operator" || !strings.HasPrefix(ack.Reason, "drain absorbed lanes: ") {
		t.Fatalf("swept acknowledgement = %+v, %v", ack, err)
	}
	if got := outcomes(true)[name]; got.Outcome != WorktreeMergeTargetAbsorbedAlreadyReleased || got.AcknowledgementPath != applied.AcknowledgementPath {
		t.Fatalf("repeat sweep = %+v", got)
	}
	if _, err := SweepTargetAbsorbedWorktreeMergeReceipts(ctx, WorktreeMergeTargetAbsorbedSweepOptions{ProjectsRoot: fixture.githubDir, Receipts: []string{" "}}); err == nil {
		t.Fatal("an empty receipt argument was accepted")
	}
}

func TestE2ESweepTargetAbsorbedReceiptsReportsEveryOtherOutcome(t *testing.T) {
	t.Parallel()
	fixture, held := targetAbsorbedFixture(t)
	ctx := context.Background()
	reports := filepath.Dir(held.ReceiptPath)
	variant := func(suffix string, edit func(*WorktreeMergeReceipt)) string {
		t.Helper()
		receipt := held
		receipt.ID += suffix
		receipt.ReceiptPath = filepath.Join(reports, receipt.ID+".json")
		edit(&receipt)
		if err := persistWorktreeMergeReceipt(receipt); err != nil {
			t.Fatal(err)
		}
		return filepath.Base(receipt.ReceiptPath)
	}
	complete := variant("-complete", func(receipt *WorktreeMergeReceipt) { receipt.Status = WorktreeMergeComplete })
	preparing := variant("-preparing", func(receipt *WorktreeMergeReceipt) { receipt.Status = WorktreeMergePreparing })
	rebatched := variant("-rebatched", func(receipt *WorktreeMergeReceipt) {})
	writeEngineFile(t, filepath.Join(reports, rebatched+worktreeMergePreparedRebatchSuffix), "{")
	stranded := variant("-stranded", func(receipt *WorktreeMergeReceipt) {})
	writeEngineFile(t, filepath.Join(reports, stranded+worktreeMergeStrandedLandingAcknowledgementSuffix), "{")
	tampered := variant("-tampered", func(receipt *WorktreeMergeReceipt) {})
	tamperedReceipt, err := readWorktreeMergeReceipt(filepath.Join(reports, tampered))
	if err != nil {
		t.Fatal(err)
	}
	hash, err := worktreeMergeReceiptSHA256(tamperedReceipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	writeEngineFile(t, worktreeMergeTargetAbsorbedAcknowledgementPath(tamperedReceipt.ReceiptPath, hash), "{}")

	sweep, err := SweepTargetAbsorbedWorktreeMergeReceipts(ctx, WorktreeMergeTargetAbsorbedSweepOptions{ProjectsRoot: fixture.githubDir})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, result := range sweep.Results {
		got[filepath.Base(result.Receipt)] = result.Outcome
	}
	encoded, _ := json.Marshal(got)
	if _, listed := got[complete]; listed {
		t.Fatalf("complete receipt was listed: %s", encoded)
	}
	for name, want := range map[string]string{
		preparing: WorktreeMergeTargetAbsorbedIneligible,
		rebatched: WorktreeMergeTargetAbsorbedAlreadyReleased,
		stranded:  WorktreeMergeTargetAbsorbedNotAbsorbed,
		tampered:  WorktreeMergeTargetAbsorbedNotAbsorbed,
	} {
		if got[name] != want {
			t.Errorf("%s outcome = %q, want %q (all: %s)", name, got[name], want, encoded)
		}
	}
}

func TestE2ESweepTargetAbsorbedReceiptsHandlesMissingOrUnreadableReports(t *testing.T) {
	t.Parallel()
	fixture := newExplicitRootEngineFixture(t)
	sweep, err := SweepTargetAbsorbedWorktreeMergeReceipts(context.Background(), WorktreeMergeTargetAbsorbedSweepOptions{ProjectsRoot: fixture.githubDir})
	if err != nil || len(sweep.Results) != 0 {
		t.Fatalf("sweep without a reports directory = %v, %v", sweep.Results, err)
	}
	reports := filepath.Join(fixture.githubDir, ".wb", "reports", "worktree-merge")
	writeEngineFile(t, reports, "not a directory")
	if _, err := SweepTargetAbsorbedWorktreeMergeReceipts(context.Background(), WorktreeMergeTargetAbsorbedSweepOptions{ProjectsRoot: fixture.githubDir}); err == nil {
		t.Fatal("an unreadable reports directory was swept")
	}
	if _, err := SweepTargetAbsorbedWorktreeMergeReceipts(context.Background(), WorktreeMergeTargetAbsorbedSweepOptions{ProjectsRoot: fixture.githubDir, Apply: true}); err == nil || !strings.Contains(err.Error(), "--actor and --reason") {
		t.Fatalf("apply without an actor = %v", err)
	}
	unresolvable := errors.New("unresolvable WB home")
	if _, err := targetAbsorbedSweepReceiptPaths(WorktreeMergeTargetAbsorbedSweepOptions{ProjectsRoot: fixture.githubDir}, func(string) (string, error) { return "", unresolvable }); !errors.Is(err, unresolvable) {
		t.Fatalf("sweep with an unresolvable WB home = %v", err)
	}
}
