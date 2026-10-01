package hub

import (
	"net/http"
	"strings"
)

// MachineExportPath serves the host machine's own Cockpit export envelope to
// another machine's daemon (cockpit-views#req:hub-export-route).
const MachineExportPath = APIPrefix + "/machines/export"

// metricsOnlyParameter selects the metrics-only envelope when it is "1".
const metricsOnlyParameter = "metrics_only"

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
	// Export returns the host machine's own envelope as JSON, built in process
	// from the daemon's state, or false when it cannot be built yet. It must
	// run nothing and return only that machine's own entries.
	Export func(metricsOnly bool) (body []byte, ok bool)
}

func (export *MachineExport) usable() bool {
	return export != nil && export.Export != nil && strings.TrimSpace(export.OwnerIdentityID) != ""
}

// exportMachine answers MachineExportPath. It is authenticated only by a
// machine bearer credential: no viewer, cookie or anonymous principal is
// consulted. The credential must carry ScopeSnapshotRead (a peer credential,
// which holds peer:session alone, does not) and be of the host owner's
// identity; another identity is refused with 403 and everything else with 401.
// No part of the request chooses what is exported: the only input is whether
// the fleet is left out.
func (h apiHandler) exportMachine(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	machine, ok := h.machine(r)
	if !ok || !hasScope(machine.Scopes, ScopeSnapshotRead) {
		writeError(w, http.StatusUnauthorized, "machine_bearer_unavailable")
		return
	}
	if machine.IdentityID != h.options.MachineExport.OwnerIdentityID {
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
	body, ok := h.options.MachineExport.Export(metricsOnly)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "machine_export_unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
