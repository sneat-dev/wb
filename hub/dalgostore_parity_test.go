// Copyright 2026 Sneat Co.

package hub

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dal-go/dalgo/adapters/dalgo2memory"

	"github.com/sneat-dev/wb/api/githubapp"
	"github.com/sneat-dev/wb/api/githubapp/dalgostore"
	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
)

// newDALgoDocumentStore builds the real dalgostore adapter over an in-memory
// DALgo engine, which is what the provider-owned stores will run on in
// production (dalgo2firestore for the hosted instance, any DALgo engine for a
// self-hoster). The other tests in this package use firestoreMemoryBackend, a
// hand-written map that never serializes a document; these tests exist to
// prove the same journeys survive a real engine — key construction from
// slash-joined collection paths, JSON round-tripping of every stored struct,
// and a genuine transaction.
//
// The strict Firestore profile is deliberate: like the real client it rejects
// a transactional read that follows a write, so these journeys also prove the
// stores order every read before the first write.
func newDALgoDocumentStore() githubapp.DocumentStore {
	return dalgostore.New(dalgo2memory.New(dalgo2memory.FirestoreProfile()))
}

// TestDALgoDocumentStoreRunsTheInstallationJourney walks issue → transition →
// complete over dalgostore, then reads the binding and its entitlements back,
// mirroring TestInstallationStateStoreReadsAndTransitionsAtomically and
// TestBindingCompletionPreservesBindingsAndScopesEntitlements.
func TestDALgoDocumentStoreRunsTheInstallationJourney(t *testing.T) {
	ctx := context.Background()
	states, bindings, entitlements, _ := NewInstallationStores(newDALgoDocumentStore())
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

	currentDigest, nextDigest := InstallationStateDigest{1}, InstallationStateDigest{2}
	current := oauthState(now, 7)
	next := oauthState(now.Add(time.Second), 7)
	if err := states.IssueInstallationState(ctx, currentDigest, current); err != nil {
		t.Fatalf("issue: %v", err)
	}
	if got, err := states.ReadInstallationState(ctx, currentDigest, now); err != nil || got != current {
		t.Fatalf("read = %+v, err %v; want %+v", got, err, current)
	}
	if err := states.TransitionInstallationState(ctx, currentDigest, current, now, nextDigest, next); err != nil {
		t.Fatalf("transition: %v", err)
	}
	// The consumed state must be gone: the transaction's Delete has to reach
	// the engine, not merely the port.
	if _, err := states.ReadInstallationState(ctx, currentDigest, now); !errors.Is(err, ErrInvalidInstallationState) {
		t.Fatalf("replay of the consumed state = %v, want ErrInvalidInstallationState", err)
	}
	if got, err := states.ReadInstallationState(ctx, nextDigest, now); err != nil || got != next {
		t.Fatalf("next state = %+v, err %v; want %+v", got, err, next)
	}

	binding := installationBinding(now, 7, 99, "github.com/acme/app")
	if err := bindings.CompleteIdentityInstallationBinding(ctx, nextDigest, next, now, binding); err != nil {
		t.Fatalf("complete: %v", err)
	}
	// Completion consumes the state document too, so the binding cannot be
	// replayed.
	if err := bindings.CompleteIdentityInstallationBinding(ctx, nextDigest, next, now, binding); !errors.Is(err, ErrInvalidInstallationState) {
		t.Fatalf("replayed completion = %v, want ErrInvalidInstallationState", err)
	}

	// Reading the bindings back exercises Query over a four-level nested
	// collection path and the repository-chunk subcollection beneath it.
	listed, err := bindings.ListIdentityInstallationBindings(ctx, binding.IdentityID)
	if err != nil || len(listed) != 1 {
		t.Fatalf("list = %+v, err %v; want one binding", listed, err)
	}
	if listed[0].Installation.ID != 7 || len(listed[0].Installation.Repositories) != 1 ||
		listed[0].Installation.Repositories[0].Repository != "github.com/acme/app" {
		t.Fatalf("listed binding = %+v, want the completed installation", listed[0])
	}
	allowed, err := entitlements.IdentityHasRepositoryEntitlement(ctx, binding.IdentityID, 7, 99)
	if err != nil || !allowed {
		t.Fatalf("entitlement = %v, err %v; want true", allowed, err)
	}
	allowed, err = entitlements.IdentityHasRepositoryEntitlement(ctx, binding.IdentityID, 7, 100)
	if err != nil || allowed {
		t.Fatalf("entitlement for an unbound repository = %v, err %v; want false", allowed, err)
	}
}

