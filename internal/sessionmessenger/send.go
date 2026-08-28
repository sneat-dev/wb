// Package sessionmessenger owns source-side durable session-message delivery.
package sessionmessenger

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessioncourier"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// Hooks are test-only crash-boundary probes. Production callers leave them
// empty; they never change the durable protocol.
type Hooks struct {
	BeforeCourier func(sessionmove.MessageState) error
}

// WorkLogRecord is the exact source-side evidence recorded only after the
// target has acknowledged durable recording plus a successful tmux paste.
type WorkLogRecord struct {
	ProjectsRoot   string
	Request        sessionmove.Request
	RequestDigest  sessionmove.Digest
	HandoffReceipt sessionmove.Receipt
	Address        sessionmove.SuccessorAddress
	SourceSession  session.Record
	Message        sessionmove.Message
	Record         sessionmove.MessageRecord
	MessageReceipt sessionmove.MessageReceipt
}

// Options selects a successor solely by its durable WB session address. A
// fresh caller supplies MessageID; a retry supplies only ResumeMessageID and
// therefore cannot accidentally construct different bytes.
type Options struct {
	Store             sessionmove.Store
	ProjectsRoot      string
	TargetWBSessionID string
	SourceSession     session.Record
	Kind              sessionmove.MessageKind
	Body              string
	MessageID         string
	ResumeMessageID   string
	Now               func() time.Time
	NewDeliverer      func(sessionmove.SuccessorAddress, sessioncourier.MessageSynchestraOptions) (sessioncourier.MessageDeliverer, error)
	RecordSent        func(WorkLogRecord) error
	Hooks             Hooks
}

// Result identifies the exact durable outbox message and its target
// acknowledgement. Receipt means recorded+pasted, never agent-processed.
type Result struct {
	Message sessionmove.Message          `json:"message"`
	Digest  sessionmove.Digest           `json:"message_digest"`
	Receipt sessionmove.MessageReceipt   `json:"receipt"`
	Address sessionmove.SuccessorAddress `json:"successor_address"`
	Replay  bool                         `json:"replay"`
}

// DeliveryError preserves the caller-owned durable identity needed to retry
// an ambiguous transport or a post-delivery source-recording failure.
type DeliveryError struct {
	MessageID         string
	TargetWBSessionID string
	Cause             error
}

func (err *DeliveryError) Error() string {
	return fmt.Sprintf("session message %s to %s is durably resumable after an ambiguous or incomplete delivery: %v",
		err.MessageID, err.TargetWBSessionID, err.Cause)
}

func (err *DeliveryError) Unwrap() error { return err.Cause }

