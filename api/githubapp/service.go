package githubapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrPrivateData = errors.New("private Workbench data requires membership")
	ErrNoReadModel = errors.New("Workbench read model is not configured")
	ErrNoWebhook   = errors.New("Workbench webhook processor is not configured")
)

// ReadModel owns persistence and GitHub data projection. It must return only
// subjects whose public opt-in is recorded, unless the supplied viewer is an
// authenticated member of the private subject.
type ReadModel interface {
	Dashboard(context.Context, Viewer) (Access[Dashboard], error)
	Stats(context.Context, Viewer, Scope, string) (Access[Stat], error)
	Series(context.Context, Viewer, Scope, string, string) (Access[Series], error)
	Leaderboard(context.Context, Viewer, string) (Access[Leaderboard], error)
	LatestMerges(context.Context, Viewer, int) (Access[[]LatestMerge], error)
}

// DeliveryStore must be backed by durable storage. HasDelivery is a cheap
// preflight that avoids repeating an authoritative GitHub read for a redelivery.
// CommitDeliveryAndWakeup atomically records the delivery ID and persists a
// wakeup coalesced by its scope key. It returns false when a concurrent request
// committed the same delivery first.
type DeliveryStore interface {
	HasDelivery(context.Context, string) (bool, error)
	CommitDeliveryAndWakeup(context.Context, string, Wakeup) (bool, error)
}

// AuthoritativeReader refreshes the GitHub App's authoritative view before an
// action is queued. A cache hit alone must never satisfy this call.
type AuthoritativeReader interface {
	Refresh(context.Context, WebhookDelivery) error
}

// EventSource durably replays events strictly after a cursor, then exposes a
// live subscription. Implementations must retain enough history for reconnects
// and issue globally monotonic IDs across daemon generations.
type EventSource interface {
	Replay(context.Context, EventFilter) ([]Event, error)
	Subscribe(context.Context, EventFilter) (<-chan Event, error)
}

// Wakeup is a durable, coalescible unit of refresh work.
type Wakeup struct {
	Key        string
	Repository string
	Event      string
}

// WebhookDelivery is the verified, minimally parsed GitHub webhook envelope.
type WebhookDelivery struct {
	ID         string
	Event      string
	Repository string
	Payload    []byte
}

// Service applies disclosure policy around a Workbench read model and processes
// signed GitHub App webhook deliveries.
type Service struct {
	ReadModel           ReadModel
	Deliveries          DeliveryStore
	AuthoritativeReader AuthoritativeReader
	WebhookSecret       []byte
	Events              EventSource
}

func (service Service) Dashboard(ctx context.Context, viewer Viewer) (Dashboard, error) {
	if service.ReadModel == nil {
		return Dashboard{}, ErrNoReadModel
	}
	value, err := service.ReadModel.Dashboard(ctx, viewer)
	if err != nil {
		return Dashboard{}, err
	}
	return disclose(viewer, value)
}

func (service Service) Stats(ctx context.Context, viewer Viewer, scope Scope, id string) (Stat, error) {
	if service.ReadModel == nil {
		return Stat{}, ErrNoReadModel
	}
	value, err := service.ReadModel.Stats(ctx, viewer, scope, id)
	if err != nil {
		return Stat{}, err
	}
	return disclose(viewer, value)
}

func (service Service) Series(ctx context.Context, viewer Viewer, scope Scope, id, metric string) (Series, error) {
	if service.ReadModel == nil {
		return Series{}, ErrNoReadModel
	}
	value, err := service.ReadModel.Series(ctx, viewer, scope, id, metric)
	if err != nil {
		return Series{}, err
	}
	return disclose(viewer, value)
}

func (service Service) Leaderboard(ctx context.Context, viewer Viewer, metric string) (Leaderboard, error) {
	if service.ReadModel == nil {
		return Leaderboard{}, ErrNoReadModel
	}
	value, err := service.ReadModel.Leaderboard(ctx, viewer, metric)
	if err != nil {
		return Leaderboard{}, err
	}
	return disclose(viewer, value)
}

func (service Service) LatestMerges(ctx context.Context, viewer Viewer, limit int) ([]LatestMerge, error) {
	if service.ReadModel == nil {
		return nil, ErrNoReadModel
	}
	value, err := service.ReadModel.LatestMerges(ctx, viewer, limit)
	if err != nil {
		return nil, err
	}
	return disclose(viewer, value)
}

