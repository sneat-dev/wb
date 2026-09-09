package sessionmove

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/unixcompat"
)

const (
	messageOutboxDirName   = "outbox"
	messageInboxDirName    = "inbox"
	messagePayloadFileName = "message.json"
	messageRecordFileName  = "record.json"
	messageIntentFileName  = "paste-intent.json"
	messageReceiptFileName = "receipt.json"
	maxMessageBytes        = MaxMessageBodyBytes + (16 << 10)
	maxMessageRecordBytes  = 16 << 10
	maxMessageIntentBytes  = 16 << 10
	maxMessageReceiptBytes = 32 << 10
)

var tmuxPaneIDPattern = regexp.MustCompile(`^%[0-9]+$`)

// MessageDirection distinguishes the predecessor's durable outbox from the
// successor's durable inbox. Both records bind the same caller-owned ID and
// exact payload digest.
type MessageDirection string

const (
	MessageDirectionOutgoing MessageDirection = "outgoing"
	MessageDirectionIncoming MessageDirection = "incoming"
)

// MessageRecord is the strict durable source/target admission record.
type MessageRecord struct {
	SchemaVersion int              `json:"schema_version"`
	Direction     MessageDirection `json:"direction"`
	MessageID     string           `json:"message_id"`
	MessageDigest Digest           `json:"message_digest"`
	HandoffID     string           `json:"handoff_id"`
	RecordedAt    time.Time        `json:"recorded_at"`
}

// MessagePasteIntent is published before the sole automatic tmux paste
// attempt. Its presence without a receipt is deliberately ambiguous: replay
// must not paste again automatically.
type MessagePasteIntent struct {
	SchemaVersion        int       `json:"schema_version"`
	MessageID            string    `json:"message_id"`
	MessageDigest        Digest    `json:"message_digest"`
	HandoffID            string    `json:"handoff_id"`
	RecipientWBSessionID string    `json:"recipient_wb_session_id"`
	TmuxName             string    `json:"tmux_name"`
	PaneID               string    `json:"pane_id"`
	PID                  int       `json:"pid"`
	IntendedAt           time.Time `json:"intended_at"`
}

// MessageState is one loaded durable outbox or inbox entry.
type MessageState struct {
	Message Message
	Digest  Digest
	Raw     []byte
	Record  MessageRecord
	Replay  bool
	Intent  *MessagePasteIntent
	Receipt *MessageReceipt
}

// NewMessageID returns a caller-owned identity that can be persisted before
// courier use and supplied to the explicit retry path after ambiguity.
func NewMessageID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate session message ID: %w", err)
	}
	return "message-" + hex.EncodeToString(random[:]), nil
}

func (r MessageReceipt) validate() error {
	if err := validateSchema("session message receipt", r.SchemaVersion, MessageReceiptSchemaVersion); err != nil {
		return err
	}
	for field, value := range map[string]string{
		"message_id": r.MessageID, "handoff_id": r.HandoffID,
		"sender_wb_session_id": r.SenderWBSessionID, "recipient_wb_session_id": r.RecipientWBSessionID,
		"reply_to_wb_session_id": r.ReplyToWBSessionID, "tmux_name": r.TmuxName,
	} {
		if err := validateID(field, value); err != nil {
			return err
		}
	}
	if err := r.MessageDigest.validate(); err != nil {
		return fmt.Errorf("message_digest: %w", err)
	}
	if r.Kind != MessageKindText && r.Kind != MessageKindRequestHandoff {
		return fmt.Errorf("message receipt kind %q is unsupported", r.Kind)
	}
	if !tmuxPaneIDPattern.MatchString(r.PaneID) {
		return fmt.Errorf("pane_id %q is not a canonical tmux pane ID", r.PaneID)
	}
	if r.PID <= 0 {
		return fmt.Errorf("message receipt pid must be positive")
	}
	if r.RecordedAt.IsZero() {
		return fmt.Errorf("recorded_at is required")
	}
	if r.PastedAt.IsZero() {
		return fmt.Errorf("pasted_at is required")
	}
	if r.PastedAt.Before(r.RecordedAt) {
		return fmt.Errorf("pasted_at precedes recorded_at")
	}
	return nil
}

