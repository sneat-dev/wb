// Package sessionreceive owns target-side request admission, pinned-worktree
// recovery, successor launch, and completed-receipt publication.
package sessionreceive

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/worktrees"
)

const maxFailureDiagnosticBytes = 1024

// Options supplies exact courier bytes plus operator-owned target identity.
// LocalMachine must come from validated local configuration, never the request
// or a public receive flag.
type Options struct {
	Store            sessionmove.Store
	ProjectsRoot     string
	LocalMachine     string
	RawRequest       []byte
	Now              func() time.Time
	ReceiveWorktree  func(context.Context, worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error)
	VerifyWorktree   func(context.Context, worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error)
	StartSuccessor   func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error)
	InspectSuccessor func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error)
	BeforeRelease    sessionlaunch.BeforeRelease

	workLog targetWorkLogDependencies
	hooks   receiveHooks
}

type receiveHooks struct {
	afterTargetCompleted func() error
	afterReceipt         func() error
}

type targetWorkLogDependencies struct {
	prepare  func(context.Context, worktrees.ExternalSessionWorkLogPrepareOptions) (worktrees.ExternalSessionWorkLogPrepareResult, error)
	complete func(worktrees.ExternalTargetCompletionOptions) (worktrees.LocalWorkLogEvent, error)
	fail     func(worktrees.ExternalTargetAttemptFailureOptions) (worktrees.LocalWorkLogEvent, error)
}

// Result reports target progress and the exact completed successor receipt.
type Result struct {
	Request   sessionmove.Request             `json:"request"`
	Digest    sessionmove.Digest              `json:"request_digest"`
	Phase     sessionmove.Phase               `json:"phase"`
	Worktree  *worktrees.SessionReceiveResult `json:"worktree,omitempty"`
	Receipt   *sessionmove.Receipt            `json:"receipt,omitempty"`
	Successor *sessionlaunch.Result           `json:"successor,omitempty"`
	Replay    bool                            `json:"replay"`
}

