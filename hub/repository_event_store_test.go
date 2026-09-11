package hub

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
)

func TestRepositoryEventCursorRoundTripsAndRejectsForeignValues(t *testing.T) {
	for _, sequence := range []int64{0, 1, 42, 1<<62 - 1} {
		cursor := encodeRepositoryEventCursor(sequence)
		decoded, err := decodeRepositoryEventCursor(cursor)
		if err != nil || decoded != sequence {
			t.Fatalf("cursor %q decoded to %d, %v; want %d", cursor, decoded, err, sequence)
		}
	}
	if decoded, err := decodeRepositoryEventCursor(""); err != nil || decoded != 0 {
		t.Fatalf("initial cursor decoded to %d, %v", decoded, err)
	}
	for _, invalid := range []string{"not-base64", "djE6MA", encodeRepositoryEventCursor(1) + "x"} {
		if _, err := decodeRepositoryEventCursor(invalid); !errors.Is(err, repositoryevent.ErrInvalidCursor) {
			t.Fatalf("cursor %q error = %v", invalid, err)
		}
	}
}

func TestUniqueRepositoryEventMachinesDeduplicatesStableMachineID(t *testing.T) {
	machine := Machine{ID: "machine_1", Name: "laptop", IdentityID: "firebase-user"}
	unique, err := uniqueRepositoryEventMachines([]Machine{machine, machine})
	if err != nil || len(unique) != 1 || !reflect.DeepEqual(unique[0], machine) {
		t.Fatalf("unique machines = %+v, %v", unique, err)
	}
	if _, err := uniqueRepositoryEventMachines([]Machine{{ID: "", Name: "laptop", IdentityID: "firebase-user"}}); err == nil {
		t.Fatal("invalid machine accepted")
	}
}

func TestRepositoryEventStorageKeysDoNotExposeIdentityOrDelivery(t *testing.T) {
	delivery := "delivery-private-123"
	if got := repositoryEventDocumentID(delivery); got == delivery || len(got) != 64 {
		t.Fatalf("event document ID = %q", got)
	}
	cursor := encodeRepositoryEventCursor(42)
	if got := repositoryEventPollReceiptID(cursor); got == cursor || len(got) != 64 {
		t.Fatalf("poll receipt ID = %q", got)
	}
}

func testRepositoryEvent(id string) repositoryevent.Event {
	return repositoryevent.Event{Version: repositoryevent.ContractVersion, ID: id, Repository: "github.com/acme/app", Ref: "refs/heads/main", Reason: repositoryevent.ReasonDefaultBranchUpdated, TargetSHA: "0123456789abcdef0123456789abcdef01234567"}
}

func testMachine(id string) Machine {
	return Machine{ID: id, Name: "laptop", IdentityID: "firebase-user-" + id}
}

// TestEnqueueForMachinesIsIdempotentByEventID covers RepositoryEventStore's
// dedup rule: enqueuing the same event ID twice must not double-deliver it,
// and must report the second attempt as a duplicate.
func TestEnqueueForMachinesIsIdempotentByEventID(t *testing.T) {
	store := repositoryEventStore{backend: newFirestoreMemoryBackend(), now: fixedNow(t)}
	event := testRepositoryEvent("evt-1")
	machine := testMachine("m1")

	result, err := store.EnqueueForMachines(context.Background(), event, []Machine{machine})
	if err != nil || result != (EnqueueResult{Enqueued: 1}) {
		t.Fatalf("first enqueue result=%+v err=%v", result, err)
	}
	result, err = store.EnqueueForMachines(context.Background(), event, []Machine{machine})
	if err != nil || result != (EnqueueResult{Duplicate: true}) {
		t.Fatalf("replayed enqueue result=%+v err=%v", result, err)
	}
	response, err := store.Poll(context.Background(), machine, "", 10)
	if err != nil || len(response.Events) != 1 {
		t.Fatalf("poll after replay events=%+v err=%v", response, err)
	}
}