// ValidateMessageReceipt binds an acknowledgement to exact message bytes and
// the previously corroborated tmux identity. Success means recorded+pasted;
// it never means processed by the harness.
func ValidateMessageReceipt(receipt MessageReceipt, message Message, digest Digest, tmuxName string, pid int) error {
	if err := receipt.validate(); err != nil {
		return err
	}
	if err := message.validate(); err != nil {
		return err
	}
	if err := digest.validate(); err != nil {
		return err
	}
	if receipt.MessageID != message.MessageID || receipt.MessageDigest != digest || receipt.HandoffID != message.HandoffID ||
		receipt.SenderWBSessionID != message.SenderWBSessionID || receipt.RecipientWBSessionID != message.RecipientWBSessionID ||
		receipt.ReplyToWBSessionID != message.ReplyToWBSessionID || receipt.Kind != message.Kind ||
		receipt.TmuxName != tmuxName || receipt.PID != pid {
		return fmt.Errorf("%w: message receipt does not match exact message and tmux identity", ErrHandoffConflict)
	}
	return nil
}

// ValidateMessageForRequest corroborates all immutable lineage carried by a
// message against the original handoff. Only the predecessor may address its
// recorded successor, and replies always return to that predecessor.
func ValidateMessageForRequest(message Message, request Request) error {
	if err := message.validate(); err != nil {
		return err
	}
	if err := request.validate(); err != nil {
		return err
	}
	if message.HandoffID != request.HandoffID || message.SenderWBSessionID != request.PredecessorWBSessionID ||
		message.RecipientWBSessionID != request.SuccessorWBSessionID || message.ReplyToWBSessionID != request.PredecessorWBSessionID {
		return fmt.Errorf("%w: session message lineage does not match completed handoff", ErrHandoffConflict)
	}
	if message.SentAt.Before(request.CreatedAt.UTC()) {
		return fmt.Errorf("%w: session message predates its original handoff", ErrHandoffConflict)
	}
	return nil
}

// AdmitOutgoingMessageUnderLock installs exact canonical payload bytes in the
// predecessor outbox before any courier is invoked.
func (s Store) AdmitOutgoingMessageUnderLock(lock *ExecutionLock, handoffID string, requestDigest Digest, raw []byte, recordedAt time.Time) (MessageState, error) {
	return s.admitMessageUnderLock(lock, handoffID, requestDigest, MessageDirectionOutgoing, raw, recordedAt)
}

// AdmitIncomingMessageUnderLock installs exact canonical payload bytes in the
// successor inbox before any paste intent is published.
func (s Store) AdmitIncomingMessageUnderLock(lock *ExecutionLock, handoffID string, requestDigest Digest, raw []byte, recordedAt time.Time) (MessageState, error) {
	return s.admitMessageUnderLock(lock, handoffID, requestDigest, MessageDirectionIncoming, raw, recordedAt)
}