// TestDALgoDocumentStoreRunsTheRepositoryEventJourney walks enqueue → poll →
// acknowledge over dalgostore, mirroring
// TestEnqueueForMachinesIsIdempotentByEventID and
// TestAcknowledgeRetiresDeliveredEventsFromTheMachineQueue.
func TestDALgoDocumentStoreRunsTheRepositoryEventJourney(t *testing.T) {
	ctx := context.Background()
	backend := newDALgoDocumentStore()
	events, status := NewRepositoryEventStore(backend)
	machine := testMachine("m1")
	event := testRepositoryEvent("evt-1")

	result, err := events.EnqueueForMachines(ctx, event, []Machine{machine})
	if err != nil || result != (EnqueueResult{Enqueued: 1}) {
		t.Fatalf("enqueue = %+v, err %v; want one enqueued", result, err)
	}
	// Dedup is keyed by event ID in a separate marker collection, so a replay
	// must be reported rather than fanned out again.
	result, err = events.EnqueueForMachines(ctx, event, []Machine{machine})
	if err != nil || result != (EnqueueResult{Duplicate: true}) {
		t.Fatalf("replayed enqueue = %+v, err %v; want a duplicate", result, err)
	}

	response, err := events.Poll(ctx, machine, "", 10)
	if err != nil || len(response.Events) != 1 || response.Events[0].ID != event.ID {
		t.Fatalf("poll = %+v, err %v; want the enqueued event", response, err)
	}

	pendingBefore, err := pendingRefreshCount(ctx, status, machine.IdentityID)
	if err != nil || pendingBefore != 1 {
		t.Fatalf("pending before ack = %d, err %v; want 1", pendingBefore, err)
	}

	ack, err := events.Acknowledge(ctx, machine, repositoryevent.AckRequest{
		Version:  repositoryevent.ContractVersion,
		Cursor:   response.NextCursor,
		EventIDs: []string{event.ID},
	})
	if err != nil || ack.Cursor != response.NextCursor {
		t.Fatalf("acknowledge = %+v, err %v", ack, err)
	}
	// Acknowledged work leaves the queue and the pending list; both deletions
	// happen inside the transaction, so they prove tx.Delete reaches the
	// engine for nested collection paths.
	response, err = events.Poll(ctx, machine, ack.Cursor, 10)
	if err != nil || len(response.Events) != 0 {
		t.Fatalf("poll after ack = %+v, err %v; want an empty queue", response, err)
	}
	pendingAfter, err := pendingRefreshCount(ctx, status, machine.IdentityID)
	if err != nil || pendingAfter != 0 {
		t.Fatalf("pending after ack = %d, err %v; want 0", pendingAfter, err)
	}
	delivery, _, _, err := status.IdentityRepositoryEventStatus(ctx, machine.IdentityID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if delivery == nil || delivery.LastAcknowledged == nil || delivery.LastAcknowledged.DeliveryID != event.ID {
		t.Fatalf("status delivery = %+v, want the acknowledged event", delivery)
	}
}

func pendingRefreshCount(ctx context.Context, status RepositoryEventStatusStore, identityID string) (int, error) {
	_, pending, _, err := status.IdentityRepositoryEventStatus(ctx, identityID)
	return len(pending), err
}