// TestPollDeliversStrictlyOrderedEventsAfterCursor covers Poll's cursor
// contract: events are delivered in ascending enqueue order, only events
// strictly after the supplied cursor are returned, and the response cursor
// advances to the last delivered event so the next poll continues from
// there.
func TestPollDeliversStrictlyOrderedEventsAfterCursor(t *testing.T) {
	store := repositoryEventStore{backend: newFirestoreMemoryBackend(), now: fixedNow(t)}
	machine := testMachine("m1")
	for _, id := range []string{"evt-1", "evt-2", "evt-3"} {
		if _, err := store.EnqueueForMachines(context.Background(), testRepositoryEvent(id), []Machine{machine}); err != nil {
			t.Fatal(err)
		}
	}

	first, err := store.Poll(context.Background(), machine, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 2 || first.Events[0].ID != "evt-1" || first.Events[1].ID != "evt-2" {
		t.Fatalf("first page = %+v", first.Events)
	}
	second, err := store.Poll(context.Background(), machine, first.NextCursor, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Events) != 1 || second.Events[0].ID != "evt-3" {
		t.Fatalf("second page = %+v", second.Events)
	}
	third, err := store.Poll(context.Background(), machine, second.NextCursor, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Events) != 0 || third.NextCursor != second.NextCursor {
		t.Fatalf("third page = %+v", third)
	}
}

// TestPollScopesEventsToTheirOwnMachine covers that Poll only ever returns
// events enqueued for the polling machine, even when other machines have
// their own queued events.
func TestPollScopesEventsToTheirOwnMachine(t *testing.T) {
	store := repositoryEventStore{backend: newFirestoreMemoryBackend(), now: fixedNow(t)}
	machineA, machineB := testMachine("a"), testMachine("b")
	if _, err := store.EnqueueForMachines(context.Background(), testRepositoryEvent("evt-a"), []Machine{machineA}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueForMachines(context.Background(), testRepositoryEvent("evt-b"), []Machine{machineB}); err != nil {
		t.Fatal(err)
	}
	response, err := store.Poll(context.Background(), machineA, "", 10)
	if err != nil || len(response.Events) != 1 || response.Events[0].ID != "evt-a" {
		t.Fatalf("machine A poll = %+v, %v", response, err)
	}
}

// TestAcknowledgeIsIdempotentAndClearsPendingRefreshes covers Acknowledge:
// acknowledging the same cursor twice must not regress the acknowledged
// sequence or error, and acknowledging must clear every pending refresh the
// acknowledged poll delivered.
func TestAcknowledgeIsIdempotentAndClearsPendingRefreshes(t *testing.T) {
	store := repositoryEventStore{backend: newFirestoreMemoryBackend(), now: fixedNow(t)}
	machine := testMachine("m1")
	event := testRepositoryEvent("evt-1")
	if _, err := store.EnqueueForMachines(context.Background(), event, []Machine{machine}); err != nil {
		t.Fatal(err)
	}
	_, pending, _, err := store.IdentityRepositoryEventStatus(context.Background(), machine.IdentityID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending before ack = %+v, %v", pending, err)
	}

	response, err := store.Poll(context.Background(), machine, "", 10)
	if err != nil || len(response.Events) != 1 {
		t.Fatalf("poll = %+v, %v", response, err)
	}
	ackRequest := repositoryevent.AckRequest{Version: repositoryevent.ContractVersion, Cursor: response.NextCursor, EventIDs: []string{event.ID}}
	if _, err := store.Acknowledge(context.Background(), machine, ackRequest); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Acknowledge(context.Background(), machine, ackRequest); err != nil {
		t.Fatalf("replayed acknowledge: %v", err)
	}

	_, pending, _, err = store.IdentityRepositoryEventStatus(context.Background(), machine.IdentityID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after ack = %+v, %v", pending, err)
	}
}

// TestAcknowledgeRejectsMismatchedEventIDs covers that Acknowledge only
// accepts the exact event IDs delivered under a cursor -- a caller cannot
// acknowledge a different or partial event list under someone else's
// cursor.
func TestAcknowledgeRejectsMismatchedEventIDs(t *testing.T) {
	store := repositoryEventStore{backend: newFirestoreMemoryBackend(), now: fixedNow(t)}
	machine := testMachine("m1")
	event := testRepositoryEvent("evt-1")
	if _, err := store.EnqueueForMachines(context.Background(), event, []Machine{machine}); err != nil {
		t.Fatal(err)
	}
	response, err := store.Poll(context.Background(), machine, "", 10)
	if err != nil || len(response.Events) != 1 {
		t.Fatalf("poll = %+v, %v", response, err)
	}
	mismatched := repositoryevent.AckRequest{Version: repositoryevent.ContractVersion, Cursor: response.NextCursor, EventIDs: []string{"not-the-delivered-event"}}
	if _, err := store.Acknowledge(context.Background(), machine, mismatched); !errors.Is(err, repositoryevent.ErrInvalidCursor) {
		t.Fatalf("mismatched acknowledge err=%v", err)
	}
}

// TestAcknowledgeRejectsUnknownCursor covers that a cursor which was never
// handed out by Poll (or belongs to a different machine) cannot be
// acknowledged.
func TestAcknowledgeRejectsUnknownCursor(t *testing.T) {
	store := repositoryEventStore{backend: newFirestoreMemoryBackend(), now: fixedNow(t)}
	machine := testMachine("m1")
	request := repositoryevent.AckRequest{Version: repositoryevent.ContractVersion, Cursor: encodeRepositoryEventCursor(1), EventIDs: []string{"evt-1"}}
	if _, err := store.Acknowledge(context.Background(), machine, request); !errors.Is(err, repositoryevent.ErrInvalidCursor) {
		t.Fatalf("unknown cursor acknowledge err=%v", err)
	}
}

// TestIdentityRepositoryEventStatusReportsReceivedAndAcknowledgedMarkers
// covers RepositoryEventStatusStore.IdentityRepositoryEventStatus: last
// received advances on every enqueue, and last acknowledged advances only
// once Acknowledge succeeds.
func TestIdentityRepositoryEventStatusReportsReceivedAndAcknowledgedMarkers(t *testing.T) {
	store := repositoryEventStore{backend: newFirestoreMemoryBackend(), now: fixedNow(t)}
	machine := testMachine("m1")
	event := testRepositoryEvent("evt-1")
	if _, err := store.EnqueueForMachines(context.Background(), event, []Machine{machine}); err != nil {
		t.Fatal(err)
	}
	delivery, _, _, err := store.IdentityRepositoryEventStatus(context.Background(), machine.IdentityID)
	if err != nil || delivery == nil || delivery.LastReceived == nil || delivery.LastReceived.DeliveryID != event.ID || delivery.LastAcknowledged != nil {
		t.Fatalf("delivery before ack = %+v, %v", delivery, err)
	}
	response, err := store.Poll(context.Background(), machine, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Acknowledge(context.Background(), machine, repositoryevent.AckRequest{Version: repositoryevent.ContractVersion, Cursor: response.NextCursor, EventIDs: []string{event.ID}}); err != nil {
		t.Fatal(err)
	}
	delivery, _, _, err = store.IdentityRepositoryEventStatus(context.Background(), machine.IdentityID)
	if err != nil || delivery == nil || delivery.LastAcknowledged == nil || delivery.LastAcknowledged.DeliveryID != event.ID {
		t.Fatalf("delivery after ack = %+v, %v", delivery, err)
	}
}

// TestEnqueueForMachinesRejectsFanoutAboveBound covers the documented host
// transaction bound on machine fanout per event.
func TestEnqueueForMachinesRejectsFanoutAboveBound(t *testing.T) {
	store := repositoryEventStore{backend: newFirestoreMemoryBackend(), now: fixedNow(t)}
	machines := make([]Machine, maxRepositoryEventFanout+1)
	for i := range machines {
		machines[i] = testMachine(repositoryEventQueueDocumentID(int64(i)))
	}
	if _, err := store.EnqueueForMachines(context.Background(), testRepositoryEvent("evt-1"), machines); err == nil {
		t.Fatal("fanout above bound accepted")
	}
}

// TestEnqueueForMachinesGuardsAndBackendErrors covers EnqueueForMachines'
// guard clause and every backend Get/Set error and stored-data-validation
// branch in its transaction.
func TestEnqueueForMachinesGuardsAndBackendErrors(t *testing.T) {
	machine := testMachine("m1")

	if _, err := (repositoryEventStore{}).EnqueueForMachines(context.Background(), testRepositoryEvent("evt-1"), []Machine{machine}); !errors.Is(err, errRepositoryEventStoreUnavailable) {
		t.Fatalf("nil backend err=%v", err)
	}
	invalidEvent := repositoryevent.Event{}
	if _, err := (repositoryEventStore{backend: newFirestoreMemoryBackend()}).EnqueueForMachines(context.Background(), invalidEvent, []Machine{machine}); !errors.Is(err, errRepositoryEventStoreUnavailable) {
		t.Fatalf("invalid event err=%v", err)
	}
	invalidMachine := Machine{ID: "", Name: "laptop", IdentityID: "firebase-user"}
	if _, err := (repositoryEventStore{backend: newFirestoreMemoryBackend()}).EnqueueForMachines(context.Background(), testRepositoryEvent("evt-1"), []Machine{invalidMachine}); err == nil {
		t.Fatal("expected invalid machine error")
	}

	t.Run("Get marker error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend, now: fixedNow(t)}
		event := testRepositoryEvent("evt-1")
		backend.failGet = failOnID(repositoryEventDocumentID(event.ID))
		if _, err := store.EnqueueForMachines(context.Background(), event, []Machine{machine}); err == nil {
			t.Fatal("expected marker read error")
		}
	})

	t.Run("stored marker invalid", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend, now: fixedNow(t)}
		event := testRepositoryEvent("evt-1")
		backend.putDocument(repositoryEventCollection, repositoryEventDocumentID(event.ID), repositoryEventMarker{EventID: "different-event", Sequence: 1})
		if _, err := store.EnqueueForMachines(context.Background(), event, []Machine{machine}); err == nil {
			t.Fatal("expected invalid stored marker error")
		}
	})

	t.Run("Get sequence error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend, now: fixedNow(t)}
		backend.failGet = failOnID(repositoryEventSequenceDocument)
		if _, err := store.EnqueueForMachines(context.Background(), testRepositoryEvent("evt-1"), []Machine{machine}); err == nil {
			t.Fatal("expected sequence read error")
		}
	})

	t.Run("stored sequence invalid", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend, now: fixedNow(t)}
		backend.putDocument(repositoryEventMetaCollection, repositoryEventSequenceDocument, repositoryEventSequence{Value: -1})
		if _, err := store.EnqueueForMachines(context.Background(), testRepositoryEvent("evt-1"), []Machine{machine}); err == nil {
			t.Fatal("expected invalid stored sequence error")
		}
	})

	t.Run("sequence exhausted", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend, now: fixedNow(t)}
		backend.putDocument(repositoryEventMetaCollection, repositoryEventSequenceDocument, repositoryEventSequence{Value: int64(^uint64(0) >> 1)})
		if _, err := store.EnqueueForMachines(context.Background(), testRepositoryEvent("evt-1"), []Machine{machine}); err == nil {
			t.Fatal("expected sequence exhaustion error")
		}
	})

	t.Run("Set sequence error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend, now: fixedNow(t)}
		backend.failSet = failOnID(repositoryEventSequenceDocument)
		if _, err := store.EnqueueForMachines(context.Background(), testRepositoryEvent("evt-1"), []Machine{machine}); err == nil {
			t.Fatal("expected sequence write error")
		}
	})

	t.Run("Set marker error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend, now: fixedNow(t)}
		event := testRepositoryEvent("evt-1")
		backend.failSet = failOnID(repositoryEventDocumentID(event.ID))
		if _, err := store.EnqueueForMachines(context.Background(), event, []Machine{machine}); err == nil {
			t.Fatal("expected marker write error")
		}
	})

	t.Run("Set queued event error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend, now: fixedNow(t)}
		backend.failSet = failOnCollection(repositoryEventQueueEventsCollection(machine.ID))
		if _, err := store.EnqueueForMachines(context.Background(), testRepositoryEvent("evt-1"), []Machine{machine}); err == nil {
			t.Fatal("expected queued event write error")
		}
	})

	t.Run("Set pending refresh error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend, now: fixedNow(t)}
		backend.failSet = failOnCollection(repositoryEventPendingCollection(machine.IdentityID))
		if _, err := store.EnqueueForMachines(context.Background(), testRepositoryEvent("evt-1"), []Machine{machine}); err == nil {
			t.Fatal("expected pending refresh write error")
		}
	})

	t.Run("Get receipt error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend, now: fixedNow(t)}
		backend.failGet = failOnID(repositoryEventIdentityDocumentID(machine.IdentityID))
		if _, err := store.EnqueueForMachines(context.Background(), testRepositoryEvent("evt-1"), []Machine{machine}); err == nil {
			t.Fatal("expected receipt read error")
		}
	})

	t.Run("Set receipt error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend, now: fixedNow(t)}
		backend.failSet = failOnID(repositoryEventIdentityDocumentID(machine.IdentityID))
		if _, err := store.EnqueueForMachines(context.Background(), testRepositoryEvent("evt-1"), []Machine{machine}); err == nil {
			t.Fatal("expected receipt write error")
		}
	})
}

