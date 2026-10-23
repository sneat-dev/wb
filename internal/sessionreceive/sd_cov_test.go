package sessionreceive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// sdCovEventTime is a fixed, non-zero event time for seeded phases.
var sdCovEventTime = time.Date(2026, time.August, 25, 12, 45, 0, 0, time.UTC)

// sdCovHandoffDir returns the durable aggregate directory for a handoff.
func sdCovHandoffDir(store sessionmove.Store, handoffID string) string {
	return filepath.Join(store.Root, handoffID)
}

// sdCovSeedPhases installs durable phases in order before a receive attempt.
func sdCovSeedPhases(t *testing.T, store sessionmove.Store, handoffID string, digest sessionmove.Digest, phases ...sessionmove.Phase) {
	t.Helper()
	for _, phase := range phases {
		if _, err := store.AppendEvent(handoffID, digest, sessionmove.HandoffEvent{Phase: phase, At: sdCovEventTime}); err != nil {
			t.Fatal(err)
		}
	}
}

// sdCovBlockEventSequence makes the named immutable event sequence impossible
// to publish by planting a directory where the event file belongs. Readers
// skip directories, so the aggregate stays readable while the append cannot
// ever win its no-replace publication.
func sdCovBlockEventSequence(t *testing.T, store sessionmove.Store, handoffID string, sequence int) {
	t.Helper()
	trap := filepath.Join(sdCovHandoffDir(store, handoffID), "events", fmt.Sprintf("%020d.json", sequence))
	if err := os.MkdirAll(trap, 0o700); err != nil {
		t.Fatal(err)
	}
}

// sdCovReceiveOptions builds the standard options for a target receiver test.
func sdCovReceiveOptions(store sessionmove.Store, projectsRoot string, request sessionmove.Request, raw []byte) Options {
	return Options{
		Store: store, ProjectsRoot: projectsRoot, LocalMachine: request.TargetMachine, RawRequest: raw,
		workLog: receiveTestWorkLog(),
	}
}

// sdCovDurablePhases reads the append-only event evidence directly from disk so
// tests can assert durable order even when a planted storage fault deliberately
// makes the aggregate unprojectable through Store.Load.
func sdCovDurablePhases(t *testing.T, store sessionmove.Store, handoffID string) []sessionmove.Phase {
	t.Helper()
	directory := filepath.Join(sdCovHandoffDir(store, handoffID), "events")
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	phases := make([]sessionmove.Phase, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var event sessionmove.HandoffEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		phases = append(phases, event.Phase)
	}
	return phases
}

// sdCovAttemptFailure is the exact post-release launcher failure proof the
// retryable-launch recovery path must record.
func sdCovAttemptFailure(request sessionmove.Request, digest sessionmove.Digest) *sessionlaunch.AttemptFailureError {
	return &sessionlaunch.AttemptFailureError{Evidence: sessionlaunch.FailureEvidence{
		HandoffID: request.HandoffID, RequestDigest: digest, AttemptID: "000001-" + strings.Repeat("2", 32),
		AttemptIndex: 1, PID: 4242, StartedAt: sdCovEventTime, FailedAt: sdCovEventTime.Add(time.Second),
		Diagnostic: "post-release exec failed",
	}}
}

