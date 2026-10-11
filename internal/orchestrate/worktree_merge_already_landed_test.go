package orchestrate

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/runner"
)

type alreadyLandedFaultRunner struct {
	runner.Runner
	refuse func(dir, name string, args []string) bool
	err    error
}

type alreadyLandedFaultLock struct {
	err error
}

func (l *alreadyLandedFaultLock) Release() error {
	return l.err
}

func (r *alreadyLandedFaultRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if r.refuse != nil && r.refuse(dir, name, args) {
		return runner.Result{}, r.err
	}
	if r.Runner != nil {
		return r.Runner.RunOpts(ctx, dir, opts, name, args...)
	}
	return defaultRunner.RunOpts(ctx, dir, opts, name, args...)
}

type alreadyLandedScriptedRunner struct {
	runner.Runner
	responses map[string]string
}

func newAlreadyLandedScriptedRunner(responses map[string]string) *alreadyLandedScriptedRunner {
	return &alreadyLandedScriptedRunner{
		Runner:    defaultRunner,
		responses: responses,
	}
}

func (r *alreadyLandedScriptedRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	key := strings.Join(args, " ")
	if resp, ok := r.responses[key]; ok {
		return runner.Result{Stdout: resp}, nil
	}
	return r.Runner.RunOpts(ctx, dir, opts, name, args...)
}

