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

// StatusSnapshot is one consistent read of the machine snapshots and task
// claims currently held by a remote store.
type StatusSnapshot struct {
	Machines []Entry      `json:"machines"`
	Claims   []ClaimEntry `json:"claims"`
}

// StatusProvider is the optional provider capability used by remote status to
// refresh once and read both projections from that same store view. Providers
// that cannot batch the reads keep the Provider contract below; ReadStatus
// falls back to its two self-contained methods.
type StatusProvider interface {
	Status(ctx context.Context) (StatusSnapshot, error)
}

// ReadStatus returns the machine and claim projections needed by remote
// status. A provider-level Status implementation can share one refresh; the
// fallback preserves compatibility with providers that expose only the
// self-contained Provider methods.
func ReadStatus(ctx context.Context, provider Provider) (StatusSnapshot, error) {
	if statusProvider, ok := provider.(StatusProvider); ok {
		return statusProvider.Status(ctx)
	}
	machines, err := provider.List(ctx)
	if err != nil {
		return StatusSnapshot{}, err
	}
	claims, err := provider.Claims(ctx)
	if err != nil {
		return StatusSnapshot{}, err
	}
	return StatusSnapshot{Machines: machines, Claims: claims}, nil
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
	//
	// expectedHolder is the "<login>/<machine>" the caller judged stale and
	// is authorizing replacement of; it is "" for ClaimNormal and
	// ClaimForce, which do not need one. For ClaimTakeOverStale it is a
	// precondition: a hub provider maps this to a conditional PUT keyed on
	// the current holder, and a git-backed provider re-checks it against
	// the freshly fetched store before writing. If the actual current
	// holder no longer matches (they released and a third party claimed,
	// or refreshed away their own staleness, between the caller's judgment
	// and this call), the provider must not replace them — it reports an
	// ordinary ClaimHeld naming the real current holder instead.
	Claim(ctx context.Context, claim Claim, mode ClaimMode, expectedHolder string) (ClaimOutcome, error)
	// Release removes a claim. It is self-contained, refreshing the store view
	// before acting.
	Release(ctx context.Context, task, login, machine string, force bool) (ReleaseOutcome, error)
	// Claims returns every claim currently in the store, sorted by task name.
	// It is self-contained, refreshing the store view itself before reading.
	Claims(ctx context.Context) ([]ClaimEntry, error)
}
