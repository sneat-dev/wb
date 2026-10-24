//go:build !windows

package agents

import "time"

// acquireLeaseForMode takes the inherited writer lease. Every mode is a writable
// checkout here, so every mode is protected.
func acquireLeaseForMode(store Store, request DispatchRequest, target worktreeResolution, now time.Time) (*Lease, error) {
	return store.AcquireLease(target.Dir, request.AgentID, request.ExperimentID, target.Repository, now)
}
