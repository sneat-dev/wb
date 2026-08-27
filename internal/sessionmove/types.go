// Package sessionmove defines WB's portable agent-session handoff protocol
// and the local durable aggregate used by both source and target machines.
// Couriers transport these types but do not interpret or own them.
package sessionmove

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	RequestSchemaVersion            = 1
	ReceiptSchemaVersion            = 1
	MessageSchemaVersion            = 1
	MessageReceiptSchemaVersion     = 1
	MessageRecordSchemaVersion      = 1
	MessagePasteIntentSchemaVersion = 1
	EventSchemaVersion              = 1
	DigestAlgorithmSHA256           = "sha256"
	// MaxMessageBodyBytes bounds agent-authored message content before it is
	// admitted to an outbox, transported, or copied into a tmux buffer.
	MaxMessageBodyBytes = 64 << 10
	// MaxSourceOfferFieldBytes bounds each exact Work Log offer field carried
	// by a request. Keeping the source-authored content in the immutable
	// request lets crash repair recreate the offer without parsing Markdown.
	MaxSourceOfferFieldBytes = 64 << 10
	// MaxHandoverContentBytes bounds Request.HandoverContent: the rendered
	// handover document, which wraps an operator-supplied body (itself capped
	// at 1<<20 bytes at the CLI boundary) in a small fixed header. The extra
	// headroom here is for that wrapper, not a separate operator-facing limit.
	MaxHandoverContentBytes = 2 << 20
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
	SchemaVersion          int    `json:"schema_version"`
	HandoffID              string `json:"handoff_id"`
	SuccessorWBSessionID   string `json:"successor_wb_session_id"`
	PredecessorWBSessionID string `json:"predecessor_wb_session_id"`
	SourceMachine          string `json:"source_machine"`
	TargetMachine          string `json:"target_machine"`
	RepositoryRemote       string `json:"repository_remote"`
	Branch                 string `json:"branch"`
	SourceWorkCommit       string `json:"source_work_commit"`
	BundleCommit           string `json:"bundle_commit"`
	// HandoverPath is deprecated and retained only so a handoff admitted by a
	// binary older than the ContinuationPrivate cutover still decodes and
	// replays: it names the repository-relative path, under the pinned
	// worktree, where that older binary committed the handover document into
	// the repo under work. It is set only together with an empty
	// HandoverContent, and no code path emits it anymore.
	HandoverPath   string `json:"handover_path,omitempty"`
	HandoverDigest Digest `json:"handover_digest"`
	// HandoverContent is the rendered handover document itself, carried
	// inline so it never touches the repo under work. A non-empty value
	// means this handoff is delivered as sessionauthority.ContinuationPrivate:
	// the target materializes it as a private 0600 file (see
	// Store.EnsureHandoverUnderLock) and hands the successor that path via
	// WB_SESSION_CONTINUATION_FILE. Every checkpoint created after the
	// ContinuationPrivate cutover sets this and leaves HandoverPath empty.
	HandoverContent       string    `json:"handover_content,omitempty"`
	SourceRuntime         string    `json:"source_runtime"`
	SourceModel           string    `json:"source_model,omitempty"`
	SourceNativeHarnessID string    `json:"source_native_harness_id,omitempty"`
	RequestedHarness      string    `json:"requested_harness,omitempty"`
	WorkLogReference      string    `json:"work_log_reference"`
	SourceOfferMessage    string    `json:"source_offer_message"`
	SourceOfferNextAction string    `json:"source_offer_next_action"`
	SourceOfferDigest     Digest    `json:"source_offer_digest"`
	CreatedAt             time.Time `json:"created_at"`
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
	AttemptID              string    `json:"attempt_id"`
	AttemptIndex           uint64    `json:"attempt_index"`
	PID                    int       `json:"pid"`
	TargetWorkLogReference string    `json:"target_work_log_reference"`
	PinnedCommit           string    `json:"pinned_commit"`
	StartedAt              time.Time `json:"started_at"`
}