func (s Store) admitMessageUnderLock(lock *ExecutionLock, handoffID string, requestDigest Digest, direction MessageDirection, raw []byte, recordedAt time.Time) (MessageState, error) {
	var state MessageState
	if len(raw) == 0 || len(raw) > maxMessageBytes {
		return state, fmt.Errorf("session message must be non-empty and at most %d bytes", maxMessageBytes)
	}
	message, err := DecodeMessage(raw)
	if err != nil {
		return state, err
	}
	canonical, err := EncodeMessage(message)
	if err != nil {
		return state, err
	}
	if !bytes.Equal(raw, canonical) {
		return state, fmt.Errorf("session message must use WB's canonical JSON encoding")
	}
	request, handoff, err := s.retainHandoffUnderLock(lock, handoffID, requestDigest)
	if err != nil {
		return state, err
	}
	defer func() { _ = handoff.Close() }()
	if err := ValidateMessageForRequest(message, request); err != nil {
		return state, err
	}
	handoffReceipt, _, err := loadReceiptAt(handoff, request, requestDigest)
	if err != nil {
		return state, err
	} else if handoffReceipt == nil {
		return state, fmt.Errorf("session message requires a durable completed handoff receipt")
	}
	if recordedAt.IsZero() {
		return state, fmt.Errorf("message recorded_at is required")
	}
	if direction == MessageDirectionOutgoing && !recordedAt.UTC().Equal(message.SentAt.UTC()) {
		return state, fmt.Errorf("outgoing message recorded_at must equal its caller-owned sent_at")
	}
	directory, err := openMessageEntryAt(handoff, direction, message.MessageID, true)
	if err != nil {
		return state, err
	}
	defer func() { _ = directory.Close() }()
	created, err := publishImmutableAt(directory, messagePayloadFileName, raw, 0o600)
	if err != nil {
		return state, fmt.Errorf("persist exact session message: %w", err)
	}
	existingRaw, err := readImmutableAt(directory, messagePayloadFileName, maxMessageBytes, "durable session message")
	if err != nil {
		return state, err
	}
	if !bytes.Equal(existingRaw, raw) {
		return state, fmt.Errorf("%w: message ID %s already has different exact bytes", ErrHandoffConflict, message.MessageID)
	}
	record := MessageRecord{
		SchemaVersion: MessageRecordSchemaVersion, Direction: direction, MessageID: message.MessageID,
		MessageDigest: DigestBytes(raw), HandoffID: handoffID, RecordedAt: recordedAt.UTC(),
	}
	recordRaw, err := marshalJSON(record)
	if err != nil {
		return state, err
	}
	if _, err := publishImmutableAt(directory, messageRecordFileName, recordRaw, 0o600); err != nil {
		return state, fmt.Errorf("persist session message record: %w", err)
	}
	state, err = loadMessageStateAt(directory, request, direction, message.MessageID, handoffReceipt)
	if err != nil {
		return MessageState{}, err
	}
	state.Replay = !created
	return state, nil
}

func (s Store) LoadIncomingMessageUnderLock(lock *ExecutionLock, handoffID string, requestDigest Digest, messageID string) (MessageState, error) {
	return s.loadMessageUnderLock(lock, handoffID, requestDigest, MessageDirectionIncoming, messageID)
}

func (s Store) LoadOutgoingMessageUnderLock(lock *ExecutionLock, handoffID string, requestDigest Digest, messageID string) (MessageState, error) {
	return s.loadMessageUnderLock(lock, handoffID, requestDigest, MessageDirectionOutgoing, messageID)
}