func TestSdCovReceiveRejectsUndecodableOrMisaddressedRequests(t *testing.T) {
	t.Parallel()
	request, raw, _ := receiveTestRequest(t)
	projectsRoot := t.TempDir()

	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	_, err := Receive(context.Background(), Options{
		Store: store, ProjectsRoot: projectsRoot, LocalMachine: request.TargetMachine, RawRequest: []byte("{"),
		workLog: receiveTestWorkLog(),
		ReceiveWorktree: func(context.Context, worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
			t.Fatal("undecodable request reached the target worktree receiver")
			return worktrees.SessionReceiveResult{}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "parse session move request") {
		t.Fatalf("Receive error = %v, want request decode failure", err)
	}

	_, err = Receive(context.Background(), Options{
		Store: store, ProjectsRoot: projectsRoot, LocalMachine: "  ", RawRequest: raw,
		workLog: receiveTestWorkLog(),
	})
	if err == nil || !strings.Contains(err.Error(), "validated local remote.machine identity is required") {
		t.Fatalf("Receive error = %v, want local machine requirement", err)
	}
}

func TestSdCovReceiveFailsWhenExecutionLockCannotBeAcquired(t *testing.T) {
	t.Parallel()
	request, raw, _ := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	// A directory where the per-handoff execution fence belongs can never be
	// opened, so admission state may exist but no target work may start.
	if err := os.MkdirAll(filepath.Join(sdCovHandoffDir(store, request.HandoffID), "receive.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	options := sdCovReceiveOptions(store, t.TempDir(), request, raw)
	options.ReceiveWorktree = func(context.Context, worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
		t.Fatal("target work started without the handoff execution fence")
		return worktrees.SessionReceiveResult{}, nil
	}
	_, err := Receive(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "execution lock") {
		t.Fatalf("Receive error = %v, want execution-lock refusal", err)
	}
}

func TestSdCovReceiveFailsWhenAggregateEventsAreUnreadable(t *testing.T) {
	t.Parallel()
	request, raw, _ := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	if err := os.MkdirAll(sdCovHandoffDir(store, request.HandoffID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sdCovHandoffDir(store, request.HandoffID), "events"), []byte("no"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := sdCovReceiveOptions(store, t.TempDir(), request, raw)
	options.ReceiveWorktree = func(context.Context, worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
		t.Fatal("target work started without a readable aggregate projection")
		return worktrees.SessionReceiveResult{}, nil
	}
	_, err := Receive(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "events directory") {
		t.Fatalf("Receive error = %v, want unreadable-aggregate failure", err)
	}
}

func TestSdCovReceiveFailsWhenReceivedEventCannotBeAppended(t *testing.T) {
	t.Parallel()
	request, raw, digest := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	sdCovBlockEventSequence(t, store, request.HandoffID, 1)
	options := sdCovReceiveOptions(store, t.TempDir(), request, raw)
	options.ReceiveWorktree = func(context.Context, worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
		t.Fatal("target work started before the received phase was durable")
		return worktrees.SessionReceiveResult{}, nil
	}
	_, err := Receive(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "record target received phase") {
		t.Fatalf("Receive error = %v, want received-phase persistence failure", err)
	}
}

func TestSdCovReceiveRejectsImpossibleTargetWorktreePath(t *testing.T) {
	t.Parallel()
	request, raw, _ := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	options := sdCovReceiveOptions(store, "", request, raw)
	options.ReceiveWorktree = func(context.Context, worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
		t.Fatal("target work started without a derivable deterministic worktree")
		return worktrees.SessionReceiveResult{}, nil
	}
	_, err := Receive(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "derive deterministic target worktree") {
		t.Fatalf("Receive error = %v, want deterministic-path derivation failure", err)
	}
	state, loadErr := store.Load(request.HandoffID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(state.Events) != 0 {
		t.Fatalf("events recorded without a derivable target path: %#v", state.Events)
	}
}

func TestSdCovReceiveRepairsCompletedPhaseWhenCompletedEventCannotBeAppended(t *testing.T) {
	t.Parallel()
	request, raw, digest := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	receipt := receiveTestReceipt(t, request, digest)
	if _, _, err := store.SaveReceipt(request.HandoffID, digest, receipt); err != nil {
		t.Fatal(err)
	}
	sdCovBlockEventSequence(t, store, request.HandoffID, 1)
	options := sdCovReceiveOptions(store, t.TempDir(), request, raw)
	options.ReceiveWorktree = func(context.Context, worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
		t.Fatal("completed receipt replay executed target work")
		return worktrees.SessionReceiveResult{}, nil
	}
	_, err := Receive(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "repair target completed phase from durable receipt") {
		t.Fatalf("Receive error = %v, want completed-phase repair failure", err)
	}
	state, loadErr := store.Load(request.HandoffID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if state.Receipt == nil || *state.Receipt != receipt || stateHasPhase(state, sessionmove.PhaseCompleted) {
		t.Fatalf("receipt repair state = %#v", state)
	}
}

func TestSdCovReceiveReportsSuccessorInspectionFailureOnStartedReplay(t *testing.T) {
	t.Parallel()
	injected := errors.New("published successor inspection failed")
	request, raw, digest := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	sdCovSeedPhases(t, store, request.HandoffID, digest,
		sessionmove.PhaseReceived, sessionmove.PhaseWorktreeReady, sessionmove.PhaseSuccessorStarted)

	options := sdCovReceiveOptions(store, t.TempDir(), request, raw)
	options.InspectSuccessor = func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
		return sessionlaunch.Result{}, injected
	}
	if _, err := Receive(context.Background(), options); !errors.Is(err, injected) {
		t.Fatalf("Receive error = %v, want successor inspection failure", err)
	}

	options.InspectSuccessor = nil
	if _, err := Receive(context.Background(), options); err == nil {
		t.Fatal("Receive with the production inspection seam = nil, want failure for an absent successor")
	}
}

func TestSdCovReceiveDefaultsToProductionVerifierOnWorktreeReadyReplay(t *testing.T) {
	t.Parallel()
	request, raw, digest := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	sdCovSeedPhases(t, store, request.HandoffID, digest, sessionmove.PhaseReceived, sessionmove.PhaseWorktreeReady)
	options := sdCovReceiveOptions(store, t.TempDir(), request, raw)
	options.InspectSuccessor = receiveTestMissingInspect
	_, err := Receive(context.Background(), options)
	if err == nil {
		t.Fatal("Receive with the production verifier = nil, want refusal for an absent pinned worktree")
	}
	state, loadErr := store.Load(request.HandoffID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if state.Receipt != nil {
		t.Fatalf("receipt published without a verified target worktree: %#v", state.Receipt)
	}
}

func TestSdCovReceiveDefaultsToProductionReceiverWithoutDurableWorktree(t *testing.T) {
	t.Parallel()
	request, raw, _ := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	options := sdCovReceiveOptions(store, t.TempDir(), request, raw)
	_, err := Receive(context.Background(), options)
	if err == nil {
		t.Fatal("Receive with the production receiver = nil, want refusal for an absent bundle remote")
	}
	state, loadErr := store.Load(request.HandoffID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if state.Receipt != nil {
		t.Fatalf("receipt published without a received worktree: %#v", state.Receipt)
	}
	if len(state.Events) != 2 || state.Events[0].Phase != sessionmove.PhaseReceived || state.Events[1].Phase != sessionmove.PhaseFailed {
		t.Fatalf("events = %#v, want received then failed", state.Events)
	}
	if !strings.Contains(state.Events[1].Diagnostic, "retry") {
		t.Fatalf("failed diagnostic = %q, want actionable retry guidance", state.Events[1].Diagnostic)
	}
}

func TestSdCovReceiveReportsFailureWhenFailedEventCannotBeAppended(t *testing.T) {
	t.Parallel()
	request, raw, _ := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	receiveErr := errors.New("remote branch tip moved")
	projectsRoot := t.TempDir()
	options := sdCovReceiveOptions(store, projectsRoot, request, raw)
	options.ReceiveWorktree = func(context.Context, worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
		sdCovBlockEventSequence(t, store, request.HandoffID, 2)
		return worktrees.SessionReceiveResult{}, receiveErr
	}
	_, err := Receive(context.Background(), options)
	if !errors.Is(err, receiveErr) || !strings.Contains(err.Error(), "record failed target receive phase") {
		t.Fatalf("Receive error = %v, want combined receive and event failure", err)
	}
}

func TestSdCovReceiveFailsWhenWorktreeReadyEventCannotBeAppended(t *testing.T) {
	t.Parallel()
	request, raw, _ := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	projectsRoot := t.TempDir()
	expectedWorktree := receiveTestWorktree(t, projectsRoot, request)
	options := sdCovReceiveOptions(store, projectsRoot, request, raw)
	options.ReceiveWorktree = func(_ context.Context, options worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
		sdCovBlockEventSequence(t, store, request.HandoffID, 2)
		return worktrees.SessionReceiveResult{WorktreeDir: expectedWorktree, Commit: options.Request.BundleCommit}, nil
	}
	_, err := Receive(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "record target worktree-ready phase") {
		t.Fatalf("Receive error = %v, want worktree-ready persistence failure", err)
	}
}

func TestSdCovReceiveRejectsWorktreeOutsideDeterministicTargetPath(t *testing.T) {
	t.Parallel()
	request, raw, _ := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	projectsRoot := t.TempDir()
	options := sdCovReceiveOptions(store, projectsRoot, request, raw)
	options.ReceiveWorktree = func(_ context.Context, options worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
		return worktrees.SessionReceiveResult{
			WorktreeDir: filepath.Join(projectsRoot, "somewhere-else"), Commit: options.Request.BundleCommit,
		}, nil
	}
	options.StartSuccessor = func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
		t.Fatal("successor started from a worktree outside the admitted target path")
		return sessionlaunch.Result{}, nil
	}
	_, err := Receive(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "does not match deterministic target path") {
		t.Fatalf("Receive error = %v, want deterministic-path mismatch", err)
	}
	state, loadErr := store.Load(request.HandoffID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(state.Events) != 2 || stateHasPhase(state, sessionmove.PhaseSuccessorStarted) {
		t.Fatalf("events = %#v, want only received and worktree_ready", state.Events)
	}
}

func TestSdCovReceiveRejectsSuccessorIdentityThatConflictsWithAdmittedHandoff(t *testing.T) {
	t.Parallel()
	request, raw, _ := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	projectsRoot := t.TempDir()
	expectedWorktree := receiveTestWorktree(t, projectsRoot, request)
	options := sdCovReceiveOptions(store, projectsRoot, request, raw)
	options.ReceiveWorktree = func(_ context.Context, options worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
		return worktrees.SessionReceiveResult{WorktreeDir: expectedWorktree, Commit: options.Request.BundleCommit}, nil
	}
	options.StartSuccessor = func(ctx context.Context, options sessionlaunch.Options) (sessionlaunch.Result, error) {
		successor, err := receiveTestStart(ctx, options)
		successor.WBSessionID = "wbs-impostor"
		return successor, err
	}
	_, err := Receive(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "live successor identity conflicts with admitted handoff") {
		t.Fatalf("Receive error = %v, want successor identity conflict", err)
	}
	state, loadErr := store.Load(request.HandoffID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if state.Receipt != nil {
		t.Fatalf("receipt published for a conflicting successor: %#v", state.Receipt)
	}
}

func TestSdCovReceiveRejectsSuccessorReceiptThatFailsAdmission(t *testing.T) {
	t.Parallel()
	request, raw, _ := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	projectsRoot := t.TempDir()
	expectedWorktree := receiveTestWorktree(t, projectsRoot, request)
	options := sdCovReceiveOptions(store, projectsRoot, request, raw)
	options.ReceiveWorktree = func(_ context.Context, options worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
		return worktrees.SessionReceiveResult{WorktreeDir: expectedWorktree, Commit: options.Request.BundleCommit}, nil
	}
	options.StartSuccessor = func(ctx context.Context, options sessionlaunch.Options) (sessionlaunch.Result, error) {
		successor, err := receiveTestStart(ctx, options)
		successor.TmuxName = "wb-session-not-the-admitted-name"
		return successor, err
	}
	_, err := Receive(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "tmux_name") {
		t.Fatalf("Receive error = %v, want successor receipt admission failure", err)
	}
	state, loadErr := store.Load(request.HandoffID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if state.Receipt != nil {
		t.Fatalf("invalid successor receipt was published: %#v", state.Receipt)
	}
}

func TestSdCovReceiveFailsWhenSuccessorStartedEventCannotBeAppended(t *testing.T) {
	t.Parallel()
	request, raw, _ := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	projectsRoot := t.TempDir()
	expectedWorktree := receiveTestWorktree(t, projectsRoot, request)
	options := sdCovReceiveOptions(store, projectsRoot, request, raw)
	options.ReceiveWorktree = func(_ context.Context, options worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
		sdCovBlockEventSequence(t, store, request.HandoffID, 3)
		return worktrees.SessionReceiveResult{WorktreeDir: expectedWorktree, Commit: options.Request.BundleCommit}, nil
	}
	options.StartSuccessor = receiveTestStart
	options.InspectSuccessor = receiveTestInspect
	_, err := Receive(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "record target successor-started phase") {
		t.Fatalf("Receive error = %v, want successor-started persistence failure", err)
	}
	state, loadErr := store.Load(request.HandoffID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if state.Receipt != nil {
		t.Fatalf("receipt published without a durable successor-started phase: %#v", state.Receipt)
	}
}

func TestSdCovReceiveFailsWhenTargetCompletionCannotBeRecorded(t *testing.T) {
	t.Parallel()
	request, raw, _ := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	projectsRoot := t.TempDir()
	expectedWorktree := receiveTestWorktree(t, projectsRoot, request)
	completionErr := errors.New("target Work Log terminal refused")
	workLog := receiveTestWorkLog()
	workLog.complete = func(worktrees.ExternalTargetCompletionOptions) (worktrees.LocalWorkLogEvent, error) {
		return worktrees.LocalWorkLogEvent{}, completionErr
	}
	options := sdCovReceiveOptions(store, projectsRoot, request, raw)
	options.workLog = workLog
	options.ReceiveWorktree = func(_ context.Context, options worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
		return worktrees.SessionReceiveResult{WorktreeDir: expectedWorktree, Commit: options.Request.BundleCommit}, nil
	}
	options.StartSuccessor = receiveTestStart
	options.InspectSuccessor = receiveTestInspect
	_, err := Receive(context.Background(), options)
	if !errors.Is(err, completionErr) || !strings.Contains(err.Error(), "record completed target Work Log custody before receipt") {
		t.Fatalf("Receive error = %v, want target completion failure", err)
	}
	state, loadErr := store.Load(request.HandoffID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if state.Receipt != nil {
		t.Fatalf("receipt published before the target Work Log terminal: %#v", state.Receipt)
	}
}

func TestSdCovReceiveDefaultsToProductionCompletionSeam(t *testing.T) {
	t.Parallel()
	request, raw, _ := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	projectsRoot := t.TempDir()
	expectedWorktree := receiveTestWorktree(t, projectsRoot, request)
	workLog := receiveTestWorkLog()
	workLog.complete = nil
	options := sdCovReceiveOptions(store, projectsRoot, request, raw)
	options.workLog = workLog
	options.ReceiveWorktree = func(_ context.Context, options worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
		return worktrees.SessionReceiveResult{WorktreeDir: expectedWorktree, Commit: options.Request.BundleCommit}, nil
	}
	options.StartSuccessor = receiveTestStart
	options.InspectSuccessor = receiveTestInspect
	_, err := Receive(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "record completed target Work Log custody before receipt") {
		t.Fatalf("Receive error = %v, want production target completion refusal", err)
	}
	state, loadErr := store.Load(request.HandoffID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if state.Receipt != nil {
		t.Fatalf("receipt published without the production Work Log terminal: %#v", state.Receipt)
	}
}

func TestSdCovReceiveFailsWhenReceiptPublicationIsBlocked(t *testing.T) {
	t.Parallel()
	request, raw, _ := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	projectsRoot := t.TempDir()
	expectedWorktree := receiveTestWorktree(t, projectsRoot, request)
	options := sdCovReceiveOptions(store, projectsRoot, request, raw)
	options.ReceiveWorktree = func(_ context.Context, options worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
		if err := os.MkdirAll(filepath.Join(sdCovHandoffDir(store, request.HandoffID), "receipt.json"), 0o700); err != nil {
			t.Fatal(err)
		}
		return worktrees.SessionReceiveResult{WorktreeDir: expectedWorktree, Commit: options.Request.BundleCommit}, nil
	}
	options.StartSuccessor = receiveTestStart
	options.InspectSuccessor = receiveTestInspect
	_, err := Receive(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "publish completed successor receipt") {
		t.Fatalf("Receive error = %v, want blocked receipt publication", err)
	}
	// The blocked aggregate cannot be projected through Store.Load, so assert
	// the durable event evidence directly.
	phases := sdCovDurablePhases(t, store, request.HandoffID)
	want := []sessionmove.Phase{sessionmove.PhaseReceived, sessionmove.PhaseWorktreeReady, sessionmove.PhaseSuccessorStarted}
	if len(phases) != len(want) {
		t.Fatalf("durable phases = %v, want %v", phases, want)
	}
	for index, phase := range want {
		if phases[index] != phase {
			t.Fatalf("durable phase %d = %s, want %s", index, phases[index], phase)
		}
	}
	info, statErr := os.Stat(filepath.Join(sdCovHandoffDir(store, request.HandoffID), "receipt.json"))
	if statErr != nil || !info.IsDir() {
		t.Fatalf("blocked receipt publication replaced the foreign entry: info=%v err=%v", info, statErr)
	}
}

func TestSdCovReceiveFailsWhenCompletedEventCannotBeAppendedAfterReceipt(t *testing.T) {
	t.Parallel()
	request, raw, _ := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	projectsRoot := t.TempDir()
	expectedWorktree := receiveTestWorktree(t, projectsRoot, request)
	options := sdCovReceiveOptions(store, projectsRoot, request, raw)
	options.ReceiveWorktree = func(_ context.Context, options worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
		sdCovBlockEventSequence(t, store, request.HandoffID, 4)
		return worktrees.SessionReceiveResult{WorktreeDir: expectedWorktree, Commit: options.Request.BundleCommit}, nil
	}
	options.StartSuccessor = receiveTestStart
	options.InspectSuccessor = receiveTestInspect
	_, err := Receive(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "record target completed phase after durable receipt") {
		t.Fatalf("Receive error = %v, want completed-phase persistence failure", err)
	}
	state, loadErr := store.Load(request.HandoffID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if state.Receipt == nil || stateHasPhase(state, sessionmove.PhaseCompleted) {
		t.Fatalf("state = %#v, want the durable receipt to precede the completed phase", state)
	}
}

func TestSdCovReceiveReportsRetryableLaunchWhenFailurePhaseCannotBeRecorded(t *testing.T) {
	t.Parallel()
	request, raw, digest := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	sdCovSeedPhases(t, store, request.HandoffID, digest, sessionmove.PhaseReceived, sessionmove.PhaseWorktreeReady)
	sdCovBlockEventSequence(t, store, request.HandoffID, 3)
	options := sdCovReceiveOptions(store, t.TempDir(), request, raw)
	options.InspectSuccessor = func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
		return sessionlaunch.Result{}, fmt.Errorf("%w: attempt 1 has exact release-bound exec failure", sessionlaunch.ErrRetryableLaunch)
	}
	options.StartSuccessor = func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
		t.Fatal("successor replaced before the failed attempt was durable")
		return sessionlaunch.Result{}, nil
	}
	_, err := Receive(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "record missing failed successor attempt phase") {
		t.Fatalf("Receive error = %v, want failure-phase persistence refusal", err)
	}
	state, loadErr := store.Load(request.HandoffID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(state.Events) != 2 || stateHasPhase(state, sessionmove.PhaseFailed) {
		t.Fatalf("events = %#v, want no unrecorded failure", state.Events)
	}
}

func TestSdCovReceiveReportsRetryableLaunchReplacementFailure(t *testing.T) {
	t.Parallel()
	request, raw, digest := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	sdCovSeedPhases(t, store, request.HandoffID, digest, sessionmove.PhaseReceived, sessionmove.PhaseWorktreeReady)
	sdCovBlockEventSequence(t, store, request.HandoffID, 4)
	failErr := errors.New("failed-attempt Work Log refused")
	workLog := receiveTestWorkLog()
	workLog.fail = func(worktrees.ExternalTargetAttemptFailureOptions) (worktrees.LocalWorkLogEvent, error) {
		return worktrees.LocalWorkLogEvent{}, failErr
	}
	options := sdCovReceiveOptions(store, t.TempDir(), request, raw)
	options.workLog = workLog
	options.InspectSuccessor = func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
		return sessionlaunch.Result{}, fmt.Errorf("%w: attempt 1 has exact release-bound exec failure", sessionlaunch.ErrRetryableLaunch)
	}
	options.StartSuccessor = func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
		return sessionlaunch.Result{}, sdCovAttemptFailure(request, digest)
	}
	_, err := Receive(context.Background(), options)
	if !errors.Is(err, failErr) {
		t.Fatalf("Receive error = %v, want the failed-attempt recording failure", err)
	}
	for _, want := range []string{"record exact failed target launcher attempt", "record failed successor start phase", "post-release exec failed"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Receive error = %q, want it to contain %q", err, want)
		}
	}
	state, loadErr := store.Load(request.HandoffID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(state.Events) != 3 {
		t.Fatalf("events = %#v, want received, worktree_ready and one failed attempt", state.Events)
	}
}

func TestSdCovReceiveUsesProductionFailureSeamWhenUnset(t *testing.T) {
	t.Parallel()
	request, raw, digest := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	sdCovSeedPhases(t, store, request.HandoffID, digest, sessionmove.PhaseReceived, sessionmove.PhaseWorktreeReady)
	workLog := receiveTestWorkLog()
	workLog.fail = nil
	options := sdCovReceiveOptions(store, t.TempDir(), request, raw)
	options.workLog = workLog
	options.InspectSuccessor = func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
		return sessionlaunch.Result{}, fmt.Errorf("%w: attempt 1 has exact release-bound exec failure", sessionlaunch.ErrRetryableLaunch)
	}
	startErr := sdCovAttemptFailure(request, digest)
	options.StartSuccessor = func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
		return sessionlaunch.Result{}, startErr
	}
	_, err := Receive(context.Background(), options)
	if !strings.Contains(err.Error(), "post-release exec failed") {
		t.Fatalf("Receive error = %v, want the exact launcher failure surfaced", err)
	}
	state, loadErr := store.Load(request.HandoffID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(state.Events) != 4 {
		t.Fatalf("events = %#v, want received, worktree_ready and both failed-attempt records", state.Events)
	}
	if !strings.Contains(state.Events[3].Diagnostic, "after release") {
		t.Fatalf("failed diagnostic = %q, want the post-release attempt diagnostic", state.Events[3].Diagnostic)
	}
}

func TestSdCovReceiveReportsNonReleasedInspectionFailureBeforeWorktreeReplay(t *testing.T) {
	t.Parallel()
	request, raw, digest := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	sdCovSeedPhases(t, store, request.HandoffID, digest, sessionmove.PhaseReceived, sessionmove.PhaseWorktreeReady)
	injected := errors.New("inspection exploded")
	options := sdCovReceiveOptions(store, t.TempDir(), request, raw)
	options.InspectSuccessor = func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
		return sessionlaunch.Result{}, injected
	}
	options.VerifyWorktree = func(context.Context, worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
		t.Fatal("worktree replay ran after an untyped inspection failure")
		return worktrees.SessionReceiveResult{}, nil
	}
	_, err := Receive(context.Background(), options)
	if !errors.Is(err, injected) || !strings.Contains(err.Error(), "inspect possible released successor before worktree replay") {
		t.Fatalf("Receive error = %v, want typed inspection wrap", err)
	}
}

func TestSdCovReceiveDefaultsToProductionLauncherSeam(t *testing.T) {
	t.Parallel()
	request, raw, _ := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	projectsRoot := t.TempDir()
	expectedWorktree := receiveTestWorktree(t, projectsRoot, request)
	options := sdCovReceiveOptions(store, projectsRoot, request, raw)
	options.ReceiveWorktree = func(_ context.Context, options worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
		return worktrees.SessionReceiveResult{WorktreeDir: expectedWorktree, Commit: options.Request.BundleCommit}, nil
	}
	_, err := Receive(context.Background(), options)
	if err == nil {
		t.Fatal("Receive with the production launcher seam = nil, want refusal without a real release fence")
	}
	state, loadErr := store.Load(request.HandoffID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if state.Receipt != nil || !stateHasPhase(state, sessionmove.PhaseFailed) {
		t.Fatalf("state = %#v, want a recorded failure and no receipt", state)
	}
}

func TestSdCovReceiveDefaultsToProductionInspectionOnWorktreeReadyReplay(t *testing.T) {
	t.Parallel()
	request, raw, digest := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	sdCovSeedPhases(t, store, request.HandoffID, digest, sessionmove.PhaseReceived, sessionmove.PhaseWorktreeReady)
	options := sdCovReceiveOptions(store, t.TempDir(), request, raw)
	// InspectSuccessor stays nil so the production inspection seam is used.
	_, err := Receive(context.Background(), options)
	if err == nil {
		t.Fatal("Receive with the production inspection seam = nil, want refusal for an absent successor")
	}
	state, loadErr := store.Load(request.HandoffID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if state.Receipt != nil {
		t.Fatalf("receipt published without a live successor: %#v", state.Receipt)
	}
}

func TestSdCovReceiveCompletesRetryableLaunchReplacement(t *testing.T) {
	t.Parallel()
	request, raw, digest := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	sdCovSeedPhases(t, store, request.HandoffID, digest, sessionmove.PhaseReceived, sessionmove.PhaseWorktreeReady)
	failedAttempts := 0
	workLog := receiveTestWorkLog()
	workLog.fail = func(options worktrees.ExternalTargetAttemptFailureOptions) (worktrees.LocalWorkLogEvent, error) {
		failedAttempts++
		if options.Failure.Diagnostic != "post-release exec failed" {
			t.Fatalf("failed-attempt evidence = %#v", options.Failure)
		}
		return worktrees.LocalWorkLogEvent{ID: "failed"}, nil
	}
	options := sdCovReceiveOptions(store, t.TempDir(), request, raw)
	options.workLog = workLog
	options.InspectSuccessor = func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
		return sessionlaunch.Result{}, fmt.Errorf("%w: attempt 1 has exact release-bound exec failure", sessionlaunch.ErrRetryableLaunch)
	}
	options.StartSuccessor = func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
		return sessionlaunch.Result{}, sdCovAttemptFailure(request, digest)
	}
	_, err := Receive(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "post-release exec failed") {
		t.Fatalf("Receive error = %v, want the exact launcher failure surfaced", err)
	}
	if failedAttempts != 1 {
		t.Fatalf("failed-attempt recordings = %d, want 1", failedAttempts)
	}
	state, loadErr := store.Load(request.HandoffID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(state.Events) != 4 || state.Receipt != nil {
		t.Fatalf("state = %#v, want two seeded phases plus the recorded failed attempt and no receipt", state)
	}
	if !strings.Contains(state.Events[3].Diagnostic, "after release") {
		t.Fatalf("failed diagnostic = %q, want the post-release attempt diagnostic", state.Events[3].Diagnostic)
	}
}

func TestSdCovReceiveReportsReplacementFailureWhenFailedAttemptCannotBeRecorded(t *testing.T) {
	t.Parallel()
	request, raw, digest := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	projectsRoot := t.TempDir()
	expectedWorktree := receiveTestWorktree(t, projectsRoot, request)
	failErr := errors.New("failed-attempt Work Log refused")
	workLog := receiveTestWorkLog()
	workLog.fail = func(worktrees.ExternalTargetAttemptFailureOptions) (worktrees.LocalWorkLogEvent, error) {
		return worktrees.LocalWorkLogEvent{}, failErr
	}
	options := sdCovReceiveOptions(store, projectsRoot, request, raw)
	options.workLog = workLog
	options.ReceiveWorktree = func(_ context.Context, options worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
		return worktrees.SessionReceiveResult{WorktreeDir: expectedWorktree, Commit: options.Request.BundleCommit}, nil
	}
	options.StartSuccessor = func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
		return sessionlaunch.Result{}, sdCovAttemptFailure(request, digest)
	}
	_, err := Receive(context.Background(), options)
	if !errors.Is(err, failErr) {
		t.Fatalf("Receive error = %v, want the failed-attempt recording failure", err)
	}
	if !strings.Contains(err.Error(), "post-release exec failed") || !strings.Contains(err.Error(), "record exact failed target launcher attempt") {
		t.Fatalf("Receive error = %v, want both the launcher failure and the recording failure", err)
	}
	state, loadErr := store.Load(request.HandoffID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(state.Events) != 3 || state.Receipt != nil {
		t.Fatalf("state = %#v, want received, worktree_ready and one failed attempt", state)
	}
}

func TestSdCovReceiveCompletesSuccessorWithCustomReleaseSeam(t *testing.T) {
	t.Parallel()
	request, raw, _ := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	projectsRoot := t.TempDir()
	expectedWorktree := receiveTestWorktree(t, projectsRoot, request)
	expectedReference, err := sessionmove.ExpectedTargetWorkLogReference(request, sessionmove.DigestBytes(raw))
	if err != nil {
		t.Fatal(err)
	}
	options := sdCovReceiveOptions(store, projectsRoot, request, raw)
	options.ReceiveWorktree = func(_ context.Context, options worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
		return worktrees.SessionReceiveResult{WorktreeDir: expectedWorktree, Commit: options.Request.BundleCommit}, nil
	}
	options.BeforeRelease = func(context.Context, sessionlaunch.Prepared) (string, error) {
		return expectedReference.String(), nil
	}
	options.StartSuccessor = receiveTestStart
	options.InspectSuccessor = receiveTestInspect
	result, err := Receive(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != sessionmove.PhaseCompleted || result.Receipt == nil || !strings.HasPrefix(result.Receipt.TargetWorkLogReference, "worklog:") {
		t.Fatalf("result = %#v, want a completed receipt bound to the admitted Work Log lineage", result)
	}
	if result.Receipt.TargetWorkLogReference != expectedReference.String() {
		t.Fatalf("receipt reference = %q, want %q", result.Receipt.TargetWorkLogReference, expectedReference.String())
	}
}

func TestSdCovReceiveRejectsSuccessorWorktreeOutsideDeterministicTargetPath(t *testing.T) {
	t.Parallel()
	request, raw, _ := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	projectsRoot := t.TempDir()
	expectedWorktree := receiveTestWorktree(t, projectsRoot, request)
	options := sdCovReceiveOptions(store, projectsRoot, request, raw)
	options.ReceiveWorktree = func(_ context.Context, options worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
		return worktrees.SessionReceiveResult{WorktreeDir: expectedWorktree, Commit: options.Request.BundleCommit}, nil
	}
	options.StartSuccessor = func(ctx context.Context, options sessionlaunch.Options) (sessionlaunch.Result, error) {
		successor, err := receiveTestStart(ctx, options)
		successor.WorktreeDir = filepath.Join(projectsRoot, "somewhere-else")
		return successor, err
	}
	options.InspectSuccessor = receiveTestInspect
	_, err := Receive(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "live successor worktree") {
		t.Fatalf("Receive error = %v, want deterministic successor worktree refusal", err)
	}
	state, loadErr := store.Load(request.HandoffID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if state.Receipt != nil || stateHasPhase(state, sessionmove.PhaseSuccessorStarted) {
		t.Fatalf("state = %#v, want no successor-started phase and no receipt", state)
	}
}

func TestSdCovReceiveRejectsReceiptThatDiffersFromItsDurableProjection(t *testing.T) {
	t.Parallel()
	request, raw, _ := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	projectsRoot := t.TempDir()
	expectedWorktree := receiveTestWorktree(t, projectsRoot, request)
	options := sdCovReceiveOptions(store, projectsRoot, request, raw)
	options.ReceiveWorktree = func(_ context.Context, options worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
		return worktrees.SessionReceiveResult{WorktreeDir: expectedWorktree, Commit: options.Request.BundleCommit}, nil
	}
	options.StartSuccessor = func(ctx context.Context, options sessionlaunch.Options) (sessionlaunch.Result, error) {
		successor, err := receiveTestStart(ctx, options)
		// A named fixed zone re-decodes from RFC3339 as an equivalent instant
		// carrying a different in-memory location, which is not the exact
		// value the caller supplied. The durable receipt must still equal the
		// caller's value or completion must be refused.
		successor.StartedAt = time.Date(2026, time.August, 25, 13, 0, 0, 0, time.FixedZone("SDCOV", 3600))
		return successor, err
	}
	options.InspectSuccessor = receiveTestInspect
	_, err := Receive(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "durable successor receipt changed during completion") {
		t.Fatalf("Receive error = %v, want durable-receipt identity refusal", err)
	}
	state, loadErr := store.Load(request.HandoffID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if state.Receipt == nil || stateHasPhase(state, sessionmove.PhaseCompleted) {
		t.Fatalf("state = %#v, want a durable receipt without the completed phase", state)
	}
}

func TestSdCovReceiveReportsTargetWorkLogPrepareSeamFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		mutate  func(*Options, worktrees.SessionReceiveResult)
		wantErr string
	}{
		{
			name: "production prepare seam refuses",
			mutate: func(options *Options, _ worktrees.SessionReceiveResult) {
				workLog := receiveTestWorkLog()
				workLog.prepare = nil
				options.workLog = workLog
			},
			wantErr: "worktree",
		},
		{
			name: "prepare seam returns an error",
			mutate: func(options *Options, _ worktrees.SessionReceiveResult) {
				workLog := receiveTestWorkLog()
				workLog.prepare = func(context.Context, worktrees.ExternalSessionWorkLogPrepareOptions) (worktrees.ExternalSessionWorkLogPrepareResult, error) {
					return worktrees.ExternalSessionWorkLogPrepareResult{}, errors.New("prepare refused")
				}
				options.workLog = workLog
			},
			wantErr: "prepare refused",
		},
		{
			name: "prepare seam returns a mismatched reference",
			mutate: func(options *Options, _ worktrees.SessionReceiveResult) {
				workLog := receiveTestWorkLog()
				workLog.prepare = func(context.Context, worktrees.ExternalSessionWorkLogPrepareOptions) (worktrees.ExternalSessionWorkLogPrepareResult, error) {
					return worktrees.ExternalSessionWorkLogPrepareResult{WorkLogReference: "worklog:other/run/" + strings.Repeat("f", 64)}, nil
				}
				options.workLog = workLog
			},
			wantErr: "does not match deterministic admitted lineage",
		},
		{
			name: "custom before-release seam returns an error",
			mutate: func(options *Options, _ worktrees.SessionReceiveResult) {
				options.BeforeRelease = func(context.Context, sessionlaunch.Prepared) (string, error) {
					return "", errors.New("custom release seam refused")
				}
			},
			wantErr: "custom release seam refused",
		},
		{
			name: "custom before-release seam returns a mismatched reference",
			mutate: func(options *Options, _ worktrees.SessionReceiveResult) {
				options.BeforeRelease = func(context.Context, sessionlaunch.Prepared) (string, error) {
					return "worklog:other/run/" + strings.Repeat("f", 64), nil
				}
			},
			wantErr: "custom target Work Log reference",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			request, raw, _ := receiveTestRequest(t)
			store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
			projectsRoot := t.TempDir()
			expectedWorktree := receiveTestWorktree(t, projectsRoot, request)
			options := sdCovReceiveOptions(store, projectsRoot, request, raw)
			options.ReceiveWorktree = func(_ context.Context, options worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error) {
				return worktrees.SessionReceiveResult{WorktreeDir: expectedWorktree, Commit: options.Request.BundleCommit}, nil
			}
			options.StartSuccessor = receiveTestStart
			options.InspectSuccessor = receiveTestInspect
			tc.mutate(&options, worktrees.SessionReceiveResult{})
			_, err := Receive(context.Background(), options)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Receive error = %v, want it to contain %q", err, tc.wantErr)
			}
			state, loadErr := store.Load(request.HandoffID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if state.Receipt != nil {
				t.Fatalf("receipt published after a refused target Work Log prepare: %#v", state.Receipt)
			}
		})
	}
}
