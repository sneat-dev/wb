// Package sessionreceive owns target-side request admission, pinned-worktree
// recovery, and successor-start phase fencing. Receipts and source-custody
// transfer remain gated to the later receipt task.
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
}

// Result reports target progress without manufacturing a successor receipt.
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
// advances idempotently through successor_started. A failure is append-only
// and retryable; predecessor custody remains unchanged until a later receipt.
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
	if admission.Receipt != nil {
		receipt := *admission.Receipt
		result.Phase, result.Receipt, result.Replay = sessionmove.PhaseCompleted, &receipt, true
		return result, nil
	}

	lock, err := options.Store.AcquireExecutionLock(ctx, request.HandoffID, digest)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = lock.Close() }()
	// Re-admit under the fence: another process may have completed and saved a
	// receipt while this caller waited.
	admission, err = options.Store.Admit(options.RawRequest, digest)
	if err != nil {
		return Result{}, err
	}
	if admission.Receipt != nil {
		receipt := *admission.Receipt
		result.Phase, result.Receipt, result.Replay = sessionmove.PhaseCompleted, &receipt, true
		return result, nil
	}
	state, err := options.Store.Load(request.HandoffID)
	if err != nil {
		return Result{}, err
	}
	hadReceived := stateHasPhase(state, sessionmove.PhaseReceived)
	hadWorktreeReady := stateHasPhase(state, sessionmove.PhaseWorktreeReady)
	hadSuccessorStarted := stateHasPhase(state, sessionmove.PhaseSuccessorStarted)
	result.Replay = result.Replay || hadReceived || hadWorktreeReady || hadSuccessorStarted
	now := time.Now().UTC
	if options.Now != nil {
		now = options.Now
	}
	if !hadReceived {
		if _, err := options.Store.AppendEvent(request.HandoffID, digest, sessionmove.HandoffEvent{Phase: sessionmove.PhaseReceived, At: now()}); err != nil {
			return Result{}, fmt.Errorf("record target received phase: %w", err)
		}
	}
	launchOptions := sessionlaunch.Options{Store: options.Store, ProjectsRoot: options.ProjectsRoot,
		Request: request, RequestDigest: digest, PinnedCommit: request.BundleCommit, ExecutionLock: lock,
		BeforeRelease: options.BeforeRelease}
	expectedWorktree, err := worktrees.SessionReceiveWorktreePath(options.ProjectsRoot, request)
	if err != nil {
		return Result{}, fmt.Errorf("derive deterministic target worktree: %w", err)
	}
	launchOptions.WorktreeDir = expectedWorktree
	if hadSuccessorStarted {
		inspect := options.InspectSuccessor
		if inspect == nil {
			inspect = sessionlaunch.Inspect
		}
		successor, inspectErr := inspect(ctx, launchOptions)
		if inspectErr != nil {
			return Result{}, inspectErr
		}
		result.Phase, result.Successor, result.Replay = sessionmove.PhaseSuccessorStarted, &successor, true
		return result, nil
	}
	if hadWorktreeReady {
		inspect := options.InspectSuccessor
		if inspect == nil {
			inspect = sessionlaunch.Inspect
		}
		successor, inspectErr := inspect(ctx, launchOptions)
		if inspectErr == nil {
			if _, err := options.Store.AppendEvent(request.HandoffID, digest, sessionmove.HandoffEvent{Phase: sessionmove.PhaseSuccessorStarted, At: now()}); err != nil {
				return Result{}, fmt.Errorf("recover missing target successor-started phase: %w", err)
			}
			result.Phase, result.Successor, result.Replay = sessionmove.PhaseSuccessorStarted, &successor, true
			return result, nil
		}
		if errors.Is(inspectErr, sessionlaunch.ErrRetryableLaunch) {
			// Inspect has already proved an exact immutable terminal launcher
			// failure, dead PID, released fence, and absent tmux session. Route
			// only that typed outcome back through Start under the retained
			// execution lock. Start rechecks every replacement gate; Git must
			// not be consulted because the failed harness may have changed the
			// already-accepted worktree after release.
			successor, startErr := startSuccessor(ctx, options, launchOptions)
			if startErr != nil {
				if err := appendReceiveFailure(options.Store, request.HandoffID, digest, now(), startErr); err != nil {
					return Result{}, err
				}
				return Result{}, startErr
			}
			if _, err := options.Store.AppendEvent(request.HandoffID, digest, sessionmove.HandoffEvent{Phase: sessionmove.PhaseSuccessorStarted, At: now()}); err != nil {
				return Result{}, fmt.Errorf("record target successor-started phase after exact launcher retry: %w", err)
			}
			result.Phase, result.Successor, result.Replay = sessionmove.PhaseSuccessorStarted, &successor, true
			return result, nil
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
		if _, eventErr := options.Store.AppendEvent(request.HandoffID, digest, sessionmove.HandoffEvent{
			Phase: sessionmove.PhaseFailed, At: now(), Diagnostic: diagnostic,
		}); eventErr != nil {
			return Result{}, fmt.Errorf("%w; record failed target receive phase: %v", receiveErr, eventErr)
		}
		return Result{}, receiveErr
	}
	if !hadWorktreeReady {
		if _, err := options.Store.AppendEvent(request.HandoffID, digest, sessionmove.HandoffEvent{Phase: sessionmove.PhaseWorktreeReady, At: now()}); err != nil {
			return Result{}, fmt.Errorf("record target worktree-ready phase: %w", err)
		}
	}
	if worktree.WorktreeDir != expectedWorktree {
		return Result{}, fmt.Errorf("received worktree %s does not match deterministic target path %s", worktree.WorktreeDir, expectedWorktree)
	}
	successor, startErr := startSuccessor(ctx, options, launchOptions)
	if startErr != nil {
		if err := appendReceiveFailure(options.Store, request.HandoffID, digest, now(), startErr); err != nil {
			return Result{}, err
		}
		return Result{}, startErr
	}
	if _, err := options.Store.AppendEvent(request.HandoffID, digest, sessionmove.HandoffEvent{Phase: sessionmove.PhaseSuccessorStarted, At: now()}); err != nil {
		return Result{}, fmt.Errorf("record target successor-started phase: %w", err)
	}
	result.Phase = sessionmove.PhaseSuccessorStarted
	result.Worktree = &worktree
	result.Successor = &successor
	return result, nil
}

func startSuccessor(ctx context.Context, options Options, launchOptions sessionlaunch.Options) (sessionlaunch.Result, error) {
	start := options.StartSuccessor
	if start == nil {
		start = sessionlaunch.Start
	}
	return start(ctx, launchOptions)
}

func appendReceiveFailure(store sessionmove.Store, handoffID string, digest sessionmove.Digest, at time.Time, failure error) error {
	diagnostic := receiveFailureDiagnostic(failure)
	if _, eventErr := store.AppendEvent(handoffID, digest, sessionmove.HandoffEvent{
		Phase: sessionmove.PhaseFailed, At: at, Diagnostic: diagnostic,
	}); eventErr != nil {
		return fmt.Errorf("%w; record failed successor start phase: %v", failure, eventErr)
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
	diagnostic := "target receive failed; correct the reported Git/worktree condition and retry the identical handoff: " + err.Error()
	diagnostic = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return ' '
		}
		return r
	}, diagnostic)
	if len(diagnostic) > maxFailureDiagnosticBytes {
		diagnostic = diagnostic[:maxFailureDiagnosticBytes]
	}
	return diagnostic
}