// WorkLogReference is the parsed identity of one Work Log claim. Its wire
// spelling stays a single portable string so handoff protocol values do not
// expose a machine-local Work Log directory layout.
type WorkLogReference struct {
	EffortID string
	RunID    string
	ClaimID  string
}

const (
	workLogReferencePrefix       = "worklog:"
	externalHandoffClaimDomain   = "wb.session.external-handoff-claim.v1"
	sourceOfferDigestDomain      = "wb.session.source-offer.v1"
	externalHandoffHashPartCount = 3
)

var workLogClaimID = regexp.MustCompile(`^[0-9a-f]{64}$`)
var launchAttemptID = regexp.MustCompile(`^[0-9]{6}-[0-9a-f]{32}$`)

// ParseWorkLogReference accepts only the canonical
// worklog:<effort>/<run>/<64-lowercase-hex-claim> spelling.
func ParseWorkLogReference(value string) (WorkLogReference, error) {
	if !strings.HasPrefix(value, workLogReferencePrefix) {
		return WorkLogReference{}, fmt.Errorf("work log reference %q must start with %q", value, workLogReferencePrefix)
	}
	parts := strings.Split(strings.TrimPrefix(value, workLogReferencePrefix), "/")
	if len(parts) != 3 {
		return WorkLogReference{}, fmt.Errorf("work log reference %q must be worklog:<effort>/<run>/<64 lowercase hex characters>", value)
	}
	if err := validateID("work log effort", parts[0]); err != nil {
		return WorkLogReference{}, err
	}
	if parts[0] == "." || parts[0] == ".." {
		return WorkLogReference{}, fmt.Errorf("work log effort %q is not a safe path segment", parts[0])
	}
	if err := validateID("work log run", parts[1]); err != nil {
		return WorkLogReference{}, err
	}
	if parts[1] == "." || parts[1] == ".." {
		return WorkLogReference{}, fmt.Errorf("work log run %q is not a safe path segment", parts[1])
	}
	if !workLogClaimID.MatchString(parts[2]) {
		return WorkLogReference{}, fmt.Errorf("work log claim %q must contain exactly 64 lowercase hex characters", parts[2])
	}
	return WorkLogReference{EffortID: parts[0], RunID: parts[1], ClaimID: parts[2]}, nil
}

// String returns the canonical portable reference spelling.
func (reference WorkLogReference) String() string {
	return workLogReferencePrefix + reference.EffortID + "/" + reference.RunID + "/" + reference.ClaimID
}

// ExternalHandoffClaimID derives the stable target claim identity from only
// immutable move identity. Length prefixes prevent field-boundary ambiguity;
// attempt PIDs and timestamps deliberately do not participate.
func ExternalHandoffClaimID(requestDigest Digest, successorWBSessionID string) (string, error) {
	if err := requestDigest.validate(); err != nil {
		return "", fmt.Errorf("request digest: %w", err)
	}
	if err := validateID("successor_wb_session_id", successorWBSessionID); err != nil {
		return "", err
	}
	hasher := sha256.New()
	parts := [externalHandoffHashPartCount]string{
		externalHandoffClaimDomain,
		string(requestDigest),
		successorWBSessionID,
	}
	for _, part := range parts {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(part)))
		_, _ = hasher.Write(length[:])
		_, _ = hasher.Write([]byte(part))
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// NormalizeSourceOfferContent returns the one canonical spelling checkpoint
// writers must place in Request. The exact normalized strings are carried on
// the wire so receipt-time crash repair never has to reverse-parse a handover
// document to recover Work Log event content.
func NormalizeSourceOfferContent(message, nextAction string) (string, string) {
	return strings.TrimSpace(message), strings.TrimSpace(nextAction)
}

