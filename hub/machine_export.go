package hub

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// MachineExportPath serves the host machine's own Cockpit export envelope to
// another machine's daemon (cockpit-views#req:hub-export-route).
const MachineExportPath = APIPrefix + "/machines/export"

// metricsOnlyParameter selects the metrics-only envelope when it is "1".
const metricsOnlyParameter = "metrics_only"

// The rate one credential may read the export at: a burst of exportBurst
// requests, refilled at one a second. A reader asks once per refresh interval
// and, for metrics, every 30 seconds.
const (
	exportBurst    = 5
	exportInterval = time.Second
)

// MachineExport is what a daemon-hosted hub needs to serve MachineExportPath.
// The hosted multi-identity service never sets it, and then the route does not
// exist: it exposes one machine's state, the machine the hub runs on, and a
// service with many identities has no such machine.
type MachineExport struct {
	// OwnerIdentityID is the identity of the host owner: the one identity a
	// daemon-hosted hub enrols machines under, which is the identity of the
	// operator the daemon runs for. Only a machine credential of this identity
	// may read the export.
	OwnerIdentityID string
	// Serve answers an authenticated request with the host machine's own
	// envelope, built in process from the daemon's state, or with the typed
	// reason there is none. It must run nothing and expose only that machine's
	// own entries. metricsOnly is the one thing a request chooses.
	Serve func(writer http.ResponseWriter, request *http.Request, metricsOnly bool)
	// Now is the clock of the rate limit; nil means time.Now.
	Now func() time.Time
}

func (export *MachineExport) usable() bool {
	return export != nil && export.Serve != nil && strings.TrimSpace(export.OwnerIdentityID) != ""
}

// exportLimiter is a token bucket for each credential that reads the export.
// Only a credential that passed the checks reaches it, so its size is bounded
// by the machines the owner enrolled.
type exportLimiter struct {
	mu      sync.Mutex
	buckets map[string]exportBucket
}

type exportBucket struct {
	tokens float64
	at     time.Time
}

// allow takes one token of machineID's bucket and reports whether it had one.
func (limiter *exportLimiter) allow(machineID string, now time.Time) bool {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if limiter.buckets == nil {
		limiter.buckets = map[string]exportBucket{}
	}
	bucket, known := limiter.buckets[machineID]
	if !known {
		bucket = exportBucket{tokens: exportBurst, at: now}
	}
	if elapsed := now.Sub(bucket.at); elapsed > 0 {
		bucket.tokens = min(exportBurst, bucket.tokens+float64(elapsed)/float64(exportInterval))
		bucket.at = now
	}
	allowed := bucket.tokens >= 1
	if allowed {
		bucket.tokens--
	}
	limiter.buckets[machineID] = bucket
	return allowed
}

// exportMachine answers MachineExportPath. It is authenticated only by a
// machine bearer credential: no viewer, cookie or anonymous principal is
// consulted. The credential must carry ScopeSnapshotRead (a peer credential,
// which holds peer:session alone, does not) and be of the host owner's
// identity; another identity is refused with 403 and everything else with 401.
// A credential that asks more often than the rate allows is answered 429. No
// part of the request chooses what is exported: the only input is whether the
// fleet is left out.
func (h apiHandler) exportMachine(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	machine, ok := h.machine(r)
	if !ok || !hasScope(machine.Scopes, ScopeSnapshotRead) {
		writeError(w, http.StatusUnauthorized, "machine_bearer_unavailable")
		return
	}
	export := h.options.MachineExport
	if machine.IdentityID != export.OwnerIdentityID {
		writeError(w, http.StatusForbidden, "not_the_host_owner")
		return
	}
	metricsOnly := false
	if values, present := r.URL.Query()[metricsOnlyParameter]; present {
		if len(values) != 1 || values[0] != "1" {
			writeError(w, http.StatusBadRequest, "invalid_metrics_only")
			return
		}
		metricsOnly = true
	}
	now := time.Now()
	if export.Now != nil {
		now = export.Now()
	}
	if !h.exports.allow(machine.ID, now) {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "rate_limited")
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	export.Serve(w, r, metricsOnly)
}
