package githubapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	deliveryCollection   = "workbench_deliveries"
	wakeupCollection     = "workbench_wakeups"
	latestMergesDocument = "public"
)

// FirestoreBackend is the deliberately small seam implemented by the host's
// Firestore client. Values are decoded into out by the backend. UpdateAtomic
// must provide Firestore transaction semantics.
type FirestoreBackend interface {
	Get(context.Context, string, string, any) (bool, error)
	Query(context.Context, string, map[string]any, int, any) error
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
	if err := store.Backend.Query(ctx, ProjectionCollection, map[string]any{"scope": scope}, 0, &documents); err != nil {
		return nil, err
	}
	return documents, nil
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
	if err := store.Backend.Query(ctx, SeriesCollection, map[string]any{"scope": scope, "id": id, "metric": metric}, 1, &documents); err != nil {
		return SeriesDocument{}, err
	}
	if len(documents) == 1 {
		return documents[0], nil
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
	if err := validateProjectionDeliveryID(deliveryID); err != nil {
		return err
	}
	if err := writer.Backend.Set(ctx, MergeCollection, latestMergesDocument, PublicLatestMerges{Entries: records}); err != nil {
		return err
	}
	return nil
}
func (writer FirestoreProjectionWriter) writeDocuments(ctx context.Context, deliveryID string, scope Scope, records []ProjectionDocument) error {
	if writer.Backend == nil {
		return errors.New("firestore projection backend is not configured")
	}
	if err := validateProjectionDeliveryID(deliveryID); err != nil {
		return err
	}
	for _, document := range records {
		if document.Scope != scope {
			return fmt.Errorf("projection scope %q does not match %q", document.Scope, scope)
		}
		if err := writer.Backend.Set(ctx, ProjectionCollection, ProjectionKey(scope, document.ID), document); err != nil {
			return err
		}
	}
	return nil
}
func validateProjectionDeliveryID(deliveryID string) error {
	if strings.TrimSpace(deliveryID) == "" {
		return errors.New("projection delivery ID is required")
	}
	return nil
}

type firestoreDeliveryRecord struct {
	Status     string    `json:"status" firestore:"status"`
	LeaseUntil time.Time `json:"lease_until" firestore:"lease_until"`
	Attempts   int       `json:"attempts" firestore:"attempts"`
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
	if store.Backend == nil {
		return false, errors.New("firestore delivery backend is not configured")
	}
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
		if err := tx.Set(ctx, wakeupCollection, wakeupDocumentID(wakeup.Key), wakeup); err != nil {
			return err
		}
		committed = true
		return nil
	})
	return committed, err
}

func wakeupDocumentID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "wakeup_" + hex.EncodeToString(sum[:])
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