func TestWorktreeMergeAlreadyContainedInTargetUnit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// 1. Empty candidate SHA -> false, nil
	ok, err := worktreeMergeAlreadyContainedInTargetWithRunner(ctx, defaultRunner, "", "", []WorktreeMergeSource{{SHA: "s1"}}, nil, "t1")
	if ok || err != nil {
		t.Fatalf("expected false, nil for empty candidate SHA, got %v, %v", ok, err)
	}

	// 2. Empty remote target -> false, nil
	ok, err = worktreeMergeAlreadyContainedInTargetWithRunner(ctx, defaultRunner, "", "c1", []WorktreeMergeSource{{SHA: "s1"}}, nil, "")
	if ok || err != nil {
		t.Fatalf("expected false, nil for empty target, got %v, %v", ok, err)
	}

	// 3. Empty sources slice -> false, nil
	ok, err = worktreeMergeAlreadyContainedInTargetWithRunner(ctx, defaultRunner, "", "c1", nil, nil, "t1")
	if ok || err != nil {
		t.Fatalf("expected false, nil for nil sources, got %v, %v", ok, err)
	}

	// 4. Source with empty SHA -> false, nil
	mockRunnerCandidateOnly := newAlreadyLandedScriptedRunner(map[string]string{
		"merge-base c1 t1": "c1\n",
	})
	ok, err = worktreeMergeAlreadyContainedInTargetWithRunner(ctx, mockRunnerCandidateOnly, t.TempDir(), "c1", []WorktreeMergeSource{{SHA: ""}}, nil, "t1")
	if ok || err != nil {
		t.Fatalf("expected false, nil for empty source SHA, got %v, %v", ok, err)
	}

	// 5. Runner error on candidate check
	sentinel := errors.New("candidate runner error")
	faultRunner := &alreadyLandedFaultRunner{
		err: sentinel,
		refuse: func(dir, name string, args []string) bool {
			return len(args) >= 3 && args[0] == "merge-base" && args[1] == "c1"
		},
	}
	ok, err = worktreeMergeAlreadyContainedInTargetWithRunner(ctx, faultRunner, t.TempDir(), "c1", []WorktreeMergeSource{{SHA: "s1"}}, nil, "t1")
	if ok || !errors.Is(err, sentinel) {
		t.Fatalf("expected candidate runner error, got %v, %v", ok, err)
	}

	// 6. Runner error on source check
	sentinelSource := errors.New("source runner error")
	mockRunner := newAlreadyLandedScriptedRunner(map[string]string{
		"merge-base c1 t1": "c1\n",
		"merge-base s1 t1": "",
	})
	sourceErrRunner := &alreadyLandedFaultRunner{
		Runner: mockRunner,
		refuse: func(dir, name string, args []string) bool {
			return len(args) >= 3 && args[0] == "merge-base" && args[1] == "s1"
		},
		err: sentinelSource,
	}
	ok, err = worktreeMergeAlreadyContainedInTargetWithRunner(ctx, sourceErrRunner, t.TempDir(), "c1", []WorktreeMergeSource{{SHA: "s1"}}, nil, "t1")
	if ok || !errors.Is(err, sentinelSource) {
		t.Fatalf("expected source runner error, got %v, %v", ok, err)
	}

	// 7. Source not contained (ancestor returns different SHA)
	mockRunnerSourceNotAncestor := newAlreadyLandedScriptedRunner(map[string]string{
		"merge-base c1 t1": "c1\n",
		"merge-base s1 t1": "other\n",
	})
	ok, err = worktreeMergeAlreadyContainedInTargetWithRunner(ctx, mockRunnerSourceNotAncestor, t.TempDir(), "c1", []WorktreeMergeSource{{SHA: "s1"}}, nil, "t1")
	if ok || err != nil {
		t.Fatalf("expected false, nil for non-ancestor source, got %v, %v", ok, err)
	}

	// 8. Rebatched candidate error
	sentinelRebatch := errors.New("rebatch runner error")
	mockRunnerRebatch := newAlreadyLandedScriptedRunner(map[string]string{
		"merge-base c1 t1": "c1\n",
		"merge-base s1 t1": "s1\n",
	})
	rebatchErrRunner := &alreadyLandedFaultRunner{
		Runner: mockRunnerRebatch,
		refuse: func(dir, name string, args []string) bool {
			return len(args) >= 3 && args[0] == "merge-base" && args[1] == "r1"
		},
		err: sentinelRebatch,
	}
	ok, err = worktreeMergeAlreadyContainedInTargetWithRunner(ctx, rebatchErrRunner, t.TempDir(), "c1", []WorktreeMergeSource{{SHA: "s1"}}, []WorktreeMergeCandidate{{SHA: "r1"}}, "t1")
	if ok || !errors.Is(err, sentinelRebatch) {
		t.Fatalf("expected rebatch runner error, got %v, %v", ok, err)
	}

	// 9. Rebatched candidate not contained
	mockRunnerRebatchNotAncestor := newAlreadyLandedScriptedRunner(map[string]string{
		"merge-base c1 t1": "c1\n",
		"merge-base s1 t1": "s1\n",
		"merge-base r1 t1": "different\n",
	})
	ok, err = worktreeMergeAlreadyContainedInTargetWithRunner(ctx, mockRunnerRebatchNotAncestor, t.TempDir(), "c1", []WorktreeMergeSource{{SHA: "s1"}}, []WorktreeMergeCandidate{{SHA: "r1"}}, "t1")
	if ok || err != nil {
		t.Fatalf("expected false, nil for non-ancestor rebatch, got %v, %v", ok, err)
	}

	// 10. All pass, with empty rebatched candidate SHA skipped
	mockRunnerAllPass := newAlreadyLandedScriptedRunner(map[string]string{
		"merge-base c1 t1": "c1\n",
		"merge-base s1 t1": "s1\n",
		"merge-base r1 t1": "r1\n",
	})
	ok, err = worktreeMergeAlreadyContainedInTargetWithRunner(ctx, mockRunnerAllPass, t.TempDir(), "c1", []WorktreeMergeSource{{SHA: "s1"}}, []WorktreeMergeCandidate{{SHA: ""}, {SHA: "r1"}}, "t1")
	if !ok || err != nil {
		t.Fatalf("expected true, nil for all contained, got %v, %v", ok, err)
	}
}

func TestRetireAlreadyLandedWorktreeMergeUnit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// 1. StopBeforeMerge: true -> fails with conflict
	receipt := WorktreeMergeReceipt{
		Candidate: WorktreeMergeCandidate{SHA: "cand1"},
	}
	got, err := retireAlreadyLandedWorktreeMerge(ctx, WorktreeMergeLandOptions{StopBeforeMerge: true}, &receipt, "target1", OperationLock{}, new(bool), func(r WorktreeMergeReceipt) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "cannot publish pull request") || got.Status != WorktreeMergeConflict {
		t.Fatalf("expected StopBeforeMerge conflict, got %+v, %v", got, err)
	}

	// 2. save returns error
	saveSentinel := errors.New("save failure")
	_, err = retireAlreadyLandedWorktreeMerge(ctx, WorktreeMergeLandOptions{}, &receipt, "target1", OperationLock{}, new(bool), func(r WorktreeMergeReceipt) error { return saveSentinel })
	if !errors.Is(err, saveSentinel) {
		t.Fatalf("expected save error, got %v", err)
	}

	// 3. lock.Release returns error
	lockSentinel := errors.New("lock release failure")
	faultLock := &alreadyLandedFaultLock{err: lockSentinel}
	lockedState := true
	_, err = retireAlreadyLandedWorktreeMerge(ctx, WorktreeMergeLandOptions{}, &receipt, "target1", faultLock, &lockedState, func(r WorktreeMergeReceipt) error { return nil })
	if !errors.Is(err, lockSentinel) {
		t.Fatalf("expected lock release error, got %v", err)
	}
	if !lockedState {
		t.Fatal("expected locked state to remain true when lock.Release() fails")
	}
}

