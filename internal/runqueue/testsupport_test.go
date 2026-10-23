package runqueue

// Helpers kept for tests only: no production caller remains.

// Heartbeat refreshes this Lease's holder record(s) immediately, on top of
// the automatic background heartbeat armHeartbeat already runs. Safe to call
// on a nil Lease or one with nothing to refresh; callers no longer need to
// call this on a timer themselves.
func (lease *Lease) Heartbeat() {
	if lease == nil || lease.heartbeat == nil {
		return
	}
	lease.heartbeat()
}
