package orchestrate

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/runner"
	"strings"
	"testing"
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
