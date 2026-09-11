// Copyright 2026 Sneat Co.

package hub

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"github.com/sneat-dev/wb/api/githubapp"
)

// firestoreMemoryBackend is a small in-memory stand-in for
// github.com/sneat-dev/wb/api/githubapp.FirestoreBackend, used to exercise
// the provider-owned stores without a real Firestore. The wb module has its
// own equivalent fake (firestoreFake in api/githubapp/firestore_test.go), but
// it is unexported in a _test.go file and so cannot be imported here.
type firestoreMemoryBackend struct {
	documents map[string]any

	// Fault-injection hooks let tests exercise the provider stores' backend
	// error branches without a real Firestore. Each hook is optional (nil
	// means "never fail") and is consulted before the corresponding
	// operation runs; a returned non-nil error is surfaced exactly as a real
	// backend error would be. Hooks are plain closures rather than
	// collection-keyed maps so a test can target one specific call (by
	// collection, document id, or an internal call counter) without
	// accidentally failing an unrelated call that happens to share a
	// collection name.
	failGet    func(collection, id string) error
	failQuery  func(collection string) error
	failSet    func(collection, id string) error
	failDelete func(collection, id string) error
}

func newFirestoreMemoryBackend() *firestoreMemoryBackend {
	return &firestoreMemoryBackend{documents: map[string]any{}}
}

// putDocument seeds a document directly into the backend's storage, bypassing
// Set. Tests use it to plant malformed or inconsistent stored documents (for
// example a partially-written index) that a store's own write path would
// never produce, in order to exercise the store's defensive validation of
// data it reads back.
func (b *firestoreMemoryBackend) putDocument(collection, id string, value any) {
	b.documents[collection+"/"+id] = value
}

func (b *firestoreMemoryBackend) Get(_ context.Context, collection, id string, out any) (bool, error) {
	if b.failGet != nil {
		if err := b.failGet(collection, id); err != nil {
			return false, err
		}
	}
	value, ok := b.documents[collection+"/"+id]
	if !ok {
		return false, nil
	}
	reflect.ValueOf(out).Elem().Set(reflect.ValueOf(value))
	return true, nil
}

// Query returns every document directly under collection (ignoring further
// nested subcollections) whose type matches the slice element type of out.
// It ignores filters: none of the provider-owned stores use equality filters
// today, since every collection path is already scoped to its owner.
func (b *firestoreMemoryBackend) Query(_ context.Context, collection string, _ map[string]any, limit int, out any) error {
	if b.failQuery != nil {
		if err := b.failQuery(collection); err != nil {
			return err
		}
	}
	result := reflect.MakeSlice(reflect.ValueOf(out).Elem().Type(), 0, 0)
	prefix := collection + "/"
	for key, value := range b.documents {
		if !strings.HasPrefix(key, prefix) || strings.Contains(strings.TrimPrefix(key, prefix), "/") {
			continue
		}
		if reflect.TypeOf(value) != result.Type().Elem() {
			continue
		}
		result = reflect.Append(result, reflect.ValueOf(value))
		if limit > 0 && result.Len() == limit {
			break
		}
	}
	reflect.ValueOf(out).Elem().Set(result)
	return nil
}

func (b *firestoreMemoryBackend) Set(_ context.Context, collection, id string, value any) error {
	if b.failSet != nil {
		if err := b.failSet(collection, id); err != nil {
			return err
		}
	}
	b.documents[collection+"/"+id] = value
	return nil
}

func (b *firestoreMemoryBackend) UpdateAtomic(ctx context.Context, update func(githubapp.FirestoreTransaction) error) error {
	return update(firestoreMemoryTransaction{backend: b})
}

type firestoreMemoryTransaction struct{ backend *firestoreMemoryBackend }

func (tx firestoreMemoryTransaction) Get(ctx context.Context, collection, id string, out any) (bool, error) {
	return tx.backend.Get(ctx, collection, id, out)
}
func (tx firestoreMemoryTransaction) Set(ctx context.Context, collection, id string, value any) error {
	return tx.backend.Set(ctx, collection, id, value)
}
func (tx firestoreMemoryTransaction) Delete(_ context.Context, collection, id string) error {
	if tx.backend.failDelete != nil {
		if err := tx.backend.failDelete(collection, id); err != nil {
			return err
		}
	}
	delete(tx.backend.documents, collection+"/"+id)
	return nil
}

var _ githubapp.FirestoreBackend = (*firestoreMemoryBackend)(nil)
var _ githubapp.FirestoreTransaction = firestoreMemoryTransaction{}

var errFirestoreMemoryFault = errors.New("firestore memory backend fault injected by test")

// failOnID returns a fault-injection hook that fails exactly the call whose
// document id equals id, regardless of collection. It is the common case:
// most stores use a distinct document id per logical read/write within a
// single transaction, so matching on id alone is precise enough to target
// one specific backend call.
func failOnID(id string) func(collection, docID string) error {
	return func(_, docID string) error {
		if docID == id {
			return errFirestoreMemoryFault
		}
		return nil
	}
}

// failOnCollection returns a fault-injection hook that fails every call
// against the given collection, regardless of document id.
func failOnCollection(collection string) func(collection, docID string) error {
	return func(c, _ string) error {
		if c == collection {
			return errFirestoreMemoryFault
		}
		return nil
	}
}

// failQueryOnCollection returns a Query fault-injection hook that fails every
// call against the given collection.
func failQueryOnCollection(collection string) func(c string) error {
	return func(c string) error {
		if c == collection {
			return errFirestoreMemoryFault
		}
		return nil
	}
}