// TestPollGuardsAndBackendErrors covers Poll's guard clause and every backend
// error and stored-data-validation branch.
func TestPollGuardsAndBackendErrors(t *testing.T) {
	machine := testMachine("m1")

	if _, err := (repositoryEventStore{}).Poll(context.Background(), machine, "", 10); !errors.Is(err, errRepositoryEventStoreUnavailable) {
		t.Fatalf("nil backend err=%v", err)
	}
	backend := newFirestoreMemoryBackend()
	if _, err := (repositoryEventStore{backend: backend}).Poll(context.Background(), Machine{}, "", 10); !errors.Is(err, errRepositoryEventStoreUnavailable) {
		t.Fatalf("invalid machine err=%v", err)
	}
	if _, err := (repositoryEventStore{backend: backend}).Poll(context.Background(), machine, "", 0); !errors.Is(err, errRepositoryEventStoreUnavailable) {
		t.Fatalf("zero limit err=%v", err)
	}
	if _, err := (repositoryEventStore{backend: backend}).Poll(context.Background(), machine, "", repositoryevent.MaxLimit+1); !errors.Is(err, errRepositoryEventStoreUnavailable) {
		t.Fatalf("over-limit err=%v", err)
	}
	if _, err := (repositoryEventStore{backend: backend}).Poll(context.Background(), machine, "not-a-cursor", 10); !errors.Is(err, repositoryevent.ErrInvalidCursor) {
		t.Fatalf("invalid cursor err=%v", err)
	}

	t.Run("Get queue state error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend, now: fixedNow(t)}
		backend.failGet = failOnID(machine.ID)
		if _, err := store.Poll(context.Background(), machine, "", 10); err == nil {
			t.Fatal("expected queue state read error")
		}
	})

	t.Run("stored queue state invalid", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend, now: fixedNow(t)}
		backend.putDocument(repositoryEventQueueCollection, machine.ID, repositoryEventQueueState{AcknowledgedSequence: -1})
		if _, err := store.Poll(context.Background(), machine, "", 10); err == nil {
			t.Fatal("expected invalid stored queue state error")
		}
	})

	t.Run("acknowledged sequence advances the cursor floor", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend, now: fixedNow(t)}
		if _, err := store.EnqueueForMachines(context.Background(), testRepositoryEvent("evt-1"), []Machine{machine}); err != nil {
			t.Fatal(err)
		}
		backend.putDocument(repositoryEventQueueCollection, machine.ID, repositoryEventQueueState{AcknowledgedSequence: 1})
		response, err := store.Poll(context.Background(), machine, "", 10)
		if err != nil || len(response.Events) != 0 {
			t.Fatalf("response=%+v err=%v", response, err)
		}
	})

	t.Run("Query error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend, now: fixedNow(t)}
		backend.failQuery = failQueryOnCollection(repositoryEventQueueEventsCollection(machine.ID))
		if _, err := store.Poll(context.Background(), machine, "", 10); err == nil {
			t.Fatal("expected query error")
		}
	})

	t.Run("stored queued event invalid", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend, now: fixedNow(t)}
		backend.putDocument(repositoryEventQueueEventsCollection(machine.ID), repositoryEventQueueDocumentID(1), queuedRepositoryEvent{Sequence: 1, Event: repositoryevent.Event{}})
		if _, err := store.Poll(context.Background(), machine, "", 10); err == nil {
			t.Fatal("expected invalid stored event error")
		}
	})

	t.Run("stored queued events unordered", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend, now: fixedNow(t)}
		event := testRepositoryEvent("evt-1")
		backend.putDocument(repositoryEventQueueEventsCollection(machine.ID), repositoryEventQueueDocumentID(1), queuedRepositoryEvent{Sequence: 1, Event: event})
		backend.putDocument(repositoryEventQueueEventsCollection(machine.ID), repositoryEventQueueDocumentID(1)+"dup", queuedRepositoryEvent{Sequence: 1, Event: event})
		if _, err := store.Poll(context.Background(), machine, "", 10); err == nil {
			t.Fatal("expected unordered event error")
		}
	})

	t.Run("Set receipt error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend, now: fixedNow(t)}
		if _, err := store.EnqueueForMachines(context.Background(), testRepositoryEvent("evt-1"), []Machine{machine}); err != nil {
			t.Fatal(err)
		}
		backend.failSet = failOnCollection(repositoryEventQueuePollsCollection(machine.ID))
		if _, err := store.Poll(context.Background(), machine, "", 10); err == nil {
			t.Fatal("expected receipt write error")
		}
	})
}

