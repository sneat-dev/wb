// Copyright 2026 Sneat Co.

package hub

import (
	"context"
	"errors"
	"testing"
	"time"
)

func testObservation(repository string, at time.Time) PollObservation {
	return PollObservation{
		Repository: repository, FullName: "acme/app", DefaultBranch: "main",
		SHA: "0123456789abcdef0123456789abcdef01234567", ObservedAt: at,
	}
}

// TestPollObservationStoreRemembersRekeysAndForgets is the poller's whole
// memory journey: record a repository, read it back, re-key it under a new
// name after a rename, and drop the name it was renamed away from.
func TestPollObservationStoreRemembersRekeysAndForgets(t *testing.T) {
	ctx := context.Background()
	backend := newFirestoreMemoryBackend()
	store := NewPollObservationStore(backend)
	at := time.Date(2026, 9, 11, 14, 0, 0, 0, time.UTC)

	if _, found, err := store.LoadPollObservation(ctx, "github.com/acme/app"); err != nil || found {
		t.Fatalf("first load = %t, %v; a repository never polled must read as absent", found, err)
	}
	if err := store.SavePollObservation(ctx, testObservation("github.com/acme/app", at)); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := store.LoadPollObservation(ctx, "github.com/acme/app")
	if err != nil || !found || loaded.SHA != "0123456789abcdef0123456789abcdef01234567" || loaded.DefaultBranch != "main" {
		t.Fatalf("load = %+v, %t, %v", loaded, found, err)
	}

	if err := store.SavePollObservation(ctx, testObservation("github.com/acme/renamed", at)); err != nil {
		t.Fatal(err)
	}
	if err := store.DeletePollObservation(ctx, "github.com/acme/app"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.LoadPollObservation(ctx, "github.com/acme/app"); err != nil || found {
		t.Fatalf("old key after rename = %t, %v; it must not be readable again", found, err)
	}
	if _, found, err := store.LoadPollObservation(ctx, "github.com/acme/renamed"); err != nil || !found {
		t.Fatalf("new key after rename = %t, %v", found, err)
	}
	// Deleting a key the poller never wrote is not an error: a rename that
	// crashed between the save and the delete is replayed, not repaired.
	if err := store.DeletePollObservation(ctx, "github.com/acme/never-seen"); err != nil {
		t.Fatalf("delete of an absent observation = %v", err)
	}
}

// TestPollObservationStoreRefusesWhatThePollerCouldNotHaveWritten keeps a
// hand-edited or half-written document from silently suppressing an event:
// an observation that looks current but is not would make the next real push
// read as "no change".
func TestPollObservationStoreRefusesWhatThePollerCouldNotHaveWritten(t *testing.T) {
	at := time.Date(2026, 9, 11, 14, 0, 0, 0, time.UTC)

	for _, testCase := range []struct {
		name    string
		planted PollObservation
	}{
		{"missing sha", PollObservation{Repository: "github.com/acme/app", FullName: "acme/app", DefaultBranch: "main", ObservedAt: at}},
		{"missing branch", PollObservation{Repository: "github.com/acme/app", FullName: "acme/app", SHA: "abc", ObservedAt: at}},
		{"missing full name", PollObservation{Repository: "github.com/acme/app", DefaultBranch: "main", SHA: "abc", ObservedAt: at}},
		{"missing timestamp", PollObservation{Repository: "github.com/acme/app", FullName: "acme/app", DefaultBranch: "main", SHA: "abc"}},
		{"keyed under another repository", PollObservation{Repository: "github.com/acme/other", FullName: "acme/other", DefaultBranch: "main", SHA: "abc", ObservedAt: at}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			backend := newFirestoreMemoryBackend()
			backend.putDocument(pollObservationCollection, pollObservationDocumentID("github.com/acme/app"), testCase.planted)
			if _, _, err := NewPollObservationStore(backend).LoadPollObservation(context.Background(), "github.com/acme/app"); err == nil {
				t.Fatal("a stored observation the poller could not have written must be refused")
			}
		})
	}
}

// TestPollObservationStoreRefusesAnIncompleteWriteOrMissingBackend covers the
// guards that keep a partial observation out of the store in the first place.
func TestPollObservationStoreRefusesAnIncompleteWriteOrMissingBackend(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 9, 11, 14, 0, 0, 0, time.UTC)

	unavailable := NewPollObservationStore(nil)
	if _, _, err := unavailable.LoadPollObservation(ctx, "github.com/acme/app"); !errors.Is(err, errPollObservationStoreUnavailable) {
		t.Fatalf("load without a backend = %v", err)
	}
	if err := unavailable.SavePollObservation(ctx, testObservation("github.com/acme/app", at)); !errors.Is(err, errPollObservationStoreUnavailable) {
		t.Fatalf("save without a backend = %v", err)
	}
	if err := unavailable.DeletePollObservation(ctx, "github.com/acme/app"); !errors.Is(err, errPollObservationStoreUnavailable) {
		t.Fatalf("delete without a backend = %v", err)
	}

	store := NewPollObservationStore(newFirestoreMemoryBackend())
	if _, _, err := store.LoadPollObservation(ctx, "  "); !errors.Is(err, errPollObservationStoreUnavailable) {
		t.Fatalf("load of a blank repository = %v", err)
	}
	if err := store.SavePollObservation(ctx, PollObservation{Repository: "github.com/acme/app"}); !errors.Is(err, errPollObservationStoreUnavailable) {
		t.Fatalf("save of an incomplete observation = %v", err)
	}
	if err := store.DeletePollObservation(ctx, "  "); !errors.Is(err, errPollObservationStoreUnavailable) {
		t.Fatalf("delete of a blank repository = %v", err)
	}
}

// TestPollObservationStoreSurfacesBackendFailures proves a storage fault is
// reported rather than read as "never seen", which would re-enqueue the
// current head on every tick.
func TestPollObservationStoreSurfacesBackendFailures(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 9, 11, 14, 0, 0, 0, time.UTC)
	fault := errors.New("backend is down")

	backend := newFirestoreMemoryBackend()
	backend.failGet = func(string, string) error { return fault }
	if _, _, err := NewPollObservationStore(backend).LoadPollObservation(ctx, "github.com/acme/app"); !errors.Is(err, fault) {
		t.Fatalf("load = %v", err)
	}

	backend = newFirestoreMemoryBackend()
	backend.failSet = func(string, string) error { return fault }
	if err := NewPollObservationStore(backend).SavePollObservation(ctx, testObservation("github.com/acme/app", at)); !errors.Is(err, fault) {
		t.Fatalf("save = %v", err)
	}

	backend = newFirestoreMemoryBackend()
	backend.failDelete = func(string, string) error { return fault }
	if err := NewPollObservationStore(backend).DeletePollObservation(ctx, "github.com/acme/app"); !errors.Is(err, fault) {
		t.Fatalf("delete = %v", err)
	}
}
