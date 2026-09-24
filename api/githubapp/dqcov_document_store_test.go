package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

// dqCovDocumentStore is a DocumentStore with independently scriptable
// failures, so every fail-closed branch of the projection document adapters
// can be asserted rather than merely executed.
type dqCovDocumentStore struct {
	documents map[string]json.RawMessage
	getErr    error
	setErr    error
	queryErr  error
	updateErr error
	failSetOn string
	queries   []firestoreQuery
}

func (store *dqCovDocumentStore) key(collection, id string) string { return collection + "\x00" + id }

func (store *dqCovDocumentStore) Get(_ context.Context, collection, id string, out any) (bool, error) {
	if store.getErr != nil {
		return false, store.getErr
	}
	raw, ok := store.documents[store.key(collection, id)]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, out)
}

func (store *dqCovDocumentStore) Query(_ context.Context, collection string, equals map[string]any, limit int, out any) error {
	store.queries = append(store.queries, firestoreQuery{collection: collection, equals: equals, limit: limit})
	if store.queryErr != nil {
		return store.queryErr
	}
	values := make([]json.RawMessage, 0)
	prefix := collection + "\x00"
	for key, raw := range store.documents {
		if len(key) < len(prefix) || key[:len(prefix)] != prefix {
			continue
		}
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		match := true
		for field, want := range equals {
			if fmt.Sprint(fields[field]) != fmt.Sprint(want) {
				match = false
				break
			}
		}
		if match {
			values = append(values, raw)
		}
	}
	if limit > 0 && len(values) > limit {
		values = values[:limit]
	}
	return json.Unmarshal(mustJSON(values), out)
}

func (store *dqCovDocumentStore) Set(_ context.Context, collection, id string, value any) error {
	if store.setErr != nil {
		return store.setErr
	}
	if store.failSetOn != "" && store.failSetOn == collection {
		return fmt.Errorf("set refused for collection %q", collection)
	}
	if store.documents == nil {
		store.documents = map[string]json.RawMessage{}
	}
	raw, err := json.Marshal(value)
	if err == nil {
		store.documents[store.key(collection, id)] = raw
	}
	return err
}

func (store *dqCovDocumentStore) Delete(_ context.Context, collection, id string) error {
	delete(store.documents, store.key(collection, id))
	return nil
}

func (store *dqCovDocumentStore) UpdateAtomic(ctx context.Context, fn func(DocumentTransaction) error) error {
	if store.updateErr != nil {
		return store.updateErr
	}
	return fn(store)
}

func dqCovEmptyDocuments() map[string]json.RawMessage {
	return map[string]json.RawMessage{}
}

func TestDQCovProjectionStoreFailsClosedWithoutBackend(t *testing.T) {
	t.Parallel()
	store := DocumentProjectionStore{}
	ctx := context.Background()
	if _, err := store.ListProjections(ctx, ScopeRepository); err == nil {
		t.Error("ListProjections accepted a nil backend")
	}
	if _, err := store.GetProjection(ctx, ScopeRepository, "id"); err == nil {
		t.Error("GetProjection accepted a nil backend")
	}
	if _, err := store.ListSeries(ctx, ScopeRepository, "id", "metric"); err == nil {
		t.Error("ListSeries accepted a nil backend")
	}
	if _, err := store.GetLeaderboard(ctx, "metric"); err == nil {
		t.Error("GetLeaderboard accepted a nil backend")
	}
	if _, err := store.ListPublicLatestMerges(ctx, 5); err == nil {
		t.Error("ListPublicLatestMerges accepted a nil backend")
	}
}

func TestDQCovProjectionStorePropagatesBackendFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	failing := &dqCovDocumentStore{documents: dqCovEmptyDocuments(), getErr: errors.New("get down"), queryErr: errors.New("query down")}
	store := DocumentProjectionStore{Backend: failing}
	if _, err := store.ListProjections(ctx, ScopeRepository); err == nil || err.Error() != "query down" {
		t.Errorf("ListProjections err = %v", err)
	}
	if _, err := store.GetProjection(ctx, ScopeRepository, "id"); err == nil || err.Error() != "get down" {
		t.Errorf("GetProjection err = %v", err)
	}
	if _, err := store.ListSeries(ctx, ScopeRepository, "id", "metric"); err == nil || err.Error() != "query down" {
		t.Errorf("ListSeries err = %v", err)
	}
	if _, err := store.GetLeaderboard(ctx, "metric"); err == nil || err.Error() != "get down" {
		t.Errorf("GetLeaderboard err = %v", err)
	}
	if _, err := store.ListPublicLatestMerges(ctx, 5); err == nil || err.Error() != "get down" {
		t.Errorf("ListPublicLatestMerges err = %v", err)
	}
}

func TestDQCovProjectionStoreReportsMissingDocuments(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := DocumentProjectionStore{Backend: &dqCovDocumentStore{documents: dqCovEmptyDocuments()}}
	if _, err := store.ListSeries(ctx, ScopeRepository, "id", "merged"); !errors.Is(err, ErrProjectionNotFound) {
		t.Errorf("missing series err = %v", err)
	}
	if _, err := store.GetLeaderboard(ctx, "merged"); !errors.Is(err, ErrProjectionNotFound) {
		t.Errorf("missing leaderboard err = %v", err)
	}
	merges, err := store.ListPublicLatestMerges(ctx, 5)
	if err != nil || len(merges.Entries) != 0 {
		t.Errorf("missing merges = %#v, %v", merges, err)
	}
}

