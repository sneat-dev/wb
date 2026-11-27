// Package desk is the public wire contract between a WB daemon and Sneat Dev
// Desk (spec/features/desk-connect). It holds only request and response types,
// their validation, and strict decoding; it imports nothing from internal/ so
// the private Desk server can depend on it.
package desk

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"
)

// SchemaVersion is the Desk payload schema this build sends.
const SchemaVersion = 1

// DefaultURL is the hosted Desk origin.
const DefaultURL = "https://desk.sneat.dev"

// Routes, relative to the Desk origin.
const (
	PathConnectStart = "/v0/desk/connect/start"
	PathConnectToken = "/v0/desk/connect/token"
	PathMachineSelf  = "/v0/desk/machines/self"
	PathSnapshot     = PathMachineSelf + "/snapshot"
	PathHeartbeat    = PathMachineSelf + "/heartbeat"
)

// Bounds of the contract.
const (
	MaxGrantSeconds            = 600
	MaxMachineNameLength       = 128
	MaxVersionLength           = 64
	MaxPlatformLength          = 32
	MaxSpaceIDLength           = 128
	MaxCredentialLength        = 1024
	MaxDeviceCodeLength        = 512
	MaxURILength               = 2048
	MaxShares                  = 64
	MaxCompressedSnapshotBytes = 1 << 20
	MaxDecodedSnapshotBytes    = 8 << 20
	// MaxResponseBytes caps every non-snapshot response body a client reads.
	MaxResponseBytes = 64 << 10
)

// Level is a machine's detail level: what the daemon sends.
type Level string

const (
	LevelRepositories Level = "repositories"
	LevelFull         Level = "full"
)

// Valid reports whether level is a machine detail level.
func (level Level) Valid() bool { return level == LevelRepositories || level == LevelFull }

// ShareLevel is what a space shows of a shared machine (desk-spaces).
type ShareLevel string

const (
	ShareSummary      ShareLevel = "summary"
	ShareRepositories ShareLevel = "repositories"
	ShareFull         ShareLevel = "full"
)

// Valid reports whether level is a share level.
func (level ShareLevel) Valid() bool {
	return level == ShareSummary || level == ShareRepositories || level == ShareFull
}

// ShareState is the lifecycle state of a share.
type ShareState string

const (
	SharePending ShareState = "pending"
	ShareActive  ShareState = "active"
)

// Valid reports whether state is a share state a grant can return.
func (state ShareState) Valid() bool { return state == SharePending || state == ShareActive }

// Scope is a Desk machine credential scope.
type Scope string

const (
	ScopeSnapshotPublish Scope = "desk_snapshot:publish"
	ScopeHeartbeatWrite  Scope = "desk_heartbeat:write"
	ScopeMachineReadSelf Scope = "desk_machine:read_self"
)

// Scopes is the exact scope set a desk-connect credential carries.
func Scopes() []Scope {
	return []Scope{ScopeSnapshotPublish, ScopeHeartbeatWrite, ScopeMachineReadSelf}
}

// GrantError is a device-authorization polling outcome (RFC 8628 §3.5).
type GrantError string

const (
	GrantPending      GrantError = "authorization_pending"
	GrantSlowDown     GrantError = "slow_down"
	GrantAccessDenied GrantError = "access_denied"
	GrantExpired      GrantError = "expired_token"
)

// Valid reports whether code is a known grant error.
func (code GrantError) Valid() bool {
	switch code {
	case GrantPending, GrantSlowDown, GrantAccessDenied, GrantExpired:
		return true
	}
	return false
}

// ErrorCode is a typed publish or heartbeat failure recorded by the daemon.
// Desk's response text is never recorded.
type ErrorCode string

const (
	ErrorUnreachable       ErrorCode = "unreachable"
	ErrorTimeout           ErrorCode = "timeout"
	ErrorRevoked           ErrorCode = "revoked"
	ErrorRejectedPayload   ErrorCode = "rejected_payload"
	ErrorSchemaUnsupported ErrorCode = "schema_unsupported"
	ErrorTooLarge          ErrorCode = "too_large"
	ErrorRateLimited       ErrorCode = "rate_limited"
)

