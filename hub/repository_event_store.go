package hub

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/sneat-dev/wb/api/githubapp"
	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
)

const (
	repositoryEventCollection       = "workbench_repository_events"
	repositoryEventQueueCollection  = "workbench_repository_event_queues"
	repositoryEventMetaCollection   = "workbench_repository_event_meta"
	repositoryEventSequenceDocument = "sequence"
	repositoryEventQueueEvents      = "events"
	repositoryEventQueuePolls       = "polls"
	repositoryEventStatusCollection = "workbench_repository_event_status"
	repositoryEventStatusPending    = "pending"
	maxRepositoryEventFanout        = 150
)

var errRepositoryEventStoreUnavailable = errors.New("workbench repository event store is unavailable")

// NewRepositoryEventStore wires the provider-owned repository event store to
// a host's github.com/sneat-dev/wb/api/githubapp DocumentStore. The
// returned value implements both RepositoryEventStore and
// RepositoryEventStatusStore.
func NewRepositoryEventStore(backend githubapp.DocumentStore) (RepositoryEventStore, RepositoryEventStatusStore) {
	store := repositoryEventStore{backend: backend}
	return store, store
}

type repositoryEventSequence struct {
	Value int64 `firestore:"value"`
}

type repositoryEventMarker struct {
	EventID    string    `firestore:"event_id"`
	Sequence   int64     `firestore:"sequence"`
	EnqueuedAt time.Time `firestore:"enqueued_at"`
}

type queuedRepositoryEvent struct {
	Sequence   int64                 `firestore:"sequence"`
	IdentityID string                `firestore:"identity_id"`
	MachineID  string                `firestore:"machine_id"`
	EnqueuedAt time.Time             `firestore:"enqueued_at"`
	Event      repositoryevent.Event `firestore:"event"`
}

type repositoryEventPollReceipt struct {
	MachineID   string                  `firestore:"machine_id"`
	IdentityID  string                  `firestore:"identity_id"`
	Cursor      string                  `firestore:"cursor"`
	Events      []repositoryevent.Event `firestore:"events"`
	Sequences   []int64                 `firestore:"sequences"`
	DeliveredAt time.Time               `firestore:"delivered_at"`
}

type repositoryEventIdentityState struct {
	LastReceived     *StatusDeliveryMarker `firestore:"last_received,omitempty"`
	LastAcknowledged *StatusDeliveryMarker `firestore:"last_acknowledged,omitempty"`
}

type repositoryEventQueueState struct {
	AcknowledgedSequence int64     `firestore:"acknowledged_sequence"`
	AcknowledgedAt       time.Time `firestore:"acknowledged_at"`
}

type repositoryEventStore struct {
	backend githubapp.DocumentStore
	now     func() time.Time
}

func (store repositoryEventStore) nowFunc() time.Time {
	if store.now != nil {
		return store.now().UTC()
	}
	return time.Now().UTC()
}

