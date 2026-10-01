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
	messageDigest, err := validatedExternalMessageDigest(options.Request, options.Message, options.Record, sessionmove.MessageDirectionOutgoing)
	if err != nil {
		return LocalWorkLogEvent{}, err
	}
	if err := sessionmove.ValidateMessageReceipt(options.MessageReceipt, options.Message, messageDigest,
		options.Receipt.TmuxName, options.Receipt.PID); err != nil {
		return LocalWorkLogEvent{}, err
	}

	// ValidateReceiptForRequest already derived the target reference from this
	// unchanged request and digest, including parsing the source reference.
	sourceReference, _ := sessionmove.ParseWorkLogReference(options.Request.WorkLogReference)
	targetReference, _ := sessionmove.ExpectedTargetWorkLogReference(options.Request, options.RequestDigest)
	home, err := wbhome.Root(options.ProjectsRoot)
	if err != nil {
		return LocalWorkLogEvent{}, err
	}
	locked, err := openLockedWorkLogRun(home, sourceReference.EffortID, sourceReference.RunID, sourceReference.ClaimID, false)
	if err != nil {
		return LocalWorkLogEvent{}, err
	}
	defer locked.close()
	runDir := locked.directory
	claim, err := readWorkLogClaimAt(runDir, sourceReference.ClaimID)
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
	extra := externalMessageEventFields(options.Request, options.Message, messageDigest)
	extra["endpoint"] = "source"
	extra["source_work_log_reference"] = sourceReference.String()
	extra["target_work_log_reference"] = targetReference.String()
	event := LocalWorkLogEvent{
		ID: externalLocalEventID("source-message-sent", messageDigest, options.Message.MessageID), Type: LocalEventHandoff,
		At:      options.MessageReceipt.PastedAt.UTC(),
		Message: "session message acknowledged as durably recorded and pasted; agent processing is not claimed",
		Result:  "pasted",
		Extra:   extra,
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
	messageDigest, err := validatedExternalMessageDigest(options.Request, options.Message, options.Record, sessionmove.MessageDirectionIncoming)
	if err != nil {
		return LocalWorkLogEvent{}, err
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
	extra := externalMessageEventFields(options.Request, options.Message, messageDigest)
	extra["endpoint"] = "target"
	extra["target_work_log_reference"] = reference.String()
	event := LocalWorkLogEvent{
		ID:      externalLocalEventID("target-message-received", messageDigest, options.Message.MessageID),
		Type:    LocalEventHandoff,
		At:      options.Record.RecordedAt.UTC(),
		Message: "session message received and durably recorded for one tmux paste attempt",
		Result:  "recorded",
		Extra:   extra,
	}
	event, _, err = appendLocalEventWithoutCustody(claim.Worktree, event)
	return event, err
}

// validatedExternalMessageDigest binds a caller-owned message to its exact
// durable record. A valid message can still fail canonical JSON encoding when
// sent_at has a nonzero year outside JSON's supported time range.
func validatedExternalMessageDigest(request sessionmove.Request, message sessionmove.Message, record sessionmove.MessageRecord,
	direction sessionmove.MessageDirection,
) (sessionmove.Digest, error) {
	if err := sessionmove.ValidateMessageForRequest(message, request); err != nil {
		return "", err
	}
	raw, err := sessionmove.EncodeMessage(message)
	if err != nil {
		return "", err
	}
	digest := sessionmove.DigestBytes(raw)
	validTime := !record.RecordedAt.IsZero()
	if direction == sessionmove.MessageDirectionOutgoing {
		validTime = record.RecordedAt.Equal(message.SentAt.UTC())
	}
	if record.SchemaVersion != sessionmove.MessageRecordSchemaVersion || record.Direction != direction ||
		record.MessageID != message.MessageID || record.MessageDigest != digest || record.HandoffID != request.HandoffID || !validTime {
		if direction == sessionmove.MessageDirectionOutgoing {
			return "", fmt.Errorf("source message Work Log record does not match exact durable outbox")
		}
		return "", fmt.Errorf("target message Work Log record does not match exact durable inbox")
	}
	return digest, nil
}

func externalMessageEventFields(request sessionmove.Request, message sessionmove.Message, digest sessionmove.Digest) map[string]any {
	return map[string]any{
		"handoff_id": request.HandoffID, "message_id": message.MessageID,
		"message_digest": string(digest), "message_kind": string(message.Kind),
		"sender_wb_session_id": message.SenderWBSessionID, "recipient_wb_session_id": message.RecipientWBSessionID,
		"reply_to_wb_session_id": message.ReplyToWBSessionID,
		"acknowledgement_scope":  "durable_record_and_tmux_paste_only",
	}
}