func TestLandWorktreeMergeAlreadyContainedBeforePrepare(t *testing.T) {
	fixture := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, fixture, "pre-merged-source", "feature/pre-merged", "pre-merged.txt", "pre merged\n")
	sourceHead := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))

	// Merge source into canonical main before PrepareWorktreeMerge (simulating PR merged on GitHub)
	runEngineGit(t, fixture.canonical, "merge", "--no-ff", "-m", "merge pre-merged feature into main", sourceHead)
	targetSHA := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, fixture.canonical, "push", "origin", "main")

	// PrepareWorktreeMerge sees that candidate is at targetSHA and contains source.SHA
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Candidate.SHA != targetSHA {
		t.Fatalf("candidate SHA = %s, want target SHA %s", receipt.Candidate.SHA, targetSHA)
	}

	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_TARGET_SHA", targetSHA)

	var events []progress.Event
	landed, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Cleanup: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
		Progress: func(e progress.Event) { events = append(events, e) },
	})
	if err != nil {
		t.Fatalf("LandWorktreeMerge error: %v", err)
	}
	if landed.Status != WorktreeMergeComplete {
		t.Fatalf("landed status = %s, want %s", landed.Status, WorktreeMergeComplete)
	}
	if landed.LandingSHA != targetSHA {
		t.Fatalf("landed.LandingSHA = %s, want %s", landed.LandingSHA, targetSHA)
	}

	// Verify "already_landed" progress event was emitted
	foundAlreadyLanded := false
	for _, e := range events {
		if e.Phase == "already_landed" && e.State == progress.Completed {
			foundAlreadyLanded = true
			break
		}
	}
	if !foundAlreadyLanded {
		t.Fatalf("expected already_landed progress event in: %+v", events)
	}

	// Verify candidate and source worktrees are cleaned up
	if _, err := os.Stat(receipt.Candidate.Worktree); !os.IsNotExist(err) {
		t.Fatalf("candidate worktree was not cleaned up: %v", err)
	}
	if _, err := os.Stat(source.WorktreeDir); !os.IsNotExist(err) {
		t.Fatalf("source worktree was not cleaned up: %v", err)
	}
}

func TestLandWorktreeMergeAlreadyContainedBeforePrepareNoCleanup(t *testing.T) {
	fixture := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, fixture, "pre-merged-no-clean", "feature/pre-merged-no-clean", "no-clean.txt", "no clean\n")
	sourceHead := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))

	runEngineGit(t, fixture.canonical, "merge", "--no-ff", "-m", "merge feature into main", sourceHead)
	targetSHA := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, fixture.canonical, "push", "origin", "main")

	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_TARGET_SHA", targetSHA)

	landed, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Cleanup: false, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("LandWorktreeMerge error: %v", err)
	}
	if landed.Status != WorktreeMergeLanded {
		t.Fatalf("landed status = %s, want %s (landed_cleanup_pending)", landed.Status, WorktreeMergeLanded)
	}
	if landed.LandingSHA != targetSHA {
		t.Fatalf("landed.LandingSHA = %s, want %s", landed.LandingSHA, targetSHA)
	}

	// Verify worktrees are preserved when Cleanup is false
	if _, err := os.Stat(receipt.Candidate.Worktree); err != nil {
		t.Fatalf("candidate worktree should be preserved when cleanup=false: %v", err)
	}
	if _, err := os.Stat(source.WorktreeDir); err != nil {
		t.Fatalf("source worktree should be preserved when cleanup=false: %v", err)
	}
}