func (store repositoryEventStore) EnqueueForMachines(ctx context.Context, event repositoryevent.Event, machines []Machine) (result EnqueueResult, err error) {
	if store.backend == nil || event.Validate() != nil {
		return EnqueueResult{}, errRepositoryEventStoreUnavailable
	}
	machines, err = uniqueRepositoryEventMachines(machines)
	if err != nil {
		return EnqueueResult{}, err
	}
	if len(machines) > maxRepositoryEventFanout {
		return EnqueueResult{}, fmt.Errorf("repository event fanout exceeds host transaction bound of %d machines", maxRepositoryEventFanout)
	}
	now := store.nowFunc()
	eventDocumentID := repositoryEventDocumentID(event.ID)
	err = store.backend.UpdateAtomic(ctx, func(transaction githubapp.DocumentTransaction) error {
		var marker repositoryEventMarker
		found, getErr := transaction.Get(ctx, repositoryEventCollection, eventDocumentID, &marker)
		if getErr != nil {
			return fmt.Errorf("read repository event marker: %w", getErr)
		}
		if found {
			if marker.EventID != event.ID || marker.Sequence < 1 {
				return errors.New("repository event deduplication record is invalid")
			}
			result = EnqueueResult{Duplicate: true}
			return nil
		}

		var sequence repositoryEventSequence
		found, getErr = transaction.Get(ctx, repositoryEventMetaCollection, repositoryEventSequenceDocument, &sequence)
		if getErr != nil {
			return fmt.Errorf("read repository event sequence: %w", getErr)
		}
		if found && sequence.Value < 0 {
			return errors.New("repository event sequence record is invalid")
		}
		if sequence.Value == int64(^uint64(0)>>1) {
			return errors.New("repository event sequence is exhausted")
		}
		// Firestore transactions reject any read that follows a write, so
		// every identity receipt is read here, before the first Set.
		identityStates := make(map[string]repositoryEventIdentityState)
		for _, machine := range machines {
			if _, seen := identityStates[machine.IdentityID]; seen {
				continue
			}
			var state repositoryEventIdentityState
			if _, getErr := transaction.Get(ctx, repositoryEventStatusCollection, repositoryEventIdentityDocumentID(machine.IdentityID), &state); getErr != nil {
				return fmt.Errorf("read repository event receipt: %w", getErr)
			}
			identityStates[machine.IdentityID] = state
		}

		sequence.Value++
		if setErr := transaction.Set(ctx, repositoryEventMetaCollection, repositoryEventSequenceDocument, sequence); setErr != nil {
			return fmt.Errorf("advance repository event sequence: %w", setErr)
		}
		if setErr := transaction.Set(ctx, repositoryEventCollection, eventDocumentID, repositoryEventMarker{EventID: event.ID, Sequence: sequence.Value, EnqueuedAt: now}); setErr != nil {
			return fmt.Errorf("write repository event marker: %w", setErr)
		}

		for _, machine := range machines {
			queued := queuedRepositoryEvent{Sequence: sequence.Value, IdentityID: machine.IdentityID, MachineID: machine.ID, EnqueuedAt: now, Event: event}
			if setErr := transaction.Set(ctx, repositoryEventQueueEventsCollection(machine.ID), repositoryEventQueueDocumentID(sequence.Value), queued); setErr != nil {
				return fmt.Errorf("write machine repository event: %w", setErr)
			}
			pending := PendingRefresh{ID: event.ID, Repository: event.Repository, Event: string(event.Reason), QueuedAt: now, MachineID: machine.ID}
			if setErr := transaction.Set(ctx, repositoryEventPendingCollection(machine.IdentityID), repositoryEventPendingID(machine.ID, event.ID), pending); setErr != nil {
				return fmt.Errorf("write pending repository refresh: %w", setErr)
			}
		}
		lastReceived := StatusDeliveryMarker{DeliveryID: event.ID, Event: string(event.Reason), OccurredAt: now}
		for identityID, state := range identityStates {
			state.LastReceived = &lastReceived
			if setErr := transaction.Set(ctx, repositoryEventStatusCollection, repositoryEventIdentityDocumentID(identityID), state); setErr != nil {
				return fmt.Errorf("write repository event receipt: %w", setErr)
			}
		}
		result = EnqueueResult{Enqueued: len(machines)}
		return nil
	})
	if err != nil {
		return EnqueueResult{}, err
	}
	return result, nil
}

func (store repositoryEventStore) Poll(ctx context.Context, machine Machine, cursor string, limit int) (repositoryevent.PollResponse, error) {
	if store.backend == nil || !validRepositoryEventMachine(machine) || limit < 1 || limit > repositoryevent.MaxLimit {
		return repositoryevent.PollResponse{}, errRepositoryEventStoreUnavailable
	}
	after, err := decodeRepositoryEventCursor(cursor)
	if err != nil {
		return repositoryevent.PollResponse{}, err
	}
	var state repositoryEventQueueState
	found, getErr := store.backend.Get(ctx, repositoryEventQueueCollection, machine.ID, &state)
	if getErr != nil {
		return repositoryevent.PollResponse{}, fmt.Errorf("read repository event queue state: %w", getErr)
	}
	if found {
		if state.AcknowledgedSequence < 0 {
			return repositoryevent.PollResponse{}, errors.New("repository event queue state is invalid")
		}
		if state.AcknowledgedSequence > after {
			after = state.AcknowledgedSequence
		}
	}

	// The port's Query has no OrderBy/StartAfter: it is an equality-filter
	// scan with a flat limit. So the ascending, strictly-ordered walk that
	// selects the next `limit` events after `after` is done here in Go
	// instead of pushed down to Firestore.
	var queued []queuedRepositoryEvent
	if err := store.backend.Query(ctx, repositoryEventQueueEventsCollection(machine.ID), nil, 0, &queued); err != nil {
		return repositoryevent.PollResponse{}, fmt.Errorf("list machine repository events: %w", err)
	}
	sort.Slice(queued, func(i, j int) bool { return queued[i].Sequence < queued[j].Sequence })
	events := make([]repositoryevent.Event, 0, limit)
	sequences := make([]int64, 0, limit)
	nextSequence := after
	for _, item := range queued {
		if item.Sequence <= after {
			continue
		}
		if item.Sequence <= nextSequence || item.Event.Validate() != nil {
			return repositoryevent.PollResponse{}, errors.New("stored machine repository event is invalid or unordered")
		}
		nextSequence = item.Sequence
		events = append(events, item.Event)
		sequences = append(sequences, item.Sequence)
		if len(events) == limit {
			break
		}
	}
	nextCursor := encodeRepositoryEventCursor(nextSequence)
	response := repositoryevent.PollResponse{Version: repositoryevent.ContractVersion, Cursor: cursor, NextCursor: nextCursor, Events: events}
	if len(events) == 0 {
		return response, nil
	}
	receipt := repositoryEventPollReceipt{
		MachineID:   machine.ID,
		IdentityID:  machine.IdentityID,
		Cursor:      nextCursor,
		Events:      append([]repositoryevent.Event(nil), events...),
		Sequences:   sequences,
		DeliveredAt: store.nowFunc(),
	}
	if setErr := store.backend.Set(ctx, repositoryEventQueuePollsCollection(machine.ID), repositoryEventPollReceiptID(nextCursor), receipt); setErr != nil {
		return repositoryevent.PollResponse{}, fmt.Errorf("record repository event delivery: %w", setErr)
	}
	return response, nil
}

