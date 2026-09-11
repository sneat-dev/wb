// Copyright 2026 Sneat Co.

// Package dalgostore implements githubapp.DocumentStore on DALgo.
//
// The bench hub persists through the deliberately small githubapp.DocumentStore
// seam: a hierarchical document database addressed by a slash-joined collection
// path plus a document id. This package is the one implementation wb ships, and
// it is written against github.com/dal-go/dalgo rather than any single vendor's
// SDK — so the hosted instance runs on dalgo2firestore while a self-hoster
// supplies any other DALgo engine, including OpenVaultDB. See
// spec/decisions/0002-bench-open-source-and-self-hosting.md, step 4.
//
// The seam's semantics are narrow on purpose and are reproduced exactly:
//
//   - a missing document is (false, nil) from Get, never an error;
//   - Query takes equality filters only, applied in sorted key order so the
//     composite index a backend needs is stable, with limit 0 meaning
//     unbounded;
//   - Set replaces the whole document;
//   - UpdateAtomic runs a read-write transaction and, because DALgo may retry
//     a conflicted worker, may invoke its callback more than once.
package dalgostore

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"

	"github.com/sneat-dev/wb/api/githubapp"
)

// New returns a githubapp.DocumentStore backed by db.
func New(db dal.DB) githubapp.DocumentStore { return documentStore{db: db} }

type documentStore struct{ db dal.DB }

var _ githubapp.DocumentStore = documentStore{}

func (store documentStore) Get(ctx context.Context, collection, id string, out any) (bool, error) {
	return getDocument(ctx, store.db, collection, id, out)
}

// Set replaces one whole document. dal.DB is a read session plus a transaction
// coordinator; writing outside a transaction is an optional capability, so an
// engine that does not offer it gets a single-write transaction instead of a
// failure.
func (store documentStore) Set(ctx context.Context, collection, id string, value any) error {
	if setter, ok := dal.As[dal.Setter](store.db); ok {
		return setDocument(ctx, setter, collection, id, value)
	}
	return store.db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return setDocument(ctx, tx, collection, id, value)
	})
}

// Query runs an equality-filtered scan of one collection. equals may be nil or
// empty, in which case the whole collection is read. out must be a pointer to a
// slice of structs; it is replaced (never appended to) with the documents in
// the order the engine returns them.
func (store documentStore) Query(ctx context.Context, collection string, equals map[string]any, limit int, out any) error {
	target, element, err := sliceTarget(out)
	if err != nil {
		return err
	}
	parent, name, err := splitCollectionPath(collection)
	if err != nil {
		return err
	}
	collectionRef := dal.NewRootCollectionRef(name, "")
	if parent != nil {
		collectionRef = dal.NewCollectionRef(name, "", parent)
	}
	var builder dal.IQueryBuilder = dal.From(collectionRef).NewQuery()
	// Sorted key order keeps the filter sequence — and therefore the composite
	// index a backend asks for — stable across runs, which a map range would
	// not be.
	for _, field := range sortedKeys(equals) {
		builder = builder.WhereField(field, dal.Equal, equals[field])
	}
	if limit > 0 {
		builder = builder.Limit(limit)
	}
	query := builder.SelectIntoRecord(func() record.Record {
		return record.NewRecordWithIncompleteKey(name, reflect.String, reflect.New(element).Interface())
	})
	records, err := dal.ExecuteQueryAndReadAllToRecords(ctx, query, store.db)
	if err != nil {
		return fmt.Errorf("query %s: %w", collection, err)
	}
	documents := reflect.MakeSlice(target.Type(), 0, len(records))
	for _, found := range records {
		documents = reflect.Append(documents, reflect.ValueOf(found.Data()).Elem())
	}
	target.Set(documents)
	return nil
}

// UpdateAtomic runs update inside a DALgo read-write transaction. DALgo retries
// a conflicted worker, so update may run more than once — which is exactly what
// githubapp.DocumentStore documents.
func (store documentStore) UpdateAtomic(ctx context.Context, update func(githubapp.DocumentTransaction) error) error {
	if update == nil {
		return errors.New("dalgostore: UpdateAtomic requires a callback")
	}
	return store.db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return update(documentTransaction{ctx: ctx, tx: tx})
	})
}

// documentTransaction carries the transactional context DALgo hands the worker.
// The port passes a context to every method for symmetry with the store, but a
// DALgo transaction is bound to the worker's context, so that one is used and
// the per-call context is deliberately ignored: honouring it would run the
// operation outside the transaction.
type documentTransaction struct {
	ctx context.Context
	tx  dal.ReadwriteTransaction
}

