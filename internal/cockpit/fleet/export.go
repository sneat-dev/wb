package fleet

import (
	"reflect"
	"slices"
	"time"

	"github.com/sneat-dev/wb/internal/cockpit/machinemetrics"
)

// The export envelope is what one machine tells another about itself
// (cockpit-views#req:cockpit-export-verb): this machine's own entries of the
// fleet document, which the anonymous-local principal may read, and its
// machine-metrics history. It travels over the loopback transport of the CLI
// verb, over the hub route and over SSH, and the same strict decoder in
// exportcheck.go is the security boundary for every one of them
// (cockpit-views#req:remote-envelope-is-untrusted): what a remote machine
// sends is data, never trusted, never applied to this machine.

// ExportSchemaVersion is the envelope format this binary writes and reads.
const ExportSchemaVersion = 1

// MaxEnvelopeBytes bounds an envelope as printed and as read.
const MaxEnvelopeBytes = 8 << 20

// The three error codes of an export that could not be made, printed as an
// ExportError: no daemon is running, the daemon refuses anonymous reads, or
// anything else went wrong (an unreadable record, a daemon that answers badly
// or that this binary does not understand).
const (
	ErrorDaemonNotRunning = "daemon_not_running"
	ErrorExportRefused    = "export_refused"
	ErrorExportFailed     = "export_failed"
)

// Envelope is the export envelope. Fleet is absent in a metrics-only export.
type Envelope struct {
	SchemaVersion int `json:"schema_version"`
	// Machine is the exporting machine's name. It is informational: no reader
	// places an entry by it.
	Machine    string           `json:"machine"`
	ExportedAt time.Time        `json:"exported_at"`
	Fleet      *Document        `json:"fleet,omitempty"`
	Metrics    *EnvelopeMetrics `json:"metrics,omitempty"`
	// Dropped is the number of this machine's own entries left out because a
	// value of theirs would not pass the envelope's rules (a name longer than
	// the cap, a time in the future): one odd entry is dropped, and the export
	// is still made.
	Dropped int `json:"dropped,omitempty"`
}

// EnvelopeMetrics is the exporting machine's own metrics: the route it came
// by (`local`, or `none` with a Reason) and the history, oldest first, whose
// last element is the latest sample.
type EnvelopeMetrics struct {
	Route   string                  `json:"route"`
	Samples []machinemetrics.Sample `json:"samples"`
	Reason  string                  `json:"reason,omitempty"`
}

// ExportError is what the export verb prints when it could not make an
// envelope.
type ExportError struct {
	SchemaVersion int    `json:"schema_version"`
	Error         string `json:"error"`
}

// NewExportError is the ExportError for code.
func NewExportError(code string) ExportError {
	return ExportError{SchemaVersion: ExportSchemaVersion, Error: code}
}

// NewEnvelope is the one function that builds an envelope, for the CLI verb
// (from what the daemon's routes served) and for the hub route (from the
// daemon's own state in process). It keeps this machine's own entries only:
// an entry that is cached from another machine, or live from one, is never
// re-exported. A metrics-only envelope has no fleet.
//
// An entry of this machine that would not pass the strict decoder's rules for
// its fields (a task or branch name over the cap, a time in the future) is left
// out and counted in Dropped, so one odd name cannot take a machine's whole
// export down. The machine entry itself is always kept: an export whose machine
// entry is not valid is refused as a whole by the decoder.
//
// Every scalar the document carries besides its collections is about this
// machine alone: repositories_total, repositories_scanned and diagnostics count
// what this daemon scanned and read locally (another machine's entries are
// mapped separately and do not enter them), error and code_index_provider are
// this daemon's own, snapshot_at is its own snapshot's time, and agents_truncated
// says that this machine's own agents were cut at 200, so it stays true to the
// envelope's own agents list.
func NewEnvelope(document Document, metrics MetricsResponse, now time.Time, metricsOnly bool) Envelope {
	envelope := Envelope{SchemaVersion: ExportSchemaVersion, ExportedAt: now.UTC(), Metrics: exportMetrics(metrics)}
	for _, machine := range document.Machines {
		if machine.Route == RouteLocal {
			envelope.Machine = machine.Machine
			break
		}
	}
	if !metricsOnly {
		own := document
		own.Machines = keepLocal(document.Machines, func(machine Machine) Entry { return machine.Entry })
		own.Repositories = keepValid(document.Repositories, func(repository Repository) Entry { return repository.Entry }, now, &envelope.Dropped)
		own.Worktrees = keepValid(document.Worktrees, func(worktree Worktree) Entry { return worktree.Entry }, now, &envelope.Dropped)
		own.PullRequests = keepValid(document.PullRequests, func(pull PullRequest) Entry { return pull.Entry }, now, &envelope.Dropped)
		own.Agents = keepValid(document.Agents, func(agent Agent) Entry { return agent.Entry }, now, &envelope.Dropped)
		envelope.Fleet = &own
	}
	return envelope
}

// keepLocal is the entries of list that this machine observed itself, as a
// list that is never nil.
func keepLocal[T any](list []T, entry func(T) Entry) []T {
	kept := make([]T, 0, len(list))
	for _, item := range list {
		if entry(item).Route == RouteLocal {
			kept = append(kept, item)
		}
	}
	return kept
}

// keepValid is keepLocal less the entries that the strict decoder would refuse
// for a field of theirs, which it counts in dropped.
func keepValid[T any](list []T, entry func(T) Entry, now time.Time, dropped *int) []T {
	kept := make([]T, 0, len(list))
	for _, item := range keepLocal(list, entry) {
		if checkStruct(reflect.ValueOf(item), "", now) != nil {
			*dropped++
			continue
		}
		kept = append(kept, item)
	}
	return kept
}

// exportMetrics maps the machine-metrics answer for this machine: only its
// own history (`local`) is exported, and any other answer is `none`.
func exportMetrics(response MetricsResponse) *EnvelopeMetrics {
	switch response.Route {
	case RouteLocal:
		samples := response.Samples
		if samples == nil {
			samples = []machinemetrics.Sample{}
		}
		return &EnvelopeMetrics{Route: RouteLocal, Samples: samples}
	case RouteNone:
		if slices.Contains([]string{ReasonNoSource, ReasonUnsupported, ReasonUnavailable}, response.Reason) {
			return &EnvelopeMetrics{Route: RouteNone, Samples: []machinemetrics.Sample{}, Reason: response.Reason}
		}
	}
	return &EnvelopeMetrics{Route: RouteNone, Samples: []machinemetrics.Sample{}, Reason: ReasonUnavailable}
}

// Export is this machine's export envelope built in process from the last
// published document and the sampler, without running anything: what the hub
// route serves (cockpit-views#req:hub-export-route). It reads memory only.
func (s *Snapshotter) Export(metricsOnly bool) Envelope {
	s.mu.RLock()
	document := s.doc
	s.mu.RUnlock()
	now := s.now()
	answer := sanitizeMetrics(s.metricsAnswerFor(localMachineID(s.machine)), now)
	return NewEnvelope(document, MetricsResponse{Route: answer.Route, Samples: answer.Samples, Reason: answer.Reason}, now, metricsOnly)
}
