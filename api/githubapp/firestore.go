package githubapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	deliveryCollection       = "workbench_deliveries"
	wakeupCollection         = "workbench_wakeups"
	projectionDeliveryRecord = "workbench_projection_deliveries"
	latestMergesDocument     = "public"
)

// FirestoreBackend is the deliberately small seam implemented by the host's
// Firestore client. Values are decoded into out by the backend. UpdateAtomic
// must provide Firestore transaction semantics.
type FirestoreBackend interface {
	Get(context.Context, string, string, any) (bool, error)
	List(context.Context, string, any) error
	Set(context.Context, string, string, any) error
	UpdateAtomic(context.Context, func(FirestoreTransaction) error) error
}

type FirestoreTransaction interface {
	Get(context.Context, string, string, any) (bool, error)
	Set(context.Context, string, string, any) error
}

// FirestoreProjectionStore maps the documented Workbench collections to the
// host backend. The adapter contains no cloud.google.com imports, keeping the
// provider portable and letting Sneat Go supply its configured client.
type FirestoreProjectionStore struct{ Backend FirestoreBackend }

func (store FirestoreProjectionStore) ListProjections(ctx context.Context, scope Scope) ([]ProjectionDocument, error) {
	if store.Backend == nil {
		return nil, errors.New("firestore projection backend is not configured")
	}
	var documents []ProjectionDocument
	if err := store.Backend.List(ctx, ProjectionCollection, &documents); err != nil {
		return nil, err
	}
	filtered := documents[:0]
	for _, document := range documents {
		if document.Scope == scope {
			filtered = append(filtered, document)
		}
	}
	return filtered, nil
}

func (store FirestoreProjectionStore) GetProjection(ctx context.Context, scope Scope, id string) (ProjectionDocument, error) {
	if store.Backend == nil {
		return ProjectionDocument{}, errors.New("firestore projection backend is not configured")
	}
	var document ProjectionDocument
	found, err := store.Backend.Get(ctx, ProjectionCollection, ProjectionKey(scope, id), &document)
	if err != nil {
		return ProjectionDocument{}, err
	}
	if !found {
		return ProjectionDocument{}, ErrProjectionNotFound
	}
	return document, nil
}

func (store FirestoreProjectionStore) ListSeries(ctx context.Context, scope Scope, id, metric string) (SeriesDocument, error) {
	if store.Backend == nil {
		return SeriesDocument{}, errors.New("firestore projection backend is not configured")
	}
	var documents []SeriesDocument
	if err := store.Backend.List(ctx, SeriesCollection, &documents); err != nil {
		return SeriesDocument{}, err
	}
	for _, document := range documents {
		if document.Scope == scope && document.ID == id && document.Metric == metric {
			return document, nil
		}
	}
	return SeriesDocument{}, ErrProjectionNotFound
}

func (store FirestoreProjectionStore) GetLeaderboard(ctx context.Context, metric string) (LeaderboardDocument, error) {
	if store.Backend == nil {
		return LeaderboardDocument{}, errors.New("firestore projection backend is not configured")
	}
	var document LeaderboardDocument
	found, err := store.Backend.Get(ctx, LeaderboardCollection, metric, &document)
	if err != nil {
		return LeaderboardDocument{}, err
	}
	if !found {
		return LeaderboardDocument{}, ErrProjectionNotFound
	}
	return document, nil
}

func (store FirestoreProjectionStore) ListPublicLatestMerges(ctx context.Context, limit int) (PublicLatestMerges, error) {
	if store.Backend == nil {
		return PublicLatestMerges{}, errors.New("firestore projection backend is not configured")
	}
	var document PublicLatestMerges
	found, err := store.Backend.Get(ctx, MergeCollection, latestMergesDocument, &document)
	if err != nil {
		return PublicLatestMerges{}, err
	}
	if !found {
		return PublicLatestMerges{}, nil
	}
	if limit >= 0 && len(document.Entries) > limit {
		document.Entries = document.Entries[:limit]
	}
	return document, nil
}

// FirestoreProjectionWriter applies complete batches by stable document key.
// Set is intentionally idempotent, so a retry after a crash cannot duplicate
// projections or public merges.
type FirestoreProjectionWriter struct{ Backend FirestoreBackend }

