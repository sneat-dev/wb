// Package sessionmove defines WB's portable agent-session handoff protocol
// and the local durable aggregate used by both source and target machines.
// Couriers transport these types but do not interpret or own them.
package sessionmove

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"time"
)

const (
	RequestSchemaVersion  = 1
	ReceiptSchemaVersion  = 1
	MessageSchemaVersion  = 1
	EventSchemaVersion    = 1
	DigestAlgorithmSHA256 = "sha256"
)

// Digest identifies exact bytes at a courier or durable-state boundary. Its
// textual form names the algorithm so a future protocol can add one without
// silently reinterpreting old state.
type Digest string

// NewHandoffID returns an opaque identity suitable for both the tracked
// handover filename and the private aggregate directory. It is deliberately
// independent of either endpoint session ID.
func NewHandoffID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate session handoff ID: %w", err)
	}
	return fmt.Sprintf("handoff-%x", random[:]), nil
}

// DigestBytes returns the sha256 digest of exact bytes.
func DigestBytes(raw []byte) Digest {
	sum := sha256.Sum256(raw)
	return Digest(DigestAlgorithmSHA256 + ":" + hex.EncodeToString(sum[:]))
}

func (d Digest) validate() error {
	algorithm, encoded, ok := strings.Cut(string(d), ":")
	if !ok || algorithm != DigestAlgorithmSHA256 || len(encoded) != sha256.Size*2 {
		return fmt.Errorf("digest %q must be sha256:<64 lowercase hex characters>", d)
	}
	decoded, err := hex.DecodeString(encoded)
	if err != nil || hex.EncodeToString(decoded) != encoded {
		return fmt.Errorf("digest %q must be sha256:<64 lowercase hex characters>", d)
	}
	return nil
}

// Matches reports whether d names the exact supplied bytes.
func (d Digest) Matches(raw []byte) bool {
	return d.validate() == nil && d == DigestBytes(raw)
}

// Request is the immutable courier-neutral handoff description. Its encoded
// bytes, rather than a re-marshalled projection, are what Digest authenticates.
type Request struct {
	SchemaVersion          int       `json:"schema_version"`
	HandoffID              string    `json:"handoff_id"`
	SuccessorWBSessionID   string    `json:"successor_wb_session_id"`
	PredecessorWBSessionID string    `json:"predecessor_wb_session_id"`
	SourceMachine          string    `json:"source_machine"`
	TargetMachine          string    `json:"target_machine"`
	RepositoryRemote       string    `json:"repository_remote"`
	Branch                 string    `json:"branch"`
	SourceWorkCommit       string    `json:"source_work_commit"`
	BundleCommit           string    `json:"bundle_commit"`
	HandoverPath           string    `json:"handover_path"`
	HandoverDigest         Digest    `json:"handover_digest"`
	SourceRuntime          string    `json:"source_runtime"`
	SourceModel            string    `json:"source_model,omitempty"`
	SourceNativeHarnessID  string    `json:"source_native_harness_id,omitempty"`
	RequestedHarness       string    `json:"requested_harness,omitempty"`
	WorkLogReference       string    `json:"work_log_reference,omitempty"`
	CreatedAt              time.Time `json:"created_at"`
}

// Receipt is written only after the target successor is durably registered.
// RequestDigest binds it to the exact admitted request bytes.
type Receipt struct {
	SchemaVersion          int       `json:"schema_version"`
	HandoffID              string    `json:"handoff_id"`
	RequestDigest          Digest    `json:"request_digest"`
	SuccessorWBSessionID   string    `json:"successor_wb_session_id"`
	PredecessorWBSessionID string    `json:"predecessor_wb_session_id"`
	TargetMachine          string    `json:"target_machine"`
	TmuxName               string    `json:"tmux_name"`
	Runtime                string    `json:"runtime"`
	Model                  string    `json:"model,omitempty"`
	NativeHarnessID        string    `json:"native_harness_id,omitempty"`
	PinnedCommit           string    `json:"pinned_commit"`
	StartedAt              time.Time `json:"started_at"`
}

// MessageKind distinguishes ordinary successor input from WB's standard
// request to hand control back. Delivery is implemented by a later layer.
type MessageKind string