// ErrorCodes lists every ErrorCode in a stable order.
func ErrorCodes() []ErrorCode {
	return []ErrorCode{ErrorUnreachable, ErrorTimeout, ErrorRevoked, ErrorRejectedPayload,
		ErrorSchemaUnsupported, ErrorTooLarge, ErrorRateLimited}
}

// Valid reports whether code is a known ErrorCode.
func (code ErrorCode) Valid() bool {
	for _, known := range ErrorCodes() {
		if code == known {
			return true
		}
	}
	return false
}

// ErrInvalid is wrapped by every validation failure.
var ErrInvalid = errors.New("invalid desk payload")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

var (
	safeName  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	safeToken = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)
	// wbVersion admits semver build metadata, as in 0.211.0+dirty.
	wbVersion    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_~-]*$`)
	machineID    = regexp.MustCompile(`^[0-9a-f]{32}$`)
	userCode     = regexp.MustCompile(`^[BCDFGHJKLMNPQRSTVWXZ]{4}-[BCDFGHJKLMNPQRSTVWXZ]{4}$`)
	snapshotHash = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	taskKeyID    = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

func checkText(field, value string, limit int, pattern *regexp.Regexp) error {
	if value == "" || len(value) > limit || !pattern.MatchString(value) {
		return invalid("%s", field)
	}
	return nil
}

func checkHTTPS(field, value string) error {
	if len(value) > MaxURILength || !bytes.HasPrefix([]byte(value), []byte("https://")) {
		return invalid("%s", field)
	}
	return nil
}

func checkVersion(version int) error {
	if version != SchemaVersion {
		return invalid("schema_version")
	}
	return nil
}

// MachineRef names a machine.
type MachineRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Validate checks the machine reference.
func (ref MachineRef) Validate() error {
	if err := checkText("machine.id", ref.ID, 32, machineID); err != nil {
		return err
	}
	return checkText("machine.name", ref.Name, MaxMachineNameLength, safeName)
}

// Share is one space a grant shared the machine into.
type Share struct {
	SpaceID string     `json:"space_id"`
	Level   ShareLevel `json:"level"`
	State   ShareState `json:"state"`
}

func validateShares(personal string, shares []Share) error {
	if err := checkText("personal_space_id", personal, MaxSpaceIDLength, safeName); err != nil {
		return err
	}
	if len(shares) > MaxShares {
		return invalid("shares")
	}
	seen := map[string]bool{personal: true}
	for _, share := range shares {
		if err := checkText("shares.space_id", share.SpaceID, MaxSpaceIDLength, safeName); err != nil {
			return err
		}
		if seen[share.SpaceID] || !share.Level.Valid() || !share.State.Valid() {
			return invalid("shares")
		}
		seen[share.SpaceID] = true
	}
	return nil
}

// ConnectStartRequest opens a device grant.
type ConnectStartRequest struct {
	SchemaVersion int        `json:"schema_version"`
	Machine       MachineRef `json:"machine"`
	WBVersion     string     `json:"wb_version"`
	OS            string     `json:"os"`
	Arch          string     `json:"arch"`
	Detail        Level      `json:"detail"`
	SpaceHint     string     `json:"space_hint,omitempty"`
}

// Validate checks the request.
func (request ConnectStartRequest) Validate() error {
	if err := checkVersion(request.SchemaVersion); err != nil {
		return err
	}
	if err := request.Machine.Validate(); err != nil {
		return err
	}
	if err := checkText("wb_version", request.WBVersion, MaxVersionLength, wbVersion); err != nil {
		return err
	}
	if err := checkText("os", request.OS, MaxPlatformLength, safeName); err != nil {
		return err
	}
	if err := checkText("arch", request.Arch, MaxPlatformLength, safeName); err != nil {
		return err
	}
	if !request.Detail.Valid() {
		return invalid("detail")
	}
	if request.SpaceHint != "" {
		return checkText("space_hint", request.SpaceHint, MaxSpaceIDLength, safeName)
	}
	return nil
}

// ConnectStartResponse is the grant Desk opened.
type ConnectStartResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// Validate checks the response.
func (response ConnectStartResponse) Validate() error {
	if err := checkText("device_code", response.DeviceCode, MaxDeviceCodeLength, safeToken); err != nil {
		return err
	}
	if !userCode.MatchString(response.UserCode) {
		return invalid("user_code")
	}
	if err := checkHTTPS("verification_uri", response.VerificationURI); err != nil {
		return err
	}
	if err := checkHTTPS("verification_uri_complete", response.VerificationURIComplete); err != nil {
		return err
	}
	if response.ExpiresIn < 1 || response.ExpiresIn > MaxGrantSeconds {
		return invalid("expires_in")
	}
	if response.Interval < 1 || response.Interval > response.ExpiresIn {
		return invalid("interval")
	}
	return nil
}

// ConnectTokenRequest polls a device grant.
type ConnectTokenRequest struct {
	DeviceCode string `json:"device_code"`
}

// Validate checks the request.
func (request ConnectTokenRequest) Validate() error {
	return checkText("device_code", request.DeviceCode, MaxDeviceCodeLength, safeToken)
}

// ConnectTokenResponse is either a grant error or an issued credential.
type ConnectTokenResponse struct {
	Error           GrantError `json:"error,omitempty"`
	Credential      string     `json:"credential,omitempty"`
	Machine         MachineRef `json:"machine,omitzero"`
	PersonalSpaceID string     `json:"personal_space_id,omitempty"`
	Shares          []Share    `json:"shares,omitempty"`
	Scopes          []Scope    `json:"scopes,omitempty"`
}

// Validate checks the response: a pending or failed grant carries only its
// error; an issued one carries the credential, the machine, the personal space,
// and exactly the desk-connect scopes.
func (response ConnectTokenResponse) Validate() error {
	if response.Error != "" {
		if !response.Error.Valid() || response.Credential != "" || response.Machine != (MachineRef{}) ||
			response.PersonalSpaceID != "" || len(response.Shares) != 0 || len(response.Scopes) != 0 {
			return invalid("error")
		}
		return nil
	}
	if err := checkText("credential", response.Credential, MaxCredentialLength, safeToken); err != nil {
		return err
	}
	if err := response.Machine.Validate(); err != nil {
		return err
	}
	if err := validateShares(response.PersonalSpaceID, response.Shares); err != nil {
		return err
	}
	want := Scopes()
	if len(response.Scopes) != len(want) {
		return invalid("scopes")
	}
	for index := range want {
		if response.Scopes[index] != want[index] {
			return invalid("scopes")
		}
	}
	return nil
}

// MachineSelf is Desk's record of the calling machine.
type MachineSelf struct {
	Machine          MachineRef `json:"machine"`
	PersonalSpaceID  string     `json:"personal_space_id"`
	Shares           []Share    `json:"shares,omitempty"`
	Detail           Level      `json:"detail"`
	LastSnapshotHash string     `json:"last_snapshot_hash,omitempty"`
	LastSnapshotAt   time.Time  `json:"last_snapshot_at,omitzero"`
	LastHeartbeatAt  time.Time  `json:"last_heartbeat_at,omitzero"`
}

// Validate checks the record.
func (record MachineSelf) Validate() error {
	if err := record.Machine.Validate(); err != nil {
		return err
	}
	if err := validateShares(record.PersonalSpaceID, record.Shares); err != nil {
		return err
	}
	if !record.Detail.Valid() {
		return invalid("detail")
	}
	if (record.LastSnapshotHash == "") != record.LastSnapshotAt.IsZero() {
		return invalid("last_snapshot")
	}
	if record.LastSnapshotHash != "" && !snapshotHash.MatchString(record.LastSnapshotHash) {
		return invalid("last_snapshot_hash")
	}
	return nil
}

// SnapshotRequest publishes the machine's reduced export envelope. The
// envelope is the Cockpit export envelope after the machine's detail-level
// reduction; it is opaque JSON at this boundary.
type SnapshotRequest struct {
	SchemaVersion int             `json:"schema_version"`
	Detail        Level           `json:"detail"`
	TaskKeyID     string          `json:"task_key_id,omitempty"`
	ObservedAt    time.Time       `json:"observed_at"`
	Hash          string          `json:"hash"`
	Envelope      json.RawMessage `json:"envelope"`
}

// Validate checks the request. A `repositories` snapshot must name its task
// key; a `full` one must not. The envelope must carry nothing above the
// declared detail (CheckLevel).
func (request SnapshotRequest) Validate() error {
	if err := checkVersion(request.SchemaVersion); err != nil {
		return err
	}
	if !request.Detail.Valid() {
		return invalid("detail")
	}
	if (request.Detail == LevelRepositories) != (request.TaskKeyID != "") {
		return invalid("task_key_id")
	}
	if request.TaskKeyID != "" && !taskKeyID.MatchString(request.TaskKeyID) {
		return invalid("task_key_id")
	}
	if request.ObservedAt.IsZero() {
		return invalid("observed_at")
	}
	if !snapshotHash.MatchString(request.Hash) {
		return invalid("hash")
	}
	if len(request.Envelope) == 0 || !json.Valid(request.Envelope) || request.Envelope[0] != '{' {
		return invalid("envelope")
	}
	return CheckLevel(request.Envelope, request.Detail)
}

// SnapshotReceipt acknowledges an accepted snapshot.
type SnapshotReceipt struct {
	Hash       string    `json:"hash"`
	AcceptedAt time.Time `json:"accepted_at"`
}

// Validate checks the receipt.
func (receipt SnapshotReceipt) Validate() error {
	if !snapshotHash.MatchString(receipt.Hash) || receipt.AcceptedAt.IsZero() {
		return invalid("receipt")
	}
	return nil
}

// HeartbeatRequest is the small periodic liveness signal.
type HeartbeatRequest struct {
	SchemaVersion    int       `json:"schema_version"`
	ObservedAt       time.Time `json:"observed_at"`
	WBVersion        string    `json:"wb_version"`
	LastSnapshotHash string    `json:"last_snapshot_hash,omitempty"`
}

// Validate checks the request.
func (request HeartbeatRequest) Validate() error {
	if err := checkVersion(request.SchemaVersion); err != nil {
		return err
	}
	if request.ObservedAt.IsZero() {
		return invalid("observed_at")
	}
	if err := checkText("wb_version", request.WBVersion, MaxVersionLength, wbVersion); err != nil {
		return err
	}
	if request.LastSnapshotHash != "" && !snapshotHash.MatchString(request.LastSnapshotHash) {
		return invalid("last_snapshot_hash")
	}
	return nil
}

// SchemaUnsupported is the body of a 426 answer.
type SchemaUnsupported struct {
	MinimumSchemaVersion int `json:"minimum_schema_version"`
}

// Validate checks the body.
func (body SchemaUnsupported) Validate() error {
	if body.MinimumSchemaVersion <= SchemaVersion {
		return invalid("minimum_schema_version")
	}
	return nil
}

// Validator is any contract type.
type Validator interface{ Validate() error }

// Decode reads at most limit bytes of one JSON value into target, refusing
// unknown fields and trailing data, then validates it.
func Decode(reader io.Reader, limit int64, target Validator) error {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return err
	}
	if int64(len(body)) > limit {
		return invalid("body exceeds %d bytes", limit)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return invalid("decode: %v", err)
	}
	if decoder.More() {
		return invalid("trailing data")
	}
	return target.Validate()
}
