// Package hubstore opens the DALgo engine a self-hosted bench hub runs on and
// wraps it in the githubapp.DocumentStore seam every hub-owned store writes
// through.
//
// The engine is configuration, not code: memory for tests and throwaway runs,
// inGitDB for durable inspectable files under ~/.wb/hub, OpenVaultDB for an
// operator who already runs one. The hosted instance keeps dalgo2firestore in
// sneat-go and is untouched by any of this. See
// spec/decisions/0002-bench-open-source-and-self-hosting.md.
package hubstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/dal-go/dalgo/adapters/dalgo2memory"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/ddl"
	"github.com/dal-go/dalgo2openvaultdb"
	"github.com/ingitdb/dalgo2ingitdb"
	"github.com/ingitdb/ingitdb-go/ingitdb/validator"

	"github.com/sneat-dev/wb/api/githubapp"
	"github.com/sneat-dev/wb/api/githubapp/dalgostore"
	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/internal/hubconfig"
)

const defaultOpenVaultDatabaseID = "wb"

// nopCloser lets Open always return a non-nil Closer, so a caller's `defer
// closer.Close()` never needs a nil check.
type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// Open builds the configured engine and returns it behind the document-store
// seam. The returned Closer releases whatever the engine holds; it is never
// nil.
func Open(ctx context.Context, store hubconfig.Store) (githubapp.DocumentStore, io.Closer, error) {
	db, closer, err := openDB(ctx, store)
	if err != nil {
		return nil, nopCloser{}, err
	}
	return dalgostore.New(db), closer, nil
}

func openDB(ctx context.Context, store hubconfig.Store) (dal.DB, io.Closer, error) {
	switch store.Engine {
	case hubconfig.EngineMemory, "":
		// The Firestore profile is the strict one: like the real client it
		// rejects a transactional read that follows a write, so a journey
		// that passes here cannot be relying on looser ordering.
		return dalgo2memory.New(dalgo2memory.FirestoreProfile()), nopCloser{}, nil
	case hubconfig.EngineInGitDB:
		return openInGitDB(ctx, store.Path)
	case hubconfig.EngineOpenVaultDB:
		databaseID := store.DatabaseID
		if databaseID == "" {
			databaseID = defaultOpenVaultDatabaseID
		}
		db, err := dalgo2openvaultdb.NewDB(store.URL, databaseID)
		if err != nil {
			return nil, nopCloser{}, fmt.Errorf("open openvaultdb hub store at %s: %w", store.URL, err)
		}
		return db, nopCloser{}, nil
	default:
		return nil, nopCloser{}, fmt.Errorf("hub.store.engine %q is not supported", store.Engine)
	}
}

// openInGitDB creates the project directory when it is missing and declares
// every collection the hub writes to. inGitDB is schema-first: a collection
// that has no definition on disk is "not found in definition" rather than
// created on first write, and nested paths must be declared as subcollections
// of their root. Declaring them here is idempotent, so a restart over an
// existing project is a no-op.
func openInGitDB(ctx context.Context, path string) (dal.DB, io.Closer, error) {
	if path == "" {
		return nil, nopCloser{}, errors.New("hub.store.path is required when hub.store.engine is ingitdb")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, nopCloser{}, fmt.Errorf("create ingitdb hub store directory %s: %w", path, err)
	}
	db, err := dalgo2ingitdb.NewDatabase(path, validator.NewCollectionsReader())
	if err != nil {
		return nil, nopCloser{}, fmt.Errorf("open ingitdb hub store at %s: %w", path, err)
	}
	modifier, ok := dal.As[ddl.SchemaModifier](db)
	if !ok {
		return nil, nopCloser{}, errors.New("ingitdb hub store does not offer schema management")
	}
	for _, collection := range hub.Collections() {
		if err := modifier.CreateCollection(ctx, dbschema.CollectionDef{Name: collection}, ddl.IfNotExists()); err != nil {
			return nil, nopCloser{}, fmt.Errorf("declare ingitdb collection %s: %w", collection, err)
		}
	}
	return db, nopCloser{}, nil
}