// Send persists exact message bytes before courier use, then records only an
// acknowledgement that those bytes were recorded and pasted at the verified
// successor pane. It holds the handoff execution fence across one courier
// attempt so concurrent local retries cannot create duplicate attempts.
func Send(ctx context.Context, options Options) (Result, error) {
	var result Result
	if ctx == nil {
		return result, fmt.Errorf("send session message: context is required")
	}
	targetID := strings.TrimSpace(options.TargetWBSessionID)
	if targetID == "" {
		return result, fmt.Errorf("target WB session ID is required")
	}
	address, err := options.Store.LoadSuccessorAddress(targetID)
	if err != nil {
		return result, fmt.Errorf("resolve durable successor address: %w", err)
	}
	lock, err := options.Store.AcquireExecutionLock(ctx, address.HandoffID, address.RequestDigest)
	if err != nil {
		return result, err
	}
	defer func() { _ = lock.Close() }()

	lockedAddress, err := options.Store.LoadSuccessorAddressUnderLock(lock, address.HandoffID, address.RequestDigest)
	if err != nil {
		return result, err
	}
	if !reflect.DeepEqual(address, lockedAddress) || lockedAddress.SuccessorWBSessionID != targetID {
		return result, fmt.Errorf("%w: successor address changed while acquiring exact handoff authority", sessionmove.ErrHandoffConflict)
	}
	address = lockedAddress
	state, err := options.Store.LoadUnderLock(lock, address.HandoffID, address.RequestDigest)
	if err != nil {
		return result, err
	}
	if state.Receipt == nil {
		return result, fmt.Errorf("successor address has no durable completed handoff receipt")
	}
	if err := validateSource(options.SourceSession, state.Request, address); err != nil {
		return result, err
	}

	messageState, err := admitOrResume(options, lock, address, state.Request)
	if err != nil {
		return result, err
	}
	result.Message, result.Digest, result.Address = messageState.Message, messageState.Digest, address
	result.Replay = messageState.Replay
	if messageState.Receipt != nil {
		result.Receipt = *messageState.Receipt
		result.Replay = true
		if err := recordSent(options, state.Request, address.RequestDigest, *state.Receipt, address, messageState, result.Receipt); err != nil {
			return result, deliveryError(result, err)
		}
		return result, nil
	}
	if options.Hooks.BeforeCourier != nil {
		if err := options.Hooks.BeforeCourier(messageState); err != nil {
			return result, deliveryError(result, err)
		}
	}

	synchestraOptions, err := messageSynchestraOptions(options.Store, lock, address, messageState)
	if err != nil {
		return result, deliveryError(result, err)
	}
	deliverer, err := newDeliverer(options, address, synchestraOptions)
	if err != nil {
		return result, deliveryError(result, err)
	}
	receipt, err := deliverer.DeliverMessage(ctx, messageState.Raw)
	if err != nil {
		return result, deliveryError(result, err)
	}
	if err := sessionmove.ValidateMessageReceipt(receipt, messageState.Message, messageState.Digest, address.TmuxName, address.PID); err != nil {
		return result, deliveryError(result, fmt.Errorf("validate target acknowledgement: %w", err))
	}
	receipt, receiptReplay, err := options.Store.SaveOutgoingMessageReceiptUnderLock(lock, address.HandoffID, address.RequestDigest, receipt)
	if err != nil {
		return result, deliveryError(result, err)
	}
	result.Receipt = receipt
	result.Replay = result.Replay || receiptReplay
	messageState.Receipt = &receipt
	if err := recordSent(options, state.Request, address.RequestDigest, *state.Receipt, address, messageState, receipt); err != nil {
		return result, deliveryError(result, err)
	}
	return result, nil
}

func admitOrResume(options Options, lock *sessionmove.ExecutionLock, address sessionmove.SuccessorAddress, request sessionmove.Request) (sessionmove.MessageState, error) {
	resumeID := strings.TrimSpace(options.ResumeMessageID)
	if resumeID != "" {
		if strings.TrimSpace(options.MessageID) != "" || options.Body != "" {
			return sessionmove.MessageState{}, fmt.Errorf("resume accepts only the durable message ID; message ID and body must not be supplied")
		}
		state, err := options.Store.ResumeOutgoingMessageUnderLock(lock, address.HandoffID, address.RequestDigest, resumeID)
		if err != nil {
			return sessionmove.MessageState{}, err
		}
		if state.Message.Kind != options.Kind {
			return sessionmove.MessageState{}, fmt.Errorf("%w: durable message %s has kind %q, not %q",
				sessionmove.ErrHandoffConflict, resumeID, state.Message.Kind, options.Kind)
		}
		state.Replay = true
		return state, nil
	}
	if strings.TrimSpace(options.MessageID) == "" {
		return sessionmove.MessageState{}, fmt.Errorf("caller-owned message ID is required before courier use")
	}
	if options.Now == nil {
		return sessionmove.MessageState{}, fmt.Errorf("session message clock is required")
	}
	message := sessionmove.Message{
		SchemaVersion: sessionmove.MessageSchemaVersion,
		MessageID:     strings.TrimSpace(options.MessageID), HandoffID: request.HandoffID,
		SenderWBSessionID: request.PredecessorWBSessionID, RecipientWBSessionID: request.SuccessorWBSessionID,
		ReplyToWBSessionID: request.PredecessorWBSessionID, Kind: options.Kind, Body: options.Body,
		SentAt: options.Now().UTC(),
	}
	raw, err := sessionmove.EncodeMessage(message)
	if err != nil {
		return sessionmove.MessageState{}, err
	}
	return options.Store.AdmitOutgoingMessageUnderLock(lock, address.HandoffID, address.RequestDigest, raw, message.SentAt)
}