// ResumeOutgoingMessageUnderLock repairs the sole safe source-side admission
// crash gap: message.json was published but record.json was not. Outgoing
// RecordedAt is defined to equal caller-owned SentAt, so the exact payload
// carries all bytes needed to recreate the missing record without guessing.
func (s Store) ResumeOutgoingMessageUnderLock(lock *ExecutionLock, handoffID string, requestDigest Digest, messageID string) (MessageState, error) {
	if err := validateID("message_id", messageID); err != nil {
		return MessageState{}, err
	}
	request, handoff, err := s.retainHandoffUnderLock(lock, handoffID, requestDigest)
	if err != nil {
		return MessageState{}, err
	}
	defer func() { _ = handoff.Close() }()
	handoffReceipt, _, err := loadReceiptAt(handoff, request, requestDigest)
	if err != nil || handoffReceipt == nil {
		if err != nil {
			return MessageState{}, err
		}
		return MessageState{}, fmt.Errorf("session message requires a durable completed handoff receipt")
	}
	directory, err := openMessageEntryAt(handoff, MessageDirectionOutgoing, messageID, false)
	if err != nil {
		return MessageState{}, err
	}
	defer func() { _ = directory.Close() }()
	raw, err := readImmutableAt(directory, messagePayloadFileName, maxMessageBytes, "durable outgoing session message")
	if err != nil {
		return MessageState{}, err
	}
	message, err := DecodeMessage(raw)
	if err != nil {
		return MessageState{}, err
	}
	canonical, err := EncodeMessage(message)
	if err != nil || !bytes.Equal(canonical, raw) || message.MessageID != messageID {
		return MessageState{}, fmt.Errorf("%w: durable outgoing message is not the exact canonical retry payload", ErrHandoffConflict)
	}
	if err := ValidateMessageForRequest(message, request); err != nil {
		return MessageState{}, err
	}
	if _, err := readImmutableAt(directory, messageRecordFileName, maxMessageRecordBytes, "session message record"); errors.Is(err, os.ErrNotExist) {
		record := MessageRecord{
			SchemaVersion: MessageRecordSchemaVersion, Direction: MessageDirectionOutgoing,
			MessageID: message.MessageID, MessageDigest: DigestBytes(raw), HandoffID: handoffID, RecordedAt: message.SentAt.UTC(),
		}
		recordRaw, marshalErr := marshalJSON(record)
		if marshalErr != nil {
			return MessageState{}, marshalErr
		}
		if _, publishErr := publishImmutableAt(directory, messageRecordFileName, recordRaw, 0o600); publishErr != nil {
			return MessageState{}, fmt.Errorf("repair outgoing session message record: %w", publishErr)
		}
	} else if err != nil {
		return MessageState{}, err
	}
	state, err := loadMessageStateAt(directory, request, MessageDirectionOutgoing, messageID, handoffReceipt)
	state.Replay = err == nil
	return state, err
}

func (s Store) loadMessageUnderLock(lock *ExecutionLock, handoffID string, requestDigest Digest, direction MessageDirection, messageID string) (MessageState, error) {
	if err := validateID("message_id", messageID); err != nil {
		return MessageState{}, err
	}
	request, handoff, err := s.retainHandoffUnderLock(lock, handoffID, requestDigest)
	if err != nil {
		return MessageState{}, err
	}
	defer func() { _ = handoff.Close() }()
	handoffReceipt, _, err := loadReceiptAt(handoff, request, requestDigest)
	if err != nil || handoffReceipt == nil {
		if err != nil {
			return MessageState{}, err
		}
		return MessageState{}, fmt.Errorf("session message requires a durable completed handoff receipt")
	}
	directory, err := openMessageEntryAt(handoff, direction, messageID, false)
	if err != nil {
		return MessageState{}, err
	}
	defer func() { _ = directory.Close() }()
	return loadMessageStateAt(directory, request, direction, messageID, handoffReceipt)
}

func (s Store) SaveIncomingPasteIntentUnderLock(lock *ExecutionLock, handoffID string, requestDigest Digest, intent MessagePasteIntent) (MessagePasteIntent, bool, error) {
	request, handoff, err := s.retainHandoffUnderLock(lock, handoffID, requestDigest)
	if err != nil {
		return MessagePasteIntent{}, false, err
	}
	defer func() { _ = handoff.Close() }()
	handoffReceipt, _, err := loadReceiptAt(handoff, request, requestDigest)
	if err != nil || handoffReceipt == nil {
		if err != nil {
			return MessagePasteIntent{}, false, err
		}
		return MessagePasteIntent{}, false, fmt.Errorf("message paste intent requires a durable handoff receipt")
	}
	directory, err := openMessageEntryAt(handoff, MessageDirectionIncoming, intent.MessageID, false)
	if err != nil {
		return MessagePasteIntent{}, false, err
	}
	defer func() { _ = directory.Close() }()
	state, err := loadMessageStateAt(directory, request, MessageDirectionIncoming, intent.MessageID, handoffReceipt)
	if err != nil {
		return MessagePasteIntent{}, false, err
	}
	if err := validatePasteIntent(intent, state); err != nil {
		return MessagePasteIntent{}, false, err
	}
	raw, err := marshalJSON(intent)
	if err != nil {
		return MessagePasteIntent{}, false, err
	}
	created, err := publishImmutableAt(directory, messageIntentFileName, raw, 0o600)
	if err != nil {
		return MessagePasteIntent{}, false, err
	}
	existingRaw, err := readImmutableAt(directory, messageIntentFileName, maxMessageIntentBytes, "message paste intent")
	if err != nil {
		return MessagePasteIntent{}, false, err
	}
	if !bytes.Equal(existingRaw, raw) {
		return MessagePasteIntent{}, false, fmt.Errorf("%w: message %s already has a different paste intent", ErrHandoffConflict, intent.MessageID)
	}
	existing, err := decodeMessagePasteIntent(existingRaw)
	return existing, !created, err
}

