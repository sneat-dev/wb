// Copyright 2026 Sneat Co.

package dalgostore_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/adapters/dalgo2memory"
	"github.com/dal-go/dalgo/dal"

	"github.com/sneat-dev/wb/api/githubapp"
	"github.com/sneat-dev/wb/api/githubapp/dalgostore"
)

// widget is a stand-in for a hub document: a plain struct carrying both tag
// families, because dalgo2firestore reads the firestore tags and dalgo2memory
// serializes through encoding/json.
type widget struct {
	Name  string `json:"name" firestore:"name"`
	Kind  string `json:"kind" firestore:"kind"`
	Count int    `json:"count" firestore:"count"`
}

// unserializable cannot be marshalled by dalgo2memory, which is how a write
// failure from the engine is produced without a fault-injection hook.
type unserializable struct {
	Signal chan int `json:"signal"`
}

func newStore(t *testing.T) githubapp.DocumentStore {
	t.Helper()
	return dalgostore.New(dalgo2memory.New(dalgo2memory.FirestoreProfile()))
}

// guardedStore has a schema that declares no collections and forbids undefined
// ones, so every read against it fails with an engine error that is not a
// missing document.
func guardedStore(t *testing.T) githubapp.DocumentStore {
	t.Helper()
	return dalgostore.New(dalgo2memory.New(dalgo2memory.FirestoreProfile(), dalgo2memory.WithSchema(false)))
}

func TestGetReturnsStoredDocument(t *testing.T) {
	ctx, store := context.Background(), newStore(t)
	want := widget{Name: "alpha", Kind: "gear", Count: 3}
	if err := store.Set(ctx, "widgets", "w1", want); err != nil {
		t.Fatalf("Set: %v", err)
	}
	var got widget
	found, err := store.Get(ctx, "widgets", "w1", &got)
	if err != nil || !found {
		t.Fatalf("Get = (%v, %v), want (true, nil)", found, err)
	}
	if got != want {
		t.Fatalf("Get decoded %+v, want %+v", got, want)
	}
}

