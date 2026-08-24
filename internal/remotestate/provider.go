package remotestate

import "context"

// PublishResult says where a snapshot landed: a commit SHA for a git store,
// a URL for a hub.
type PublishResult struct {
	Location string `json:"location"`
}

// Entry is one machine as read from the store. Error is set when the stored
// snapshot could not be decoded; Snapshot then carries only Login/Machine.
type Entry struct {
	Snapshot Snapshot `json:"snapshot"`
	Error    string   `json:"error,omitempty"`
}

// Provider is a shared store of machine snapshots. Implementations must be
// safe to call from several machines at once; the git provider relies on
// per-machine files plus rebase for that.
type Provider interface {
	// Publish overwrites the caller's own login/machine entry. It is
	// self-contained: implementations refresh their own view of the store
	// before writing, so callers never need a separate refresh step first.
	Publish(ctx context.Context, snapshot Snapshot) (PublishResult, error)
	// List returns every machine currently in the store, including the
	// caller's own last-published entry, sorted by Key(). It is also
	// self-contained, refreshing the store view itself before reading.
	List(ctx context.Context) ([]Entry, error)
	// Claim acquires or refreshes a claim on a task. It is self-contained:
	// implementations refresh the store view before acting. The provider never
	// judges staleness; ClaimTakeOverStale merely authorizes replacing another
	// holder — commands establish staleness first.
	Claim(ctx context.Context, claim Claim, mode ClaimMode) (ClaimOutcome, error)
	// Release removes a claim. It is self-contained, refreshing the store view
	// before acting.
	Release(ctx context.Context, task, login, machine string, force bool) (ReleaseOutcome, error)
	// Claims returns every claim currently in the store, sorted by task name.
	// It is self-contained, refreshing the store view itself before reading.
	Claims(ctx context.Context) ([]ClaimEntry, error)
}
