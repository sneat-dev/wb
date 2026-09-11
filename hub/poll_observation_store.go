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

const pollObservationCollection = "workbench_poll_observations"

var errPollObservationStoreUnavailable = errors.New("workbench poll observation store is unavailable")

// PollObservation is the last state the polling ingester saw for one
// repository. It is the poller's entire memory: without it every restart
// would re-enqueue the current head as though it had just been pushed.
//
// Only the three fields the poller compares are kept. Nothing here is a
// secret: a repository's name, its default branch, and the object ID at that
// branch's head are all already part of the repository-event contract.
type PollObservation struct {
	// Repository is the canonical `github.com/owner/repository` this
	// observation is keyed by, lowercased the way machine snapshots are.
	Repository string `firestore:"repository"`
	// FullName is `owner/repository` exactly as GitHub reported it, so a
	// rename is detected against what the API said rather than against a
	// normalisation of it.
	FullName string `firestore:"full_name"`
	// DefaultBranch is the branch name GitHub reported, without a ref prefix.
	DefaultBranch string `firestore:"default_branch"`
	// SHA is the object ID at the head of DefaultBranch.
	SHA string `firestore:"sha"`
	// ObservedAt is when the poller read those values.
	ObservedAt time.Time `firestore:"observed_at"`
}

func (observation PollObservation) valid() bool {
	return strings.TrimSpace(observation.Repository) != "" && strings.TrimSpace(observation.FullName) != "" &&
		strings.TrimSpace(observation.DefaultBranch) != "" && strings.TrimSpace(observation.SHA) != "" &&
		!observation.ObservedAt.IsZero()
}

// PollObservationStore persists what the poller last saw, keyed by the
// canonical repository name.
type PollObservationStore interface {
	// LoadPollObservation returns the stored observation for a repository.
	// The boolean is false when the poller has never seen it, which is the
	// signal to record without enqueueing.
	LoadPollObservation(context.Context, string) (PollObservation, bool, error)
	// SavePollObservation replaces the observation for observation.Repository.
	SavePollObservation(context.Context, PollObservation) error
	// DeletePollObservation drops the observation a rename re-keyed away
	// from. Deleting one that is not there is not an error.
	DeletePollObservation(context.Context, string) error
}

// NewPollObservationStore binds the poller's memory to a host's
// githubapp.DocumentStore, the same seam every other hub-owned store writes
// through.
func NewPollObservationStore(backend githubapp.DocumentStore) PollObservationStore {
	return pollObservationStore{backend: backend}
}

type pollObservationStore struct {
	backend githubapp.DocumentStore
}

func (store pollObservationStore) LoadPollObservation(ctx context.Context, repository string) (PollObservation, bool, error) {
	if store.backend == nil || strings.TrimSpace(repository) == "" {
		return PollObservation{}, false, errPollObservationStoreUnavailable
	}
	var observation PollObservation
	found, err := store.backend.Get(ctx, pollObservationCollection, pollObservationDocumentID(repository), &observation)
	if err != nil {
		return PollObservation{}, false, fmt.Errorf("read poll observation: %w", err)
	}
	if !found {
		return PollObservation{}, false, nil
	}
	// A stored observation the poller itself could not have written means the
	// document was hand-edited or half-written. Treating it as authoritative
	// would silently suppress an event, so refuse instead.
	if !observation.valid() || observation.Repository != repository {
		return PollObservation{}, false, errors.New("stored poll observation is invalid")
	}
	return observation, true, nil
}

func (store pollObservationStore) SavePollObservation(ctx context.Context, observation PollObservation) error {
	if store.backend == nil || !observation.valid() {
		return errPollObservationStoreUnavailable
	}
	if err := store.backend.Set(ctx, pollObservationCollection, pollObservationDocumentID(observation.Repository), observation); err != nil {
		return fmt.Errorf("write poll observation: %w", err)
	}
	return nil
}

func (store pollObservationStore) DeletePollObservation(ctx context.Context, repository string) error {
	if store.backend == nil || strings.TrimSpace(repository) == "" {
		return errPollObservationStoreUnavailable
	}
	// The document-store seam exposes Delete only inside a transaction, so a
	// single-document delete runs as a one-write atomic update.
	err := store.backend.UpdateAtomic(ctx, func(transaction githubapp.DocumentTransaction) error {
		return transaction.Delete(ctx, pollObservationCollection, pollObservationDocumentID(repository))
	})
	if err != nil {
		return fmt.Errorf("delete poll observation: %w", err)
	}
	return nil
}

func pollObservationDocumentID(repository string) string {
	digest := sha256.Sum256([]byte(repository))
	return hex.EncodeToString(digest[:])
}

var _ PollObservationStore = pollObservationStore{}