func (writer FirestoreProjectionWriter) WriteRepositories(ctx context.Context, deliveryID string, records []ProjectionDocument) error {
	return writer.writeDocuments(ctx, deliveryID, ScopeRepository, records)
}
func (writer FirestoreProjectionWriter) WriteOrganizations(ctx context.Context, deliveryID string, records []ProjectionDocument) error {
	return writer.writeDocuments(ctx, deliveryID, ScopeOrganization, records)
}
func (writer FirestoreProjectionWriter) WriteLatestMerges(ctx context.Context, deliveryID string, records []LatestMerge) error {
	if writer.Backend == nil {
		return errors.New("firestore projection backend is not configured")
	}
	if err := writer.Backend.Set(ctx, MergeCollection, latestMergesDocument, PublicLatestMerges{Entries: records}); err != nil {
		return err
	}
	return writer.markDelivery(ctx, deliveryID, "latest_merges")
}
func (writer FirestoreProjectionWriter) writeDocuments(ctx context.Context, deliveryID string, scope Scope, records []ProjectionDocument) error {
	if writer.Backend == nil {
		return errors.New("firestore projection backend is not configured")
	}
	for _, document := range records {
		if document.Scope != scope {
			return fmt.Errorf("projection scope %q does not match %q", document.Scope, scope)
		}
		if err := writer.Backend.Set(ctx, ProjectionCollection, ProjectionKey(scope, document.ID), document); err != nil {
			return err
		}
	}
	return writer.markDelivery(ctx, deliveryID, string(scope))
}
func (writer FirestoreProjectionWriter) markDelivery(ctx context.Context, deliveryID, batch string) error {
	if strings.TrimSpace(deliveryID) == "" {
		return errors.New("projection delivery ID is required")
	}
	return writer.Backend.Set(ctx, projectionDeliveryRecord, deliveryID, map[string]string{"batch": batch})
}

type firestoreDeliveryRecord struct {
	Status     string    `json:"status"`
	LeaseUntil time.Time `json:"lease_until"`
	Attempts   int       `json:"attempts"`
}

// FirestoreProjectionDeliveryStore provides the atomic claim and coalesced
// wakeup used by ProjectionEngine. An expired claim is recoverable.
type FirestoreProjectionDeliveryStore struct {
	Backend FirestoreBackend
	Now     func() time.Time
	Lease   time.Duration
}

func (store FirestoreProjectionDeliveryStore) now() time.Time {
	if store.Now != nil {
		return store.Now()
	}
	return time.Now().UTC()
}
func (store FirestoreProjectionDeliveryStore) lease() time.Duration {
	if store.Lease > 0 {
		return store.Lease
	}
	return 5 * time.Minute
}
func (store FirestoreProjectionDeliveryStore) HasDelivery(ctx context.Context, id string) (bool, error) {
	var record firestoreDeliveryRecord
	found, err := store.Backend.Get(ctx, deliveryCollection, id, &record)
	return found && record.Status == "committed", err
}
func (store FirestoreProjectionDeliveryStore) ClaimDelivery(ctx context.Context, id string) (bool, error) {
	if store.Backend == nil {
		return false, errors.New("firestore delivery backend is not configured")
	}
	claimed := false
	err := store.Backend.UpdateAtomic(ctx, func(tx FirestoreTransaction) error {
		var record firestoreDeliveryRecord
		found, err := tx.Get(ctx, deliveryCollection, id, &record)
		if err != nil {
			return err
		}
		now := store.now()
		if found && (record.Status == "committed" || (record.Status == "claimed" && record.LeaseUntil.After(now))) {
			return nil
		}
		record.Status, record.LeaseUntil, record.Attempts = "claimed", now.Add(store.lease()), record.Attempts+1
		if err := tx.Set(ctx, deliveryCollection, id, record); err != nil {
			return err
		}
		claimed = true
		return nil
	})
	return claimed, err
}
func (store FirestoreProjectionDeliveryStore) ReleaseDelivery(ctx context.Context, id string) error {
	return store.updateStatus(ctx, id, "retryable")
}
func (store FirestoreProjectionDeliveryStore) CommitDeliveryAndWakeup(ctx context.Context, id string, wakeup Wakeup) (bool, error) {
	if store.Backend == nil {
		return false, errors.New("firestore delivery backend is not configured")
	}
	committed := false
	err := store.Backend.UpdateAtomic(ctx, func(tx FirestoreTransaction) error {
		var record firestoreDeliveryRecord
		found, err := tx.Get(ctx, deliveryCollection, id, &record)
		if err != nil {
			return err
		}
		if found && record.Status == "committed" {
			return nil
		}
		record.Status, record.LeaseUntil = "committed", time.Time{}
		if err := tx.Set(ctx, deliveryCollection, id, record); err != nil {
			return err
		}
		if err := tx.Set(ctx, wakeupCollection, wakeup.Key, wakeup); err != nil {
			return err
		}
		committed = true
		return nil
	})
	return committed, err
}
func (store FirestoreProjectionDeliveryStore) updateStatus(ctx context.Context, id, status string) error {
	if store.Backend == nil {
		return errors.New("firestore delivery backend is not configured")
	}
	return store.Backend.UpdateAtomic(ctx, func(tx FirestoreTransaction) error {
		var record firestoreDeliveryRecord
		found, err := tx.Get(ctx, deliveryCollection, id, &record)
		if err != nil || !found {
			return err
		}
		record.Status, record.LeaseUntil = status, time.Time{}
		return tx.Set(ctx, deliveryCollection, id, record)
	})
}
