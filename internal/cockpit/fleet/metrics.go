package fleet

import (
	"encoding/json"
	"math"
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
	ReasonUnavailable = "unavailable"
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

// MetricsAnswer is what one MetricsSource knows of a machine.
type MetricsAnswer struct {
	Route     string
	FetchedAt *time.Time
	Samples   []machinemetrics.Sample
	Reason    string
	Version   uint64
}

// MetricsSource answers for the machines it holds metrics of, from memory only: it
// must never fetch, read a file or start anything. The contract:
//   - It returns false for a machine it knows nothing of, and the next source is
//     asked (live remote, then cached, as cockpit-views#req:machine-metrics-route
//     orders them); the first that returns true answers.
//   - Staleness is the source's job: a source whose data is too old to serve
//     returns false rather than an old answer.
//   - Version must change whenever anything else in the answer does, and must not go
//     back while the route stays the same: the route prepares the body once for each
//     (route, version), never once for each request.
//   - Whatever it returns is untrusted: the route sanitizes every answer (see
//     sanitizeMetrics) before it is marshalled.
//
// This machine's own id is never put to the sources: it is always answered by the
// local sampler.
type MetricsSource interface {
	MachineMetrics(machineID string) (MetricsAnswer, bool)
}

// maxSkew is how far ahead of the daemon's clock a sample or fetch time may be
// before it is taken as wrong; it allows for ordinary clock differences between
// machines.
const maxSkew = 5 * time.Second

// marshalMetrics marshals a response; a test replaces it.
var marshalMetrics = json.Marshal

// localMetrics answers for this machine from its sampler: its history as
// `local`, or `none` with a reason where the platform has no sampler or the
// sampler cannot read anything.
type localMetrics struct {
	sampler *machinemetrics.Sampler
}

func (l localMetrics) answer() MetricsAnswer {
	snapshot := l.sampler.Snapshot()
	switch {
	case !snapshot.Supported:
		return MetricsAnswer{Route: RouteNone, Reason: ReasonUnsupported, Version: snapshot.Version}
	case snapshot.Failing:
		return MetricsAnswer{Route: RouteNone, Reason: ReasonUnavailable, Version: snapshot.Version}
	}
	return MetricsAnswer{Route: RouteLocal, Samples: snapshot.Samples, Version: snapshot.Version}
}

// sanitizeMetrics makes an answer fit to serve whatever its source gave: the route
// is one of the four (and `live-remote` has a fetch time that is not in the future,
// and only it keeps one), the reason one of the closed set, at most 360 samples, each
// with its time set and not in the future, in strictly increasing time order (a
// clock step drops what is out of order), a percent within 0 to 100, no negative or
// non-finite number, and a memory or disk figure only with its total and not above
// it. An answer that cannot be made fit becomes `none`.
func sanitizeMetrics(answer MetricsAnswer, now time.Time) MetricsAnswer {
	none := func(reason string) MetricsAnswer {
		return MetricsAnswer{Route: RouteNone, Reason: reason, Version: answer.Version}
	}
	switch answer.Route {
	case RouteLocal, RouteCached:
		answer.FetchedAt = nil
	case RouteLiveRemote:
		if answer.FetchedAt == nil || answer.FetchedAt.After(now.Add(maxSkew)) {
			return none(ReasonUnavailable)
		}
	case RouteNone:
		if answer.Reason != ReasonUnsupported && answer.Reason != ReasonUnavailable {
			answer.Reason = ReasonNoSource
		}
		return none(answer.Reason)
	default:
		return none(ReasonUnavailable)
	}
	answer.Reason = ""
	kept := make([]machinemetrics.Sample, 0, min(len(answer.Samples), machinemetrics.Capacity))
	var last time.Time
	for _, sample := range answer.Samples {
		if sample.SampledAt.IsZero() || sample.SampledAt.After(now.Add(maxSkew)) || !sample.SampledAt.After(last) {
			continue
		}
		last = sample.SampledAt
		kept = append(kept, cleanSample(sample))
	}
	answer.Samples = kept[max(0, len(kept)-machinemetrics.Capacity):]
	return answer
}

// cleanSample drops the figures of a sample that are out of range.
func cleanSample(sample machinemetrics.Sample) machinemetrics.Sample {
	if sample.CPUPercent != nil && !(*sample.CPUPercent >= 0 && *sample.CPUPercent <= 100) {
		sample.CPUPercent = nil
	}
	if sample.Load1 != nil && !(*sample.Load1 >= 0 && !math.IsInf(*sample.Load1, 0)) {
		sample.Load1 = nil
	}
	if sample.MemoryUsedBytes == nil || sample.MemoryTotalBytes == nil || *sample.MemoryUsedBytes > *sample.MemoryTotalBytes {
		sample.MemoryUsedBytes, sample.MemoryTotalBytes = nil, nil
	}
	if sample.DiskFreeBytes == nil || sample.DiskTotalBytes == nil || *sample.DiskFreeBytes > *sample.DiskTotalBytes {
		sample.DiskFreeBytes, sample.DiskTotalBytes = nil, nil
	}
	return sample
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

// machineIDs is the ids of the fleet document's machines and this one.
func (s *Snapshotter) machineIDs() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := map[string]bool{localMachineID(s.machine): true}
	for _, machine := range s.doc.Machines {
		ids[machine.ID] = true
	}
	return ids
}

// metricsAnswerFor asks for id's answer: this machine's own id is answered by its
// sampler (or has no source), any other by the first source that knows it.
func (s *Snapshotter) metricsAnswerFor(id string) MetricsAnswer {
	if id == localMachineID(s.machine) {
		if s.sampler == nil {
			return MetricsAnswer{Route: RouteNone, Reason: ReasonNoSource}
		}
		return localMetrics{sampler: s.sampler}.answer()
	}
	for _, source := range s.metricsSources {
		if known, ok := source.MachineMetrics(id); ok {
			return known
		}
	}
	return MetricsAnswer{Route: RouteNone, Reason: ReasonNoSource}
}

// MachineMetrics returns the metrics answer for machine id prepared for serving,
// and found false for an id that is not in the fleet document. It reads memory only.
func (s *Snapshotter) MachineMetrics(id string) (payload cockpit.Payload, found bool) {
	known := s.machineIDs()
	if !known[id] {
		return cockpit.Payload{}, false
	}
	answer := s.metricsAnswerFor(id)
	s.metrics.mu.Lock()
	held, ok := s.metrics.entries[id]
	s.metrics.mu.Unlock()
	if ok && held.route == answer.Route && held.version == answer.Version {
		return held.payload, true
	}
	// Sanitize, marshal and compress outside the lock, as Branches does: two requests
	// that race build the same bytes.
	answer = sanitizeMetrics(answer, s.now())
	response := MetricsResponse{Machine: id, Route: answer.Route, FetchedAt: answer.FetchedAt, Samples: answer.Samples, Reason: answer.Reason}
	if response.Samples == nil {
		response.Samples = []machinemetrics.Sample{}
	}
	body, err := marshalMetrics(response)
	if err != nil {
		answer = MetricsAnswer{Route: RouteNone, Reason: ReasonUnavailable, Version: answer.Version}
		body, _ = json.Marshal(MetricsResponse{Machine: id, Route: RouteNone, Samples: []machinemetrics.Sample{}, Reason: ReasonUnavailable})
	}
	payload = cockpit.NewPayload(append(body, '\n'), s.compress)
	s.metrics.mu.Lock()
	defer s.metrics.mu.Unlock()
	// Another source's version is not comparable with this one's: a change of route
	// replaces the body, and within a route an older version never replaces a newer.
	if current, exists := s.metrics.entries[id]; !exists || current.route != answer.Route || current.version <= answer.Version {
		s.metrics.entries[id] = cachedMetrics{route: answer.Route, version: answer.Version, payload: payload}
	}
	for held := range s.metrics.entries {
		if !known[held] {
			delete(s.metrics.entries, held)
		}
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
