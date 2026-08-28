package sessionmove

import (
	"bytes"
	"fmt"
)

// SaveOutgoingMessageSynchestraDispatchUnderLock publishes the first exact
// dispatch returned for an already-durable outbox message.
func (s Store) SaveOutgoingMessageSynchestraDispatchUnderLock(lock *ExecutionLock, handoffID string, requestDigest Digest, identity MessageSynchestraDispatch) (MessageSynchestraDispatch, bool, error) {
	request, handoff, err := s.retainHandoffUnderLock(lock, handoffID, requestDigest)
	if err != nil {
		return MessageSynchestraDispatch{}, false, err
	}
	defer func() { _ = handoff.Close() }()
	route, err := loadRouteAt(handoff, request, requestDigest)
	if err != nil {
		return MessageSynchestraDispatch{}, false, err
	}
	handoffReceipt, _, err := loadReceiptAt(handoff, request, requestDigest)
	if err != nil || handoffReceipt == nil {
		if err != nil {
			return MessageSynchestraDispatch{}, false, err
		}
		return MessageSynchestraDispatch{}, false, fmt.Errorf("message dispatch requires a durable handoff receipt")
	}
	directory, err := openMessageEntryAt(handoff, MessageDirectionOutgoing, identity.MessageID, false)
	if err != nil {
		return MessageSynchestraDispatch{}, false, err
	}
	defer func() { _ = directory.Close() }()
	state, err := loadMessageStateAt(directory, request, MessageDirectionOutgoing, identity.MessageID, handoffReceipt)
	if err != nil {
		return MessageSynchestraDispatch{}, false, err
	}
	identity.SchemaVersion = MessageSynchestraDispatchSchemaVersion
	if err := validateMessageSynchestraDispatch(identity, requestDigest, state, route); err != nil {
		return MessageSynchestraDispatch{}, false, err
	}
	raw, err := marshalJSON(identity)
	if err != nil {
		return MessageSynchestraDispatch{}, false, err
	}
	created, err := publishImmutableAt(directory, messageSynchestraDispatchFileName, raw, 0o600)
	if err != nil {
		return MessageSynchestraDispatch{}, false, err
	}
	existingRaw, err := readImmutableAt(directory, messageSynchestraDispatchFileName, maxSynchestraDispatchBytes, "message synchestra dispatch identity")
	if err != nil {
		return MessageSynchestraDispatch{}, false, err
	}
	if !bytes.Equal(existingRaw, raw) {
		return MessageSynchestraDispatch{}, false, fmt.Errorf("%w: message %s already has a different Synchestra dispatch", ErrHandoffConflict, identity.MessageID)
	}
	existing, err := decodeMessageSynchestraDispatch(existingRaw, requestDigest, state, route)
	return existing, !created, err
}

func (s Store) LoadOutgoingMessageSynchestraDispatchUnderLock(lock *ExecutionLock, handoffID string, requestDigest Digest, messageID string) (MessageSynchestraDispatch, error) {
	request, handoff, err := s.retainHandoffUnderLock(lock, handoffID, requestDigest)
	if err != nil {
		return MessageSynchestraDispatch{}, err
	}
	defer func() { _ = handoff.Close() }()
	route, err := loadRouteAt(handoff, request, requestDigest)
	if err != nil {
		return MessageSynchestraDispatch{}, err
	}
	handoffReceipt, _, err := loadReceiptAt(handoff, request, requestDigest)
	if err != nil || handoffReceipt == nil {
		if err != nil {
			return MessageSynchestraDispatch{}, err
		}
		return MessageSynchestraDispatch{}, fmt.Errorf("message dispatch requires a durable handoff receipt")
	}
	directory, err := openMessageEntryAt(handoff, MessageDirectionOutgoing, messageID, false)
	if err != nil {
		return MessageSynchestraDispatch{}, err
	}
	defer func() { _ = directory.Close() }()
	state, err := loadMessageStateAt(directory, request, MessageDirectionOutgoing, messageID, handoffReceipt)
	if err != nil {
		return MessageSynchestraDispatch{}, err
	}
	raw, err := readImmutableAt(directory, messageSynchestraDispatchFileName, maxSynchestraDispatchBytes, "message synchestra dispatch identity")
	if err != nil {
		return MessageSynchestraDispatch{}, err
	}
	return decodeMessageSynchestraDispatch(raw, requestDigest, state, route)
}

func decodeMessageSynchestraDispatch(raw []byte, requestDigest Digest, state MessageState, route Route) (MessageSynchestraDispatch, error) {
	var identity MessageSynchestraDispatch
	if err := decodeJSON(raw, &identity); err != nil {
		return MessageSynchestraDispatch{}, fmt.Errorf("decode message Synchestra dispatch identity: %w", err)
	}
	if err := validateMessageSynchestraDispatch(identity, requestDigest, state, route); err != nil {
		return MessageSynchestraDispatch{}, err
	}
	return identity, nil
}

func validateMessageSynchestraDispatch(identity MessageSynchestraDispatch, requestDigest Digest, state MessageState, route Route) error {
	if identity.SchemaVersion != MessageSynchestraDispatchSchemaVersion || route.Courier != CourierSynchestra || route.Synchestra == nil ||
		identity.HandoffID != state.Message.HandoffID || identity.RequestDigest != requestDigest ||
		identity.MessageID != state.Message.MessageID || identity.MessageDigest != state.Digest || identity.Runner != route.Synchestra.Runner ||
		identity.InvocationID != state.Message.MessageID || identity.Handler != SynchestraSessionMessageHandler {
		return fmt.Errorf("%w: message Synchestra dispatch does not match exact outbox message and route", ErrHandoffConflict)
	}
	if !synchestraDispatchIDPattern.MatchString(identity.DispatchID) {
		return fmt.Errorf("message Synchestra dispatch_id is invalid")
	}
	return nil
}
