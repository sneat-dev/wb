// Package sessionmessage owns durable follow-up delivery to a completed
// session-move successor. Couriers transport canonical bytes; this package
// alone admits the inbox, verifies the live target, and attempts one paste.
package sessionmessage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionterminal"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// ErrMessagePasteAmbiguous means a durable intent exists but no durable
// pasted receipt does. WB will not automatically paste the message again.
var ErrMessagePasteAmbiguous = errors.New("session message paste outcome is ambiguous")

// Pane is the exact single pane returned by one bounded list-panes query.
type Pane struct {
	ID              string
	PID             int
	Count           int
	Terminal        sessionterminal.Identity
	NativeHarnessID string
}

type terminal interface {
	Inspect(context.Context, sessionmove.Receipt, session.Record) (Pane, error)
	Submit(context.Context, Pane, []byte) error
}

// WorkLogRecord is the exact target-side evidence passed to the Work Log
// adapter. Production omits Message.Body from the event it writes.
type WorkLogRecord struct {
	ProjectsRoot  string
	Request       sessionmove.Request
	RequestDigest sessionmove.Digest
	Receipt       sessionmove.Receipt
	Message       sessionmove.Message
	Record        sessionmove.MessageRecord
}

type Hooks struct {
	AfterPaste func() error
}

type Options struct {
	Store          sessionmove.Store
	ProjectsRoot   string
	LocalMachine   string
	SessionDir     string
	RawMessage     []byte
	Terminal       terminal
	LookupSession  func(string, int) (session.Record, bool, error)
	RecordReceived func(WorkLogRecord) error
	// WithRecipient is a test seam; production always serializes against registry transitions.
	WithRecipient func(context.Context, string, session.Record, func() error) error
	Now           func() time.Time
	Hooks         Hooks
}

type Result struct {
	Message sessionmove.Message        `json:"message"`
	Digest  sessionmove.Digest         `json:"message_digest"`
	Receipt sessionmove.MessageReceipt `json:"receipt"`
	Replay  bool                       `json:"replay"`
}

