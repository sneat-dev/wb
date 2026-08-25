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
	"github.com/sneat-dev/wb/internal/worktrees"
)

// ErrMessagePasteAmbiguous means a durable intent exists but no durable
// pasted receipt does. WB will not automatically paste the message again.
var ErrMessagePasteAmbiguous = errors.New("session message paste outcome is ambiguous")

// Pane is the exact single pane returned by one bounded list-panes query.
type Pane struct {
	SessionName string
	ID          string
	PID         int
	Count       int
}

type tmux interface {
	Inspect(context.Context, string) (Pane, error)
	LoadBuffer(context.Context, string, []byte) error
	SaveBuffer(context.Context, string) ([]byte, error)
	PasteBuffer(context.Context, string, string) error
	DeleteBuffer(context.Context, string) error
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
	Tmux           tmux
	LookupSession  func(string, int) (session.Record, bool, error)
	RecordReceived func(WorkLogRecord) error
	Now            func() time.Time
	Hooks          Hooks
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
	if err := sessionmove.ValidateReceiptForRequest(handoffReceipt, request, requestDigest); err != nil {
		return result, err
	}
	// The immutable successor index and courier route are source-side
	// transport authority. They cannot be independently loaded on the target.
	// Target authority is instead the exact admitted request plus this durable
	// completed receipt, corroborated below against the live registered WB
	// session and the exact single tmux pane/PID.
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
	if inbox.Intent != nil {
		return result, fmt.Errorf("%w for message %s; inspect the verified target tmux pane and durable inbox before any manual recovery",
			ErrMessagePasteAmbiguous, message.MessageID)
	}

	lookup := options.LookupSession
	if lookup == nil {
		lookup = session.LookupExact
	}
	record, live, err := lookup(options.SessionDir, handoffReceipt.PID)
	if err != nil {
		return result, fmt.Errorf("corroborate registered message recipient: %w", err)
	}
	if !live || !matchesRecipient(record, request, handoffReceipt) {
		return result, fmt.Errorf("registered WB successor does not match the live completed handoff recipient")
	}
	tmuxClient := options.Tmux
	if tmuxClient == nil {
		tmuxClient, err = newOSTmux()
		if err != nil {
			return result, err
		}
	}
	pane, err := tmuxClient.Inspect(ctx, handoffReceipt.TmuxName)
	if err != nil {
		return result, err
	}
	if pane.Count != 1 || pane.SessionName != handoffReceipt.TmuxName || pane.PID != handoffReceipt.PID || pane.ID == "" {
		return result, fmt.Errorf("tmux successor does not contain the exact single recorded pane and PID")
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
		RecipientWBSessionID: message.RecipientWBSessionID, TmuxName: pane.SessionName,
		PaneID: pane.ID, PID: pane.PID, IntendedAt: now(options),
	}
	if _, replay, err := options.Store.SaveIncomingPasteIntentUnderLock(lock, request.HandoffID, requestDigest, intent); err != nil {
		return result, err
	} else if replay {
		return result, fmt.Errorf("%w for message %s; a concurrent receiver already owns the sole automatic paste attempt",
			ErrMessagePasteAmbiguous, message.MessageID)
	}
	bufferName := "wb-message-" + message.MessageID
	if err := pasteExact(ctx, tmuxClient, bufferName, pane.ID, options.RawMessage); err != nil {
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
		TmuxName: pane.SessionName, PaneID: pane.ID, PID: pane.PID,
		RecordedAt: inbox.Record.RecordedAt, PastedAt: now(options),
	}
	durable, _, err := options.Store.SaveIncomingMessageReceiptUnderLock(lock, request.HandoffID, requestDigest, receipt)
	if err != nil {
		return result, ambiguous(message.MessageID, err)
	}
	result.Receipt = durable
	return result, nil
}

func pasteExact(ctx context.Context, client tmux, bufferName, paneID string, raw []byte) error {
	if err := client.LoadBuffer(ctx, bufferName, raw); err != nil {
		_ = client.DeleteBuffer(ctx, bufferName)
		return fmt.Errorf("load exact message bytes into named tmux buffer: %w", err)
	}
	verified, err := client.SaveBuffer(ctx, bufferName)
	if err != nil {
		_ = client.DeleteBuffer(ctx, bufferName)
		return fmt.Errorf("verify named tmux buffer: %w", err)
	}
	if !bytes.Equal(verified, raw) {
		_ = client.DeleteBuffer(ctx, bufferName)
		return fmt.Errorf("named tmux buffer verification did not return the exact message bytes")
	}
	if err := client.PasteBuffer(ctx, bufferName, paneID); err != nil {
		_ = client.DeleteBuffer(ctx, bufferName)
		return fmt.Errorf("paste named tmux buffer into exact pane: %w", err)
	}
	if err := client.DeleteBuffer(ctx, bufferName); err != nil {
		return fmt.Errorf("delete named tmux buffer after paste: %w", err)
	}
	return nil
}

func matchesRecipient(record session.Record, request sessionmove.Request, receipt sessionmove.Receipt) bool {
	nativeID := strings.TrimSpace(record.NativeHarnessID)
	if nativeID == "" {
		nativeID = strings.TrimSpace(record.AgentID)
	}
	return record.PID == receipt.PID && record.WBSessionID == receipt.SuccessorWBSessionID &&
		record.Machine == receipt.TargetMachine && record.Runtime == receipt.Runtime && strings.TrimSpace(record.Model) == receipt.Model &&
		nativeID == receipt.NativeHarnessID && record.TmuxName == receipt.TmuxName &&
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
