package hub

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sneat-dev/wb/api/githubapp"
)

// workbench_peer_trust and workbench_peer_stats are the two of the three
// documents peer-connectivity#req:peer-is-persistent-state splits the peer
// record into: trust, written only by admin operations, and statistics,
// written by the persisters (Task 2 and Task 6). The third — the per-machine
// queue-state document — already exists as
// workbench_repository_event_queues/<machineID> and gains a field there,
// maintained by the store's own enqueue/acknowledge/reset transactions
// (Tasks 3 and 4).
const (
	peerTrustCollection = "workbench_peer_trust"
	peerStatsCollection = "workbench_peer_stats"
)

// PeerTrust is the one bit of trust state a peer record carries: active peers
// are admitted, blocked peers are refused everywhere a credential is
// resolved.
type PeerTrust string

const (
	PeerTrustActive  PeerTrust = "active"
	PeerTrustBlocked PeerTrust = "blocked"
)

// PeerRecord is the trust document: display name and owning identity, the
// bound node ID (empty until Task 2 binds it), trust state, created and
// trust-changed times, and reset_pending. Only admin operations
// (hub.PeerAdminService) write it, as transactional field merges.
type PeerRecord struct {
	MachineID      string    `firestore:"machine_id"`
	Name           string    `firestore:"name"`
	IdentityID     string    `firestore:"identity_id"`
	NodeID         string    `firestore:"node_id,omitempty"`
	Trust          PeerTrust `firestore:"trust"`
	CreatedAt      time.Time `firestore:"created_at"`
	TrustChangedAt time.Time `firestore:"trust_changed_at"`
	ResetPending   bool      `firestore:"reset_pending"`
}

func (record PeerRecord) valid() bool {
	return record.MachineID != "" && record.Name != "" && record.IdentityID != "" &&
		(record.Trust == PeerTrustActive || record.Trust == PeerTrustBlocked) && !record.CreatedAt.IsZero()
}

// PeerStats is the statistics document: reported version/platform/protocol
// and lifetime counters. Every counter is zero until Task 6 fills it; this
// task only creates the document at invite time, so later writers have
// somewhere to merge into.
type PeerStats struct {
	MachineID        string    `firestore:"machine_id"`
	LastSeenAt       time.Time `firestore:"last_seen_at,omitempty"`
	LastConnectedAt  time.Time `firestore:"last_connected_at,omitempty"`
	WBVersion        string    `firestore:"wb_version,omitempty"`
	OS               string    `firestore:"os,omitempty"`
	Arch             string    `firestore:"arch,omitempty"`
	Protocol         int       `firestore:"protocol,omitempty"`
	RXPayloadBytes   uint64    `firestore:"rx_payload_bytes"`
	TXPayloadBytes   uint64    `firestore:"tx_payload_bytes"`
	RXMessages       uint64    `firestore:"rx_messages"`
	TXMessages       uint64    `firestore:"tx_messages"`
	RXEvents         uint64    `firestore:"rx_events"`
	TXEvents         uint64    `firestore:"tx_events"`
	Connections      uint64    `firestore:"connections"`
	ConnectedSeconds uint64    `firestore:"connected_seconds"`
}

var (
	// ErrPeerExists is returned by CreatePeer when a trust document already
	// exists for the given MachineID.
	ErrPeerExists = errors.New("peer record already exists")
	// ErrPeerNotFound is returned by the read and mutate operations below
	// when no trust document exists for the given MachineID or name. A
	// credential with no such record is not a peer at all, which is a
	// distinct, non-error outcome reported through the bool return instead.
	ErrPeerNotFound = errors.New("peer record not found")
)

// PeerTrustStore is the trust document's persistence seam, backed by the same
// DocumentStore port every other hub store uses.
type PeerTrustStore interface {
	// CreatePeer writes a new trust document. It fails with ErrPeerExists if
	// one already exists for record.MachineID.
	CreatePeer(context.Context, PeerRecord) error
	// GetPeer reads the trust document by MachineID. found is false, with a
	// nil error, when no peer record exists — the credential is not a peer.
	GetPeer(context.Context, string) (record PeerRecord, found bool, err error)
	// FindPeerByName reads the trust document by display name, for `<peer>`
	// arguments and invite's existing-name refusal.
	FindPeerByName(context.Context, string) (record PeerRecord, found bool, err error)
	// ListPeers returns every peer trust document, for `wb peers list`.
	ListPeers(context.Context) ([]PeerRecord, error)
	// UpdateTrust reads the current record inside a transaction, applies
	// mutate, and writes the result back — the "transactional field merge"
	// every admin write is. mutate must not change MachineID, Name,
	// IdentityID, or CreatedAt; it returns ErrPeerNotFound when machineID has
	// no trust document.
	UpdateTrust(ctx context.Context, machineID string, mutate func(*PeerRecord)) (PeerRecord, error)
}

// PeerStatsStore is the statistics document's persistence seam.
type PeerStatsStore interface {
	// CreateStats writes a new, all-zero statistics document for machineID.
	// It is idempotent: an existing document is left untouched rather than
	// reset, so a later writer's progress survives a repeated invite.
	CreateStats(context.Context, string) error
	// GetStats reads the statistics document. found is false when none
	// exists yet.
	GetStats(context.Context, string) (stats PeerStats, found bool, err error)
}