// Receive admits exact request bytes, serializes execution by handoff ID, and
// advances idempotently through a receipt-backed completed phase. Target Work
// Log completion is durable before the receipt is published; source custody is
// still a separate source-side acknowledgement transaction.
func Receive(ctx context.Context, options Options) (Result, error) {
	var result Result
	request, err := sessionmove.DecodeRequest(options.RawRequest)
	if err != nil {
		return result, err
	}
	localMachine := strings.TrimSpace(options.LocalMachine)
	if localMachine == "" {
		return result, fmt.Errorf("validated local remote.machine identity is required")
	}
	if request.TargetMachine != localMachine {
		return result, fmt.Errorf("handoff %s targets machine %q, but this receiver is configured as %q", request.HandoffID, request.TargetMachine, localMachine)
	}
	digest := sessionmove.DigestBytes(options.RawRequest)
	admission, err := options.Store.Admit(options.RawRequest, digest)
	if err != nil {
		return result, err
	}
	result = Result{Request: request, Digest: digest, Replay: admission.Replay}
	lock, err := options.Store.AcquireExecutionLock(ctx, request.HandoffID, digest)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = lock.Close() }()
	// Re-admit under the fence: another process may have completed and saved a
	// receipt while this caller waited.
	admission, err = options.Store.ReadmitUnderLock(lock, request.HandoffID, digest, options.RawRequest)
	if err != nil {
		return Result{}, err
	}
	state, err := options.Store.LoadUnderLock(lock, request.HandoffID, digest)
	if err != nil {
		return Result{}, err
	}
	hadReceived := stateHasPhase(state, sessionmove.PhaseReceived)
	hadWorktreeReady := stateHasPhase(state, sessionmove.PhaseWorktreeReady)
	hadSuccessorStarted := stateHasPhase(state, sessionmove.PhaseSuccessorStarted)
	hadCompleted := stateHasPhase(state, sessionmove.PhaseCompleted)
	result.Replay = result.Replay || hadReceived || hadWorktreeReady || hadSuccessorStarted || hadCompleted || admission.Receipt != nil
	now := time.Now().UTC
	if options.Now != nil {
		now = options.Now
	}
	// A receipt fast path still enters the exact aggregate fence. A crash after
	// receipt publication but before PhaseCompleted must repair that phase, and
	// no path-based Store read may split evidence across a swapped directory.
	// The immutable receipt is sufficient replay authority even after the
	// successor harness exits; completed replay must not relaunch or require Git.
	if state.Receipt != nil {
		receipt := *state.Receipt
		if err := sessionmove.ValidateReceiptForRequest(receipt, request, digest); err != nil {
			return Result{}, fmt.Errorf("validate durable completed successor receipt: %w", err)
		}
		if !hadCompleted {
			if _, err := options.Store.AppendEventUnderLock(lock, request.HandoffID, digest,
				sessionmove.HandoffEvent{Phase: sessionmove.PhaseCompleted, At: now()}); err != nil {
				return Result{}, fmt.Errorf("repair target completed phase from durable receipt: %w", err)
			}
		}
		result.Phase, result.Receipt, result.Replay = sessionmove.PhaseCompleted, &receipt, true
		return result, nil
	}
	expectedWorktree, err := worktrees.SessionReceiveWorktreePath(options.ProjectsRoot, request)
	if err != nil {
		return Result{}, fmt.Errorf("derive deterministic target worktree: %w", err)
	}
	receivedAt := request.CreatedAt.UTC()
	for _, event := range state.Events {
		if event.Phase == sessionmove.PhaseReceived {
			receivedAt = event.At.UTC()
			break
		}
	}
	launchOptions, err := receiveLaunchOptions(options, request, digest, lock, expectedWorktree, receivedAt)
	if err != nil {
		return Result{}, err
	}

	if !hadReceived {
		if _, err := options.Store.AppendEventUnderLock(lock, request.HandoffID, digest,
			sessionmove.HandoffEvent{Phase: sessionmove.PhaseReceived, At: now()}); err != nil {
			return Result{}, fmt.Errorf("record target received phase: %w", err)
		}
	}
	if hadSuccessorStarted {
		inspect := options.InspectSuccessor
		if inspect == nil {
			inspect = sessionlaunch.Inspect
		}
		successor, inspectErr := inspect(ctx, launchOptions)
		if inspectErr != nil {
			return Result{}, inspectErr
		}
		return finalizeTargetReceipt(options, result, lock, launchOptions, successor, nil, true, false, now)
	}
	if hadWorktreeReady {
		inspect := options.InspectSuccessor
		if inspect == nil {
			inspect = sessionlaunch.Inspect
		}
		successor, inspectErr := inspect(ctx, launchOptions)
		if inspectErr == nil {
			return finalizeTargetReceipt(options, result, lock, launchOptions, successor, nil, false, false, now)
		}
		if errors.Is(inspectErr, sessionlaunch.ErrRetryableLaunch) {
			// Inspect has already proved an exact immutable terminal launcher
			// failure, dead PID, released fence, and absent tmux session. Route
			// only that typed outcome back through Start under the retained
			// execution lock. Start rechecks every replacement gate; Git must
			// not be consulted because the failed harness may have changed the
			// already-accepted worktree after release.
			workLogErr := recordReceiveAttemptFailure(options, request, digest, expectedWorktree, inspectErr)
			phaseErr := ensureReceiveFailureUnderLock(options.Store, lock, state, request.HandoffID, digest, now(), inspectErr)
			if workLogErr != nil || phaseErr != nil {
				return Result{}, errors.Join(workLogErr, phaseErr)
			}
			successor, startErr := startSuccessor(ctx, options, launchOptions)
			if startErr != nil {
				workLogErr := recordReceiveAttemptFailure(options, request, digest, expectedWorktree, startErr)
				phaseErr := appendReceiveFailureUnderLock(options.Store, lock, request.HandoffID, digest, now(), startErr)
				if workLogErr != nil || phaseErr != nil {
					return Result{}, errors.Join(startErr, workLogErr, phaseErr)
				}
				return Result{}, startErr
			}
			return finalizeTargetReceipt(options, result, lock, launchOptions, successor, nil, false, false, now)
		}
		if !errors.Is(inspectErr, sessionlaunch.ErrNotReleased) {
			return Result{}, fmt.Errorf("inspect possible released successor before worktree replay: %w", inspectErr)
		}
	}
	receiveWorktree := options.ReceiveWorktree
	if hadWorktreeReady {
		receiveWorktree = options.VerifyWorktree
		if receiveWorktree == nil {
			receiveWorktree = worktrees.VerifyReceivedSessionBundle
		}
	} else if receiveWorktree == nil {
		receiveWorktree = worktrees.ReceiveSessionBundle
	}
	worktree, receiveErr := receiveWorktree(ctx, worktrees.SessionReceiveOptions{
		ProjectsRoot:  options.ProjectsRoot,
		Request:       request,
		RequestDigest: digest,
		ExecutionLock: lock,
	})
	if receiveErr != nil {
		diagnostic := receiveFailureDiagnostic(receiveErr)
		if _, eventErr := options.Store.AppendEventUnderLock(lock, request.HandoffID, digest, sessionmove.HandoffEvent{
			Phase: sessionmove.PhaseFailed, At: now(), Diagnostic: diagnostic,
		}); eventErr != nil {
			return Result{}, fmt.Errorf("%w; record failed target receive phase: %v", receiveErr, eventErr)
		}
		return Result{}, receiveErr
	}
	if !hadWorktreeReady {
		if _, err := options.Store.AppendEventUnderLock(lock, request.HandoffID, digest,
			sessionmove.HandoffEvent{Phase: sessionmove.PhaseWorktreeReady, At: now()}); err != nil {
			return Result{}, fmt.Errorf("record target worktree-ready phase: %w", err)
		}
	}
	if worktree.WorktreeDir != expectedWorktree {
		return Result{}, fmt.Errorf("received worktree %s does not match deterministic target path %s", worktree.WorktreeDir, expectedWorktree)
	}
	successor, startErr := startSuccessor(ctx, options, launchOptions)
	if startErr != nil {
		workLogErr := recordReceiveAttemptFailure(options, request, digest, expectedWorktree, startErr)
		phaseErr := appendReceiveFailureUnderLock(options.Store, lock, request.HandoffID, digest, now(), startErr)
		if workLogErr != nil || phaseErr != nil {
			return Result{}, errors.Join(startErr, workLogErr, phaseErr)
		}
		return Result{}, startErr
	}
	return finalizeTargetReceipt(options, result, lock, launchOptions, successor, &worktree, false, false, now)
}