// EventStream replays visible durable events after cursor and returns the
// filtered live channel. It validates monotonic order so a bad source cannot
// cause a browser to skip or regress a reconnect cursor.
func (service Service) EventStream(ctx context.Context, viewer Viewer, filter EventFilter) ([]Event, <-chan Event, error) {
	if service.Events == nil {
		return nil, nil, ErrNoReadModel
	}
	replay, err := service.Events.Replay(ctx, filter)
	if err != nil {
		return nil, nil, fmt.Errorf("replay daemon events: %w", err)
	}
	visibleReplay, err := visibleEvents(viewer, filter, replay)
	if err != nil {
		return nil, nil, err
	}
	filter.After = lastEventID(filter.After, replay)
	live, err := service.Events.Subscribe(ctx, filter)
	if err != nil {
		return nil, nil, fmt.Errorf("subscribe daemon events: %w", err)
	}
	filtered := make(chan Event)
	go func() {
		defer close(filtered)
		last := filter.After
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-live:
				if !ok {
					return
				}
				if event.ID <= last {
					continue
				}
				last = event.ID
				if event.Visibility == VisibilityPrivate && (!viewer.Authenticated || !viewer.Member) {
					continue
				}
				select {
				case <-ctx.Done():
					return
				case filtered <- event:
				}
			}
		}
	}()
	return visibleReplay, filtered, nil
}

func visibleEvents(viewer Viewer, filter EventFilter, events []Event) ([]Event, error) {
	visible := make([]Event, 0, len(events))
	last := filter.After
	for _, event := range events {
		if event.ID <= last {
			return nil, fmt.Errorf("daemon event cursor is not monotonic: %d after %d", event.ID, last)
		}
		last = event.ID
		if !matchesFilter(event, filter) {
			continue
		}
		if event.Visibility == VisibilityPrivate && (!viewer.Authenticated || !viewer.Member) {
			continue
		}
		visible = append(visible, event)
	}
	return visible, nil
}

func matchesFilter(event Event, filter EventFilter) bool {
	if !filter.Since.IsZero() && event.At.Before(filter.Since) {
		return false
	}
	return (filter.Repository == "" || event.Repository == filter.Repository) &&
		(filter.Task == "" || event.Task == filter.Task) &&
		(filter.Operation == "" || event.Operation == filter.Operation) &&
		(filter.Session == "" || event.Session == filter.Session) &&
		(filter.Severity == "" || event.Severity == filter.Severity)
}

func lastEventID(cursor uint64, events []Event) uint64 {
	if len(events) == 0 {
		return cursor
	}
	return events[len(events)-1].ID
}

// ProcessWebhook verifies a signed delivery, cheap-checks its durable receipt,
// refreshes GitHub authoritatively, then atomically records the delivery and
// persists one coalesced wakeup. Failed refreshes leave no delivery receipt, so
// GitHub retries remain safe; a race is resolved by the atomic final commit.
func (service Service) ProcessWebhook(ctx context.Context, delivery WebhookDelivery, signature string) (bool, error) {
	if service.Deliveries == nil || service.AuthoritativeReader == nil || len(service.WebhookSecret) == 0 {
		return false, ErrNoWebhook
	}
	if strings.TrimSpace(delivery.ID) == "" || strings.TrimSpace(delivery.Event) == "" {
		return false, errors.New("GitHub delivery ID and event are required")
	}
	if !verifySignature(service.WebhookSecret, delivery.Payload, signature) {
		return false, errors.New("GitHub webhook signature is invalid")
	}
	seen, err := service.Deliveries.HasDelivery(ctx, delivery.ID)
	if err != nil || seen {
		return false, err
	}
	if err := service.AuthoritativeReader.Refresh(ctx, delivery); err != nil {
		return false, fmt.Errorf("refresh authoritative GitHub state: %w", err)
	}
	key := strings.TrimSpace(delivery.Repository)
	if key == "" {
		key = "installation"
	}
	queued, err := service.Deliveries.CommitDeliveryAndWakeup(ctx, delivery.ID, Wakeup{Key: key, Repository: delivery.Repository, Event: delivery.Event})
	if err != nil {
		return false, fmt.Errorf("persist delivery and coalesced wakeup: %w", err)
	}
	return queued, nil
}

func disclose[T any](viewer Viewer, value Access[T]) (T, error) {
	if value.Visibility == VisibilityPrivate && (!viewer.Authenticated || !viewer.Member) {
		var zero T
		return zero, ErrPrivateData
	}
	return value.Value, nil
}

func verifySignature(secret, payload []byte, supplied string) bool {
	supplied = strings.TrimSpace(supplied)
	const prefix = "sha256="
	if !strings.HasPrefix(supplied, prefix) {
		return false
	}
	signature, err := hex.DecodeString(strings.TrimPrefix(supplied, prefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	return hmac.Equal(signature, mac.Sum(nil))
}