func (s Store) SaveIncomingMessageReceiptUnderLock(lock *ExecutionLock, handoffID string, requestDigest Digest, receipt MessageReceipt) (MessageReceipt, bool, error) {
	return s.saveMessageReceiptUnderLock(lock, handoffID, requestDigest, MessageDirectionIncoming, receipt)
}

func (s Store) SaveOutgoingMessageReceiptUnderLock(lock *ExecutionLock, handoffID string, requestDigest Digest, receipt MessageReceipt) (MessageReceipt, bool, error) {
	return s.saveMessageReceiptUnderLock(lock, handoffID, requestDigest, MessageDirectionOutgoing, receipt)
}

func (s Store) saveMessageReceiptUnderLock(lock *ExecutionLock, handoffID string, requestDigest Digest, direction MessageDirection, receipt MessageReceipt) (MessageReceipt, bool, error) {
	request, handoff, err := s.retainHandoffUnderLock(lock, handoffID, requestDigest)
	if err != nil {
		return MessageReceipt{}, false, err
	}
	defer func() { _ = handoff.Close() }()
	handoffReceipt, _, err := loadReceiptAt(handoff, request, requestDigest)
	if err != nil || handoffReceipt == nil {
		if err != nil {
			return MessageReceipt{}, false, err
		}
		return MessageReceipt{}, false, fmt.Errorf("message receipt requires a durable handoff receipt")
	}
	directory, err := openMessageEntryAt(handoff, direction, receipt.MessageID, false)
	if err != nil {
		return MessageReceipt{}, false, err
	}
	defer func() { _ = directory.Close() }()
	state, err := loadMessageStateAt(directory, request, direction, receipt.MessageID, handoffReceipt)
	if err != nil {
		return MessageReceipt{}, false, err
	}
	if err := ValidateMessageReceipt(receipt, state.Message, state.Digest, handoffReceipt.TmuxName, handoffReceipt.PID); err != nil {
		return MessageReceipt{}, false, err
	}
	if direction == MessageDirectionIncoming {
		if state.Intent == nil {
			return MessageReceipt{}, false, fmt.Errorf("incoming message receipt requires a durable paste intent")
		}
		if receipt.RecordedAt != state.Record.RecordedAt || receipt.PaneID != state.Intent.PaneID ||
			receipt.TmuxName != state.Intent.TmuxName || receipt.PID != state.Intent.PID || receipt.PastedAt.Before(state.Intent.IntendedAt) {
			return MessageReceipt{}, false, fmt.Errorf("%w: incoming message receipt does not match its durable record and paste intent", ErrHandoffConflict)
		}
	}
	raw, err := EncodeMessageReceipt(receipt)
	if err != nil {
		return MessageReceipt{}, false, err
	}
	created, err := publishImmutableAt(directory, messageReceiptFileName, raw, 0o600)
	if err != nil {
		return MessageReceipt{}, false, err
	}
	existingRaw, err := readImmutableAt(directory, messageReceiptFileName, maxMessageReceiptBytes, "message receipt")
	if err != nil {
		return MessageReceipt{}, false, err
	}
	if !bytes.Equal(existingRaw, raw) {
		return MessageReceipt{}, false, fmt.Errorf("%w: message %s already has a different receipt", ErrHandoffConflict, receipt.MessageID)
	}
	existing, err := DecodeMessageReceipt(existingRaw)
	return existing, !created, err
}