func messageSynchestraOptions(store sessionmove.Store, lock *sessionmove.ExecutionLock, address sessionmove.SuccessorAddress, message sessionmove.MessageState) (sessioncourier.MessageSynchestraOptions, error) {
	if address.Route.Courier != sessionmove.CourierSynchestra {
		return sessioncourier.MessageSynchestraOptions{}, nil
	}
	options := sessioncourier.MessageSynchestraOptions{RequestDigest: address.RequestDigest}
	dispatch, err := store.LoadOutgoingMessageSynchestraDispatchUnderLock(lock, address.HandoffID, address.RequestDigest, message.Message.MessageID)
	if err == nil {
		options.Dispatch = &dispatch
		return options, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return sessioncourier.MessageSynchestraOptions{}, err
	}
	options.SaveDispatch = func(identity sessionmove.MessageSynchestraDispatch) error {
		_, _, saveErr := store.SaveOutgoingMessageSynchestraDispatchUnderLock(lock, address.HandoffID, address.RequestDigest, identity)
		return saveErr
	}
	return options, nil
}

func newDeliverer(options Options, address sessionmove.SuccessorAddress, synchestra sessioncourier.MessageSynchestraOptions) (sessioncourier.MessageDeliverer, error) {
	if options.NewDeliverer != nil {
		return options.NewDeliverer(address, synchestra)
	}
	switch address.Route.Courier {
	case sessionmove.CourierSSH:
		if address.Route.SSH == nil {
			return nil, fmt.Errorf("durable SSH successor address is incomplete")
		}
		return sessioncourier.NewSSHMessageDeliverer(*address.Route.SSH)
	case sessionmove.CourierSynchestra:
		if address.Route.Synchestra == nil {
			return nil, fmt.Errorf("durable Synchestra successor address is incomplete")
		}
		return sessioncourier.NewSynchestraMessageDeliverer(*address.Route.Synchestra, synchestra)
	default:
		return nil, fmt.Errorf("durable successor courier %q is unsupported", address.Route.Courier)
	}
}

func recordSent(options Options, request sessionmove.Request, digest sessionmove.Digest, handoffReceipt sessionmove.Receipt,
	address sessionmove.SuccessorAddress, message sessionmove.MessageState, receipt sessionmove.MessageReceipt,
) error {
	record := WorkLogRecord{
		ProjectsRoot: options.ProjectsRoot, Request: request, RequestDigest: digest, HandoffReceipt: handoffReceipt,
		Address: address, SourceSession: options.SourceSession, Message: message.Message, Record: message.Record, MessageReceipt: receipt,
	}
	if options.RecordSent != nil {
		return options.RecordSent(record)
	}
	_, err := worktrees.RecordExternalSourceMessageSent(worktrees.ExternalSourceMessageOptions{
		ProjectsRoot: record.ProjectsRoot, Request: record.Request, RequestDigest: record.RequestDigest,
		Receipt: record.HandoffReceipt, SourceSession: record.SourceSession, Message: record.Message,
		Record: record.Record, MessageReceipt: record.MessageReceipt,
	})
	return err
}

func validateSource(source session.Record, request sessionmove.Request, address sessionmove.SuccessorAddress) error {
	nativeID := strings.TrimSpace(source.NativeHarnessID)
	if nativeID == "" {
		nativeID = strings.TrimSpace(source.AgentID)
	}
	if source.PID <= 0 || source.StartedAt.IsZero() || source.WBSessionID != request.PredecessorWBSessionID ||
		source.Machine != request.SourceMachine || source.Runtime != request.SourceRuntime || source.Model != request.SourceModel ||
		nativeID != request.SourceNativeHarnessID || address.PredecessorWBSessionID != source.WBSessionID {
		return fmt.Errorf("live source session does not match the recorded predecessor identity")
	}
	return nil
}

func deliveryError(result Result, cause error) error {
	return &DeliveryError{MessageID: result.Message.MessageID, TargetWBSessionID: result.Address.SuccessorWBSessionID, Cause: cause}
}
