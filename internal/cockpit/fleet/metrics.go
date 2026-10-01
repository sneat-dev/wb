package fleet

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/cockpit"
	"github.com/sneat-dev/wb/internal/cockpit/machinemetrics"
)

// MetricsRoute is the machine-metrics route under cockpit.APIPrefix, and
// metricsQuery its machine parameter: the machine's id, not its name.
const (
	MetricsRoute = "machine-metrics"
	metricsQuery = "machine"
)

// The sources of a metrics answer, and the reasons of an answer with none.
const (
	RouteLiveRemote = "live-remote"
	RouteNone       = "none"

	ReasonNoSource    = "no_source"
	ReasonUnsupported = "unsupported"
)

// MetricsResponse is the body of the machine-metrics route
// (cockpit-views#req:machine-metrics-route). Samples is never null.
type MetricsResponse struct {
	Machine   string                  `json:"machine"`
	Route     string                  `json:"route"`
	FetchedAt *time.Time              `json:"fetched_at,omitempty"`
	Samples   []machinemetrics.Sample `json:"samples"`
	Reason    string                  `json:"reason,omitempty"`
}

// MetricsAnswer is what one MetricsSource knows of a machine. Version must
// change whenever anything else in the answer does: the route prepares the body
// once for each version, never once for each request.
type MetricsAnswer struct {
	Route     string
	FetchedAt *time.Time
	Samples   []machinemetrics.Sample
	Reason    string
	Version   uint64
}

// MetricsSource answers for the machines it holds metrics of, from memory only:
// it must never fetch, read a file or start anything. The sources are asked in
// order, and the first that knows the machine answers; the live remote and
// cached sources (cockpit-views tasks 5 and 8) plug in here ahead of the local one.
type MetricsSource interface {
	MachineMetrics(machineID string) (MetricsAnswer, bool)
}

// localMetrics answers for this machine from its sampler: its history as
// `local`, or `none` with a reason where the platform has no sampler.
type localMetrics struct {
	machineID string
	sampler   *machinemetrics.Sampler
}

func (l localMetrics) MachineMetrics(machineID string) (MetricsAnswer, bool) {
	if machineID != l.machineID {
		return MetricsAnswer{}, false
	}
	snapshot := l.sampler.Snapshot()
	if !snapshot.Supported {
		return MetricsAnswer{Route: RouteNone, Reason: ReasonUnsupported, Version: snapshot.Version}, true
	}
	return MetricsAnswer{Route: RouteLocal, Samples: snapshot.Samples, Version: snapshot.Version}, true
}

// metricsCache holds the body prepared for each machine's last answer.
type metricsCache struct {
	mu      sync.Mutex
	entries map[string]cachedMetrics
}

type cachedMetrics struct {
	route   string
	version uint64
	payload cockpit.Payload
}

// hasMachine reports whether id is a machine of the fleet document, or this one.
func (s *Snapshotter) hasMachine(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if id == localMachineID(s.machine) {
		return true
	}
	for _, machine := range s.doc.Machines {
		if machine.ID == id {
			return true
		}
	}
	return false
}

// MachineMetrics returns the metrics answer for machine id prepared for serving,
// asking each source in order, and found false for an id that is not in the fleet
// document. A machine no source knows has `none`. It reads memory only.
func (s *Snapshotter) MachineMetrics(id string) (payload cockpit.Payload, found bool) {
	if !s.hasMachine(id) {
		return cockpit.Payload{}, false
	}
	answer := MetricsAnswer{Route: RouteNone, Reason: ReasonNoSource}
	for _, source := range s.metricsSources {
		if known, ok := source.MachineMetrics(id); ok {
			answer = known
			break
		}
	}
	s.metrics.mu.Lock()
	held, ok := s.metrics.entries[id]
	s.metrics.mu.Unlock()
	if ok && held.route == answer.Route && held.version == answer.Version {
		return held.payload, true
	}
	// Marshal and compress outside the lock, as Branches does: two requests that
	// race build the same bytes, and an older answer never replaces a newer one.
	response := MetricsResponse{Machine: id, Route: answer.Route, FetchedAt: answer.FetchedAt, Samples: answer.Samples, Reason: answer.Reason}
	if response.Samples == nil {
		response.Samples = []machinemetrics.Sample{}
	}
	body, _ := json.Marshal(response)
	payload = cockpit.NewPayload(append(body, '\n'), s.compress)
	s.metrics.mu.Lock()
	defer s.metrics.mu.Unlock()
	if current, exists := s.metrics.entries[id]; !exists || current.version <= answer.Version {
		s.metrics.entries[id] = cachedMetrics{route: answer.Route, version: answer.Version, payload: payload}
	}
	return payload, true
}

// serveMetrics answers the machine named by the "machine" query parameter from
// the sources' memory (never a fetch), with the shared writer's gzip and ETag.
func (s *Snapshotter) serveMetrics(writer http.ResponseWriter, request *http.Request, _ cockpit.Principal) {
	payload, found := s.MachineMetrics(request.URL.Query().Get(metricsQuery))
	if !found {
		writeError(writer, http.StatusNotFound, "unknown_machine")
		return
	}
	cockpit.ServePayload(writer, request, payload)
}