const (
	MessageKindText           MessageKind = "text"
	MessageKindRequestHandoff MessageKind = "request_handoff"
)

// Message is a courier-neutral durable message addressed by WB session ID.
type Message struct {
	SchemaVersion        int         `json:"schema_version"`
	MessageID            string      `json:"message_id"`
	HandoffID            string      `json:"handoff_id,omitempty"`
	SenderWBSessionID    string      `json:"sender_wb_session_id,omitempty"`
	RecipientWBSessionID string      `json:"recipient_wb_session_id"`
	ReplyToWBSessionID   string      `json:"reply_to_wb_session_id,omitempty"`
	Kind                 MessageKind `json:"kind"`
	Body                 string      `json:"body,omitempty"`
	SentAt               time.Time   `json:"sent_at"`
}

// Phase is one durable point in the handoff state machine. Phase records are
// evidence, not a mutable current-state file; State derives the latest phase
// by reading the ordered append-only event sequence.
type Phase string

const (
	PhaseOffered          Phase = "offered"
	PhaseReceived         Phase = "received"
	PhaseWorktreeReady    Phase = "worktree_ready"
	PhaseSuccessorStarted Phase = "successor_started"
	PhaseCompleted        Phase = "completed"
	PhaseFailed           Phase = "failed"
	PhaseCancelled        Phase = "cancelled"
)

// HandoffEvent is one append-only private state record.
type HandoffEvent struct {
	SchemaVersion int       `json:"schema_version"`
	Sequence      uint64    `json:"sequence"`
	HandoffID     string    `json:"handoff_id"`
	RequestDigest Digest    `json:"request_digest"`
	Phase         Phase     `json:"phase"`
	At            time.Time `json:"at"`
	Diagnostic    string    `json:"diagnostic,omitempty"`
}

// State is the loaded projection of one durable handoff aggregate.
type State struct {
	Request Request        `json:"request"`
	Digest  Digest         `json:"request_digest"`
	Events  []HandoffEvent `json:"events"`
	Receipt *Receipt       `json:"receipt,omitempty"`
}