func startSuccessor(ctx context.Context, options Options, launchOptions sessionlaunch.Options) (sessionlaunch.Result, error) {
	start := options.StartSuccessor
	if start == nil {
		start = sessionlaunch.Start
	}
	return start(ctx, launchOptions)
}

func receiveLaunchOptions(options Options, request sessionmove.Request, digest sessionmove.Digest,
	lock *sessionmove.ExecutionLock, worktree string, receivedAt time.Time,
) (sessionlaunch.Options, error) {
	expectedReference, err := sessionmove.ExpectedTargetWorkLogReference(request, digest)
	if err != nil {
		return sessionlaunch.Options{}, err
	}
	beforeRelease := options.BeforeRelease
	if beforeRelease == nil {
		prepare := options.workLog.prepare
		if prepare == nil {
			prepare = worktrees.PrepareExternalSessionWorkLog
		}
		beforeRelease = func(ctx context.Context, prepared sessionlaunch.Prepared) (string, error) {
			result, err := prepare(ctx, worktrees.ExternalSessionWorkLogPrepareOptions{
				ProjectsRoot: options.ProjectsRoot, Request: request, RequestDigest: digest, ReceivedAt: receivedAt,
				Session: prepared.Session, AttemptID: prepared.AttemptID, AttemptIndex: prepared.AttemptIndex,
				WorktreeDir: prepared.WorktreeDir, PinnedCommit: prepared.PinnedCommit,
			})
			if err != nil {
				return "", err
			}
			if result.WorkLogReference != expectedReference.String() {
				return "", fmt.Errorf("prepared target Work Log reference %q does not match deterministic admitted lineage %q",
					result.WorkLogReference, expectedReference.String())
			}
			return result.WorkLogReference, nil
		}
	} else {
		supplied := beforeRelease
		beforeRelease = func(ctx context.Context, prepared sessionlaunch.Prepared) (string, error) {
			reference, err := supplied(ctx, prepared)
			if err != nil {
				return "", err
			}
			if reference != expectedReference.String() {
				return "", fmt.Errorf("custom target Work Log reference %q does not match deterministic admitted lineage %q",
					reference, expectedReference.String())
			}
			return reference, nil
		}
	}
	return sessionlaunch.Options{
		Store: options.Store, ProjectsRoot: options.ProjectsRoot, Request: request, RequestDigest: digest,
		WorktreeDir: worktree, PinnedCommit: request.BundleCommit, ExecutionLock: lock, BeforeRelease: beforeRelease,
	}, nil
}