func TestLandWorktreeMergeAlreadyContainedAfterTargetDriftRebase(t *testing.T) {
	fixture := newExplicitRootEngineFixture(t)
	source1 := createMergeSource(t, fixture, "drift-source-1", "feature/drift-1", "drift1.txt", "drift 1\n")
	source1Head := strings.TrimSpace(runEngineGit(t, source1.WorktreeDir, "rev-parse", "HEAD"))
	source2 := createMergeSource(t, fixture, "drift-source-2", "feature/drift-2", "drift2.txt", "drift 2\n")
	source2Head := strings.TrimSpace(runEngineGit(t, source2.WorktreeDir, "rev-parse", "HEAD"))

	// Prepare before merging into target: candidate integrates both sources, creating a local merge commit
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source1.WorktreeDir, source2.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Now merge both sources into main on remote (external PR landing on GitHub)
	runEngineGit(t, fixture.canonical, "merge", "--no-ff", "-m", "external landing of source 1", source1Head)
	runEngineGit(t, fixture.canonical, "merge", "--no-ff", "-m", "external landing of source 2", source2Head)
	targetSHA := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, fixture.canonical, "push", "origin", "main")

	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_TARGET_SHA", targetSHA)

	var events []progress.Event
	landed, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Cleanup: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
		Progress: func(e progress.Event) { events = append(events, e) },
	})
	if err != nil {
		t.Fatalf("LandWorktreeMerge error: %v", err)
	}
	if landed.Status != WorktreeMergeComplete {
		t.Fatalf("landed status = %s, want %s", landed.Status, WorktreeMergeComplete)
	}
	if landed.LandingSHA != targetSHA {
		t.Fatalf("landed.LandingSHA = %s, want %s", landed.LandingSHA, targetSHA)
	}

	// Verify rebase was recorded and already_landed was emitted
	if landed.Rebase == nil {
		t.Fatal("expected rebase record after target drift")
	}
	foundAlreadyLanded := false
	for _, e := range events {
		if e.Phase == "already_landed" && e.State == progress.Completed {
			foundAlreadyLanded = true
			break
		}
	}
	if !foundAlreadyLanded {
		t.Fatalf("expected already_landed progress event after rebase in: %+v", events)
	}
}

func TestLandWorktreeMergeAlreadyContainedRunnerErrors(t *testing.T) {
	fixture := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, fixture, "err-source", "feature/err-source", "err.txt", "err\n")
	sourceHead := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))

	runEngineGit(t, fixture.canonical, "merge", "--no-ff", "-m", "merge err feature into main", sourceHead)
	targetSHA := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, fixture.canonical, "push", "origin", "main")

	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	sentinel := errors.New("injected merge-base failure")
	faultRunner := &alreadyLandedFaultRunner{
		err: sentinel,
		refuse: func(dir, name string, args []string) bool {
			return len(args) >= 3 && args[0] == "merge-base" && args[1] == receipt.Candidate.SHA && args[2] == targetSHA
		},
	}

	got, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto,
		run: faultRunner, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err == nil || !errors.Is(err, sentinel) || got.Status != WorktreeMergeConflict {
		t.Fatalf("expected conflict with sentinel error at Point 1, got %+v, %v", got, err)
	}
}

func TestLandWorktreeMergeAlreadyContainedAfterRebaseRunnerError(t *testing.T) {
	fixture := newExplicitRootEngineFixture(t)
	source1 := createMergeSource(t, fixture, "rebase-err-1", "feature/rebase-err-1", "rebase-err-1.txt", "1\n")
	source1Head := strings.TrimSpace(runEngineGit(t, source1.WorktreeDir, "rev-parse", "HEAD"))
	source2 := createMergeSource(t, fixture, "rebase-err-2", "feature/rebase-err-2", "rebase-err-2.txt", "2\n")
	source2Head := strings.TrimSpace(runEngineGit(t, source2.WorktreeDir, "rev-parse", "HEAD"))

	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source1.WorktreeDir, source2.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	runEngineGit(t, fixture.canonical, "merge", "--no-ff", "-m", "merge 1", source1Head)
	runEngineGit(t, fixture.canonical, "merge", "--no-ff", "-m", "merge 2", source2Head)
	targetSHA := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, fixture.canonical, "push", "origin", "main")

	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_TARGET_SHA", targetSHA)

	sentinel := errors.New("injected post-rebase merge-base failure")
	rebaseCount := 0
	faultRunner := &alreadyLandedFaultRunner{
		err: sentinel,
		refuse: func(dir, name string, args []string) bool {
			if len(args) >= 1 && args[0] == "rebase" {
				rebaseCount++
			}
			// Only refuse merge-base calls after rebase has executed
			if rebaseCount > 0 && len(args) >= 3 && args[0] == "merge-base" && args[2] == targetSHA {
				return true
			}
			return false
		},
	}

	got, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto,
		run: faultRunner, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err == nil || !errors.Is(err, sentinel) || got.Status != WorktreeMergeConflict {
		t.Fatalf("expected conflict with sentinel error after rebase at Point 2, got %+v, %v", got, err)
	}
}