func TestSetReplacesTheWholeDocument(t *testing.T) {
	ctx, store := context.Background(), newStore(t)
	if err := store.Set(ctx, "widgets", "w1", widget{Name: "alpha", Kind: "gear", Count: 3}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := store.Set(ctx, "widgets", "w1", widget{Name: "alpha"}); err != nil {
		t.Fatalf("Set replacement: %v", err)
	}
	var got widget
	if _, err := store.Get(ctx, "widgets", "w1", &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != (widget{Name: "alpha"}) {
		t.Fatalf("Set left %+v behind, want a full replacement", got)
	}
}

func TestGetReportsAMissingDocumentWithoutAnError(t *testing.T) {
	ctx, store := context.Background(), newStore(t)
	got := widget{Name: "untouched"}
	found, err := store.Get(ctx, "widgets", "missing", &got)
	if found || err != nil {
		t.Fatalf("Get = (%v, %v), want (false, nil)", found, err)
	}
	if got.Name != "untouched" {
		t.Fatalf("Get overwrote out on a miss: %+v", got)
	}
}

func TestGetSurfacesEngineErrors(t *testing.T) {
	found, err := guardedStore(t).Get(context.Background(), "widgets", "w1", &widget{})
	if err == nil || found {
		t.Fatalf("Get = (%v, %v), want an engine error", found, err)
	}
}

func TestGetRejectsANilOut(t *testing.T) {
	if _, err := newStore(t).Get(context.Background(), "widgets", "w1", nil); err == nil {
		t.Fatal("Get with a nil out must fail")
	}
}

func TestNestedCollectionPathsAddressDistinctDocuments(t *testing.T) {
	ctx, store := context.Background(), newStore(t)
	const first = "installations/1/repository_generations/gen-a/chunks"
	const second = "installations/2/repository_generations/gen-a/chunks"
	if err := store.Set(ctx, first, "000000", widget{Name: "one"}); err != nil {
		t.Fatalf("Set %s: %v", first, err)
	}
	if err := store.Set(ctx, second, "000000", widget{Name: "two"}); err != nil {
		t.Fatalf("Set %s: %v", second, err)
	}
	for path, want := range map[string]string{first: "one", second: "two"} {
		var got widget
		found, err := store.Get(ctx, path, "000000", &got)
		if err != nil || !found {
			t.Fatalf("Get %s = (%v, %v)", path, found, err)
		}
		if got.Name != want {
			t.Fatalf("Get %s returned %q, want %q", path, got.Name, want)
		}
	}
}

func TestNestedCollectionQueriesAreScopedToTheirParent(t *testing.T) {
	ctx, store := context.Background(), newStore(t)
	const first = "installations/1/chunks"
	const second = "installations/2/chunks"
	if err := store.Set(ctx, first, "000000", widget{Name: "one"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := store.Set(ctx, second, "000000", widget{Name: "two"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	var got []widget
	if err := store.Query(ctx, first, nil, 0, &got); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 || got[0].Name != "one" {
		t.Fatalf("Query returned %+v, want only the documents under %s", got, first)
	}
}

func TestInvalidCollectionPathsAndIDsAreErrors(t *testing.T) {
	ctx, store := context.Background(), newStore(t)
	for name, test := range map[string]struct{ collection, id string }{
		"even segment count": {"installations/1", "w1"},
		"empty path":         {"", "w1"},
		"empty leaf":         {"installations/1/", "w1"},
		"empty middle":       {"installations//chunks", "w1"},
		"blank id":           {"widgets", "  "},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.Get(ctx, test.collection, test.id, &widget{}); err == nil {
				t.Error("Get accepted an invalid address")
			}
			if err := store.Set(ctx, test.collection, test.id, widget{}); err == nil {
				t.Error("Set accepted an invalid address")
			}
			err := store.UpdateAtomic(ctx, func(tx githubapp.DocumentTransaction) error {
				if _, txErr := tx.Get(ctx, test.collection, test.id, &widget{}); txErr == nil {
					t.Error("transaction Get accepted an invalid address")
				}
				if txErr := tx.Set(ctx, test.collection, test.id, widget{}); txErr == nil {
					t.Error("transaction Set accepted an invalid address")
				}
				if txErr := tx.Delete(ctx, test.collection, test.id); txErr == nil {
					t.Error("transaction Delete accepted an invalid address")
				}
				return nil
			})
			if err != nil {
				t.Errorf("UpdateAtomic: %v", err)
			}
		})
	}
}

func TestQueryRejectsAnInvalidCollectionPath(t *testing.T) {
	var got []widget
	if err := newStore(t).Query(context.Background(), "installations/1", nil, 0, &got); err == nil {
		t.Fatal("Query accepted an even-segment collection path")
	}
}

func TestQueryReadsAWholeCollectionWhenFiltersAreEmpty(t *testing.T) {
	ctx, store := context.Background(), seededStore(t)
	for name, filters := range map[string]map[string]any{
		"nil filters":   nil,
		"empty filters": {},
	} {
		t.Run(name, func(t *testing.T) {
			var got []widget
			if err := store.Query(ctx, "widgets", filters, 0, &got); err != nil {
				t.Fatalf("Query: %v", err)
			}
			if names := widgetNames(got); !reflect.DeepEqual(names, []string{"alpha", "beta", "gamma"}) {
				t.Fatalf("Query returned %v, want every document", names)
			}
		})
	}
}

func TestQueryAppliesEqualityFiltersAndLimits(t *testing.T) {
	ctx, store := context.Background(), seededStore(t)
	var matching []widget
	if err := store.Query(ctx, "widgets", map[string]any{"kind": "gear"}, 0, &matching); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if names := widgetNames(matching); !reflect.DeepEqual(names, []string{"alpha", "gamma"}) {
		t.Fatalf("filtered query returned %v, want the two gears", names)
	}

	// Two filters exercise the sorted-key ordering as well as the conjunction.
	var narrowed []widget
	if err := store.Query(ctx, "widgets", map[string]any{"kind": "gear", "count": 3}, 0, &narrowed); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if names := widgetNames(narrowed); !reflect.DeepEqual(names, []string{"alpha"}) {
		t.Fatalf("two-filter query returned %v, want only alpha", names)
	}

	var limited []widget
	if err := store.Query(ctx, "widgets", map[string]any{"kind": "gear"}, 1, &limited); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("limited query returned %d documents, want 1", len(limited))
	}
}

func TestQueryReplacesTheTargetSliceAndReportsNoMatches(t *testing.T) {
	ctx, store := context.Background(), seededStore(t)
	got := []widget{{Name: "stale"}}
	if err := store.Query(ctx, "widgets", map[string]any{"kind": "absent"}, 0, &got); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Query left %+v in the target slice, want it replaced", got)
	}
}

func TestQueryRejectsAWrongResultType(t *testing.T) {
	ctx, store := context.Background(), newStore(t)
	var notASlice widget
	var notStructs []string
	var nilPointer *[]widget
	for name, out := range map[string]any{
		"nil":                 nil,
		"not a pointer":       []widget{},
		"nil pointer":         nilPointer,
		"pointer to a struct": &notASlice,
		"slice of strings":    &notStructs,
	} {
		t.Run(name, func(t *testing.T) {
			err := store.Query(ctx, "widgets", nil, 0, out)
			if err == nil || !strings.Contains(err.Error(), "slice of structs") {
				t.Fatalf("Query = %v, want a result-type error", err)
			}
		})
	}
}

func TestQuerySurfacesEngineErrors(t *testing.T) {
	var got []widget
	if err := guardedStore(t).Query(context.Background(), "widgets", nil, 0, &got); err == nil {
		t.Fatal("Query must surface an engine error")
	}
}

func TestSetSurfacesEngineErrors(t *testing.T) {
	if err := newStore(t).Set(context.Background(), "widgets", "w1", unserializable{}); err == nil {
		t.Fatal("Set must surface an engine error")
	}
}

// TestSetFallsBackToATransaction covers the engines that do not offer writes
// outside a transaction: dal.DB is only a read session plus a transaction
// coordinator, so Set must not assume the optional write capability.
func TestSetFallsBackToATransaction(t *testing.T) {
	ctx := context.Background()
	db := readOnlySessionDB{DB: dalgo2memory.New(dalgo2memory.FirestoreProfile())}
	if _, isSetter := dal.As[dal.Setter](db); isSetter {
		t.Fatal("the test double must not expose a non-transactional Set")
	}
	store := dalgostore.New(db)
	if err := store.Set(ctx, "widgets", "w1", widget{Name: "alpha"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	var got widget
	found, err := store.Get(ctx, "widgets", "w1", &got)
	if err != nil || !found || got.Name != "alpha" {
		t.Fatalf("Get after the fallback Set = (%+v, %v, %v)", got, found, err)
	}
	if err := store.Set(ctx, "installations/1", "w1", widget{}); err == nil {
		t.Fatal("the fallback path must still reject an invalid address")
	}
}

// readOnlySessionDB embeds the dal.DB interface, so its method set is exactly
// dal.DB's: the non-transactional Set of the underlying adapter is not
// promoted and dal.As cannot recover it.
type readOnlySessionDB struct{ dal.DB }

func TestUpdateAtomicReadsWritesAndDeletes(t *testing.T) {
	ctx, store := context.Background(), newStore(t)
	if err := store.Set(ctx, "widgets", "w1", widget{Name: "alpha", Count: 1}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := store.Set(ctx, "widgets", "w2", widget{Name: "beta"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	err := store.UpdateAtomic(ctx, func(tx githubapp.DocumentTransaction) error {
		var current widget
		found, getErr := tx.Get(ctx, "widgets", "w1", &current)
		if getErr != nil || !found {
			t.Fatalf("transaction Get = (%v, %v)", found, getErr)
		}
		var missing widget
		found, getErr = tx.Get(ctx, "widgets", "gone", &missing)
		if found || getErr != nil {
			t.Fatalf("transaction Get of a missing document = (%v, %v)", found, getErr)
		}
		current.Count += 10
		if setErr := tx.Set(ctx, "widgets", "w1", current); setErr != nil {
			return setErr
		}
		return tx.Delete(ctx, "widgets", "w2")
	})
	if err != nil {
		t.Fatalf("UpdateAtomic: %v", err)
	}
	var updated widget
	if _, getErr := store.Get(ctx, "widgets", "w1", &updated); getErr != nil {
		t.Fatalf("Get: %v", getErr)
	}
	if updated.Count != 11 {
		t.Fatalf("transaction wrote Count=%d, want 11", updated.Count)
	}
	found, getErr := store.Get(ctx, "widgets", "w2", &widget{})
	if found || getErr != nil {
		t.Fatalf("Get of the deleted document = (%v, %v), want (false, nil)", found, getErr)
	}
}

func TestUpdateAtomicPropagatesAWorkerError(t *testing.T) {
	ctx, store := context.Background(), newStore(t)
	sentinel := errors.New("worker refused")
	if err := store.UpdateAtomic(ctx, func(githubapp.DocumentTransaction) error {
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("UpdateAtomic = %v, want the worker's error", err)
	}
}

func TestUpdateAtomicSurfacesTransactionalEngineErrors(t *testing.T) {
	ctx, store := context.Background(), guardedStore(t)
	err := store.UpdateAtomic(ctx, func(tx githubapp.DocumentTransaction) error {
		_, getErr := tx.Get(ctx, "widgets", "w1", &widget{})
		return getErr
	})
	if err == nil {
		t.Fatal("UpdateAtomic must surface an engine read error")
	}
}

func TestUpdateAtomicRequiresACallback(t *testing.T) {
	if err := newStore(t).UpdateAtomic(context.Background(), nil); err == nil {
		t.Fatal("UpdateAtomic with a nil callback must fail")
	}
}

func seededStore(t *testing.T) githubapp.DocumentStore {
	t.Helper()
	ctx, store := context.Background(), newStore(t)
	for id, document := range map[string]widget{
		"w1": {Name: "alpha", Kind: "gear", Count: 3},
		"w2": {Name: "beta", Kind: "spring", Count: 3},
		"w3": {Name: "gamma", Kind: "gear", Count: 7},
	} {
		if err := store.Set(ctx, "widgets", id, document); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	return store
}

// widgetNames sorts the result names because dalgo2memory iterates a map when
// a query carries no ORDER BY, so the engine's order is not reproducible.
func widgetNames(documents []widget) []string {
	names := make([]string, 0, len(documents))
	for _, document := range documents {
		names = append(names, document.Name)
	}
	sort.Strings(names)
	return names
}
