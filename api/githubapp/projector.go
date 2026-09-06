package githubapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ProjectionSnapshot is the authoritative result of refreshing one GitHub
// App delivery. The reader owns GitHub access; the provider owns validation
// and the stable projection write order.
type ProjectionSnapshot struct {
	Repositories  []ProjectionDocument
	Organizations []ProjectionDocument
	LatestMerges  []LatestMerge
}

// ProjectionReader refreshes and returns the authoritative records for a
// delivery. Implementations may coalesce equivalent repository refreshes, but
// must not satisfy a delivery from cache alone.
type ProjectionReader interface {
	RefreshProjection(context.Context, WebhookDelivery) (ProjectionSnapshot, error)
}

// AuthoritativeProjectionReader combines the freshness barrier and projection
// read for readers that can carry one request-scoped GitHub snapshot across the
// provider boundary. This avoids issuing the same REST reads twice.
type AuthoritativeProjectionReader interface {
	RefreshAuthoritativeProjection(context.Context, WebhookDelivery) (ProjectionSnapshot, error)
}

// ProjectionWriter durably applies a complete authoritative snapshot. Every
// method is keyed by deliveryID and must be idempotent: a crash after a write
// and before DeliveryStore commits is safe to retry. Implementations should
// replace projections by ProjectionKey and replace the public merge view as a
// single logical operation.
type ProjectionWriter interface {
	WriteRepositories(context.Context, string, []ProjectionDocument) error
	WriteOrganizations(context.Context, string, []ProjectionDocument) error
	WriteLatestMerges(context.Context, string, []LatestMerge) error
}

// ProjectionDeliveryStore extends DeliveryStore with an atomic in-flight
// claim. ClaimDelivery returns false for a committed or currently claimed
// delivery. ReleaseDelivery makes refresh/write failures retryable; the
// implementation must retain its append-only audit record.
type ProjectionDeliveryStore interface {
	DeliveryStore
	ClaimDelivery(context.Context, string) (bool, error)
	ReleaseDelivery(context.Context, string) error
}

// ProjectionEngine coordinates authoritative refresh, durable projection
// writes, and delivery receipt publication. It deliberately does not import
// GitHub, Firebase, or Firestore clients.
type ProjectionEngine struct {
	Deliveries          ProjectionDeliveryStore
	Reader              ProjectionReader
	Writer              ProjectionWriter
	AuthoritativeReader AuthoritativeReader
	WebhookSecret       []byte
}

var (
	ErrNoProjector       = errors.New("workbench projection engine is not configured")
	ErrInvalidProjection = errors.New("invalid workbench projection snapshot")
)

// Process verifies and applies one delivery. AuthoritativeReader is retained
// as a required companion to ProjectionReader so hosts cannot accidentally
// wire a projection reader that omits the existing refresh barrier.
func (engine ProjectionEngine) Process(ctx context.Context, delivery WebhookDelivery, signature string) (queued bool, processErr error) {
	if engine.Deliveries == nil || engine.Reader == nil || engine.Writer == nil || engine.AuthoritativeReader == nil || len(engine.WebhookSecret) == 0 {
		return false, ErrNoProjector
	}
	if strings.TrimSpace(delivery.ID) == "" || strings.TrimSpace(delivery.Event) == "" {
		return false, errors.New("delivery ID and event are required")
	}
	if !verifySignature(engine.WebhookSecret, delivery.Payload, signature) {
		return false, errors.New("webhook signature is invalid")
	}
	seen, err := engine.Deliveries.HasDelivery(ctx, delivery.ID)
	if err != nil {
		return false, err
	}
	if seen {
		return false, nil
	}
	claimed, err := engine.Deliveries.ClaimDelivery(ctx, delivery.ID)
	if err != nil {
		return false, err
	}
	if !claimed {
		return false, nil
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if releaseErr := engine.Deliveries.ReleaseDelivery(ctx, delivery.ID); releaseErr != nil {
			processErr = errors.Join(processErr, fmt.Errorf("release failed projection delivery claim: %w", releaseErr))
		}
	}()
	var snapshot ProjectionSnapshot
	if reader, ok := engine.Reader.(AuthoritativeProjectionReader); ok {
		snapshot, err = reader.RefreshAuthoritativeProjection(ctx, delivery)
	} else {
		if err := engine.AuthoritativeReader.Refresh(ctx, delivery); err != nil {
			return false, fmt.Errorf("refresh authoritative GitHub state: %w", err)
		}
		snapshot, err = engine.Reader.RefreshProjection(ctx, delivery)
	}
	if err != nil {
		return false, fmt.Errorf("build authoritative projections: %w", err)
	}
	if err := validateProjectionSnapshot(snapshot); err != nil {
		return false, err
	}
	if err := engine.Writer.WriteRepositories(ctx, delivery.ID, snapshot.Repositories); err != nil {
		return false, fmt.Errorf("write repository projections: %w", err)
	}
	if err := engine.Writer.WriteOrganizations(ctx, delivery.ID, snapshot.Organizations); err != nil {
		return false, fmt.Errorf("write organization projections: %w", err)
	}
	if err := engine.Writer.WriteLatestMerges(ctx, delivery.ID, snapshot.LatestMerges); err != nil {
		return false, fmt.Errorf("write latest merges: %w", err)
	}
	key := strings.TrimSpace(delivery.Repository)
	if key == "" {
		key = "installation"
	}
	queued, err = engine.Deliveries.CommitDeliveryAndWakeup(ctx, delivery.ID, Wakeup{Key: key, Repository: delivery.Repository, Event: delivery.Event})
	if err != nil {
		return false, fmt.Errorf("persist delivery and coalesced wakeup: %w", err)
	}
	committed = true
	return queued, nil
}

func validateProjectionSnapshot(snapshot ProjectionSnapshot) error {
	for _, document := range snapshot.Repositories {
		if document.Scope != ScopeRepository {
			return fmt.Errorf("%w: repository record has scope %q", ErrInvalidProjection, document.Scope)
		}
		if err := ValidateProjectionDocument(document); err != nil {
			return fmt.Errorf("%w: repository record: %v", ErrInvalidProjection, err)
		}
	}
	for _, document := range snapshot.Organizations {
		if document.Scope != ScopeOrganization {
			return fmt.Errorf("%w: organization record has scope %q", ErrInvalidProjection, document.Scope)
		}
		if err := ValidateProjectionDocument(document); err != nil {
			return fmt.Errorf("%w: organization record: %v", ErrInvalidProjection, err)
		}
	}
	for _, merge := range snapshot.LatestMerges {
		if strings.TrimSpace(merge.Repository) == "" || merge.PullRequest <= 0 {
			return fmt.Errorf("%w: latest merge repository and pull request are required", ErrInvalidProjection)
		}
	}
	return nil
}