func TestDQCovProjectionWriterValidatesDeliveryIDAndBackend(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if err := (DocumentProjectionWriter{}).WriteRepositories(ctx, "delivery-1", nil); err == nil {
		t.Error("writer accepted a nil backend")
	}
	writer := DocumentProjectionWriter{Backend: &dqCovDocumentStore{documents: dqCovEmptyDocuments()}}
	if err := writer.WriteRepositories(ctx, "   ", nil); err == nil {
		t.Error("writer accepted a blank delivery ID")
	}
	if err := writer.WriteOrganizations(ctx, "", nil); err == nil {
		t.Error("writer accepted an empty delivery ID")
	}
	record := ProjectionDocument{Scope: ScopeRepository, ID: "github.com/acme/app", DisplayName: "app", UpdatedAt: time.Unix(1, 0)}
	failing := DocumentProjectionWriter{Backend: &dqCovDocumentStore{documents: dqCovEmptyDocuments(), failSetOn: ProjectionCollection}}
	if err := failing.WriteRepositories(ctx, "delivery-1", []ProjectionDocument{record}); err == nil {
		t.Error("writer swallowed a durable set failure")
	}
}

func TestDQCovDeliveryStoreFailsClosedWithoutBackend(t *testing.T) {
	t.Parallel()
	store := DocumentProjectionDeliveryStore{}
	ctx := context.Background()
	if _, err := store.ClaimDelivery(ctx, "delivery-1"); err == nil {
		t.Error("ClaimDelivery accepted a nil backend")
	}
	if _, err := store.CommitDeliveryAndWakeup(ctx, "delivery-1", Wakeup{Key: "k"}); err == nil {
		t.Error("CommitDeliveryAndWakeup accepted a nil backend")
	}
	if err := store.ReleaseDelivery(ctx, "delivery-1"); err == nil {
		t.Error("ReleaseDelivery accepted a nil backend")
	}
}

func TestDQCovDeliveryStoreDefaultsClockAndLease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := &dqCovDocumentStore{documents: dqCovEmptyDocuments()}
	store := DocumentProjectionDeliveryStore{Backend: fake}
	before := time.Now().UTC()

	claimed, err := store.ClaimDelivery(ctx, "delivery-1")
	if err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	var record firestoreDeliveryRecord
	found, err := fake.Get(ctx, deliveryCollection, "delivery-1", &record)
	if err != nil || !found {
		t.Fatalf("stored claim found/err = %v/%v", found, err)
	}
	if record.Status != "claimed" || record.Attempts != 1 {
		t.Fatalf("stored claim = %+v", record)
	}
	if !record.LeaseUntil.After(before) || record.LeaseUntil.After(before.Add(6*time.Minute)) {
		t.Fatalf("default lease = %v, want roughly five minutes after %v", record.LeaseUntil, before)
	}
	if claimed, err := store.ClaimDelivery(ctx, "delivery-1"); err != nil || claimed {
		t.Fatalf("live duplicate claim = %v, %v", claimed, err)
	}
}

func TestDQCovDeliveryStorePropagatesTransactionFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	getFail := &dqCovDocumentStore{documents: dqCovEmptyDocuments(), getErr: errors.New("get down")}
	setFail := &dqCovDocumentStore{documents: dqCovEmptyDocuments(), failSetOn: deliveryCollection}
	wakeupFail := &dqCovDocumentStore{documents: dqCovEmptyDocuments(), failSetOn: wakeupCollection}

	if _, err := (DocumentProjectionDeliveryStore{Backend: getFail}).ClaimDelivery(ctx, "delivery-1"); err == nil {
		t.Error("ClaimDelivery swallowed a read failure")
	}
	if _, err := (DocumentProjectionDeliveryStore{Backend: setFail}).ClaimDelivery(ctx, "delivery-1"); err == nil {
		t.Error("ClaimDelivery swallowed a write failure")
	}
	if _, err := (DocumentProjectionDeliveryStore{Backend: getFail}).CommitDeliveryAndWakeup(ctx, "delivery-1", Wakeup{Key: "k"}); err == nil {
		t.Error("CommitDeliveryAndWakeup swallowed a read failure")
	}
	if _, err := (DocumentProjectionDeliveryStore{Backend: setFail}).CommitDeliveryAndWakeup(ctx, "delivery-1", Wakeup{Key: "k"}); err == nil {
		t.Error("CommitDeliveryAndWakeup swallowed a record write failure")
	}
	if _, err := (DocumentProjectionDeliveryStore{Backend: wakeupFail}).CommitDeliveryAndWakeup(ctx, "delivery-1", Wakeup{Key: "k"}); err == nil {
		t.Error("CommitDeliveryAndWakeup swallowed a wakeup write failure")
	}
	if _, ok := wakeupFail.documents[wakeupFail.key(wakeupCollection, wakeupDocumentID("k"))]; ok {
		t.Error("a failed atomic commit must not leave a durable wakeup")
	}
	if err := (DocumentProjectionDeliveryStore{Backend: getFail}).ReleaseDelivery(ctx, "delivery-1"); err == nil {
		t.Error("ReleaseDelivery swallowed a read failure")
	}
	if err := (DocumentProjectionDeliveryStore{Backend: &dqCovDocumentStore{documents: dqCovEmptyDocuments()}}).ReleaseDelivery(ctx, "missing"); err != nil {
		t.Errorf("releasing a missing delivery = %v, want nil", err)
	}
}
