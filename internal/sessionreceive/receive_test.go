package sessionreceive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestReceiveRejectsWrongTargetMachineBeforeAdmission(t *testing.T) {
	request, raw, _ := receiveTestRequest(t)
	called := false
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))

	_, err := Receive(context.Background(), Options{
		Store: store, LocalMachine: "different-vm", RawRequest: raw,
		workLog: receiveTestWorkLog(),
		ReceiveWorktree: func(context.Context, worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
			called = true
			return worktrees.SessionReceiveResult{}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "targets machine") {
		t.Fatalf("error = %v, want target-machine refusal", err)
	}
	if called {
		t.Fatal("worktree receiver called for wrong target machine")
	}
	if _, err := store.Load(request.HandoffID); err == nil {
		t.Fatal("wrong target request was admitted")
	}
}

func TestReceiveReturnsExistingReceiptWithoutExecutingTarget(t *testing.T) {
	request, raw, digest := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	receipt := receiveTestReceipt(t, request, digest)
	if _, _, err := store.SaveReceipt(request.HandoffID, digest, receipt); err != nil {
		t.Fatal(err)
	}

	result, err := Receive(context.Background(), Options{
		Store: store, LocalMachine: request.TargetMachine, RawRequest: raw,
		workLog: receiveTestWorkLog(),
		ReceiveWorktree: func(context.Context, worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
			t.Fatal("target execution ran despite existing receipt")
			return worktrees.SessionReceiveResult{}, nil
		},
		InspectSuccessor: func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
			t.Fatal("completed receipt replay inspected or required a live successor")
			return sessionlaunch.Result{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt == nil || *result.Receipt != receipt || !result.Replay || result.Phase != sessionmove.PhaseCompleted {
		t.Fatalf("result = %#v", result)
	}
	state, err := store.Load(request.HandoffID)
	if err != nil || !stateHasPhase(state, sessionmove.PhaseCompleted) {
		t.Fatalf("completed receipt replay did not repair completed phase: state=%#v err=%v", state, err)
	}
}

func TestReceiveIdenticalRetryReturnsReceiptWithoutDuplicateReceiverEffects(t *testing.T) {
	request, raw, _ := receiveTestRequest(t)
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	home := filepath.Join(root, "home")
	t.Setenv("WB_HOME", home)
	if err := os.MkdirAll(projectsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	store := sessionmove.NewStore(filepath.Join(home, sessionmove.DirName))
	expectedWorktree := receiveTestWorktree(t, projectsRoot, request)
	sessionDir := filepath.Join(home, session.DirName)

	worktreeCalls, worktreeCreations, tmuxLaunches := 0, 0, 0
	tmuxSuccessors := make(map[string]bool)
	options := Options{
		Store: store, ProjectsRoot: projectsRoot, LocalMachine: request.TargetMachine, RawRequest: raw,
		workLog: receiveTestWorkLog(),
		ReceiveWorktree: func(_ context.Context, options worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
			worktreeCalls++
			reused := true
			if _, err := os.Stat(expectedWorktree); errors.Is(err, os.ErrNotExist) {
				if err := os.MkdirAll(expectedWorktree, 0o755); err != nil {
					return worktrees.SessionReceiveResult{}, err
				}
				worktreeCreations++
				reused = false
			} else if err != nil {
				return worktrees.SessionReceiveResult{}, err
			}
			return worktrees.SessionReceiveResult{
				WorktreeDir: expectedWorktree, Commit: options.Request.BundleCommit, Reused: reused,
			}, nil
		},
		StartSuccessor: func(ctx context.Context, options sessionlaunch.Options) (sessionlaunch.Result, error) {
			tmuxLaunches++
			successor, err := receiveTestStart(ctx, options)
			if err != nil {
				return sessionlaunch.Result{}, err
			}
			successor.PID = 1000 + tmuxLaunches
			successor.StartedAt = time.Date(2026, time.August, 25, 13, 0, tmuxLaunches, 0, time.UTC)
			tmuxSuccessors[successor.TmuxName] = true
			_, err = session.Register(sessionDir, session.Record{
				PID: successor.PID, WBSessionID: successor.WBSessionID, Machine: successor.TargetMachine,
				Runtime: successor.Runtime, Model: successor.Model, NativeHarnessID: successor.NativeHarnessID,
				TmuxName: successor.TmuxName, PredecessorWBSessionID: successor.PredecessorWBSessionID,
				HandoffID: successor.HandoffID, StartedAt: successor.StartedAt,
			})
			return successor, err
		},
	}

	accepted, err := Receive(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	// Model an ambiguous courier result by discarding the first response and
	// delivering the exact same bytes again.
	replayed, err := Receive(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Receipt == nil || accepted.Replay || replayed.Receipt == nil || !replayed.Replay ||
		*replayed.Receipt != *accepted.Receipt {
		t.Fatalf("accepted=%#v replayed=%#v", accepted, replayed)
	}
	if worktreeCalls != 1 || worktreeCreations != 1 {
		t.Fatalf("worktree receiver calls=%d creations=%d, want exactly 1/1", worktreeCalls, worktreeCreations)
	}
	worktreesOnDisk, err := filepath.Glob(filepath.Join(home, "worktrees", "session-"+request.HandoffID, "*", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(worktreesOnDisk) != 1 {
		t.Fatalf("pinned target worktrees=%#v, want [%s]", worktreesOnDisk, expectedWorktree)
	}
	gotWorktree, err := os.Stat(worktreesOnDisk[0])
	if err != nil {
		t.Fatal(err)
	}
	wantWorktree, err := os.Stat(expectedWorktree)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(gotWorktree, wantWorktree) {
		t.Fatalf("pinned target worktree=%s, want %s", worktreesOnDisk[0], expectedWorktree)
	}
	if tmuxLaunches != 1 || len(tmuxSuccessors) != 1 || !tmuxSuccessors[accepted.Receipt.TmuxName] {
		t.Fatalf("tmux launches=%d successors=%#v, want one launch for %s", tmuxLaunches, tmuxSuccessors, accepted.Receipt.TmuxName)
	}
	sessions, err := session.List(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].WBSessionID != request.SuccessorWBSessionID || sessions[0].HandoffID != request.HandoffID {
		t.Fatalf("successor WB sessions=%#v, want exactly one for handoff %s", sessions, request.HandoffID)
	}
}

func TestReceiveRepairsCompletionAcrossTargetEvidenceAndReceiptBoundaries(t *testing.T) {
	request, raw, _ := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	projectsRoot := t.TempDir()
	expectedWorktree := receiveTestWorktree(t, projectsRoot, request)
	injected := errors.New("injected completion boundary")
	workLog := receiveTestWorkLog()
	completedCalls := 0
	baseComplete := workLog.complete
	workLog.complete = func(options worktrees.ExternalTargetCompletionOptions) (worktrees.LocalWorkLogEvent, error) {
		completedCalls++
		return baseComplete(options)
	}
	options := Options{
		Store: store, ProjectsRoot: projectsRoot, LocalMachine: request.TargetMachine, RawRequest: raw,
		ReceiveWorktree: func(_ context.Context, options worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
			return worktrees.SessionReceiveResult{WorktreeDir: expectedWorktree, Commit: options.Request.BundleCommit}, nil
		},
		StartSuccessor: receiveTestStart, InspectSuccessor: receiveTestInspect, workLog: workLog,
		hooks: receiveHooks{afterTargetCompleted: func() error { return injected }},
	}
	if _, err := Receive(context.Background(), options); !errors.Is(err, injected) {
		t.Fatalf("after target completion error=%v, want injected crash", err)
	}
	state, err := store.Load(request.HandoffID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Receipt != nil || stateHasPhase(state, sessionmove.PhaseCompleted) || !stateHasPhase(state, sessionmove.PhaseSuccessorStarted) {
		t.Fatalf("receipt crossed target-completed boundary: %#v", state)
	}

	options.hooks = receiveHooks{afterReceipt: func() error { return injected }}
	if _, err := Receive(context.Background(), options); !errors.Is(err, injected) {
		t.Fatalf("after receipt error=%v, want injected crash", err)
	}
	state, err = store.Load(request.HandoffID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Receipt == nil || stateHasPhase(state, sessionmove.PhaseCompleted) {
		t.Fatalf("completed phase crossed receipt boundary: %#v", state)
	}

	options.hooks = receiveHooks{}
	options.InspectSuccessor = func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
		t.Fatal("durable receipt replay inspected successor")
		return sessionlaunch.Result{}, nil
	}
	result, err := Receive(context.Background(), options)
	if err != nil || result.Receipt == nil || result.Phase != sessionmove.PhaseCompleted || !result.Replay {
		t.Fatalf("completed repair result=%#v err=%v", result, err)
	}
	if completedCalls != 2 {
		t.Fatalf("target Work Log completion calls=%d, want one before each receipt attempt and none on receipt replay", completedCalls)
	}
	state, err = store.Load(request.HandoffID)
	if err != nil || !stateHasPhase(state, sessionmove.PhaseCompleted) {
		t.Fatalf("completed phase was not repaired from receipt: state=%#v err=%v", state, err)
	}
}

func TestReceiveRejectsSameHandoffIDDifferentExactBytes(t *testing.T) {
	request, raw, _ := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	projectsRoot := t.TempDir()
	expectedWorktree := receiveTestWorktree(t, projectsRoot, request)
	calls := 0
	receiver := func(_ context.Context, options worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
		calls++
		return worktrees.SessionReceiveResult{WorktreeDir: expectedWorktree, Commit: options.Request.BundleCommit}, nil
	}
	if _, err := Receive(context.Background(), Options{
		Store: store, ProjectsRoot: projectsRoot, LocalMachine: request.TargetMachine, RawRequest: raw, ReceiveWorktree: receiver,
		workLog:        receiveTestWorkLog(),
		StartSuccessor: receiveTestStart, InspectSuccessor: receiveTestInspect,
	}); err != nil {
		t.Fatal(err)
	}
	request.SourceModel = "different-model"
	conflictingRaw, err := sessionmove.EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Receive(context.Background(), Options{
		Store: store, ProjectsRoot: projectsRoot, LocalMachine: request.TargetMachine, RawRequest: conflictingRaw, ReceiveWorktree: receiver,
		workLog: receiveTestWorkLog(),
	}); !errors.Is(err, sessionmove.ErrHandoffConflict) {
		t.Fatalf("error = %v, want handoff conflict", err)
	}
	if calls != 1 {
		t.Fatalf("target receiver calls = %d, want 1", calls)
	}
}

func TestReceiveRecordsActionableFailureWithoutReceipt(t *testing.T) {
	request, raw, _ := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	projectsRoot := t.TempDir()
	_, err := Receive(context.Background(), Options{
		Store: store, ProjectsRoot: projectsRoot, LocalMachine: request.TargetMachine, RawRequest: raw,
		workLog: receiveTestWorkLog(),
		Now:     func() time.Time { return time.Date(2026, time.August, 25, 13, 0, 0, 0, time.UTC) },
		ReceiveWorktree: func(context.Context, worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
			return worktrees.SessionReceiveResult{}, errors.New("remote branch tip moved from exact bundle commit")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "remote branch tip moved") {
		t.Fatalf("error = %v", err)
	}
	state, loadErr := store.Load(request.HandoffID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(state.Events) != 2 || state.Events[0].Phase != sessionmove.PhaseReceived || state.Events[1].Phase != sessionmove.PhaseFailed {
		t.Fatalf("events = %#v", state.Events)
	}
	if !strings.Contains(state.Events[1].Diagnostic, "retry") || state.Receipt != nil {
		t.Fatalf("failed state = %#v", state)
	}
}

func TestReceiveConcurrentIdenticalRequestsSerializeAndCreateOnce(t *testing.T) {
	request, raw, digest := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	projectsRoot := t.TempDir()
	expectedWorktree := receiveTestWorktree(t, projectsRoot, request)
	var calls, active, maxActive, created atomic.Int32
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	receiver := func(_ context.Context, options worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
		if options.RequestDigest != digest || options.ExecutionLock == nil ||
			!options.ExecutionLock.HeldForStore(store.Root, options.Request, digest) {
			return worktrees.SessionReceiveResult{}, errors.New("receiver did not get exact admitted Store authority")
		}
		call := calls.Add(1)
		current := active.Add(1)
		defer active.Add(-1)
		for {
			prior := maxActive.Load()
			if current <= prior || maxActive.CompareAndSwap(prior, current) {
				break
			}
		}
		result := worktrees.SessionReceiveResult{WorktreeDir: expectedWorktree, Commit: options.Request.BundleCommit}
		if call == 1 {
			created.Add(1)
			close(firstStarted)
			<-releaseFirst
		} else {
			result.Reused = true
		}
		return result, nil
	}
	options := Options{Store: store, ProjectsRoot: projectsRoot, LocalMachine: request.TargetMachine, RawRequest: raw, ReceiveWorktree: receiver,
		workLog:        receiveTestWorkLog(),
		StartSuccessor: receiveTestStart, InspectSuccessor: receiveTestInspect}
	results := make([]Result, 2)
	errs := make([]error, 2)
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		results[0], errs[0] = Receive(context.Background(), options)
	}()
	<-firstStarted
	wait.Add(1)
	go func() {
		defer wait.Done()
		results[1], errs[1] = Receive(context.Background(), options)
	}()
	close(releaseFirst)
	wait.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("receive %d: %v", index, err)
		}
	}
	if created.Load() != 1 || calls.Load() != 1 || maxActive.Load() != 1 {
		t.Fatalf("created=%d calls=%d max_active=%d", created.Load(), calls.Load(), maxActive.Load())
	}
	state, err := store.Load(request.HandoffID)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Events) != 4 || state.Events[0].Phase != sessionmove.PhaseReceived || state.Events[1].Phase != sessionmove.PhaseWorktreeReady ||
		state.Events[2].Phase != sessionmove.PhaseSuccessorStarted || state.Events[3].Phase != sessionmove.PhaseCompleted {
		t.Fatalf("events = %#v", state.Events)
	}
	if results[0].Receipt == nil || results[1].Receipt == nil || *results[0].Receipt != *results[1].Receipt {
		t.Fatalf("serialized completed receipts = %#v", results)
	}
}

func TestReceiveReplayAfterWorktreeReadyUsesLocalVerifierWithoutRefetch(t *testing.T) {
	request, raw, digest := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	projectsRoot := t.TempDir()
	expectedWorktree := receiveTestWorktree(t, projectsRoot, request)
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, phase := range []sessionmove.Phase{sessionmove.PhaseReceived, sessionmove.PhaseWorktreeReady} {
		if _, err := store.AppendEvent(request.HandoffID, digest, sessionmove.HandoffEvent{Phase: phase, At: now}); err != nil {
			t.Fatal(err)
		}
	}
	verified := 0
	result, err := Receive(context.Background(), Options{Store: store, ProjectsRoot: projectsRoot, LocalMachine: request.TargetMachine, RawRequest: raw,
		workLog: receiveTestWorkLog(),
		ReceiveWorktree: func(context.Context, worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
			t.Fatal("durable worktree_ready replay refetched the mutable remote branch")
			return worktrees.SessionReceiveResult{}, nil
		},
		VerifyWorktree: func(_ context.Context, options worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
			verified++
			return worktrees.SessionReceiveResult{WorktreeDir: expectedWorktree, Commit: options.Request.BundleCommit, Reused: true}, nil
		}, StartSuccessor: receiveTestStart, InspectSuccessor: receiveTestMissingInspect})
	if err != nil {
		t.Fatal(err)
	}
	if verified != 1 || result.Phase != sessionmove.PhaseCompleted || result.Receipt == nil {
		t.Fatalf("verified=%d result=%#v", verified, result)
	}
}

func TestReceiveReplayAfterSuccessorStartedBypassesAllGit(t *testing.T) {
	request, raw, digest := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	projectsRoot := t.TempDir()
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, phase := range []sessionmove.Phase{sessionmove.PhaseReceived, sessionmove.PhaseWorktreeReady, sessionmove.PhaseSuccessorStarted} {
		if _, err := store.AppendEvent(request.HandoffID, digest, sessionmove.HandoffEvent{Phase: phase, At: now}); err != nil {
			t.Fatal(err)
		}
	}
	inspected := 0
	result, err := Receive(context.Background(), Options{Store: store, ProjectsRoot: projectsRoot, LocalMachine: request.TargetMachine, RawRequest: raw,
		workLog: receiveTestWorkLog(),
		ReceiveWorktree: func(context.Context, worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
			t.Fatal("started replay refetched Git")
			return worktrees.SessionReceiveResult{}, nil
		},
		VerifyWorktree: func(context.Context, worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
			t.Fatal("started replay reverified Git")
			return worktrees.SessionReceiveResult{}, nil
		},
		InspectSuccessor: func(ctx context.Context, options sessionlaunch.Options) (sessionlaunch.Result, error) {
			inspected++
			return receiveTestInspect(ctx, options)
		}})
	if err != nil {
		t.Fatal(err)
	}
	if inspected != 1 || !result.Replay || result.Phase != sessionmove.PhaseCompleted || result.Receipt == nil {
		t.Fatalf("inspected=%d result=%#v", inspected, result)
	}
}

func TestReceiveRecoversReleasedSuccessorBeforeDirtyWorktreeReplay(t *testing.T) {
	request, raw, digest := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	projectsRoot := t.TempDir()
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, phase := range []sessionmove.Phase{sessionmove.PhaseReceived, sessionmove.PhaseWorktreeReady} {
		if _, err := store.AppendEvent(request.HandoffID, digest, sessionmove.HandoffEvent{Phase: phase, At: now}); err != nil {
			t.Fatal(err)
		}
	}
	inspected := 0
	result, err := Receive(context.Background(), Options{
		Store: store, ProjectsRoot: projectsRoot, LocalMachine: request.TargetMachine, RawRequest: raw,
		workLog: receiveTestWorkLog(),
		ReceiveWorktree: func(context.Context, worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
			t.Fatal("released successor recovery refetched mutable Git")
			return worktrees.SessionReceiveResult{}, nil
		},
		VerifyWorktree: func(context.Context, worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
			t.Fatal("released successor recovery required a clean worktree after harness start")
			return worktrees.SessionReceiveResult{}, nil
		},
		InspectSuccessor: func(ctx context.Context, options sessionlaunch.Options) (sessionlaunch.Result, error) {
			inspected++
			return receiveTestInspect(ctx, options)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if inspected != 1 || result.Successor == nil || result.Receipt == nil || result.Phase != sessionmove.PhaseCompleted || !result.Replay {
		t.Fatalf("inspected=%d result=%#v", inspected, result)
	}
	state, err := store.Load(request.HandoffID)
	if err != nil {
		t.Fatal(err)
	}
	if !stateHasPhase(state, sessionmove.PhaseSuccessorStarted) {
		t.Fatalf("missing recovered successor_started event: %#v", state.Events)
	}
}

func TestReceiveRetriesExactTerminalLauncherWithoutGitReplay(t *testing.T) {
	request, raw, _ := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	projectsRoot := t.TempDir()
	expectedWorktree := receiveTestWorktree(t, projectsRoot, request)
	var receiveCalls, verifyCalls, inspectCalls, startCalls int
	options := Options{
		Store: store, ProjectsRoot: projectsRoot, LocalMachine: request.TargetMachine, RawRequest: raw,
		workLog: receiveTestWorkLog(),
		ReceiveWorktree: func(_ context.Context, options worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
			receiveCalls++
			return worktrees.SessionReceiveResult{WorktreeDir: expectedWorktree, Commit: options.Request.BundleCommit}, nil
		},
		VerifyWorktree: func(context.Context, worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
			verifyCalls++
			return worktrees.SessionReceiveResult{}, errors.New("Git verifier must not run after exact terminal launcher proof")
		},
		InspectSuccessor: func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
			inspectCalls++
			return sessionlaunch.Result{}, fmt.Errorf("%w: attempt 1 has exact release-bound exec failure", sessionlaunch.ErrRetryableLaunch)
		},
		StartSuccessor: func(ctx context.Context, options sessionlaunch.Options) (sessionlaunch.Result, error) {
			startCalls++
			if startCalls == 1 {
				return sessionlaunch.Result{}, errors.New("successor launcher failed after release: injected Exec failure")
			}
			return receiveTestStart(ctx, options)
		},
	}

	if _, err := Receive(context.Background(), options); err == nil || !strings.Contains(err.Error(), "injected Exec failure") {
		t.Fatalf("first Receive error = %v, want post-release Exec failure", err)
	}
	result, err := Receive(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if receiveCalls != 1 || verifyCalls != 0 || inspectCalls != 1 || startCalls != 2 {
		t.Fatalf("receive=%d verify=%d inspect=%d start=%d, want 1/0/1/2", receiveCalls, verifyCalls, inspectCalls, startCalls)
	}
	if result.Phase != sessionmove.PhaseCompleted || result.Successor == nil || result.Receipt == nil || !result.Replay {
		t.Fatalf("result = %#v", result)
	}
	state, err := store.Load(request.HandoffID)
	if err != nil {
		t.Fatal(err)
	}
	wantPhases := []sessionmove.Phase{sessionmove.PhaseReceived, sessionmove.PhaseWorktreeReady, sessionmove.PhaseFailed, sessionmove.PhaseSuccessorStarted, sessionmove.PhaseCompleted}
	if len(state.Events) != len(wantPhases) {
		t.Fatalf("events = %#v", state.Events)
	}
	for index, phase := range wantPhases {
		if state.Events[index].Phase != phase {
			t.Fatalf("event %d phase = %s, want %s", index, state.Events[index].Phase, phase)
		}
	}
}

func receiveTestStart(ctx context.Context, options sessionlaunch.Options) (sessionlaunch.Result, error) {
	attemptID := "000001-" + strings.Repeat("1", 32)
	startedAt := time.Now().UTC()
	record := session.Record{
		PID: 123, WBSessionID: options.Request.SuccessorWBSessionID, Machine: options.Request.TargetMachine,
		Runtime: options.Request.SourceRuntime, Model: options.Request.SourceModel, NativeHarnessID: "native-target",
		TmuxName: "wb-session-" + options.Request.SuccessorWBSessionID, PredecessorWBSessionID: options.Request.PredecessorWBSessionID,
		HandoffID: options.Request.HandoffID, StartedAt: startedAt,
	}
	target, err := sessionmove.ExpectedTargetWorkLogReference(options.Request, options.RequestDigest)
	if err != nil {
		return sessionlaunch.Result{}, err
	}
	targetReference := target.String()
	if options.BeforeRelease != nil {
		targetReference, err = options.BeforeRelease(ctx, sessionlaunch.Prepared{
			Request: options.Request, RequestDigest: options.RequestDigest, Session: record,
			AttemptID: attemptID, AttemptIndex: 1, WorktreeDir: options.WorktreeDir, PinnedCommit: options.PinnedCommit,
		})
		if err != nil {
			return sessionlaunch.Result{}, err
		}
	}
	return sessionlaunch.Result{HandoffID: options.Request.HandoffID, WBSessionID: options.Request.SuccessorWBSessionID,
		PredecessorWBSessionID: options.Request.PredecessorWBSessionID, TargetMachine: options.Request.TargetMachine,
		PID: 123, AttemptID: attemptID, AttemptIndex: 1,
		TmuxName: "wb-session-" + options.Request.SuccessorWBSessionID, Runtime: options.Request.SourceRuntime,
		Model: options.Request.SourceModel, NativeHarnessID: record.NativeHarnessID, TargetWorkLogRef: targetReference,
		WorktreeDir: options.WorktreeDir, PinnedCommit: options.PinnedCommit, StartedAt: startedAt}, nil
}

func receiveTestInspect(_ context.Context, options sessionlaunch.Options) (sessionlaunch.Result, error) {
	result, err := receiveTestStart(context.Background(), options)
	result.Reused = true
	return result, err
}

func receiveTestMissingInspect(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
	return sessionlaunch.Result{}, sessionlaunch.ErrNotReleased
}

func receiveTestWorkLog() targetWorkLogDependencies {
	return targetWorkLogDependencies{
		prepare: func(_ context.Context, options worktrees.ExternalSessionWorkLogPrepareOptions) (worktrees.ExternalSessionWorkLogPrepareResult, error) {
			target, err := sessionmove.ExpectedTargetWorkLogReference(options.Request, options.RequestDigest)
			if err != nil {
				return worktrees.ExternalSessionWorkLogPrepareResult{}, err
			}
			return worktrees.ExternalSessionWorkLogPrepareResult{WorkLogReference: target.String(), ClaimID: target.ClaimID}, nil
		},
		complete: func(options worktrees.ExternalTargetCompletionOptions) (worktrees.LocalWorkLogEvent, error) {
			if err := sessionmove.ValidateReceiptForRequest(options.Receipt, options.Request, options.RequestDigest); err != nil {
				return worktrees.LocalWorkLogEvent{}, err
			}
			return worktrees.LocalWorkLogEvent{ID: "completed"}, nil
		},
		fail: func(worktrees.ExternalTargetAttemptFailureOptions) (worktrees.LocalWorkLogEvent, error) {
			return worktrees.LocalWorkLogEvent{ID: "failed"}, nil
		},
	}
}

func receiveTestReceipt(t *testing.T, request sessionmove.Request, digest sessionmove.Digest) sessionmove.Receipt {
	t.Helper()
	target, err := sessionmove.ExpectedTargetWorkLogReference(request, digest)
	if err != nil {
		t.Fatal(err)
	}
	return sessionmove.Receipt{
		SchemaVersion: sessionmove.ReceiptSchemaVersion, HandoffID: request.HandoffID, RequestDigest: digest,
		SuccessorWBSessionID: request.SuccessorWBSessionID, PredecessorWBSessionID: request.PredecessorWBSessionID,
		TargetMachine: request.TargetMachine, TmuxName: "wb-session-" + request.SuccessorWBSessionID,
		Runtime: request.SourceRuntime, Model: request.SourceModel, NativeHarnessID: "native-target",
		AttemptID: "000001-" + strings.Repeat("1", 32), AttemptIndex: 1, PID: 123,
		TargetWorkLogReference: target.String(), PinnedCommit: request.BundleCommit,
		StartedAt: time.Date(2026, time.August, 25, 13, 0, 0, 0, time.UTC),
	}
}

func receiveTestWorktree(t *testing.T, projectsRoot string, request sessionmove.Request) string {
	t.Helper()
	path, err := worktrees.SessionReceiveWorktreePath(projectsRoot, request)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func receiveTestRequest(t *testing.T) (sessionmove.Request, []byte, sessionmove.Digest) {
	t.Helper()
	handover := []byte("# handover\n")
	request := sessionmove.Request{
		SchemaVersion: sessionmove.RequestSchemaVersion,
		HandoffID:     "handoff-123", SuccessorWBSessionID: "wbs-successor", PredecessorWBSessionID: "wbs-source",
		SourceMachine: "source", TargetMachine: "target-vm", RepositoryRemote: "/tmp/remotes/acme/app.git",
		Branch: "feature/session", SourceWorkCommit: strings.Repeat("a", 40), BundleCommit: strings.Repeat("b", 40),
		HandoverPath: ".wb/handoffs/handoff-123.md", HandoverDigest: sessionmove.DigestBytes(handover),
		SourceRuntime: "codex", SourceModel: "gpt-5", SourceNativeHarnessID: "native-source",
		WorkLogReference:   "worklog:session-move/session-move-run/" + strings.Repeat("a", 64),
		SourceOfferMessage: "Session handoff offered", SourceOfferNextAction: "Continue from .wb/handoffs/handoff-123.md",
		CreatedAt: time.Date(2026, time.August, 25, 12, 30, 0, 0, time.UTC),
	}
	request.SourceOfferDigest = sessionmove.DigestSourceOffer(request.SourceOfferMessage, request.SourceOfferNextAction)
	raw, err := sessionmove.EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	return request, raw, sessionmove.DigestBytes(raw)
}