func (store repositoryEventStore) Acknowledge(ctx context.Context, machine Machine, request repositoryevent.AckRequest) (repositoryevent.AckResponse, error) {
	if store.backend == nil || !validRepositoryEventMachine(machine) || request.Validate() != nil {
		return repositoryevent.AckResponse{}, errRepositoryEventStoreUnavailable
	}
	sequence, err := decodeRepositoryEventCursor(request.Cursor)
	if err != nil || sequence < 1 {
		return repositoryevent.AckResponse{}, repositoryevent.ErrInvalidCursor
	}
	now := store.nowFunc()
	err = store.backend.UpdateAtomic(ctx, func(transaction githubapp.DocumentTransaction) error {
		var receipt repositoryEventPollReceipt
		found, getErr := transaction.Get(ctx, repositoryEventQueuePollsCollection(machine.ID), repositoryEventPollReceiptID(request.Cursor), &receipt)
		if getErr != nil || !found {
			return repositoryevent.ErrInvalidCursor
		}
		if receipt.MachineID != machine.ID || receipt.IdentityID != machine.IdentityID || receipt.Cursor != request.Cursor || !slices.Equal(repositoryEventIDs(receipt.Events), request.EventIDs) {
			return repositoryevent.ErrInvalidCursor
		}
		var state repositoryEventQueueState
		found, getErr = transaction.Get(ctx, repositoryEventQueueCollection, machine.ID, &state)
		if getErr != nil {
			return fmt.Errorf("read repository event acknowledgement: %w", getErr)
		}
		if found && state.AcknowledgedSequence < 0 {
			return errors.New("repository event queue state is invalid")
		}
		if sequence > state.AcknowledgedSequence {
			// Read the identity status before the first write: Firestore
			// transactions reject reads that follow writes.
			var identityState repositoryEventIdentityState
			if _, getErr := transaction.Get(ctx, repositoryEventStatusCollection, repositoryEventIdentityDocumentID(machine.IdentityID), &identityState); getErr != nil {
				return fmt.Errorf("read repository event acknowledgement status: %w", getErr)
			}
			state.AcknowledgedSequence = sequence
			state.AcknowledgedAt = now
			if setErr := transaction.Set(ctx, repositoryEventQueueCollection, machine.ID, state); setErr != nil {
				return fmt.Errorf("write repository event acknowledgement: %w", setErr)
			}
			lastEvent := receipt.Events[len(receipt.Events)-1]
			lastAcknowledged := StatusDeliveryMarker{DeliveryID: lastEvent.ID, Event: string(lastEvent.Reason), OccurredAt: state.AcknowledgedAt}
			identityState.LastAcknowledged = &lastAcknowledged
			if setErr := transaction.Set(ctx, repositoryEventStatusCollection, repositoryEventIdentityDocumentID(machine.IdentityID), identityState); setErr != nil {
				return fmt.Errorf("write repository event acknowledgement status: %w", setErr)
			}
		}
		for _, event := range receipt.Events {
			if deleteErr := transaction.Delete(ctx, repositoryEventPendingCollection(machine.IdentityID), repositoryEventPendingID(machine.ID, event.ID)); deleteErr != nil {
				return fmt.Errorf("clear pending repository refresh: %w", deleteErr)
			}
		}
		// Acknowledged events leave the machine queue so that Poll, which
		// reads the whole per-machine queue (the port has no range query),
		// only ever scans undelivered work. Dedup lives in the marker
		// collection keyed by event ID, so this deletion cannot readmit an
		// event.
		for _, queued := range receipt.Sequences {
			if deleteErr := transaction.Delete(ctx, repositoryEventQueueEventsCollection(machine.ID), repositoryEventQueueDocumentID(queued)); deleteErr != nil {
				return fmt.Errorf("retire acknowledged repository event: %w", deleteErr)
			}
		}
		return nil
	})
	if err != nil {
		return repositoryevent.AckResponse{}, err
	}
	return repositoryevent.AckResponse{Version: repositoryevent.ContractVersion, Cursor: request.Cursor}, nil
}

