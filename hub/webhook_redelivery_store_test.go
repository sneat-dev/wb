// Copyright 2026 Sneat Co.

package hub

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp"
)

func testRedeliveryRecord(guid string, at time.Time) WebhookRedeliveryRecord {
	return WebhookRedeliveryRecord{GUID: guid, Attempts: 1, LastAttemptAt: at}
}

// TestWebhookRedeliveryStoreRoundTripsListsAndDeletes is the sweep's whole
// memory journey: an unseen GUID reads absent, a saved record reads back
// exactly, ListWebhookRedeliveries enumerates every stored record for
// retention pruning, and deleting a batch removes exactly the named GUIDs,
// leaving an untouched one behind.
func TestWebhookRedeliveryStoreRoundTripsListsAndDeletes(t *testing.T) {
	ctx := context.Background()
	backend := newFirestoreMemoryBackend()
	store := NewWebhookRedeliveryStore(backend)
	at := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

	if _, found, err := store.LoadWebhookRedelivery(ctx, "guid-1"); err != nil || found {
		t.Fatalf("first load = %t, %v; an untouched GUID must read as absent", found, err)
	}

	if err := store.SaveWebhookRedelivery(ctx, testRedeliveryRecord("guid-1", at)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWebhookRedelivery(ctx, WebhookRedeliveryRecord{GUID: "guid-2", Attempts: 3, Abandoned: true, LastAttemptAt: at}); err != nil {
		t.Fatal(err)
	}

	loaded, found, err := store.LoadWebhookRedelivery(ctx, "guid-1")
	if err != nil || !found || loaded.Attempts != 1 || loaded.Abandoned {
		t.Fatalf("load guid-1 = %+v, %t, %v", loaded, found, err)
	}
	loaded, found, err = store.LoadWebhookRedelivery(ctx, "guid-2")
	if err != nil || !found || loaded.Attempts != 3 || !loaded.Abandoned {
		t.Fatalf("load guid-2 = %+v, %t, %v", loaded, found, err)
	}

	records, err := store.ListWebhookRedeliveries(ctx)
	if err != nil || len(records) != 2 {
		t.Fatalf("list = %+v, %v", records, err)
	}

	if err := store.DeleteWebhookRedeliveries(ctx, []string{"guid-1", "guid-never-stored"}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.LoadWebhookRedelivery(ctx, "guid-1"); err != nil || found {
		t.Fatalf("guid-1 after delete = %t, %v", found, err)
	}
	if _, found, err := store.LoadWebhookRedelivery(ctx, "guid-2"); err != nil || !found {
		t.Fatalf("guid-2 after an unrelated delete = %t, %v", found, err)
	}
}

// TestWebhookRedeliveryStoreDeletesInBoundedBatches proves a large prune
// never asks the backend for a transaction bigger than the repository's
// 100-write bound, by counting how many separate UpdateAtomic calls a
// 250-GUID delete makes.
func TestWebhookRedeliveryStoreDeletesInBoundedBatches(t *testing.T) {
	ctx := context.Background()
	backend := newFirestoreMemoryBackend()
	transactions := 0
	guids := make([]string, 0, 250)
	at := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	store := NewWebhookRedeliveryStore(backend)
	for i := 0; i < 250; i++ {
		guid := "guid-" + strconv.Itoa(i)
		guids = append(guids, guid)
		if err := store.SaveWebhookRedelivery(ctx, testRedeliveryRecord(guid, at)); err != nil {
			t.Fatal(err)
		}
	}
	countingBackend := countingUpdateAtomicBackend{firestoreMemoryBackend: backend, calls: &transactions}
	if err := (webhookRedeliveryStore{backend: countingBackend}).DeleteWebhookRedeliveries(ctx, guids); err != nil {
		t.Fatal(err)
	}
	if transactions != 3 {
		t.Fatalf("transactions = %d, want 3 batches of at most 100", transactions)
	}
	records, err := store.ListWebhookRedeliveries(ctx)
	if err != nil || len(records) != 0 {
		t.Fatalf("records left after a full prune = %+v, %v", records, err)
	}
}

// countingUpdateAtomicBackend wraps firestoreMemoryBackend to count how many
// times UpdateAtomic is invoked, without changing its behavior.
type countingUpdateAtomicBackend struct {
	*firestoreMemoryBackend
	calls *int
}

func (backend countingUpdateAtomicBackend) UpdateAtomic(ctx context.Context, update func(githubapp.DocumentTransaction) error) error {
	*backend.calls++
	return backend.firestoreMemoryBackend.UpdateAtomic(ctx, update)
}

var _ githubapp.DocumentStore = countingUpdateAtomicBackend{}

// TestWebhookRedeliveryStoreRefusesWhatTheSweepCouldNotHaveWritten keeps a
// hand-edited or half-written record from silently corrupting the retry
// count: a record that looks current but is not could retry a GUID forever
// or abandon one prematurely.
func TestWebhookRedeliveryStoreRefusesWhatTheSweepCouldNotHaveWritten(t *testing.T) {
	at := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	for _, testCase := range []struct {
		name    string
		planted WebhookRedeliveryRecord
	}{
		{"missing timestamp", WebhookRedeliveryRecord{GUID: "guid-1", Attempts: 1}},
		{"keyed under another guid", WebhookRedeliveryRecord{GUID: "guid-other", Attempts: 1, LastAttemptAt: at}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			backend := newFirestoreMemoryBackend()
			backend.putDocument(webhookRedeliveryCollection, webhookRedeliveryDocumentID("guid-1"), testCase.planted)
			if _, _, err := NewWebhookRedeliveryStore(backend).LoadWebhookRedelivery(context.Background(), "guid-1"); err == nil {
				t.Fatal("a stored record the sweep could not have written must be refused")
			}
		})
	}
}

// TestWebhookRedeliveryStoreRefusesAnIncompleteWriteOrMissingBackend covers
// the guards that keep an invalid record out of the store, and every method
// unavailable without a backend.
func TestWebhookRedeliveryStoreRefusesAnIncompleteWriteOrMissingBackend(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

	unavailable := NewWebhookRedeliveryStore(nil)
	if _, _, err := unavailable.LoadWebhookRedelivery(ctx, "guid-1"); !errors.Is(err, errWebhookRedeliveryStoreUnavailable) {
		t.Fatalf("load without a backend = %v", err)
	}
	if err := unavailable.SaveWebhookRedelivery(ctx, testRedeliveryRecord("guid-1", at)); !errors.Is(err, errWebhookRedeliveryStoreUnavailable) {
		t.Fatalf("save without a backend = %v", err)
	}
	if _, err := unavailable.ListWebhookRedeliveries(ctx); !errors.Is(err, errWebhookRedeliveryStoreUnavailable) {
		t.Fatalf("list without a backend = %v", err)
	}
	if err := unavailable.DeleteWebhookRedeliveries(ctx, []string{"guid-1"}); !errors.Is(err, errWebhookRedeliveryStoreUnavailable) {
		t.Fatalf("delete without a backend = %v", err)
	}

	store := NewWebhookRedeliveryStore(newFirestoreMemoryBackend())
	if _, _, err := store.LoadWebhookRedelivery(ctx, "  "); !errors.Is(err, errWebhookRedeliveryStoreUnavailable) {
		t.Fatalf("load of a blank guid = %v", err)
	}
	if err := store.SaveWebhookRedelivery(ctx, WebhookRedeliveryRecord{GUID: "guid-1"}); !errors.Is(err, errWebhookRedeliveryStoreUnavailable) {
		t.Fatalf("save of an incomplete record = %v", err)
	}
	if err := store.DeleteWebhookRedeliveries(ctx, nil); err != nil {
		t.Fatalf("delete of an empty batch = %v", err)
	}
}

// TestWebhookRedeliveryStoreSurfacesBackendFailures proves a storage fault is
// reported rather than read as "never attempted", which could redeliver the
// same GUID beyond MaxAttempts.
func TestWebhookRedeliveryStoreSurfacesBackendFailures(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	fault := errors.New("backend is down")

	backend := newFirestoreMemoryBackend()
	backend.failGet = func(string, string) error { return fault }
	if _, _, err := NewWebhookRedeliveryStore(backend).LoadWebhookRedelivery(ctx, "guid-1"); !errors.Is(err, fault) {
		t.Fatalf("load = %v", err)
	}

	backend = newFirestoreMemoryBackend()
	backend.failSet = func(string, string) error { return fault }
	if err := NewWebhookRedeliveryStore(backend).SaveWebhookRedelivery(ctx, testRedeliveryRecord("guid-1", at)); !errors.Is(err, fault) {
		t.Fatalf("save = %v", err)
	}

	backend = newFirestoreMemoryBackend()
	backend.failQuery = func(string) error { return fault }
	if _, err := NewWebhookRedeliveryStore(backend).ListWebhookRedeliveries(ctx); !errors.Is(err, fault) {
		t.Fatalf("list = %v", err)
	}

	backend = newFirestoreMemoryBackend()
	backend.failDelete = func(string, string) error { return fault }
	if err := NewWebhookRedeliveryStore(backend).DeleteWebhookRedeliveries(ctx, []string{"guid-1"}); !errors.Is(err, fault) {
		t.Fatalf("delete = %v", err)
	}
}