// TestAcknowledgeGuardsAndBackendErrors covers Acknowledge's guard clause and
// every backend error and stored-data-validation branch.
func TestAcknowledgeGuardsAndBackendErrors(t *testing.T) {
	machine := testMachine("m1")
	validRequest := repositoryevent.AckRequest{Version: repositoryevent.ContractVersion, Cursor: encodeRepositoryEventCursor(1), EventIDs: []string{"evt-1"}}

	if _, err := (repositoryEventStore{}).Acknowledge(context.Background(), machine, validRequest); !errors.Is(err, errRepositoryEventStoreUnavailable) {
		t.Fatalf("nil backend err=%v", err)
	}
	backend := newFirestoreMemoryBackend()
	if _, err := (repositoryEventStore{backend: backend}).Acknowledge(context.Background(), Machine{}, validRequest); !errors.Is(err, errRepositoryEventStoreUnavailable) {
		t.Fatalf("invalid machine err=%v", err)
	}
	if _, err := (repositoryEventStore{backend: backend}).Acknowledge(context.Background(), machine, repositoryevent.AckRequest{}); !errors.Is(err, errRepositoryEventStoreUnavailable) {
		t.Fatalf("invalid request err=%v", err)
	}
	badCursor := repositoryevent.AckRequest{Version: repositoryevent.ContractVersion, Cursor: "not-a-cursor", EventIDs: []string{"evt-1"}}
	if _, err := (repositoryEventStore{backend: backend}).Acknowledge(context.Background(), machine, badCursor); !errors.Is(err, repositoryevent.ErrInvalidCursor) {
		t.Fatalf("undecodable cursor err=%v", err)
	}
	zeroSequence := repositoryevent.AckRequest{Version: repositoryevent.ContractVersion, Cursor: encodeRepositoryEventCursor(0), EventIDs: []string{"evt-1"}}
	if _, err := (repositoryEventStore{backend: backend}).Acknowledge(context.Background(), machine, zeroSequence); !errors.Is(err, repositoryevent.ErrInvalidCursor) {
		t.Fatalf("zero sequence err=%v", err)
	}

	t.Run("Get receipt error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend, now: fixedNow(t)}
		event := testRepositoryEvent("evt-1")
		if _, err := store.EnqueueForMachines(context.Background(), event, []Machine{machine}); err != nil {
			t.Fatal(err)
		}
		response, err := store.Poll(context.Background(), machine, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		backend.failGet = failOnID(repositoryEventPollReceiptID(response.NextCursor))
		request := repositoryevent.AckRequest{Version: repositoryevent.ContractVersion, Cursor: response.NextCursor, EventIDs: []string{event.ID}}
		if _, err := store.Acknowledge(context.Background(), machine, request); !errors.Is(err, repositoryevent.ErrInvalidCursor) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("Get queue state error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend, now: fixedNow(t)}
		event := testRepositoryEvent("evt-1")
		if _, err := store.EnqueueForMachines(context.Background(), event, []Machine{machine}); err != nil {
			t.Fatal(err)
		}
		response, err := store.Poll(context.Background(), machine, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		backend.failGet = failOnID(machine.ID)
		request := repositoryevent.AckRequest{Version: repositoryevent.ContractVersion, Cursor: response.NextCursor, EventIDs: []string{event.ID}}
		if _, err := store.Acknowledge(context.Background(), machine, request); err == nil {
			t.Fatal("expected queue state read error")
		}
	})

	t.Run("stored queue state invalid", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend, now: fixedNow(t)}
		event := testRepositoryEvent("evt-1")
		if _, err := store.EnqueueForMachines(context.Background(), event, []Machine{machine}); err != nil {
			t.Fatal(err)
		}
		response, err := store.Poll(context.Background(), machine, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		backend.putDocument(repositoryEventQueueCollection, machine.ID, repositoryEventQueueState{AcknowledgedSequence: -1})
		request := repositoryevent.AckRequest{Version: repositoryevent.ContractVersion, Cursor: response.NextCursor, EventIDs: []string{event.ID}}
		if _, err := store.Acknowledge(context.Background(), machine, request); err == nil {
			t.Fatal("expected invalid stored queue state error")
		}
	})

	t.Run("Set queue state error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend, now: fixedNow(t)}
		event := testRepositoryEvent("evt-1")
		if _, err := store.EnqueueForMachines(context.Background(), event, []Machine{machine}); err != nil {
			t.Fatal(err)
		}
		response, err := store.Poll(context.Background(), machine, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		backend.failSet = failOnID(machine.ID)
		request := repositoryevent.AckRequest{Version: repositoryevent.ContractVersion, Cursor: response.NextCursor, EventIDs: []string{event.ID}}
		if _, err := store.Acknowledge(context.Background(), machine, request); err == nil {
			t.Fatal("expected queue state write error")
		}
	})

	t.Run("Get acknowledgement status error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend, now: fixedNow(t)}
		event := testRepositoryEvent("evt-1")
		if _, err := store.EnqueueForMachines(context.Background(), event, []Machine{machine}); err != nil {
			t.Fatal(err)
		}
		response, err := store.Poll(context.Background(), machine, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		backend.failGet = failOnID(repositoryEventIdentityDocumentID(machine.IdentityID))
		request := repositoryevent.AckRequest{Version: repositoryevent.ContractVersion, Cursor: response.NextCursor, EventIDs: []string{event.ID}}
		if _, err := store.Acknowledge(context.Background(), machine, request); err == nil {
			t.Fatal("expected acknowledgement status read error")
		}
	})

	t.Run("Set acknowledgement status error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend, now: fixedNow(t)}
		event := testRepositoryEvent("evt-1")
		if _, err := store.EnqueueForMachines(context.Background(), event, []Machine{machine}); err != nil {
			t.Fatal(err)
		}
		response, err := store.Poll(context.Background(), machine, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		backend.failSet = failOnID(repositoryEventIdentityDocumentID(machine.IdentityID))
		request := repositoryevent.AckRequest{Version: repositoryevent.ContractVersion, Cursor: response.NextCursor, EventIDs: []string{event.ID}}
		if _, err := store.Acknowledge(context.Background(), machine, request); err == nil {
			t.Fatal("expected acknowledgement status write error")
		}
	})

	t.Run("Delete pending refresh error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend, now: fixedNow(t)}
		event := testRepositoryEvent("evt-1")
		if _, err := store.EnqueueForMachines(context.Background(), event, []Machine{machine}); err != nil {
			t.Fatal(err)
		}
		response, err := store.Poll(context.Background(), machine, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		backend.failDelete = failOnCollection(repositoryEventPendingCollection(machine.IdentityID))
		request := repositoryevent.AckRequest{Version: repositoryevent.ContractVersion, Cursor: response.NextCursor, EventIDs: []string{event.ID}}
		if _, err := store.Acknowledge(context.Background(), machine, request); err == nil {
			t.Fatal("expected pending refresh delete error")
		}
	})

	t.Run("Delete queued event error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend, now: fixedNow(t)}
		event := testRepositoryEvent("evt-1")
		if _, err := store.EnqueueForMachines(context.Background(), event, []Machine{machine}); err != nil {
			t.Fatal(err)
		}
		response, err := store.Poll(context.Background(), machine, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		backend.failDelete = failOnCollection(repositoryEventQueueEventsCollection(machine.ID))
		request := repositoryevent.AckRequest{Version: repositoryevent.ContractVersion, Cursor: response.NextCursor, EventIDs: []string{event.ID}}
		if _, err := store.Acknowledge(context.Background(), machine, request); err == nil {
			t.Fatal("expected queued event delete error")
		}
	})
}

// TestIdentityRepositoryEventStatusGuardsAndBackendErrors covers
// IdentityRepositoryEventStatus' guard clause, backend Get/Query errors, its
// rejection of a malformed stored pending refresh, and the fanout bound that
// truncates the pending list.
func TestIdentityRepositoryEventStatusGuardsAndBackendErrors(t *testing.T) {
	if _, _, _, err := (repositoryEventStore{}).IdentityRepositoryEventStatus(context.Background(), "firebase-user"); !errors.Is(err, errRepositoryEventStoreUnavailable) {
		t.Fatalf("nil backend err=%v", err)
	}
	if _, _, _, err := (repositoryEventStore{backend: newFirestoreMemoryBackend()}).IdentityRepositoryEventStatus(context.Background(), ""); !errors.Is(err, errRepositoryEventStoreUnavailable) {
		t.Fatalf("empty identity err=%v", err)
	}

	t.Run("Get error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend}
		backend.failGet = failOnID(repositoryEventIdentityDocumentID("firebase-user"))
		if _, _, _, err := store.IdentityRepositoryEventStatus(context.Background(), "firebase-user"); err == nil {
			t.Fatal("expected status read error")
		}
	})

	t.Run("Query error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend}
		backend.failQuery = failQueryOnCollection(repositoryEventPendingCollection("firebase-user"))
		if _, _, _, err := store.IdentityRepositoryEventStatus(context.Background(), "firebase-user"); err == nil {
			t.Fatal("expected pending query error")
		}
	})

	t.Run("stored pending refresh invalid", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend}
		backend.putDocument(repositoryEventPendingCollection("firebase-user"), "bad", PendingRefresh{ID: "", Repository: "github.com/acme/app"})
		if _, _, _, err := store.IdentityRepositoryEventStatus(context.Background(), "firebase-user"); err == nil {
			t.Fatal("expected invalid pending refresh error")
		}
	})

	t.Run("fanout bound truncates and sorts pending refreshes", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := repositoryEventStore{backend: backend}
		base := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
		for i := 0; i < maxRepositoryEventFanout+5; i++ {
			id := fmt.Sprintf("machine-%03d", i)
			// Queue newest first so the sort comparator (which the closure at
			// the heart of this rule implements) must actually reorder them.
			queuedAt := base.Add(time.Duration(maxRepositoryEventFanout+5-i) * time.Minute)
			backend.putDocument(repositoryEventPendingCollection("firebase-user"), id, PendingRefresh{ID: "evt-" + id, Repository: "github.com/acme/app", QueuedAt: queuedAt, MachineID: id})
		}
		_, pending, _, err := store.IdentityRepositoryEventStatus(context.Background(), "firebase-user")
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) != maxRepositoryEventFanout {
			t.Fatalf("pending count = %d, want %d", len(pending), maxRepositoryEventFanout)
		}
		for i := 1; i < len(pending); i++ {
			if pending[i].QueuedAt.Before(pending[i-1].QueuedAt) {
				t.Fatalf("pending refreshes not sorted ascending by QueuedAt at index %d", i)
			}
		}
	})
}