func finalizeTargetReceipt(options Options, result Result, lock *sessionmove.ExecutionLock,
	launchOptions sessionlaunch.Options, successor sessionlaunch.Result, worktree *worktrees.SessionReceiveResult,
	hadSuccessorStarted, hadCompleted bool, now func() time.Time,
) (Result, error) {
	receipt, err := receiptFromSuccessor(result.Request, result.Digest, successor)
	if err != nil {
		return Result{}, err
	}
	if successor.WorktreeDir != launchOptions.WorktreeDir {
		return Result{}, fmt.Errorf("live successor worktree %q does not match deterministic target %q", successor.WorktreeDir, launchOptions.WorktreeDir)
	}
	if !hadSuccessorStarted {
		if _, err := options.Store.AppendEventUnderLock(lock, result.Request.HandoffID, result.Digest,
			sessionmove.HandoffEvent{Phase: sessionmove.PhaseSuccessorStarted, At: now()}); err != nil {
			return Result{}, fmt.Errorf("record target successor-started phase: %w", err)
		}
	}
	complete := options.workLog.complete
	if complete == nil {
		complete = worktrees.RecordExternalTargetCompleted
	}
	if _, err := complete(worktrees.ExternalTargetCompletionOptions{
		ProjectsRoot: options.ProjectsRoot, Request: result.Request, RequestDigest: result.Digest,
		Receipt: receipt, WorktreeDir: launchOptions.WorktreeDir,
	}); err != nil {
		return Result{}, fmt.Errorf("record completed target Work Log custody before receipt: %w", err)
	}
	if options.hooks.afterTargetCompleted != nil {
		if err := options.hooks.afterTargetCompleted(); err != nil {
			return Result{}, err
		}
	}
	durableReceipt, receiptReplay, err := options.Store.SaveReceiptUnderLock(lock, result.Request.HandoffID, result.Digest, receipt)
	if err != nil {
		return Result{}, fmt.Errorf("publish completed successor receipt: %w", err)
	}
	if durableReceipt != receipt {
		return Result{}, fmt.Errorf("durable successor receipt changed during completion")
	}
	if options.hooks.afterReceipt != nil {
		if err := options.hooks.afterReceipt(); err != nil {
			return Result{}, err
		}
	}
	if !hadCompleted {
		if _, err := options.Store.AppendEventUnderLock(lock, result.Request.HandoffID, result.Digest,
			sessionmove.HandoffEvent{Phase: sessionmove.PhaseCompleted, At: now()}); err != nil {
			return Result{}, fmt.Errorf("record target completed phase after durable receipt: %w", err)
		}
	}
	result.Phase, result.Receipt, result.Successor = sessionmove.PhaseCompleted, &durableReceipt, &successor
	result.Worktree = worktree
	result.Replay = result.Replay || receiptReplay || hadSuccessorStarted || hadCompleted
	return result, nil
}

