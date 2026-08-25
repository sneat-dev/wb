// Package sessionreceive owns target-side request admission and phase fencing.
// It deliberately stops at a verified pinned worktree: successor startup,
// receipts, and source-custody transfer belong to later tasks.
package sessionreceive

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/worktrees"
)

const maxFailureDiagnosticBytes = 1024

// Options supplies exact courier bytes plus operator-owned target identity.
// LocalMachine must come from validated local configuration, never the request
// or a public receive flag.
type Options struct {
	Store           sessionmove.Store
	ProjectsRoot    string
	LocalMachine    string
	RawRequest      []byte
	Now             func() time.Time
	ReceiveWorktree func(context.Context, worktrees.SessionReceiveOptions) (worktrees.SessionReceiveResult, error)
}

// Result reports target progress without manufacturing a successor receipt.
type Result struct {
	Request  sessionmove.Request             `json:"request"`
	Digest   sessionmove.Digest              `json:"request_digest"`
	Phase    sessionmove.Phase               `json:"phase"`
	Worktree *worktrees.SessionReceiveResult `json:"worktree,omitempty"`
	Receipt  *sessionmove.Receipt            `json:"receipt,omitempty"`
	Replay   bool                            `json:"replay"`
}

// Receive admits exact request bytes, serializes execution by handoff ID, and
// advances only through received/worktree_ready. A failure is append-only and
// retryable; it never starts a process or changes predecessor custody.
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
	result.Replay = result.Replay || hadReceived || hadWorktreeReady
	now := time.Now().UTC
	if options.Now != nil {
		now = options.Now
	}
	if !hadReceived {
		if _, err := options.Store.AppendEvent(request.HandoffID, digest, sessionmove.HandoffEvent{Phase: sessionmove.PhaseReceived, At: now()}); err != nil {
			return Result{}, fmt.Errorf("record target received phase: %w", err)
		}
	}
	receiveWorktree := options.ReceiveWorktree
	if receiveWorktree == nil {
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
	result.Phase = sessionmove.PhaseWorktreeReady
	result.Worktree = &worktree
	return result, nil
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