// NewPeerStores wires PeerTrustStore and PeerStatsStore to a host's
// api/githubapp DocumentStore, the same port every other hub store uses.
func NewPeerStores(backend githubapp.DocumentStore) (PeerTrustStore, PeerStatsStore) {
	return peerTrustStore{backend: backend}, peerStatsStore{backend: backend}
}

type peerTrustStore struct{ backend githubapp.DocumentStore }

func (store peerTrustStore) CreatePeer(ctx context.Context, record PeerRecord) error {
	if store.backend == nil {
		return errMachineCredentialUnavailable
	}
	if !record.valid() {
		return errors.New("peer record is invalid")
	}
	return store.backend.UpdateAtomic(ctx, func(transaction githubapp.DocumentTransaction) error {
		var existing PeerRecord
		found, err := transaction.Get(ctx, peerTrustCollection, record.MachineID, &existing)
		if err != nil {
			return fmt.Errorf("read current peer trust record: %w", err)
		}
		if found {
			return ErrPeerExists
		}
		if err := transaction.Set(ctx, peerTrustCollection, record.MachineID, record); err != nil {
			return fmt.Errorf("write peer trust record: %w", err)
		}
		return nil
	})
}

func (store peerTrustStore) GetPeer(ctx context.Context, machineID string) (PeerRecord, bool, error) {
	if store.backend == nil {
		return PeerRecord{}, false, errMachineCredentialUnavailable
	}
	var record PeerRecord
	found, err := store.backend.Get(ctx, peerTrustCollection, machineID, &record)
	if err != nil {
		return PeerRecord{}, false, err
	}
	if !found {
		return PeerRecord{}, false, nil
	}
	if !record.valid() {
		return PeerRecord{}, false, errors.New("stored peer trust record is invalid")
	}
	return record, true, nil
}

// FindPeerByName scans every trust document rather than relying on a
// backend-side equality filter: dalgo2ingitdb has no range query
// (spec/plans/peer-connectivity.md Risks), and the peer fleet a single hub
// admits is small enough that an in-process scan costs nothing worth a second
// index document.
func (store peerTrustStore) FindPeerByName(ctx context.Context, name string) (PeerRecord, bool, error) {
	records, err := store.ListPeers(ctx)
	if err != nil {
		return PeerRecord{}, false, err
	}
	for _, record := range records {
		if record.Name == name {
			return record, true, nil
		}
	}
	return PeerRecord{}, false, nil
}

func (store peerTrustStore) ListPeers(ctx context.Context) ([]PeerRecord, error) {
	if store.backend == nil {
		return nil, errMachineCredentialUnavailable
	}
	var records []PeerRecord
	if err := store.backend.Query(ctx, peerTrustCollection, nil, 0, &records); err != nil {
		return nil, fmt.Errorf("list peer trust records: %w", err)
	}
	for _, record := range records {
		if !record.valid() {
			return nil, errors.New("stored peer trust record is invalid")
		}
	}
	return records, nil
}

func (store peerTrustStore) UpdateTrust(ctx context.Context, machineID string, mutate func(*PeerRecord)) (PeerRecord, error) {
	if store.backend == nil {
		return PeerRecord{}, errMachineCredentialUnavailable
	}
	if mutate == nil {
		return PeerRecord{}, errors.New("peer trust update has no mutation")
	}
	var updated PeerRecord
	err := store.backend.UpdateAtomic(ctx, func(transaction githubapp.DocumentTransaction) error {
		var current PeerRecord
		found, err := transaction.Get(ctx, peerTrustCollection, machineID, &current)
		if err != nil {
			return fmt.Errorf("read current peer trust record: %w", err)
		}
		if !found {
			return ErrPeerNotFound
		}
		mutate(&current)
		if !current.valid() || current.MachineID != machineID {
			return errors.New("peer trust mutation produced an invalid record")
		}
		updated = current
		if err := transaction.Set(ctx, peerTrustCollection, machineID, current); err != nil {
			return fmt.Errorf("write peer trust record: %w", err)
		}
		return nil
	})
	if err != nil {
		return PeerRecord{}, err
	}
	return updated, nil
}

type peerStatsStore struct{ backend githubapp.DocumentStore }

func (store peerStatsStore) CreateStats(ctx context.Context, machineID string) error {
	if store.backend == nil {
		return errMachineCredentialUnavailable
	}
	return store.backend.UpdateAtomic(ctx, func(transaction githubapp.DocumentTransaction) error {
		var existing PeerStats
		found, err := transaction.Get(ctx, peerStatsCollection, machineID, &existing)
		if err != nil {
			return fmt.Errorf("read current peer statistics: %w", err)
		}
		if found {
			return nil
		}
		if err := transaction.Set(ctx, peerStatsCollection, machineID, PeerStats{MachineID: machineID}); err != nil {
			return fmt.Errorf("write peer statistics: %w", err)
		}
		return nil
	})
}

func (store peerStatsStore) GetStats(ctx context.Context, machineID string) (PeerStats, bool, error) {
	if store.backend == nil {
		return PeerStats{}, false, errMachineCredentialUnavailable
	}
	var stats PeerStats
	found, err := store.backend.Get(ctx, peerStatsCollection, machineID, &stats)
	if err != nil {
		return PeerStats{}, false, err
	}
	return stats, found, nil
}

var (
	_ PeerTrustStore = peerTrustStore{}
	_ PeerStatsStore = peerStatsStore{}
)