// DigestSourceOffer derives the authenticated content digest for an immutable
// source Work Log offer. Length-prefixing the domain and both normalized
// fields prevents boundary ambiguity.
func DigestSourceOffer(message, nextAction string) Digest {
	message, nextAction = NormalizeSourceOfferContent(message, nextAction)
	hasher := sha256.New()
	for _, part := range [...]string{sourceOfferDigestDomain, message, nextAction} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(part)))
		_, _ = hasher.Write(length[:])
		_, _ = hasher.Write([]byte(part))
	}
	return Digest(DigestAlgorithmSHA256 + ":" + hex.EncodeToString(hasher.Sum(nil)))
}

// ExpectedTargetWorkLogReference keeps the source effort and run while
// replacing its predecessor claim with the deterministic external target
// claim. The caller supplies the digest of the exact admitted bytes, which may
// use any valid JSON whitespace; Store or ExecutionLock owns raw-byte proof.
func ExpectedTargetWorkLogReference(request Request, requestDigest Digest) (WorkLogReference, error) {
	if err := request.validate(); err != nil {
		return WorkLogReference{}, err
	}
	if err := requestDigest.validate(); err != nil {
		return WorkLogReference{}, fmt.Errorf("request digest: %w", err)
	}
	source, err := ParseWorkLogReference(request.WorkLogReference)
	if err != nil {
		return WorkLogReference{}, fmt.Errorf("work_log_reference: %w", err)
	}
	claimID, err := ExternalHandoffClaimID(requestDigest, request.SuccessorWBSessionID)
	if err != nil {
		return WorkLogReference{}, err
	}
	return WorkLogReference{EffortID: source.EffortID, RunID: source.RunID, ClaimID: claimID}, nil
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

// MessageReceipt acknowledges only durable target recording plus one
// successful paste into the exact corroborated tmux pane. It deliberately
// contains no field that could imply the harness or agent processed the bytes.
type MessageReceipt struct {
	SchemaVersion        int         `json:"schema_version"`
	MessageID            string      `json:"message_id"`
	MessageDigest        Digest      `json:"message_digest"`
	HandoffID            string      `json:"handoff_id"`
	SenderWBSessionID    string      `json:"sender_wb_session_id"`
	RecipientWBSessionID string      `json:"recipient_wb_session_id"`
	ReplyToWBSessionID   string      `json:"reply_to_wb_session_id"`
	Kind                 MessageKind `json:"kind"`
	TmuxName             string      `json:"tmux_name"`
	PaneID               string      `json:"pane_id"`
	PID                  int         `json:"pid"`
	RecordedAt           time.Time   `json:"recorded_at"`
	PastedAt             time.Time   `json:"pasted_at"`
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
	if r.HandoffID == successorAddressesDirName {
		return fmt.Errorf("handoff_id %q is reserved for WB's completed-successor index", r.HandoffID)
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
	if r.HandoverContent == "" {
		// Legacy pre-cutover shape only: the handover was committed into the
		// pinned worktree at this repository-relative path. Nothing emits this
		// anymore; it is validated only so an already-admitted handoff created
		// before the ContinuationPrivate cutover still decodes and replays.
		cleanHandover := path.Clean(r.HandoverPath)
		if r.HandoverPath == "" || cleanHandover != r.HandoverPath || path.IsAbs(r.HandoverPath) || strings.HasSuffix(cleanHandover, "/") {
			return fmt.Errorf("handover_path %q must be a clean repository-relative path", r.HandoverPath)
		}
	} else {
		if r.HandoverPath != "" {
			return fmt.Errorf("handover_path must be empty when handover_content is set")
		}
		if len(r.HandoverContent) > MaxHandoverContentBytes {
			return fmt.Errorf("handover_content exceeds %d bytes", MaxHandoverContentBytes)
		}
		if !r.HandoverDigest.Matches([]byte(r.HandoverContent)) {
			return fmt.Errorf("handover_content does not match handover_digest")
		}
	}
	if strings.TrimSpace(r.SourceRuntime) == "" {
		return fmt.Errorf("source_runtime is required")
	}
	if _, err := ParseWorkLogReference(r.WorkLogReference); err != nil {
		return fmt.Errorf("work_log_reference: %w", err)
	}
	message, nextAction := NormalizeSourceOfferContent(r.SourceOfferMessage, r.SourceOfferNextAction)
	if message == "" || message != r.SourceOfferMessage {
		return fmt.Errorf("source_offer_message must be non-empty normalized content")
	}
	if len(message) > MaxSourceOfferFieldBytes {
		return fmt.Errorf("source_offer_message exceeds %d bytes", MaxSourceOfferFieldBytes)
	}
	if nextAction == "" || nextAction != r.SourceOfferNextAction {
		return fmt.Errorf("source_offer_next_action must be non-empty normalized content")
	}
	if len(nextAction) > MaxSourceOfferFieldBytes {
		return fmt.Errorf("source_offer_next_action exceeds %d bytes", MaxSourceOfferFieldBytes)
	}
	if err := r.SourceOfferDigest.validate(); err != nil {
		return fmt.Errorf("source_offer_digest: %w", err)
	}
	if expected := DigestSourceOffer(message, nextAction); r.SourceOfferDigest != expected {
		return fmt.Errorf("source_offer_digest %q does not match exact normalized offer content", r.SourceOfferDigest)
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
	if err := validateLaunchAttemptIdentity(r.AttemptID, r.AttemptIndex, r.PID); err != nil {
		return err
	}
	if _, err := ParseWorkLogReference(r.TargetWorkLogReference); err != nil {
		return fmt.Errorf("target_work_log_reference: %w", err)
	}
	if !gitObjectID.MatchString(r.PinnedCommit) {
		return fmt.Errorf("pinned_commit %q must be a full lowercase Git object ID", r.PinnedCommit)
	}
	if r.StartedAt.IsZero() {
		return fmt.Errorf("started_at is required")
	}
	return nil
}

func validateLaunchAttemptIdentity(attemptID string, attemptIndex uint64, pid int) error {
	if !launchAttemptID.MatchString(attemptID) {
		return fmt.Errorf("attempt_id %q must be <6-digit-index>-<32-lowercase-hex-entropy>", attemptID)
	}
	parsed, err := strconv.ParseUint(attemptID[:6], 10, 64)
	if err != nil || parsed == 0 || attemptIndex != parsed {
		return fmt.Errorf("attempt_index %d does not match canonical attempt_id %q", attemptIndex, attemptID)
	}
	if pid <= 0 {
		return fmt.Errorf("pid must be positive")
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
	for field, value := range map[string]string{
		"handoff_id": m.HandoffID, "sender_wb_session_id": m.SenderWBSessionID,
		"recipient_wb_session_id": m.RecipientWBSessionID, "reply_to_wb_session_id": m.ReplyToWBSessionID,
	} {
		if err := validateID(field, value); err != nil {
			return err
		}
	}
	if m.Kind != MessageKindText && m.Kind != MessageKindRequestHandoff {
		return fmt.Errorf("message kind %q is unsupported", m.Kind)
	}
	if m.Kind == MessageKindText && strings.TrimSpace(m.Body) == "" {
		return fmt.Errorf("text message body is required")
	}
	if len(m.Body) > MaxMessageBodyBytes {
		return fmt.Errorf("session message body exceeds %d bytes", MaxMessageBodyBytes)
	}
	if !utf8.ValidString(m.Body) {
		return fmt.Errorf("session message body must be valid UTF-8")
	}
	if m.Kind == MessageKindRequestHandoff && m.Body != "" {
		return fmt.Errorf("request_handoff message must use the standard empty body")
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

func EncodeMessageReceipt(receipt MessageReceipt) ([]byte, error) {
	if err := receipt.validate(); err != nil {
		return nil, err
	}
	return marshalJSON(receipt)
}

func DecodeMessageReceipt(raw []byte) (MessageReceipt, error) {
	var receipt MessageReceipt
	if err := decodeJSON(raw, &receipt); err != nil {
		return MessageReceipt{}, fmt.Errorf("parse session message receipt: %w", err)
	}
	if err := receipt.validate(); err != nil {
		return MessageReceipt{}, err
	}
	return receipt, nil
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
