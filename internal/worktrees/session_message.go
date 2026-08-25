package worktrees

import (
	"fmt"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// ExternalSourceMessageOptions carries the target acknowledgement and exact
// durable outbox evidence used to append one predecessor Work Log event.
type ExternalSourceMessageOptions struct {
	ProjectsRoot   string
	Request        sessionmove.Request
	RequestDigest  sessionmove.Digest
	Receipt        sessionmove.Receipt
	SourceSession  session.Record
	Message        sessionmove.Message
	Record         sessionmove.MessageRecord
	MessageReceipt sessionmove.MessageReceipt
}

// RecordExternalSourceMessageSent records only what the receipt proves:
// durable target admission plus paste into the corroborated tmux pane. It
// never says that the successor harness or agent processed the message.
func RecordExternalSourceMessageSent(options ExternalSourceMessageOptions) (LocalWorkLogEvent, error) {
	if err := sessionmove.ValidateReceiptForRequest(options.Receipt, options.Request, options.RequestDigest); err != nil {
		return LocalWorkLogEvent{}, err
	}
	if err := validateExternalSourceSession(options.SourceSession, options.Request); err != nil {
		return LocalWorkLogEvent{}, err
	}
	if err := sessionmove.ValidateMessageForRequest(options.Message, options.Request); err != nil {
		return LocalWorkLogEvent{}, err
	}
	messageRaw, err := sessionmove.EncodeMessage(options.Message)
	if err != nil {
		return LocalWorkLogEvent{}, err
	}
	messageDigest := sessionmove.DigestBytes(messageRaw)
	if options.Record.SchemaVersion != sessionmove.MessageRecordSchemaVersion ||
		options.Record.Direction != sessionmove.MessageDirectionOutgoing || options.Record.MessageID != options.Message.MessageID ||
		options.Record.MessageDigest != messageDigest || options.Record.HandoffID != options.Request.HandoffID ||
		!options.Record.RecordedAt.Equal(options.Message.SentAt.UTC()) {
		return LocalWorkLogEvent{}, fmt.Errorf("source message Work Log record does not match exact durable outbox")
	}
	if err := sessionmove.ValidateMessageReceipt(options.MessageReceipt, options.Message, messageDigest,
		options.Receipt.TmuxName, options.Receipt.PID); err != nil {
		return LocalWorkLogEvent{}, err
	}

	sourceReference, err := sessionmove.ParseWorkLogReference(options.Request.WorkLogReference)
	if err != nil {
		return LocalWorkLogEvent{}, err
	}
	targetReference, err := sessionmove.ExpectedTargetWorkLogReference(options.Request, options.RequestDigest)
	if err != nil {
		return LocalWorkLogEvent{}, err
	}
	home, err := wbhome.Root(options.ProjectsRoot)
	if err != nil {
		return LocalWorkLogEvent{}, err
	}
	runDir, _, err := openWorkLogRun(home, sourceReference.EffortID, sourceReference.RunID, false)
	if err != nil {
		return LocalWorkLogEvent{}, err
	}
	defer func() { _ = runDir.Close() }()
	unlock, err := lockClaim(runDir, sourceReference.ClaimID)
	if err != nil {
		return LocalWorkLogEvent{}, err
	}
	defer unlock()
	claims, err := openPrivateChild(runDir, "claims", false)
	if err != nil {
		return LocalWorkLogEvent{}, err
	}
	var claim workLogClaim
	err = readJSONAt(claims, sourceReference.ClaimID+".json", &claim)
	_ = claims.Close()
	if err != nil {
		return LocalWorkLogEvent{}, err
	}
	projection, err := readWorkLogProjection(claim.Worktree)
	if err != nil {
		return LocalWorkLogEvent{}, err
	}
	if claim.EffortID != sourceReference.EffortID || claim.RunID != sourceReference.RunID || claim.ClaimID != sourceReference.ClaimID ||
		projection != (workLogProjection{Version: 1, EffortID: sourceReference.EffortID, RunID: sourceReference.RunID,
			ClaimID: sourceReference.ClaimID, Lifecycle: "terminal"}) {
		return LocalWorkLogEvent{}, fmt.Errorf("source Work Log identity conflicts with completed handoff lineage")
	}
	exists, _, err := validateExistingExternalTerminal(runDir, claim, options.Request, targetReference,
		externalHandoffEvidence(options.Request, options.RequestDigest, targetReference.String()))
	if err != nil {
		return LocalWorkLogEvent{}, err
	}
	if !exists {
		return LocalWorkLogEvent{}, fmt.Errorf("source Work Log has no immutable completed handoff authority")
	}
	event := LocalWorkLogEvent{
		ID: externalLocalEventID("source-message-sent", messageDigest, options.Message.MessageID), Type: LocalEventHandoff,
		At:      options.MessageReceipt.PastedAt.UTC(),
		Message: "session message acknowledged as durably recorded and pasted; agent processing is not claimed",
		Result:  "pasted",
		Extra: map[string]any{
			"handoff_id": options.Request.HandoffID, "endpoint": "source", "message_id": options.Message.MessageID,
			"message_digest": string(messageDigest), "message_kind": string(options.Message.Kind),
			"sender_wb_session_id":      options.Message.SenderWBSessionID,
			"recipient_wb_session_id":   options.Message.RecipientWBSessionID,
			"reply_to_wb_session_id":    options.Message.ReplyToWBSessionID,
			"source_work_log_reference": sourceReference.String(),
			"target_work_log_reference": targetReference.String(),
			"acknowledgement_scope":     "durable_record_and_tmux_paste_only",
		},
	}
	event, _, err = appendLocalEventWithoutCustody(claim.Worktree, event)
	return event, err
}

// ExternalTargetMessageOptions carries only durable protocol evidence. The
// message body is used for digest validation but is never copied into Work Log
// diagnostics or event fields.
type ExternalTargetMessageOptions struct {
	ProjectsRoot  string
	Request       sessionmove.Request
	RequestDigest sessionmove.Digest
	Receipt       sessionmove.Receipt
	Message       sessionmove.Message
	Record        sessionmove.MessageRecord
}

// RecordExternalTargetMessageReceived records that exact bytes reached the
// durable inbox and are eligible for one tmux paste attempt. It deliberately
// does not claim that the harness or agent processed those bytes.
func RecordExternalTargetMessageReceived(options ExternalTargetMessageOptions) (LocalWorkLogEvent, error) {
	if err := sessionmove.ValidateReceiptForRequest(options.Receipt, options.Request, options.RequestDigest); err != nil {
		return LocalWorkLogEvent{}, err
	}
	if err := sessionmove.ValidateMessageForRequest(options.Message, options.Request); err != nil {
		return LocalWorkLogEvent{}, err
	}
	messageRaw, err := sessionmove.EncodeMessage(options.Message)
	if err != nil {
		return LocalWorkLogEvent{}, err
	}
	messageDigest := sessionmove.DigestBytes(messageRaw)
	if options.Record.SchemaVersion != sessionmove.MessageRecordSchemaVersion ||
		options.Record.Direction != sessionmove.MessageDirectionIncoming || options.Record.MessageID != options.Message.MessageID ||
		options.Record.MessageDigest != messageDigest || options.Record.HandoffID != options.Request.HandoffID || options.Record.RecordedAt.IsZero() {
		return LocalWorkLogEvent{}, fmt.Errorf("target message Work Log record does not match exact durable inbox")
	}
	worktree, err := SessionReceiveWorktreePath(options.ProjectsRoot, options.Request)
	if err != nil {
		return LocalWorkLogEvent{}, err
	}
	claim, reference, unlock, err := loadExternalTargetClaim(options.ProjectsRoot, options.Request, options.RequestDigest, worktree)
	if err != nil {
		return LocalWorkLogEvent{}, err
	}
	defer unlock()
	if err := validateExternalAttemptOwner(claim.Worktree, options.Request, options.RequestDigest, claim,
		options.Receipt.AttemptID, options.Receipt.AttemptIndex, options.Receipt.PID, options.Receipt.StartedAt, true); err != nil {
		return LocalWorkLogEvent{}, err
	}
	event := LocalWorkLogEvent{
		ID:      externalLocalEventID("target-message-received", messageDigest, options.Message.MessageID),
		Type:    LocalEventHandoff,
		At:      options.Record.RecordedAt.UTC(),
		Message: "session message received and durably recorded for one tmux paste attempt",
		Result:  "recorded",
		Extra: map[string]any{
			"handoff_id": options.Request.HandoffID, "endpoint": "target", "message_id": options.Message.MessageID,
			"message_digest": string(messageDigest), "message_kind": string(options.Message.Kind),
			"sender_wb_session_id":      options.Message.SenderWBSessionID,
			"recipient_wb_session_id":   options.Message.RecipientWBSessionID,
			"reply_to_wb_session_id":    options.Message.ReplyToWBSessionID,
			"target_work_log_reference": reference.String(),
			"acknowledgement_scope":     "durable_record_and_tmux_paste_only",
		},
	}
	event, _, err = appendLocalEventWithoutCustody(claim.Worktree, event)
	return event, err
}