func receiptFromSuccessor(request sessionmove.Request, digest sessionmove.Digest, successor sessionlaunch.Result) (sessionmove.Receipt, error) {
	if successor.HandoffID != request.HandoffID || successor.WBSessionID != request.SuccessorWBSessionID ||
		successor.PredecessorWBSessionID != request.PredecessorWBSessionID || successor.TargetMachine != request.TargetMachine ||
		successor.PinnedCommit != request.BundleCommit {
		return sessionmove.Receipt{}, fmt.Errorf("live successor identity conflicts with admitted handoff")
	}
	receipt := sessionmove.Receipt{
		SchemaVersion: sessionmove.ReceiptSchemaVersion, HandoffID: request.HandoffID, RequestDigest: digest,
		SuccessorWBSessionID: successor.WBSessionID, PredecessorWBSessionID: successor.PredecessorWBSessionID,
		TargetMachine: successor.TargetMachine, TmuxName: successor.TmuxName, Runtime: successor.Runtime,
		Model: successor.Model, NativeHarnessID: successor.NativeHarnessID,
		AttemptID: successor.AttemptID, AttemptIndex: successor.AttemptIndex, PID: successor.PID,
		TargetWorkLogReference: successor.TargetWorkLogRef, PinnedCommit: successor.PinnedCommit, StartedAt: successor.StartedAt,
	}
	if err := sessionmove.ValidateReceiptForRequest(receipt, request, digest); err != nil {
		return sessionmove.Receipt{}, err
	}
	return receipt, nil
}

func recordReceiveAttemptFailure(options Options, request sessionmove.Request, digest sessionmove.Digest, worktree string, failure error) error {
	var attemptFailure *sessionlaunch.AttemptFailureError
	if !errors.As(failure, &attemptFailure) {
		return nil
	}
	record := options.workLog.fail
	if record == nil {
		record = worktrees.RecordExternalTargetAttemptFailed
	}
	_, err := record(worktrees.ExternalTargetAttemptFailureOptions{
		ProjectsRoot: options.ProjectsRoot, Request: request, RequestDigest: digest,
		WorktreeDir: worktree, Failure: attemptFailure.Evidence,
	})
	if err != nil {
		return fmt.Errorf("record exact failed target launcher attempt: %w", err)
	}
	return nil
}

func appendReceiveFailureUnderLock(store sessionmove.Store, lock *sessionmove.ExecutionLock,
	handoffID string, digest sessionmove.Digest, at time.Time, failure error,
) error {
	diagnostic := receiveFailureDiagnostic(failure)
	if _, eventErr := store.AppendEventUnderLock(lock, handoffID, digest, sessionmove.HandoffEvent{
		Phase: sessionmove.PhaseFailed, At: at, Diagnostic: diagnostic,
	}); eventErr != nil {
		return fmt.Errorf("%w; record failed successor start phase: %v", failure, eventErr)
	}
	return nil
}

func ensureReceiveFailureUnderLock(store sessionmove.Store, lock *sessionmove.ExecutionLock, state sessionmove.State,
	handoffID string, digest sessionmove.Digest, at time.Time, failure error,
) error {
	diagnostic := receiveFailureDiagnostic(failure)
	for _, event := range state.Events {
		if event.Phase == sessionmove.PhaseFailed && event.Diagnostic == diagnostic {
			return nil
		}
	}
	if _, err := store.AppendEventUnderLock(lock, handoffID, digest, sessionmove.HandoffEvent{
		Phase: sessionmove.PhaseFailed, At: at, Diagnostic: diagnostic,
	}); err != nil {
		return fmt.Errorf("record missing failed successor attempt phase: %w", err)
	}
	return nil
}

func stateHasPhase(state sessionmove.State, phase sessionmove.Phase) bool {
	for _, event := range state.Events {
		if event.Phase == phase {
			return true
		}
	}
	return false
}

func receiveFailureDiagnostic(err error) string {
	diagnostic := "target receive failed; inspect private local diagnostics, correct the target condition, and retry the identical handoff"
	var attemptFailure *sessionlaunch.AttemptFailureError
	if errors.As(err, &attemptFailure) {
		diagnostic = "target successor launch attempt failed after release; inspect private launcher evidence and retry the identical handoff"
	}
	if len(diagnostic) > maxFailureDiagnosticBytes {
		diagnostic = diagnostic[:maxFailureDiagnosticBytes]
	}
	return diagnostic
}