func (store repositoryEventStore) IdentityRepositoryEventStatus(ctx context.Context, identityID string) (*StatusDelivery, []PendingRefresh, []StatusError, error) {
	if store.backend == nil || identityID == "" {
		return nil, nil, nil, errRepositoryEventStoreUnavailable
	}
	var state repositoryEventIdentityState
	found, err := store.backend.Get(ctx, repositoryEventStatusCollection, repositoryEventIdentityDocumentID(identityID), &state)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read repository event status: %w", err)
	}
	var delivery *StatusDelivery
	if found {
		delivery = &StatusDelivery{LastReceived: state.LastReceived, LastAcknowledged: state.LastAcknowledged}
	}
	var pending []PendingRefresh
	if err := store.backend.Query(ctx, repositoryEventPendingCollection(identityID), nil, 0, &pending); err != nil {
		return nil, nil, nil, fmt.Errorf("list pending repository refreshes: %w", err)
	}
	for _, refresh := range pending {
		if refresh.ID == "" || refresh.Repository == "" || refresh.QueuedAt.IsZero() || refresh.MachineID == "" {
			return nil, nil, nil, errors.New("pending repository refresh is invalid")
		}
	}
	sort.SliceStable(pending, func(i, j int) bool { return pending[i].QueuedAt.Before(pending[j].QueuedAt) })
	if len(pending) > maxRepositoryEventFanout {
		pending = pending[:maxRepositoryEventFanout]
	}
	return delivery, pending, []StatusError{}, nil
}

func uniqueRepositoryEventMachines(machines []Machine) ([]Machine, error) {
	seen := make(map[string]bool, len(machines))
	unique := make([]Machine, 0, len(machines))
	for _, machine := range machines {
		if !validRepositoryEventMachine(machine) {
			return nil, errRepositoryEventStoreUnavailable
		}
		if seen[machine.ID] {
			continue
		}
		seen[machine.ID] = true
		unique = append(unique, machine)
	}
	return unique, nil
}

func validRepositoryEventMachine(machine Machine) bool {
	return machine.ID != "" && machine.Name != "" && machine.IdentityID != ""
}

func repositoryEventQueueEventsCollection(machineID string) string {
	return repositoryEventQueueCollection + "/" + machineID + "/" + repositoryEventQueueEvents
}

func repositoryEventQueuePollsCollection(machineID string) string {
	return repositoryEventQueueCollection + "/" + machineID + "/" + repositoryEventQueuePolls
}

func repositoryEventPendingCollection(identityID string) string {
	return repositoryEventStatusCollection + "/" + repositoryEventIdentityDocumentID(identityID) + "/" + repositoryEventStatusPending
}

func repositoryEventIdentityDocumentID(identityID string) string {
	digest := sha256.Sum256([]byte(identityID))
	return hex.EncodeToString(digest[:])
}

func repositoryEventDocumentID(eventID string) string {
	digest := sha256.Sum256([]byte(eventID))
	return hex.EncodeToString(digest[:])
}

func repositoryEventQueueDocumentID(sequence int64) string {
	return fmt.Sprintf("%020d", sequence)
}

func repositoryEventPollReceiptID(cursor string) string {
	digest := sha256.Sum256([]byte(cursor))
	return hex.EncodeToString(digest[:])
}

func repositoryEventPendingID(machineID, eventID string) string {
	digest := sha256.Sum256([]byte(machineID + "\x00" + eventID))
	return hex.EncodeToString(digest[:])
}

func repositoryEventIDs(events []repositoryevent.Event) []string {
	ids := make([]string, len(events))
	for index := range events {
		ids[index] = events[index].ID
	}
	return ids
}

func encodeRepositoryEventCursor(sequence int64) string {
	var raw [9]byte
	raw[0] = repositoryevent.ContractVersion
	binary.BigEndian.PutUint64(raw[1:], uint64(sequence))
	return base64.RawURLEncoding.EncodeToString(raw[:])
}

func decodeRepositoryEventCursor(cursor string) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(raw) != 9 || raw[0] != repositoryevent.ContractVersion {
		return 0, repositoryevent.ErrInvalidCursor
	}
	sequence := binary.BigEndian.Uint64(raw[1:])
	if sequence > uint64(^uint64(0)>>1) {
		return 0, repositoryevent.ErrInvalidCursor
	}
	return int64(sequence), nil
}

var _ RepositoryEventStore = repositoryEventStore{}
var _ RepositoryEventStatusStore = repositoryEventStore{}