// Receive admits exact canonical bytes before inspecting target state. The
// typed canonical JSON is intentionally the agent-facing prompt contract: its
// kind and reply_to fields make even an empty-body request_handoff actionable.
// Once Receive publishes a paste intent it makes at most one automatic paste
// attempt.
func Receive(ctx context.Context, options Options) (Result, error) {
	var result Result
	if ctx == nil {
		return result, fmt.Errorf("receive session message: context is required")
	}
	message, err := sessionmove.DecodeMessage(options.RawMessage)
	if err != nil {
		return result, err
	}
	canonical, err := sessionmove.EncodeMessage(message)
	if err != nil {
		return result, err
	}
	if !bytes.Equal(canonical, options.RawMessage) {
		return result, fmt.Errorf("session message must use WB's canonical JSON encoding")
	}
	request, requestDigest, _, err := options.Store.RequestBytes(message.HandoffID)
	if err != nil {
		return result, fmt.Errorf("load message handoff authority: %w", err)
	}
	if strings.TrimSpace(options.LocalMachine) == "" || options.LocalMachine != request.TargetMachine {
		return result, fmt.Errorf("session message targets machine %q, local machine is %q", request.TargetMachine, options.LocalMachine)
	}
	if err := sessionmove.ValidateMessageForRequest(message, request); err != nil {
		return result, err
	}
	lock, err := options.Store.AcquireExecutionLock(ctx, request.HandoffID, requestDigest)
	if err != nil {
		return result, err
	}
	defer func() { _ = lock.Close() }()
	state, err := options.Store.LoadUnderLock(lock, request.HandoffID, requestDigest)
	if err != nil {
		return result, err
	}
	if state.Receipt == nil {
		return result, fmt.Errorf("session message requires a durable completed successor receipt")
	}
	handoffReceipt := *state.Receipt
	// LoadUnderLock already validates this receipt against the same request digest.
	// The immutable successor index and courier route are source-side
	// transport authority. They cannot be independently loaded on the target.
	// Target authority is instead the exact admitted request plus this durable
	// completed receipt, corroborated below against the live registered WB
	// session and the exact Herdr socket/workspace/pane/native session/PID.
	recordedAt := now(options)
	inbox, err := options.Store.AdmitIncomingMessageUnderLock(lock, request.HandoffID, requestDigest, options.RawMessage, recordedAt)
	if err != nil {
		return result, err
	}
	result.Message, result.Digest = inbox.Message, inbox.Digest
	if inbox.Receipt != nil {
		result.Receipt, result.Replay = *inbox.Receipt, true
		return result, nil
	}
	if err := sessionmove.RequireActionableMessage(message); err != nil {
		return result, err
	}
	if inbox.Intent != nil {
		return result, fmt.Errorf("%w for message %s; inspect the verified target Herdr pane and durable inbox before any manual recovery",
			ErrMessagePasteAmbiguous, message.MessageID)
	}

	lookup := options.LookupSession
	if lookup == nil {
		lookup = func(directory string, pid int) (session.Record, bool, error) {
			return session.LookupSessionIdentity(directory, pid, handoffReceipt.SuccessorWBSessionID, handoffReceipt.NativeHarnessID)
		}
	}
	record, live, err := lookup(options.SessionDir, handoffReceipt.PID)
	if err != nil {
		return result, fmt.Errorf("corroborate registered message recipient: %w", err)
	}
	if !live || !matchesRecipient(record, request, handoffReceipt) {
		return result, fmt.Errorf("registered WB successor does not match the live completed handoff recipient")
	}
	if err := handoffReceipt.Terminal.Validate(); err != nil {
		return result, fmt.Errorf("completed legacy terminal is read-only; create a new Herdr successor before sending messages: %w", err)
	}
	target := options.Terminal
	if target == nil {
		target, err = newOSHerdr(ctx, handoffReceipt, record)
		if err != nil {
			return result, err
		}
	}
	pane, err := target.Inspect(ctx, handoffReceipt, record)
	if err != nil {
		return result, err
	}
	if pane.Count != 1 || pane.Terminal != handoffReceipt.Terminal || pane.ID != handoffReceipt.Terminal.PaneID || pane.PID != handoffReceipt.PID || pane.NativeHarnessID != handoffReceipt.NativeHarnessID {
		return result, fmt.Errorf("Herdr successor does not match the exact recorded socket/workspace/pane/native session and PID")
	}
	recordReceived := options.RecordReceived
	if recordReceived == nil {
		recordReceived = recordReceivedWorkLog
	}
	workLogRecord := WorkLogRecord{
		ProjectsRoot: options.ProjectsRoot, Request: request, RequestDigest: requestDigest,
		Receipt: handoffReceipt, Message: message, Record: inbox.Record,
	}
	if err := recordReceived(workLogRecord); err != nil {
		return result, fmt.Errorf("record target session message receipt without claiming agent processing: %w", err)
	}
	intent := sessionmove.MessagePasteIntent{
		SchemaVersion: sessionmove.MessagePasteIntentSchemaVersion,
		MessageID:     message.MessageID, MessageDigest: inbox.Digest, HandoffID: request.HandoffID,
		RecipientWBSessionID: message.RecipientWBSessionID, Terminal: pane.Terminal,
		PaneID: pane.ID, PID: pane.PID, IntendedAt: now(options),
	}
	if _, replay, err := options.Store.SaveIncomingPasteIntentUnderLock(lock, request.HandoffID, requestDigest, intent); err != nil {
		return result, err
	} else if replay {
		return result, fmt.Errorf("%w for message %s; a concurrent receiver already owns the sole automatic paste attempt",
			ErrMessagePasteAmbiguous, message.MessageID)
	}
	withRecipient := options.WithRecipient
	if withRecipient == nil {
		withRecipient = session.WithActiveRecipient
	}
	// No Work Log or inbox writes occur under the PID barrier. A replacement
	// after the initial observation must lose before the sole submission; an
	// ambiguous refusal retains this intent and never automatically retries.
	if err := withRecipient(ctx, options.SessionDir, record, func() error {
		currentPane, err := target.Inspect(ctx, handoffReceipt, record)
		if err != nil {
			return err
		}
		if currentPane != pane {
			return fmt.Errorf("Herdr recipient changed after durable paste intent")
		}
		return target.Submit(ctx, currentPane, options.RawMessage)
	}); err != nil {
		return result, ambiguous(message.MessageID, err)
	}
	if options.Hooks.AfterPaste != nil {
		if err := options.Hooks.AfterPaste(); err != nil {
			return result, ambiguous(message.MessageID, err)
		}
	}
	receipt := sessionmove.MessageReceipt{
		SchemaVersion: sessionmove.MessageReceiptSchemaVersion,
		MessageID:     message.MessageID, MessageDigest: inbox.Digest, HandoffID: message.HandoffID,
		SenderWBSessionID: message.SenderWBSessionID, RecipientWBSessionID: message.RecipientWBSessionID,
		ReplyToWBSessionID: message.ReplyToWBSessionID, Kind: message.Kind,
		Terminal: pane.Terminal, PaneID: pane.ID, PID: pane.PID,
		RecordedAt: inbox.Record.RecordedAt, PastedAt: now(options),
	}
	durable, _, err := options.Store.SaveIncomingMessageReceiptUnderLock(lock, request.HandoffID, requestDigest, receipt)
	if err != nil {
		return result, ambiguous(message.MessageID, err)
	}
	result.Receipt = durable
	return result, nil
}

func matchesRecipient(record session.Record, request sessionmove.Request, receipt sessionmove.Receipt) bool {
	nativeID := strings.TrimSpace(record.NativeHarnessID)
	if nativeID == "" {
		nativeID = strings.TrimSpace(record.AgentID)
	}
	return record.PID == receipt.PID && record.WBSessionID == receipt.SuccessorWBSessionID &&
		record.Machine == receipt.TargetMachine && record.Runtime == receipt.Runtime && receipt.Terminal.MatchesModel(receipt.Model, strings.TrimSpace(record.Model)) &&
		nativeID == receipt.NativeHarnessID && record.TmuxName == receipt.TmuxName && record.Terminal == receipt.Terminal &&
		record.PredecessorWBSessionID == request.PredecessorWBSessionID && record.HandoffID == request.HandoffID &&
		record.StartedAt.Equal(receipt.StartedAt)
}

func recordReceivedWorkLog(record WorkLogRecord) error {
	_, err := worktrees.RecordExternalTargetMessageReceived(worktrees.ExternalTargetMessageOptions{
		ProjectsRoot: record.ProjectsRoot, Request: record.Request, RequestDigest: record.RequestDigest,
		Receipt: record.Receipt, Message: record.Message, Record: record.Record,
	})
	return err
}

func now(options Options) time.Time {
	if options.Now != nil {
		return options.Now().UTC()
	}
	return time.Now().UTC()
}

func ambiguous(messageID string, cause error) error {
	return fmt.Errorf("message %s has a durable paste intent but no receipt; automatic replay is disabled: %w",
		messageID, errors.Join(ErrMessagePasteAmbiguous, cause))
}