// TestDecodeRepositoryEventCursorRejectsSequenceAboveInt64Max covers the
// defensive overflow check in decodeRepositoryEventCursor: a well-formed,
// correctly versioned cursor whose encoded sequence exceeds math.MaxInt64
// must still be rejected rather than silently wrapped to a negative int64.
func TestDecodeRepositoryEventCursorRejectsSequenceAboveInt64Max(t *testing.T) {
	var raw [9]byte
	raw[0] = repositoryevent.ContractVersion
	binary.BigEndian.PutUint64(raw[1:], uint64(1)<<63) // one past math.MaxInt64
	cursor := base64.RawURLEncoding.EncodeToString(raw[:])
	if _, err := decodeRepositoryEventCursor(cursor); !errors.Is(err, repositoryevent.ErrInvalidCursor) {
		t.Fatalf("err=%v", err)
	}
}

func TestNewRepositoryEventStoreWiresBothPorts(t *testing.T) {
	backend := newFirestoreMemoryBackend()
	var events RepositoryEventStore
	var status RepositoryEventStatusStore
	events, status = NewRepositoryEventStore(backend)
	if events == nil || status == nil {
		t.Fatal("NewRepositoryEventStore returned a nil port implementation")
	}
	machine := testMachine("m1")
	if _, err := events.EnqueueForMachines(context.Background(), testRepositoryEvent("evt-1"), []Machine{machine}); err != nil {
		t.Fatal(err)
	}
	delivery, _, _, err := status.IdentityRepositoryEventStatus(context.Background(), machine.IdentityID)
	if err != nil || delivery == nil || delivery.LastReceived == nil {
		t.Fatalf("delivery=%+v err=%v", delivery, err)
	}
}

