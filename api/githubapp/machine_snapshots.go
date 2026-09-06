package githubapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
	"github.com/sneat-dev/wb/internal/remotestate"
	remotestatehub "github.com/sneat-dev/wb/internal/remotestate/hub"
)

var (
	ErrPublisherIdentity = errors.New("machine publisher identity is unavailable")
	ErrPublisherMismatch = errors.New("published login or machine does not match the authenticated publisher")
)

// MachinePublisher is the exact identity bound to a daemon credential by the
// host. Neither value comes from request headers or the submitted payload.
type MachinePublisher struct {
	Login   string
	Machine string
}

// MachinePublisherResolver binds host authentication to one exact WB machine.
type MachinePublisherResolver interface {
	Publisher(*http.Request) (MachinePublisher, error)
}

// MachineSnapshotService applies publisher identity, validation, server time,
// and privacy-safe persistence around a durable store.
type MachineSnapshotService struct {
	Store machinesnapshot.SnapshotStore
	Now   func() time.Time
}

// Publish validates and atomically stores the latest snapshot for one exact
// authenticated login/machine key.
func (service MachineSnapshotService) Publish(ctx context.Context, publisher MachinePublisher, snapshot machinesnapshot.Snapshot) (machinesnapshot.Receipt, error) {
	if strings.TrimSpace(publisher.Login) == "" || strings.TrimSpace(publisher.Machine) == "" {
		return machinesnapshot.Receipt{}, ErrPublisherIdentity
	}
	if publisher.Login != snapshot.Login || publisher.Machine != snapshot.Machine {
		return machinesnapshot.Receipt{}, ErrPublisherMismatch
	}
	if err := snapshot.Validate(); err != nil {
		return machinesnapshot.Receipt{}, err
	}
	if service.Store == nil {
		return machinesnapshot.Receipt{}, ErrNoReadModel
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return machinesnapshot.Receipt{}, fmt.Errorf("encode validated hosted snapshot: %w", err)
	}
	digestBytes := sha256.Sum256(payload)
	receivedAt := time.Now().UTC()
	if service.Now != nil {
		receivedAt = service.Now().UTC()
	}
	result, err := service.Store.StoreLatest(ctx, machinesnapshot.StoredSnapshot{
		Snapshot: snapshot, ReceivedAt: receivedAt, Digest: hex.EncodeToString(digestBytes[:]),
	})
	if err != nil {
		return machinesnapshot.Receipt{}, fmt.Errorf("store hosted machine snapshot: %w", err)
	}
	current := result.Current
	if current.Snapshot.Login != publisher.Login || current.Snapshot.Machine != publisher.Machine ||
		current.ReceivedAt.IsZero() || current.Digest == "" || current.Snapshot.Validate() != nil {
		return machinesnapshot.Receipt{}, errors.New("snapshot store returned an invalid publisher record")
	}
	return machinesnapshot.Receipt{
		Login: current.Snapshot.Login, Machine: current.Snapshot.Machine,
		PublishedAt: current.Snapshot.PublishedAt, ReceivedAt: current.ReceivedAt,
		Updated: result.Updated,
	}, nil
}

// List returns every durable machine for the authenticated login. Publisher
// credentials never grant cross-login reads.
func (service MachineSnapshotService) List(ctx context.Context, publisher MachinePublisher) (machinesnapshot.ListResponse, error) {
	if strings.TrimSpace(publisher.Login) == "" || strings.TrimSpace(publisher.Machine) == "" {
		return machinesnapshot.ListResponse{}, ErrPublisherIdentity
	}
	if service.Store == nil {
		return machinesnapshot.ListResponse{}, ErrNoReadModel
	}
	records, err := service.Store.ListLatest(ctx)
	if err != nil {
		return machinesnapshot.ListResponse{}, fmt.Errorf("list hosted machine snapshots: %w", err)
	}
	visible := make([]machinesnapshot.PublishedSnapshot, 0, len(records))
	for _, record := range records {
		if record.Snapshot.Login != publisher.Login {
			continue
		}
		if err := record.Snapshot.Validate(); err != nil {
			return machinesnapshot.ListResponse{}, fmt.Errorf("stored hosted machine snapshot is invalid: %w", err)
		}
		if record.ReceivedAt.IsZero() || record.Digest == "" {
			return machinesnapshot.ListResponse{}, errors.New("stored hosted machine snapshot is missing receipt evidence")
		}
		visible = append(visible, machinesnapshot.PublishedSnapshot{Snapshot: record.Snapshot, ReceivedAt: record.ReceivedAt})
	}
	machinesnapshot.SortPublished(visible)
	return machinesnapshot.ListResponse{Snapshots: visible}, nil
}

// StoredSnapshotSource adapts durable hosted snapshots to the established
// RemoteStateWorktreeReadModel input. Per-viewer authorization remains in that
// read model; this adapter only removes invalid records and preserves errors.
type StoredSnapshotSource struct {
	Store machinesnapshot.SnapshotStore
}

func (source StoredSnapshotSource) List(ctx context.Context) ([]remotestate.Entry, error) {
	if source.Store == nil {
		return nil, ErrNoReadModel
	}
	records, err := source.Store.ListLatest(ctx)
	if err != nil {
		return nil, err
	}
	entries := make([]remotestate.Entry, 0, len(records))
	for _, record := range records {
		if err := record.Snapshot.Validate(); err != nil {
			entries = append(entries, remotestate.Entry{
				Snapshot: remotestate.Snapshot{Login: record.Snapshot.Login, Machine: record.Snapshot.Machine},
				Error:    err.Error(),
			})
			continue
		}
		entries = append(entries, remotestatehub.Entry(record))
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Snapshot.Key() < entries[j].Snapshot.Key() })
	return entries, nil
}