var _ githubapp.DocumentTransaction = documentTransaction{}

func (transaction documentTransaction) Get(_ context.Context, collection, id string, out any) (bool, error) {
	return getDocument(transaction.ctx, transaction.tx, collection, id, out)
}

func (transaction documentTransaction) Set(_ context.Context, collection, id string, value any) error {
	return setDocument(transaction.ctx, transaction.tx, collection, id, value)
}

func (transaction documentTransaction) Delete(_ context.Context, collection, id string) error {
	key, err := keyFor(collection, id)
	if err != nil {
		return err
	}
	return transaction.tx.Delete(transaction.ctx, key)
}

func getDocument(ctx context.Context, getter dal.Getter, collection, id string, out any) (bool, error) {
	key, err := keyFor(collection, id)
	if err != nil {
		return false, err
	}
	if out == nil {
		return false, fmt.Errorf("dalgostore: reading %s/%s requires a non-nil out", collection, id)
	}
	// A DALgo adapter reports a missing document as an error wrapping
	// record.ErrRecordNotFound (dalgo2firestore maps gRPC codes.NotFound to it,
	// dalgo2memory returns it from its engine). The port's contract is
	// (false, nil) with out untouched, so that one error is the only one
	// translated rather than returned.
	if err = getter.Get(ctx, record.NewRecordWithData(key, out)); err != nil {
		if record.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func setDocument(ctx context.Context, setter dal.Setter, collection, id string, value any) error {
	key, err := keyFor(collection, id)
	if err != nil {
		return err
	}
	return setter.Set(ctx, record.NewRecordWithData(key, value))
}

// keyFor turns a slash-joined collection path and a document id into a DALgo
// key with its full ancestor chain: "a/1/b" with id "2" becomes the key for
// document b/2 under a/1.
func keyFor(collectionPath, id string) (*record.Key, error) {
	parent, name, err := splitCollectionPath(collectionPath)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("dalgostore: document id is required in collection %q", collectionPath)
	}
	if parent == nil {
		return record.NewKeyWithID(name, id), nil
	}
	return record.NewKeyWithParentAndID(parent, name, id), nil
}

// splitCollectionPath validates a collection path and returns the parent key
// chain above it, plus the leaf collection name. A path alternates
// collection/document/collection/…, so it always has an odd number of
// non-empty segments. An invalid path is an error, never a panic: the paths
// are built from stored document ids and generation strings, so a malformed
// one must surface as a store error rather than take the process down.
func splitCollectionPath(collectionPath string) (parent *record.Key, name string, err error) {
	segments := strings.Split(collectionPath, "/")
	if len(segments)%2 == 0 {
		return nil, "", fmt.Errorf("dalgostore: collection path %q has %d segments, want an odd number (collection/document/collection/…)", collectionPath, len(segments))
	}
	for _, segment := range segments {
		if strings.TrimSpace(segment) == "" {
			return nil, "", fmt.Errorf("dalgostore: collection path %q has an empty segment", collectionPath)
		}
	}
	for i := 0; i+1 < len(segments); i += 2 {
		if parent == nil {
			parent = record.NewKeyWithID(segments[i], segments[i+1])
			continue
		}
		parent = record.NewKeyWithParentAndID(parent, segments[i], segments[i+1])
	}
	return parent, segments[len(segments)-1], nil
}

// sliceTarget checks that out is a non-nil pointer to a slice of structs and
// returns the addressable slice plus its element type.
func sliceTarget(out any) (reflect.Value, reflect.Type, error) {
	pointer := reflect.ValueOf(out)
	if !pointer.IsValid() || pointer.Kind() != reflect.Pointer || pointer.IsNil() {
		return reflect.Value{}, nil, fmt.Errorf("dalgostore: query result must be a non-nil pointer to a slice of structs, got %T", out)
	}
	target := pointer.Elem()
	if target.Kind() != reflect.Slice || target.Type().Elem().Kind() != reflect.Struct {
		return reflect.Value{}, nil, fmt.Errorf("dalgostore: query result must be a non-nil pointer to a slice of structs, got %T", out)
	}
	return target, target.Type().Elem(), nil
}

func sortedKeys(equals map[string]any) []string {
	if len(equals) == 0 {
		return nil
	}
	fields := make([]string, 0, len(equals))
	for field := range equals {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	return fields
}
