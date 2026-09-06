package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

type firestoreQuery struct {
	collection string
	equals     map[string]any
	limit      int
}

type firestoreFake struct {
	documents map[string]json.RawMessage
	queries   []firestoreQuery
}

func (fake *firestoreFake) key(collection, id string) string { return collection + "\x00" + id }
func (fake *firestoreFake) Get(_ context.Context, collection, id string, out any) (bool, error) {
	raw, ok := fake.documents[fake.key(collection, id)]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, out)
}
func (fake *firestoreFake) List(_ context.Context, collection string, out any) error {
	values := make([]json.RawMessage, 0)
	prefix := collection + "\x00"
	for key, raw := range fake.documents {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			values = append(values, raw)
		}
	}
	return json.Unmarshal(mustJSON(values), out)
}
func (fake *firestoreFake) Query(_ context.Context, collection string, equals map[string]any, limit int, out any) error {
	fake.queries = append(fake.queries, firestoreQuery{collection: collection, equals: equals, limit: limit})
	values := make([]json.RawMessage, 0)
	prefix := collection + "\x00"
	for key, raw := range fake.documents {
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
func (fake *firestoreFake) Set(_ context.Context, collection, id string, value any) error {
	raw, err := json.Marshal(value)
	if err == nil {
		fake.documents[fake.key(collection, id)] = raw
	}
	return err
}
func (fake *firestoreFake) UpdateAtomic(ctx context.Context, fn func(FirestoreTransaction) error) error {
	return fn(fake)
}

func mustJSON(value any) []byte { raw, _ := json.Marshal(value); return raw }

func TestFirestoreProjectionStoreReadsDocumentContracts(t *testing.T) {
	fake := &firestoreFake{documents: map[string]json.RawMessage{}}
	ctx := context.Background()
	repository := ProjectionDocument{Scope: ScopeRepository, ID: "github.com/acme/app", DisplayName: "app", UpdatedAt: time.Unix(1, 0)}
	organization := ProjectionDocument{Scope: ScopeOrganization, ID: "github.com/acme", DisplayName: "acme", UpdatedAt: time.Unix(1, 0)}
	if err := fake.Set(ctx, ProjectionCollection, ProjectionKey(repository.Scope, repository.ID), repository); err != nil {
		t.Fatal(err)
	}
	if err := fake.Set(ctx, ProjectionCollection, ProjectionKey(organization.Scope, organization.ID), organization); err != nil {
		t.Fatal(err)
	}
	if err := fake.Set(ctx, SeriesCollection, "series", SeriesDocument{Scope: ScopeRepository, ID: repository.ID, Metric: "merged", Points: []SeriesPoint{{Value: 2}}}); err != nil {
		t.Fatal(err)
	}
	if err := fake.Set(ctx, LeaderboardCollection, "merged", LeaderboardDocument{Metric: "merged", PublicOnly: true}); err != nil {
		t.Fatal(err)
	}
	if err := fake.Set(ctx, MergeCollection, latestMergesDocument, PublicLatestMerges{Entries: []LatestMerge{{Repository: repository.ID, PullRequest: 1}, {Repository: repository.ID, PullRequest: 2}}}); err != nil {
		t.Fatal(err)
	}
	store := FirestoreProjectionStore{Backend: fake}
	if got, err := store.GetProjection(ctx, repository.Scope, repository.ID); err != nil || got.ID != repository.ID {
		t.Fatalf("GetProjection = %#v, %v", got, err)
	}
	if got, err := store.ListProjections(ctx, ScopeOrganization); err != nil || len(got) != 1 || got[0].ID != organization.ID {
		t.Fatalf("ListProjections = %#v, %v", got, err)
	}
	if got, err := store.ListSeries(ctx, ScopeRepository, repository.ID, "merged"); err != nil || len(got.Points) != 1 {
		t.Fatalf("ListSeries = %#v, %v", got, err)
	}
	if got, err := store.GetLeaderboard(ctx, "merged"); err != nil || !got.PublicOnly {
		t.Fatalf("GetLeaderboard = %#v, %v", got, err)
	}
	if got, err := store.ListPublicLatestMerges(ctx, 1); err != nil || len(got.Entries) != 1 {
		t.Fatalf("ListPublicLatestMerges = %#v, %v", got, err)
	}
	if len(fake.queries) != 2 {
		t.Fatalf("queries = %#v", fake.queries)
	}
	if fake.queries[0].collection != ProjectionCollection || fake.queries[0].equals["scope"] != ScopeOrganization || fake.queries[0].limit != 0 {
		t.Fatalf("projection query = %#v", fake.queries[0])
	}
	if fake.queries[1].collection != SeriesCollection || fake.queries[1].equals["scope"] != ScopeRepository || fake.queries[1].equals["id"] != repository.ID || fake.queries[1].equals["metric"] != "merged" || fake.queries[1].limit != 1 {
		t.Fatalf("series query = %#v", fake.queries[1])
	}
	if _, err := store.GetProjection(ctx, ScopeUser, "missing"); !errors.Is(err, ErrProjectionNotFound) {
		t.Fatalf("missing projection error = %v", err)
	}
}

func TestFirestoreProjectionWriterIsIdempotentByStableKeys(t *testing.T) {
	fake := &firestoreFake{documents: map[string]json.RawMessage{}}
	writer := FirestoreProjectionWriter{Backend: fake}
	ctx := context.Background()
	record := ProjectionDocument{Scope: ScopeRepository, ID: "github.com/acme/app", DisplayName: "app", UpdatedAt: time.Unix(1, 0)}
	if err := writer.WriteRepositories(ctx, "delivery-1", []ProjectionDocument{record}); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteRepositories(ctx, "delivery-1", []ProjectionDocument{record}); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteOrganizations(ctx, "delivery-1", []ProjectionDocument{{Scope: ScopeOrganization, ID: "github.com/acme", DisplayName: "acme", UpdatedAt: time.Unix(1, 0)}}); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteLatestMerges(ctx, "delivery-1", []LatestMerge{{Repository: record.ID, PullRequest: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := fake.documents[fake.key(projectionDeliveryRecord, "delivery-1")]; !ok {
		t.Fatal("delivery write marker missing")
	}
	if err := writer.WriteRepositories(ctx, "delivery-1", []ProjectionDocument{{Scope: ScopeOrganization, ID: "wrong", DisplayName: "wrong", UpdatedAt: time.Unix(1, 0)}}); err == nil {
		t.Fatal("scope mismatch accepted")
	}
}

func TestFirestoreProjectionDeliveryStoreClaimsWithRecoverableLease(t *testing.T) {
	if _, err := (FirestoreProjectionDeliveryStore{}).HasDelivery(context.Background(), "missing"); err == nil {
		t.Fatal("nil delivery backend did not fail closed")
	}
	fake := &firestoreFake{documents: map[string]json.RawMessage{}}
	now := time.Unix(100, 0)
	store := FirestoreProjectionDeliveryStore{Backend: fake, Now: func() time.Time { return now }, Lease: time.Minute}
	ctx := context.Background()
	claimed, err := store.ClaimDelivery(ctx, "delivery-1")
	if err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	if claimed, err = store.ClaimDelivery(ctx, "delivery-1"); err != nil || claimed {
		t.Fatalf("live duplicate claim = %v, %v", claimed, err)
	}
	if err := store.ReleaseDelivery(ctx, "delivery-1"); err != nil {
		t.Fatal(err)
	}
	if claimed, err = store.ClaimDelivery(ctx, "delivery-1"); err != nil || !claimed {
		t.Fatalf("retry claim = %v, %v", claimed, err)
	}
	now = now.Add(2 * time.Minute)
	if claimed, err = store.ClaimDelivery(ctx, "delivery-1"); err != nil || !claimed {
		t.Fatalf("expired claim = %v, %v", claimed, err)
	}
	queued, err := store.CommitDeliveryAndWakeup(ctx, "delivery-1", Wakeup{Key: "github.com/acme/app", Repository: "github.com/acme/app", Event: "push"})
	if err != nil || !queued {
		t.Fatalf("commit = %v, %v", queued, err)
	}
	if committed, err := store.HasDelivery(ctx, "delivery-1"); err != nil || !committed {
		t.Fatalf("HasDelivery = %v, %v", committed, err)
	}
	if queued, err := store.CommitDeliveryAndWakeup(ctx, "delivery-1", Wakeup{Key: "github.com/acme/app"}); err != nil || queued {
		t.Fatalf("duplicate commit = %v, %v", queued, err)
	}
}
