// Package sessioncustody owns source-side receipt acknowledgement. Couriers
// deliver WB protocol values; this package alone seals predecessor custody.
package sessioncustody

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// Hooks are deterministic failure-injection seams at durable boundaries.
// Production callers leave them empty.
type Hooks struct {
	AfterLock      func() error
	AfterReceipt   func() error
	AfterAddress   func() error
	AfterSeal      func() error
	AfterCompleted func() error
}

// Options supplies an already delivered receipt and the exact source
// aggregate. A zero Receipt is accepted only when the exact local receipt was
// persisted by an earlier ambiguous attempt; this package never calls a
// courier.
type Options struct {
	Store             sessionmove.Store
	ProjectsRoot      string
	Request           sessionmove.Request
	RequestDigest     sessionmove.Digest
	Receipt           sessionmove.Receipt
	SourceSession     session.Record
	Now               func() time.Time
	EnsureSourceOffer func(worktrees.ExternalSourceOfferOptions) (worktrees.ExternalSourceOfferResult, error)
	SealWorkLog       func(worktrees.ExternalSourceSealOptions) (worktrees.ExternalSourceSealResult, error)
	Hooks             Hooks
}

// Result reports converged source custody and the stable successor address.
type Result struct {
	Receipt sessionmove.Receipt                `json:"receipt"`
	Address sessionmove.SuccessorAddress       `json:"successor_address"`
	WorkLog worktrees.ExternalSourceSealResult `json:"work_log"`
	Replay  bool                               `json:"replay"`
}

// Acknowledge persists proof before sealing source custody. Every step is
// replayed in the same order so a crash repairs only missing downstream
// evidence: receipt -> successor address -> Work Log terminal -> completed.
func Acknowledge(ctx context.Context, options Options) (Result, error) {
	var result Result
	if ctx == nil {
		return result, fmt.Errorf("acknowledge session custody: context is required")
	}
	if err := validateOptions(options); err != nil {
		return result, err
	}
	storedRequest, storedDigest, raw, err := options.Store.RequestBytes(options.Request.HandoffID)
	if err != nil {
		return result, fmt.Errorf("load pre-admitted source handoff: %w", err)
	}
	if storedRequest != options.Request || storedDigest != options.RequestDigest {
		return result, fmt.Errorf("%w: source acknowledgement does not match pre-admitted exact request", sessionmove.ErrHandoffConflict)
	}
	lock, err := options.Store.AcquireExecutionLock(ctx, options.Request.HandoffID, options.RequestDigest)
	if err != nil {
		return result, err
	}
	defer func() { _ = lock.Close() }()
	if err := runHook("after source acknowledgement lock", options.Hooks.AfterLock); err != nil {
		return result, err
	}
	// Re-admit descriptor-relatively after waiting so a concurrent
	// acknowledgement's durable receipt is visible without consulting or
	// mutating a path that may have been swapped after lock acquisition.
	if _, err := options.Store.ReadmitUnderLock(lock, options.Request.HandoffID, options.RequestDigest, raw); err != nil {
		return result, fmt.Errorf("re-admit exact source handoff under acknowledgement fence: %w", err)
	}
	state, err := options.Store.LoadUnderLock(lock, options.Request.HandoffID, options.RequestDigest)
	if err != nil {
		return result, err
	}
	if state.Request != options.Request {
		return result, fmt.Errorf("%w: retained source request differs from acknowledgement", sessionmove.ErrHandoffConflict)
	}
	ensureSourceOffer := options.EnsureSourceOffer
	if ensureSourceOffer == nil {
		ensureSourceOffer = worktrees.EnsureExternalSourceOfferEvidence
	}
	if _, err := ensureSourceOffer(worktrees.ExternalSourceOfferOptions{
		Store: options.Store, ExecutionLock: lock,
		ProjectsRoot: options.ProjectsRoot, Request: options.Request, RequestDigest: options.RequestDigest,
		SourceSession: options.SourceSession,
	}); err != nil {
		return result, fmt.Errorf("ensure immutable source offer evidence before receipt processing: %w", err)
	}

	receipt := options.Receipt
	receiptReplay := false
	if state.Receipt != nil {
		receipt = *state.Receipt
		receiptReplay = true
		if receiptProvided(options.Receipt) {
			durableRaw, encodeErr := sessionmove.EncodeReceipt(receipt)
			if encodeErr != nil {
				return result, encodeErr
			}
			suppliedRaw, encodeErr := sessionmove.EncodeReceipt(options.Receipt)
			if encodeErr != nil {
				return result, encodeErr
			}
			if !bytes.Equal(durableRaw, suppliedRaw) {
				return result, fmt.Errorf("%w: supplied target receipt differs from exact local receipt", sessionmove.ErrHandoffConflict)
			}
		}
	} else {
		if !receiptProvided(receipt) {
			return result, fmt.Errorf("target completion receipt is required when no exact local receipt exists")
		}
		var saveErr error
		receipt, receiptReplay, saveErr = options.Store.SaveReceiptUnderLock(lock, options.Request.HandoffID, options.RequestDigest, receipt)
		if saveErr != nil {
			return result, saveErr
		}
	}
	if err := runHook("after receipt", options.Hooks.AfterReceipt); err != nil {
		return result, err
	}

	address, addressReplay, err := options.Store.SaveSuccessorAddressUnderLock(lock, options.Request.HandoffID, options.RequestDigest, receipt)
	if err != nil {
		return result, err
	}
	corroboratedAddress, err := options.Store.LoadSuccessorAddressUnderLock(lock, options.Request.HandoffID, options.RequestDigest)
	if err != nil {
		return result, fmt.Errorf("corroborate immutable successor address before source seal: %w", err)
	}
	if !reflect.DeepEqual(corroboratedAddress, address) {
		return result, fmt.Errorf("%w: published successor address changed before source seal", sessionmove.ErrHandoffConflict)
	}
	address = corroboratedAddress
	if err := runHook("after successor address", options.Hooks.AfterAddress); err != nil {
		return result, err
	}

	seal := options.SealWorkLog
	if seal == nil {
		seal = worktrees.SealExternalSessionWorkLog
	}
	workLog, err := seal(worktrees.ExternalSourceSealOptions{
		Store: options.Store, ExecutionLock: lock,
		ProjectsRoot: options.ProjectsRoot, Request: options.Request, RequestDigest: options.RequestDigest,
		Receipt: receipt, SourceSession: options.SourceSession,
	})
	if err != nil {
		return result, fmt.Errorf("seal source Work Log after exact target receipt: %w", err)
	}
	if workLog.SourceWorkLogReference != options.Request.WorkLogReference ||
		workLog.TargetWorkLogReference != receipt.TargetWorkLogReference || workLog.SealedAt.IsZero() {
		return result, fmt.Errorf("source Work Log seal result does not match receipt lineage")
	}
	if err := runHook("after source Work Log seal", options.Hooks.AfterSeal); err != nil {
		return result, err
	}

	state, err = options.Store.LoadUnderLock(lock, options.Request.HandoffID, options.RequestDigest)
	if err != nil {
		return result, err
	}
	completedReplay := hasPhase(state, sessionmove.PhaseCompleted)
	if !completedReplay {
		completedAt := workLog.SealedAt.UTC()
		if completedAt.IsZero() {
			completedAt = now(options).UTC()
		}
		if _, err := options.Store.AppendEventUnderLock(lock, options.Request.HandoffID, options.RequestDigest,
			sessionmove.HandoffEvent{Phase: sessionmove.PhaseCompleted, At: completedAt}); err != nil {
			return result, fmt.Errorf("record source completed phase: %w", err)
		}
	}
	if err := runHook("after source completed phase", options.Hooks.AfterCompleted); err != nil {
		return result, err
	}
	result.Receipt, result.Address, result.WorkLog = receipt, address, workLog
	result.Replay = receiptReplay || addressReplay || workLog.Replayed || completedReplay
	return result, nil
}