func loadMessageStateAt(directory *os.File, request Request, direction MessageDirection, messageID string, handoffReceipt *Receipt) (MessageState, error) {
	var state MessageState
	raw, err := readImmutableAt(directory, messagePayloadFileName, maxMessageBytes, "durable session message")
	if err != nil {
		return state, err
	}
	message, err := DecodeMessage(raw)
	if err != nil {
		return state, err
	}
	if message.MessageID != messageID {
		return state, fmt.Errorf("%w: message directory %s contains message %s", ErrHandoffConflict, messageID, message.MessageID)
	}
	if err := ValidateMessageForRequest(message, request); err != nil {
		return state, err
	}
	recordRaw, err := readImmutableAt(directory, messageRecordFileName, maxMessageRecordBytes, "session message record")
	if err != nil {
		return state, err
	}
	record, err := decodeMessageRecord(recordRaw)
	if err != nil {
		return state, err
	}
	digest := DigestBytes(raw)
	if record.Direction != direction || record.MessageID != messageID || record.MessageDigest != digest || record.HandoffID != request.HandoffID {
		return state, fmt.Errorf("%w: durable message record does not match exact payload and handoff", ErrHandoffConflict)
	}
	state = MessageState{Message: message, Digest: digest, Raw: append([]byte(nil), raw...), Record: record}
	intentRaw, err := readImmutableAt(directory, messageIntentFileName, maxMessageIntentBytes, "message paste intent")
	if err == nil {
		intent, decodeErr := decodeMessagePasteIntent(intentRaw)
		if decodeErr != nil {
			return MessageState{}, decodeErr
		}
		if validateErr := validatePasteIntent(intent, state); validateErr != nil {
			return MessageState{}, validateErr
		}
		state.Intent = &intent
	} else if !errors.Is(err, os.ErrNotExist) {
		return MessageState{}, err
	}
	receiptRaw, err := readImmutableAt(directory, messageReceiptFileName, maxMessageReceiptBytes, "message receipt")
	if err == nil {
		receipt, decodeErr := DecodeMessageReceipt(receiptRaw)
		if decodeErr != nil {
			return MessageState{}, decodeErr
		}
		if handoffReceipt == nil {
			return MessageState{}, fmt.Errorf("durable message receipt has no completed handoff receipt authority")
		}
		if validateErr := ValidateMessageReceipt(receipt, message, digest, handoffReceipt.TmuxName, handoffReceipt.PID); validateErr != nil {
			return MessageState{}, validateErr
		}
		if direction == MessageDirectionIncoming {
			if state.Intent == nil || receipt.RecordedAt != state.Record.RecordedAt || receipt.PaneID != state.Intent.PaneID ||
				receipt.TmuxName != state.Intent.TmuxName || receipt.PID != state.Intent.PID || receipt.PastedAt.Before(state.Intent.IntendedAt) {
				return MessageState{}, fmt.Errorf("%w: durable inbox receipt does not match its record and paste intent", ErrHandoffConflict)
			}
		}
		state.Receipt = &receipt
	} else if !errors.Is(err, os.ErrNotExist) {
		return MessageState{}, err
	}
	return state, nil
}

