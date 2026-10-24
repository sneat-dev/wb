package remotestate

import "fmt"

// SyncError reports that a provider could not bring its local copy of the
// store up to date with the shared one. Until it is fixed nothing this
// machine writes — claims, releases, snapshots — reaches other machines, so
// callers that otherwise treat remote side effects as advisory surface it as
// a warning naming Store and Remedy (sneat-dev/wb#899).
type SyncError struct {
	// Store names the local copy that is not syncing, e.g. a clone path.
	Store string
	// Remedy says what the operator should do about it.
	Remedy string
	Err    error
}

func (e *SyncError) Error() string {
	return fmt.Sprintf("remote store %s is not syncing: %v; %s", e.Store, e.Err, e.Remedy)
}

func (e *SyncError) Unwrap() error { return e.Err }
