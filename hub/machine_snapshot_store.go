package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/sneat-dev/wb/api/githubapp"
)

const machineSnapshotCollection = "workbench_machine_snapshots"

var errMachineSnapshotStoreUnavailable = errors.New("workbench machine snapshot store is unavailable")

// machineSnapshotStore binds the host-neutral latest-snapshot contract to a
// document store. Ordering and conflict semantics remain owned by
// ResolveLatestMachineSnapshot; this adapter only executes them atomically.
type machineSnapshotStore struct {
	backend githubapp.DocumentStore
}

func (store machineSnapshotStore) StoreLatest(ctx context.Context, candidate StoredMachineSnapshot) (result MachineSnapshotStoreResult, err error) {
	if store.backend == nil {
		return MachineSnapshotStoreResult{}, errMachineSnapshotStoreUnavailable
	}
	digest := sha256.Sum256([]byte(candidate.MachineID))
	documentID := hex.EncodeToString(digest[:])
	err = store.backend.UpdateAtomic(ctx, func(transaction githubapp.DocumentTransaction) error {
		var current StoredMachineSnapshot
		found, getErr := transaction.Get(ctx, machineSnapshotCollection, documentID, &current)
		if getErr != nil {
			return fmt.Errorf("read current Workbench machine snapshot: %w", getErr)
		}
		var currentPointer *StoredMachineSnapshot
		if found {
			currentPointer = &current
		}
		resolved, resolveErr := ResolveLatestMachineSnapshot(currentPointer, candidate)
		if resolveErr != nil {
			return resolveErr
		}
		result = resolved
		if !resolved.Updated {
			return nil
		}
		if setErr := transaction.Set(ctx, machineSnapshotCollection, documentID, resolved.Current); setErr != nil {
			return fmt.Errorf("write Workbench machine snapshot: %w", setErr)
		}
		return nil
	})
	if err != nil {
		return MachineSnapshotStoreResult{}, err
	}
	return result, nil
}

func (store machineSnapshotStore) ListLatest(ctx context.Context) ([]StoredMachineSnapshot, error) {
	if store.backend == nil {
		return nil, errMachineSnapshotStoreUnavailable
	}
	var snapshots []StoredMachineSnapshot
	if err := store.backend.Query(ctx, machineSnapshotCollection, nil, 0, &snapshots); err != nil {
		return nil, fmt.Errorf("list Workbench machine snapshots: %w", err)
	}
	return snapshots, nil
}

var _ MachineSnapshotStore = machineSnapshotStore{}