// Admission reports whether the exact request already existed and, when the
// target completed previously, carries the immutable receipt to return.
type Admission struct {
	Request Request
	Digest  Digest
	Replay  bool
	Receipt *Receipt
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var gitObjectID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func validateID(field, value string) error {
	if !safeID.MatchString(value) {
		return fmt.Errorf("%s %q must start with a letter or digit and contain only letters, digits, dots, underscores, or dashes", field, value)
	}
	return nil
}

func validateSchema(kind string, got, supported int) error {
	if got > supported {
		return fmt.Errorf("%s schema_version %d is newer than supported %d; update wb", kind, got, supported)
	}
	if got != supported {
		return fmt.Errorf("%s schema_version %d is unsupported; want %d", kind, got, supported)
	}
	return nil
}

func (r Request) validate() error {
	if err := validateSchema("session move request", r.SchemaVersion, RequestSchemaVersion); err != nil {
		return err
	}
	for field, value := range map[string]string{
		"handoff_id": r.HandoffID, "successor_wb_session_id": r.SuccessorWBSessionID,
		"predecessor_wb_session_id": r.PredecessorWBSessionID,
		"source_machine":            r.SourceMachine, "target_machine": r.TargetMachine,
	} {
		if err := validateID(field, value); err != nil {
			return err
		}
	}
	if strings.TrimSpace(r.RepositoryRemote) == "" || strings.ContainsAny(r.RepositoryRemote, "\r\n") {
		return fmt.Errorf("repository_remote must be non-empty and single-line")
	}
	if strings.TrimSpace(r.Branch) == "" || strings.ContainsAny(r.Branch, "\r\n") {
		return fmt.Errorf("branch must be non-empty and single-line")
	}
	if !gitObjectID.MatchString(r.SourceWorkCommit) {
		return fmt.Errorf("source_work_commit %q must be a full lowercase Git object ID", r.SourceWorkCommit)
	}
	if !gitObjectID.MatchString(r.BundleCommit) {
		return fmt.Errorf("bundle_commit %q must be a full lowercase Git object ID", r.BundleCommit)
	}
	if err := r.HandoverDigest.validate(); err != nil {
		return fmt.Errorf("handover_digest: %w", err)
	}
	cleanHandover := path.Clean(r.HandoverPath)
	if cleanHandover != r.HandoverPath || !strings.HasPrefix(cleanHandover, ".wb/handoffs/") || strings.HasSuffix(cleanHandover, "/") {
		return fmt.Errorf("handover_path %q must be a clean repository-relative path under .wb/handoffs", r.HandoverPath)
	}
	if strings.TrimSpace(r.SourceRuntime) == "" {
		return fmt.Errorf("source_runtime is required")
	}
	if r.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	return nil
}

func (r Receipt) validate() error {
	if err := validateSchema("session move receipt", r.SchemaVersion, ReceiptSchemaVersion); err != nil {
		return err
	}
	for field, value := range map[string]string{
		"handoff_id": r.HandoffID, "successor_wb_session_id": r.SuccessorWBSessionID,
		"predecessor_wb_session_id": r.PredecessorWBSessionID, "target_machine": r.TargetMachine,
		"tmux_name": r.TmuxName,
	} {
		if err := validateID(field, value); err != nil {
			return err
		}
	}
	if err := r.RequestDigest.validate(); err != nil {
		return fmt.Errorf("request_digest: %w", err)
	}
	if strings.TrimSpace(r.Runtime) == "" {
		return fmt.Errorf("runtime is required")
	}
	if !gitObjectID.MatchString(r.PinnedCommit) {
		return fmt.Errorf("pinned_commit %q must be a full lowercase Git object ID", r.PinnedCommit)
	}
	if r.StartedAt.IsZero() {
		return fmt.Errorf("started_at is required")
	}
	return nil
}

func (m Message) validate() error {
	if err := validateSchema("session message", m.SchemaVersion, MessageSchemaVersion); err != nil {
		return err
	}
	if err := validateID("message_id", m.MessageID); err != nil {
		return err
	}
	if err := validateID("recipient_wb_session_id", m.RecipientWBSessionID); err != nil {
		return err
	}
	for field, value := range map[string]string{
		"handoff_id": m.HandoffID, "sender_wb_session_id": m.SenderWBSessionID, "reply_to_wb_session_id": m.ReplyToWBSessionID,
	} {
		if value != "" {
			if err := validateID(field, value); err != nil {
				return err
			}
		}
	}
	if m.Kind != MessageKindText && m.Kind != MessageKindRequestHandoff {
		return fmt.Errorf("message kind %q is unsupported", m.Kind)
	}
	if m.Kind == MessageKindText && strings.TrimSpace(m.Body) == "" {
		return fmt.Errorf("text message body is required")
	}
	if m.SentAt.IsZero() {
		return fmt.Errorf("sent_at is required")
	}
	return nil
}

// EncodeRequest validates and renders the canonical JSON spelling used by a
// source. A receiver still preserves whichever exact valid bytes it receives.
func EncodeRequest(request Request) ([]byte, error) {
	if err := request.validate(); err != nil {
		return nil, err
	}
	return marshalJSON(request)
}

func DecodeRequest(raw []byte) (Request, error) {
	var request Request
	if err := decodeJSON(raw, &request); err != nil {
		return Request{}, fmt.Errorf("parse session move request: %w", err)
	}
	if err := request.validate(); err != nil {
		return Request{}, err
	}
	return request, nil
}

func EncodeReceipt(receipt Receipt) ([]byte, error) {
	if err := receipt.validate(); err != nil {
		return nil, err
	}
	return marshalJSON(receipt)
}

func DecodeReceipt(raw []byte) (Receipt, error) {
	var receipt Receipt
	if err := decodeJSON(raw, &receipt); err != nil {
		return Receipt{}, fmt.Errorf("parse session move receipt: %w", err)
	}
	if err := receipt.validate(); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

func EncodeMessage(message Message) ([]byte, error) {
	if err := message.validate(); err != nil {
		return nil, err
	}
	return marshalJSON(message)
}

func DecodeMessage(raw []byte) (Message, error) {
	var message Message
	if err := decodeJSON(raw, &message); err != nil {
		return Message{}, fmt.Errorf("parse session message: %w", err)
	}
	if err := message.validate(); err != nil {
		return Message{}, err
	}
	return message, nil
}

func marshalJSON(value any) ([]byte, error) {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func decodeJSON(raw []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}