func validateOptions(options Options) error {
	if _, err := sessionmove.EncodeRequest(options.Request); err != nil {
		return err
	}
	if _, err := sessionmove.ExpectedTargetWorkLogReference(options.Request, options.RequestDigest); err != nil {
		return err
	}
	projectsRoot, err := filepath.Abs(options.ProjectsRoot)
	if err != nil || projectsRoot != options.ProjectsRoot || filepath.Clean(projectsRoot) != projectsRoot {
		return fmt.Errorf("projects root must be one clean absolute path")
	}
	if options.SourceSession.PID <= 0 || options.SourceSession.StartedAt.IsZero() ||
		options.SourceSession.WBSessionID != options.Request.PredecessorWBSessionID ||
		options.SourceSession.Machine != options.Request.SourceMachine ||
		options.SourceSession.Runtime != options.Request.SourceRuntime {
		return fmt.Errorf("source session does not match admitted predecessor identity")
	}
	if receiptProvided(options.Receipt) {
		if err := sessionmove.ValidateReceiptForRequest(options.Receipt, options.Request, options.RequestDigest); err != nil {
			return err
		}
	}
	return nil
}

func receiptProvided(receipt sessionmove.Receipt) bool { return receipt != (sessionmove.Receipt{}) }

func hasPhase(state sessionmove.State, phase sessionmove.Phase) bool {
	for _, event := range state.Events {
		if event.Phase == phase {
			return true
		}
	}
	return false
}

func runHook(boundary string, hook func() error) error {
	if hook == nil {
		return nil
	}
	if err := hook(); err != nil {
		return fmt.Errorf("%s: %w", boundary, err)
	}
	return nil
}

func now(options Options) time.Time {
	if options.Now != nil {
		return options.Now()
	}
	return time.Now().UTC()
}
