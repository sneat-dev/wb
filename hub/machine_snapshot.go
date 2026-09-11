package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
)

type MachineSnapshotService struct {
	Store MachineSnapshotStore
	Now   func() time.Time
}

func (service MachineSnapshotService) Publish(ctx context.Context, machine Machine, snapshot machinesnapshot.Snapshot) (machinesnapshot.Receipt, error) {
	if !machine.valid() || !hasScope(machine.Scopes, ScopeSnapshotPublish) || service.Store == nil {
		return machinesnapshot.Receipt{}, ErrUnauthorized
	}
	if snapshot.Machine != "" && snapshot.Machine != machine.Name {
		return machinesnapshot.Receipt{}, errors.New("snapshot machine does not match credential")
	}
	snapshot.Machine = machine.Name
	for index := range snapshot.Repositories {
		snapshot.Repositories[index] = strings.ToLower(strings.TrimSpace(snapshot.Repositories[index]))
	}
	slices.Sort(snapshot.Repositories)
	snapshot.Repositories = slices.Compact(snapshot.Repositories)
	if err := snapshot.Validate(); err != nil {
		return machinesnapshot.Receipt{}, err
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return machinesnapshot.Receipt{}, errors.New("encode machine snapshot")
	}
	digest := sha256.Sum256(payload)
	receivedAt := time.Now().UTC()
	if service.Now != nil {
		receivedAt = service.Now().UTC()
	}
	result, err := service.Store.StoreLatest(ctx, StoredMachineSnapshot{IdentityID: machine.IdentityID, MachineID: machine.ID, Snapshot: snapshot, ReceivedAt: receivedAt, Digest: hex.EncodeToString(digest[:])})
	if err != nil {
		return machinesnapshot.Receipt{}, fmt.Errorf("store machine snapshot: %w", err)
	}
	current := result.Current
	if current.IdentityID != machine.IdentityID || current.MachineID != machine.ID || current.Snapshot.Machine != machine.Name || current.ReceivedAt.IsZero() || current.Digest == "" || current.Snapshot.Validate() != nil {
		return machinesnapshot.Receipt{}, errors.New("snapshot store returned invalid record")
	}
	return machinesnapshot.Receipt{IdentityID: current.IdentityID, MachineID: current.MachineID, Login: current.Snapshot.Login, Machine: current.Snapshot.Machine, PublishedAt: current.Snapshot.PublishedAt, ReceivedAt: current.ReceivedAt, Updated: result.Updated}, nil
}

func (service MachineSnapshotService) List(ctx context.Context, machine Machine) (machinesnapshot.ListResponse, error) {
	if !machine.valid() || !hasScope(machine.Scopes, ScopeSnapshotRead) || service.Store == nil {
		return machinesnapshot.ListResponse{}, ErrUnauthorized
	}
	records, err := service.Store.ListLatest(ctx)
	if err != nil {
		return machinesnapshot.ListResponse{}, err
	}
	visible := make([]machinesnapshot.PublishedSnapshot, 0, len(records))
	for _, record := range records {
		if record.IdentityID != machine.IdentityID {
			continue
		}
		if record.Snapshot.Validate() != nil || record.ReceivedAt.IsZero() || record.Digest == "" {
			return machinesnapshot.ListResponse{}, errors.New("stored machine snapshot is invalid")
		}
		visible = append(visible, machinesnapshot.PublishedSnapshot{Snapshot: record.Snapshot, ReceivedAt: record.ReceivedAt})
	}
	slices.SortFunc(visible, func(a, b machinesnapshot.PublishedSnapshot) int {
		if a.Snapshot.Machine < b.Snapshot.Machine {
			return -1
		}
		if a.Snapshot.Machine > b.Snapshot.Machine {
			return 1
		}
		return 0
	})
	return machinesnapshot.ListResponse{Snapshots: visible}, nil
}

func ResolveLatestMachineSnapshot(current *StoredMachineSnapshot, candidate StoredMachineSnapshot) (MachineSnapshotStoreResult, error) {
	if candidate.IdentityID == "" || candidate.MachineID == "" || candidate.Digest == "" || candidate.ReceivedAt.IsZero() || candidate.Snapshot.Validate() != nil {
		return MachineSnapshotStoreResult{}, errors.New("candidate machine snapshot is invalid")
	}
	if current == nil {
		return MachineSnapshotStoreResult{Current: candidate, Updated: true}, nil
	}
	if current.IdentityID != candidate.IdentityID || current.MachineID != candidate.MachineID {
		return MachineSnapshotStoreResult{}, errors.New("cannot compare snapshots for different authenticated machines")
	}
	if current.Digest == candidate.Digest {
		return MachineSnapshotStoreResult{Current: *current}, nil
	}
	if candidate.Snapshot.PublishedAt.Before(current.Snapshot.PublishedAt) {
		return MachineSnapshotStoreResult{Current: *current}, machinesnapshot.ErrStaleSnapshot
	}
	if candidate.Snapshot.PublishedAt.Equal(current.Snapshot.PublishedAt) {
		return MachineSnapshotStoreResult{Current: *current}, machinesnapshot.ErrSnapshotConflict
	}
	return MachineSnapshotStoreResult{Current: candidate, Updated: true}, nil
}
