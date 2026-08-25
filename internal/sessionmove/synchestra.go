package sessionmove

import (
	"bytes"
	"fmt"
	"regexp"
)

const (
	SynchestraDispatchSchemaVersion        = 1
	SynchestraSessionAcceptHandler         = "wb.session.accept.v1"
	SynchestraSessionMessageHandler        = "wb.session.message.v1"
	MessageSynchestraDispatchSchemaVersion = 1

	synchestraDispatchFileName        = "synchestra-dispatch.json"
	messageSynchestraDispatchFileName = "synchestra-dispatch.json"
	maxSynchestraDispatchBytes        = 16 << 10
)

var synchestraDispatchIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// SynchestraDispatch is the immutable transport identity returned by the
// first accepted typed invocation. Resume observes this exact dispatch rather
// than selecting a new runner or manufacturing a second invocation.
type SynchestraDispatch struct {
	SchemaVersion int    `json:"schema_version"`
	HandoffID     string `json:"handoff_id"`
	RequestDigest Digest `json:"request_digest"`
	Runner        string `json:"runner"`
	InvocationID  string `json:"invocation_id"`
	Handler       string `json:"handler"`
	DispatchID    string `json:"dispatch_id"`
}

// MessageSynchestraDispatch is the exact accepted transport identity for one
// outgoing message. Target MessageID/digest admission remains authoritative:
// a hub invoke ambiguity before this record exists may create another
// dispatch, but it can never authorize a second inbox or paste.
type MessageSynchestraDispatch struct {
	SchemaVersion int    `json:"schema_version"`
	HandoffID     string `json:"handoff_id"`
	RequestDigest Digest `json:"request_digest"`
	MessageID     string `json:"message_id"`
	MessageDigest Digest `json:"message_digest"`
	Runner        string `json:"runner"`
	InvocationID  string `json:"invocation_id"`
	Handler       string `json:"handler"`
	DispatchID    string `json:"dispatch_id"`
}

// SaveSynchestraDispatch publishes one exact invocation-to-dispatch mapping
// beneath the already-admitted handoff. The immutable route corroborates the
// runner; the request corroborates invocation and payload identity.
func (s Store) SaveSynchestraDispatch(identity SynchestraDispatch) (SynchestraDispatch, bool, error) {
	request, digest, _, handoff, err := s.openRouteHandoff(identity.HandoffID)
	if err != nil {
		return SynchestraDispatch{}, false, err
	}
	defer func() { _ = handoff.Close() }()
	route, err := loadRouteAt(handoff, request, digest)
	if err != nil {
		return SynchestraDispatch{}, false, err
	}
	identity.SchemaVersion = SynchestraDispatchSchemaVersion
	if err := validateSynchestraDispatch(identity, request, digest, route); err != nil {
		return SynchestraDispatch{}, false, err
	}
	raw, err := marshalJSON(identity)
	if err != nil {
		return SynchestraDispatch{}, false, err
	}
	if len(raw) > maxSynchestraDispatchBytes {
		return SynchestraDispatch{}, false, fmt.Errorf("synchestra dispatch identity exceeds %d bytes", maxSynchestraDispatchBytes)
	}
	created, err := publishImmutableAt(handoff, synchestraDispatchFileName, raw, 0o600)
	if err != nil {
		return SynchestraDispatch{}, false, fmt.Errorf("publish immutable synchestra dispatch identity: %w", err)
	}
	existingRaw, err := readImmutableAt(handoff, synchestraDispatchFileName, maxSynchestraDispatchBytes, "synchestra dispatch identity")
	if err != nil {
		return SynchestraDispatch{}, false, err
	}
	if !bytes.Equal(existingRaw, raw) {
		return SynchestraDispatch{}, false, fmt.Errorf("%w: handoff %s already has a different synchestra dispatch identity", ErrHandoffConflict, request.HandoffID)
	}
	existing, err := decodeAndValidateSynchestraDispatch(existingRaw, request, digest, route)
	return existing, !created, err
}

// LoadSynchestraDispatch returns the exact accepted dispatch for resume.
func (s Store) LoadSynchestraDispatch(handoffID string) (SynchestraDispatch, error) {
	request, digest, _, handoff, err := s.openRouteHandoff(handoffID)
	if err != nil {
		return SynchestraDispatch{}, err
	}
	defer func() { _ = handoff.Close() }()
	route, err := loadRouteAt(handoff, request, digest)
	if err != nil {
		return SynchestraDispatch{}, err
	}
	raw, err := readImmutableAt(handoff, synchestraDispatchFileName, maxSynchestraDispatchBytes, "synchestra dispatch identity")
	if err != nil {
		return SynchestraDispatch{}, err
	}
	return decodeAndValidateSynchestraDispatch(raw, request, digest, route)
}

func decodeAndValidateSynchestraDispatch(raw []byte, request Request, digest Digest, route Route) (SynchestraDispatch, error) {
	var identity SynchestraDispatch
	if err := decodeJSON(raw, &identity); err != nil {
		return SynchestraDispatch{}, fmt.Errorf("decode synchestra dispatch identity: %w", err)
	}
	if err := validateSynchestraDispatch(identity, request, digest, route); err != nil {
		return SynchestraDispatch{}, err
	}
	return identity, nil
}

func validateSynchestraDispatch(identity SynchestraDispatch, request Request, digest Digest, route Route) error {
	if identity.SchemaVersion != SynchestraDispatchSchemaVersion {
		return fmt.Errorf("synchestra dispatch schema_version %d is unsupported", identity.SchemaVersion)
	}
	if route.Courier != CourierSynchestra || route.Synchestra == nil {
		return fmt.Errorf("handoff %s does not use the synchestra courier", request.HandoffID)
	}
	if identity.HandoffID != request.HandoffID || identity.RequestDigest != digest ||
		identity.Runner != route.Synchestra.Runner || identity.InvocationID != request.HandoffID ||
		identity.Handler != SynchestraSessionAcceptHandler {
		return fmt.Errorf("%w: synchestra dispatch identity does not match the admitted request and route", ErrHandoffConflict)
	}
	if !synchestraDispatchIDPattern.MatchString(identity.DispatchID) {
		return fmt.Errorf("synchestra dispatch_id must be 1-128 letters, digits, dots, underscores, or dashes and start with a letter or digit")
	}
	return nil
}
