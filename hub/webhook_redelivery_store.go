package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sneat-dev/wb/api/githubapp"
)

const webhookRedeliveryCollection = "workbench_webhook_redeliveries"

var errWebhookRedeliveryStoreUnavailable = errors.New("workbench webhook redelivery store is unavailable")

// WebhookRedeliveryRecord is what the hub remembers about one App webhook
// delivery GUID across missed-webhook recovery sweeps: how many times this
// hub has already asked GitHub to redeliver it, and whether it has given up.
// Nothing here is a secret or a payload: a GUID, a small counter and a
// timestamp are all a redelivery decision needs.
type WebhookRedeliveryRecord struct {
	// GUID is the delivery GUID GitHub keeps stable across every attempt and
	// every redelivery of one logical delivery.
	GUID string `firestore:"guid"`
	// Attempts is how many times this hub has asked GitHub to redeliver this
	// GUID. It never exceeds MaxAttempts in the redeliver package.
	Attempts int `firestore:"attempts"`
	// Abandoned is set once Attempts has been exhausted with the delivery
	// still failing. An abandoned GUID is never retried again.
	Abandoned bool `firestore:"abandoned"`
	// LastAttemptAt is when this record was last written, whether by a
	// redelivery or by being marked abandoned. It is the retention clock.
	LastAttemptAt time.Time `firestore:"last_attempt_at"`
}

func (record WebhookRedeliveryRecord) valid() bool {
	return strings.TrimSpace(record.GUID) != "" && record.Attempts >= 0 && !record.LastAttemptAt.IsZero()
}

// WebhookRedeliveryStore persists the missed-webhook recovery sweep's memory,
// keyed by GitHub's delivery GUID, with the 7-day retention the feature
// specifies.
type WebhookRedeliveryStore interface {
	// LoadWebhookRedelivery returns the stored record for a GUID. The boolean
	// is false when the sweep has never acted on it, which is the signal to
	// treat it as zero prior attempts.
	LoadWebhookRedelivery(ctx context.Context, guid string) (WebhookRedeliveryRecord, bool, error)
	// SaveWebhookRedelivery replaces the record for record.GUID.
	SaveWebhookRedelivery(ctx context.Context, record WebhookRedeliveryRecord) error
	// ListWebhookRedeliveries returns every stored record, for the retention
	// sweep to filter by age.
	ListWebhookRedeliveries(ctx context.Context) ([]WebhookRedeliveryRecord, error)
	// DeleteWebhookRedeliveries removes the named GUIDs' records. Deleting a
	// GUID that is not there is not an error. Callers must keep each call
	// within the store's transaction write bound.
	DeleteWebhookRedeliveries(ctx context.Context, guids []string) error
}

// NewWebhookRedeliveryStore binds the missed-webhook recovery sweep's memory
// to a host's githubapp.DocumentStore, the same seam every other hub-owned
// store writes through.
func NewWebhookRedeliveryStore(backend githubapp.DocumentStore) WebhookRedeliveryStore {
	return webhookRedeliveryStore{backend: backend}
}

type webhookRedeliveryStore struct {
	backend githubapp.DocumentStore
}

func (store webhookRedeliveryStore) LoadWebhookRedelivery(ctx context.Context, guid string) (WebhookRedeliveryRecord, bool, error) {
	if store.backend == nil || strings.TrimSpace(guid) == "" {
		return WebhookRedeliveryRecord{}, false, errWebhookRedeliveryStoreUnavailable
	}
	var record WebhookRedeliveryRecord
	found, err := store.backend.Get(ctx, webhookRedeliveryCollection, webhookRedeliveryDocumentID(guid), &record)
	if err != nil {
		return WebhookRedeliveryRecord{}, false, fmt.Errorf("read webhook redelivery record: %w", err)
	}
	if !found {
		return WebhookRedeliveryRecord{}, false, nil
	}
	// A stored record the sweep itself could not have written means the
	// document was hand-edited or half-written. Treating it as authoritative
	// could either retry a GUID forever or abandon one prematurely, so
	// refuse instead.
	if !record.valid() || record.GUID != guid {
		return WebhookRedeliveryRecord{}, false, errors.New("stored webhook redelivery record is invalid")
	}
	return record, true, nil
}

func (store webhookRedeliveryStore) SaveWebhookRedelivery(ctx context.Context, record WebhookRedeliveryRecord) error {
	if store.backend == nil || !record.valid() {
		return errWebhookRedeliveryStoreUnavailable
	}
	if err := store.backend.Set(ctx, webhookRedeliveryCollection, webhookRedeliveryDocumentID(record.GUID), record); err != nil {
		return fmt.Errorf("write webhook redelivery record: %w", err)
	}
	return nil
}

func (store webhookRedeliveryStore) ListWebhookRedeliveries(ctx context.Context) ([]WebhookRedeliveryRecord, error) {
	if store.backend == nil {
		return nil, errWebhookRedeliveryStoreUnavailable
	}
	var records []WebhookRedeliveryRecord
	if err := store.backend.Query(ctx, webhookRedeliveryCollection, nil, 0, &records); err != nil {
		return nil, fmt.Errorf("list webhook redelivery records: %w", err)
	}
	return records, nil
}

// maxWebhookRedeliveryPruneBatch keeps one retention prune inside the
// repository's 100-write transaction bound.
const maxWebhookRedeliveryPruneBatch = 100

func (store webhookRedeliveryStore) DeleteWebhookRedeliveries(ctx context.Context, guids []string) error {
	if store.backend == nil {
		return errWebhookRedeliveryStoreUnavailable
	}
	for len(guids) > 0 {
		batch := guids
		if len(batch) > maxWebhookRedeliveryPruneBatch {
			batch = batch[:maxWebhookRedeliveryPruneBatch]
		}
		err := store.backend.UpdateAtomic(ctx, func(transaction githubapp.DocumentTransaction) error {
			for _, guid := range batch {
				if err := transaction.Delete(ctx, webhookRedeliveryCollection, webhookRedeliveryDocumentID(guid)); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("delete webhook redelivery records: %w", err)
		}
		guids = guids[len(batch):]
	}
	return nil
}

func webhookRedeliveryDocumentID(guid string) string {
	// GitHub's own GUIDs are already document-id-safe (UUID format), but
	// hashing keeps this store's key derivation consistent with every other
	// hub-owned store and immune to a future GUID format change.
	digest := sha256.Sum256([]byte(guid))
	return hex.EncodeToString(digest[:])
}

var _ WebhookRedeliveryStore = webhookRedeliveryStore{}