func fixedNow(t *testing.T) func() time.Time {
	t.Helper()
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return now }
}

// TestAcknowledgeRetiresDeliveredEventsFromTheMachineQueue covers that an
// acknowledged event leaves the per-machine queue: Poll scans that whole
// queue, so an unbounded queue would make every poll cost the machine's full
// history. The dedup marker must survive so a redelivered event stays a no-op.
func TestAcknowledgeRetiresDeliveredEventsFromTheMachineQueue(t *testing.T) {
	backend := newFirestoreMemoryBackend()
	store := repositoryEventStore{backend: backend, now: fixedNow(t)}
	machine := testMachine("m1")
	event := testRepositoryEvent("evt-1")
	if _, err := store.EnqueueForMachines(context.Background(), event, []Machine{machine}); err != nil {
		t.Fatal(err)
	}
	response, err := store.Poll(context.Background(), machine, "", 10)
	if err != nil || len(response.Events) != 1 {
		t.Fatalf("poll = %+v, %v", response, err)
	}
	queueKey := repositoryEventQueueEventsCollection(machine.ID) + "/" + repositoryEventQueueDocumentID(1)
	if _, ok := backend.documents[queueKey]; !ok {
		t.Fatalf("queued event %s missing before acknowledge", queueKey)
	}
	ackRequest := repositoryevent.AckRequest{Version: repositoryevent.ContractVersion, Cursor: response.NextCursor, EventIDs: []string{event.ID}}
	if _, err := store.Acknowledge(context.Background(), machine, ackRequest); err != nil {
		t.Fatal(err)
	}
	if _, ok := backend.documents[queueKey]; ok {
		t.Fatalf("queued event %s still present after acknowledge", queueKey)
	}
	if _, err := store.EnqueueForMachines(context.Background(), event, []Machine{machine}); err != nil {
		t.Fatalf("redelivered event after acknowledge: %v", err)
	}
	if _, ok := backend.documents[queueKey]; ok {
		t.Fatalf("redelivered acknowledged event %s was requeued", event.ID)
	}
	next, err := store.Poll(context.Background(), machine, response.NextCursor, 10)
	if err != nil || len(next.Events) != 0 {
		t.Fatalf("poll after acknowledge = %+v, %v", next, err)
	}
}
