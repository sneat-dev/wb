//go:build windows

package agents

import "time"

// acquireLeaseForMode refuses a shared checkout on Windows, where this build has
// no inherited advisory lock: a second writer could not be excluded. A unique
// worktree cannot be shared by construction, so those modes keep working
// without a lease.
func acquireLeaseForMode(store Store, request DispatchRequest, target worktreeResolution, now time.Time) (*Lease, error) {
	if request.Mode == ModeCurrent || request.Mode == ModePath {
		return nil, requestErrorf("this platform cannot take an exclusive lease on a shared checkout; use --worktree auto or --new-worktree, which give the run its own worktree")
	}
	return nil, nil
}