func decodeMessageRecord(raw []byte) (MessageRecord, error) {
	var record MessageRecord
	if err := decodeJSON(raw, &record); err != nil {
		return MessageRecord{}, fmt.Errorf("decode message record: %w", err)
	}
	if record.SchemaVersion != MessageRecordSchemaVersion ||
		(record.Direction != MessageDirectionOutgoing && record.Direction != MessageDirectionIncoming) || record.RecordedAt.IsZero() {
		return MessageRecord{}, fmt.Errorf("durable message record is invalid")
	}
	if err := validateID("message_id", record.MessageID); err != nil {
		return MessageRecord{}, err
	}
	if err := validateID("handoff_id", record.HandoffID); err != nil {
		return MessageRecord{}, err
	}
	if err := record.MessageDigest.validate(); err != nil {
		return MessageRecord{}, err
	}
	return record, nil
}

func decodeMessagePasteIntent(raw []byte) (MessagePasteIntent, error) {
	var intent MessagePasteIntent
	if err := decodeJSON(raw, &intent); err != nil {
		return MessagePasteIntent{}, fmt.Errorf("decode message paste intent: %w", err)
	}
	if intent.SchemaVersion != MessagePasteIntentSchemaVersion || intent.IntendedAt.IsZero() || intent.PID <= 0 ||
		!tmuxPaneIDPattern.MatchString(intent.PaneID) {
		return MessagePasteIntent{}, fmt.Errorf("message paste intent is invalid")
	}
	for field, value := range map[string]string{
		"message_id": intent.MessageID, "handoff_id": intent.HandoffID,
		"recipient_wb_session_id": intent.RecipientWBSessionID, "tmux_name": intent.TmuxName,
	} {
		if err := validateID(field, value); err != nil {
			return MessagePasteIntent{}, err
		}
	}
	if err := intent.MessageDigest.validate(); err != nil {
		return MessagePasteIntent{}, err
	}
	return intent, nil
}

func validatePasteIntent(intent MessagePasteIntent, state MessageState) error {
	if intent.SchemaVersion != MessagePasteIntentSchemaVersion || intent.IntendedAt.IsZero() || intent.PID <= 0 ||
		!tmuxPaneIDPattern.MatchString(intent.PaneID) {
		return fmt.Errorf("message paste intent is invalid")
	}
	if intent.MessageID != state.Message.MessageID || intent.MessageDigest != state.Digest ||
		intent.HandoffID != state.Message.HandoffID || intent.RecipientWBSessionID != state.Message.RecipientWBSessionID {
		return fmt.Errorf("%w: paste intent does not match exact inbox message", ErrHandoffConflict)
	}
	return nil
}

func openMessageEntryAt(handoff *os.File, direction MessageDirection, messageID string, create bool) (*os.File, error) {
	if handoff == nil {
		return nil, fmt.Errorf("open message entry: handoff authority is required")
	}
	if err := validateID("message_id", messageID); err != nil {
		return nil, err
	}
	directoryName := messageInboxDirName
	if direction == MessageDirectionOutgoing {
		directoryName = messageOutboxDirName
	} else if direction != MessageDirectionIncoming {
		return nil, fmt.Errorf("message direction %q is unsupported", direction)
	}
	parent, err := openSecureDirectoryAt(handoff, directoryName, create, "message "+string(direction))
	if err != nil {
		return nil, err
	}
	defer func() { _ = parent.Close() }()
	return openSecureDirectoryAt(parent, messageID, create, "message entry")
}

func openSecureDirectoryAt(parent *os.File, name string, create bool, label string) (*os.File, error) {
	if create {
		if err := unix.Mkdirat(int(parent.Fd()), name, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, fmt.Errorf("create %s directory: %w", label, err)
		}
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s directory: %w", label, err)
	}
	directory := os.NewFile(uintptr(fd), "wb-session-"+strings.ReplaceAll(label, " ", "-"))
	if directory == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap %s directory", label)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o777 != 0o700 || stat.Nlink < 1 {
		_ = directory.Close()
		if err != nil {
			return nil, fmt.Errorf("inspect %s directory: %w", label, err)
		}
		return nil, fmt.Errorf("%s directory is not mode 0700", label)
	}
	return directory, nil
}
