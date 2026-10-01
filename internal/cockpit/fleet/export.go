package fleet

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/cockpit"

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

// Three of the error codes of an export that could not be made, printed as an
// ExportError: no daemon is running, the daemon refuses anonymous reads, or
// anything else went wrong (an unreadable record, a daemon that answers badly
// or that this binary does not understand).
const (
	ErrorDaemonNotRunning = "daemon_not_running"
	ErrorExportRefused    = "export_refused"
	ErrorExportFailed     = "export_failed"
)

// ErrorWarmingUp is the fourth: the daemon's first pass has not ended, so its
// fleet is partial and is not exported (a reader keeps what it holds).
const ErrorWarmingUp = "warming_up"

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

// ExportDrops counts the entries of this machine an export left out, by kind.
// It holds numbers only: what was dropped is never named.
type ExportDrops struct {
	Repositories, Worktrees, PullRequests, Agents int
}

// Total is the number of entries left out.
func (d ExportDrops) Total() int {
	return d.Repositories + d.Worktrees + d.PullRequests + d.Agents
}

// NewEnvelope is the one function that builds an envelope, for the CLI verb
// (from what the daemon's routes served) and for the hub route (from the
// daemon's own state in process). It keeps this machine's own entries only:
// an entry that is cached from another machine, or live from one, is never
// re-exported. A metrics-only envelope has no fleet.
//
// An entry of this machine that the strict decoder would refuse is left out
// and counted (in the envelope's Dropped, and by kind in the drops returned),
// so one odd entry cannot take a machine's whole export down: an entry with a
// field that breaks its rule (a name over the cap, a time in the future), one
// that breaks a rule between entries (an id used twice, a web address that is
// not the one built from its host and name, a pull request address off its
// repository's host), and every worktree, pull request and agent of a
// repository that was left out. The machine entry itself is always kept: an
// export whose machine entry is not valid is refused as a whole.
//
// Every scalar the document carries besides its collections is about this
// machine alone: repositories_total, repositories_scanned and diagnostics count
// what this daemon scanned and read locally (another machine's entries are
// mapped separately and do not enter them), error and code_index_provider are
// this daemon's own, snapshot_at is its own snapshot's time, and agents_truncated
// says that this machine's own agents were cut at 200, so it stays true to the
// envelope's own agents list.
func NewEnvelope(document Document, metrics MetricsResponse, now time.Time, metricsOnly bool) (Envelope, ExportDrops) {
	envelope := Envelope{SchemaVersion: ExportSchemaVersion, ExportedAt: now.UTC(), Metrics: exportMetrics(metrics)}
	for _, machine := range document.Machines {
		if machine.Route == RouteLocal {
			envelope.Machine = machine.Machine
			break
		}
	}
	var drops ExportDrops
	if !metricsOnly {
		var own Document
		own, drops = ownEntries(document, now)
		envelope.Fleet, envelope.Dropped = &own, drops.Total()
	}
	return envelope, drops
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

// ownEntries is document with only this machine's entries, less the ones the
// strict decoder would refuse, which it counts.
func ownEntries(document Document, now time.Time) (Document, ExportDrops) {
	var drops ExportDrops
	own := document
	own.Machines = keepLocal(document.Machines, func(machine Machine) Entry { return machine.Entry })
	machineID := ""
	if len(own.Machines) > 0 {
		machineID = own.Machines[0].ID
	}
	seen := map[string]bool{machineID: true}
	// valid holds one entry to the rules of its fields and of its identity.
	valid := func(entry Entry, value any) bool {
		if entry.MachineID != machineID || seen[entry.ID] || checkStruct(reflect.ValueOf(value), "", now) != nil {
			return false
		}
		seen[entry.ID] = true
		return true
	}
	dropped, hostOf := map[string]bool{}, map[string]string{}
	own.Repositories = make([]Repository, 0, len(document.Repositories))
	for _, repository := range keepLocal(document.Repositories, func(repository Repository) Entry { return repository.Entry }) {
		if seen[repository.ID] {
			// A repeated id is left out alone: the entry that has the id stays, and
			// so does everything of it.
			drops.Repositories++
			continue
		}
		if !valid(repository.Entry, repository) || nullKinds(repository.CodeIndex) ||
			(repository.RemoteURLWeb != "" && repository.RemoteURLWeb != webURL(repository.Host, repository.Name)) {
			dropped[repository.ID] = true
			drops.Repositories++
			continue
		}
		hostOf[repository.ID] = repository.Host
		own.Repositories = append(own.Repositories, repository)
	}
	worktrees := map[string]bool{}
	own.Worktrees = make([]Worktree, 0, len(document.Worktrees))
	for _, worktree := range keepLocal(document.Worktrees, func(worktree Worktree) Entry { return worktree.Entry }) {
		if dropped[worktree.Repository] || !valid(worktree.Entry, worktree) || nullKinds(worktree.CodeIndex) {
			drops.Worktrees++
			continue
		}
		worktrees[worktree.ID] = true
		own.Worktrees = append(own.Worktrees, worktree)
	}
	own.PullRequests = make([]PullRequest, 0, len(document.PullRequests))
	for _, pull := range keepLocal(document.PullRequests, func(pull PullRequest) Entry { return pull.Entry }) {
		host := hostOf[pull.Repository]
		if dropped[pull.Repository] || !valid(pull.Entry, pull) || (host != "" && pull.URL != "" && !strings.EqualFold(urlHost(pull.URL), host)) {
			drops.PullRequests++
			continue
		}
		if !worktrees[pull.Worktree] {
			// Its worktree was left out: the pull request stays, without the reference.
			pull.Worktree = ""
		}
		own.PullRequests = append(own.PullRequests, pull)
	}
	own.Agents = make([]Agent, 0, len(document.Agents))
	for _, agent := range keepLocal(document.Agents, func(agent Agent) Entry { return agent.Entry }) {
		if dropped[agent.Repository] || !valid(agent.Entry, agent) {
			drops.Agents++
			continue
		}
		own.Agents = append(own.Agents, agent)
	}
	return own, drops
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
// published document and the sampler, without running anything. It reads memory
// only.
func (s *Snapshotter) Export(metricsOnly bool) Envelope {
	envelope, _, _, _ := s.buildExport(metricsOnly)
	return envelope
}

// buildExport builds the envelope and returns with it what it left out and the
// versions of the document and of the metrics it was built from.
func (s *Snapshotter) buildExport(metricsOnly bool) (envelope Envelope, drops ExportDrops, documentVersion int, metricsVersion uint64) {
	s.mu.RLock()
	document, documentVersion := s.doc, s.publishes
	s.mu.RUnlock()
	now := s.now()
	local := s.metricsAnswerFor(localMachineID(s.machine))
	answer := sanitizeMetrics(local, now)
	envelope, drops = NewEnvelope(document, MetricsResponse{Route: answer.Route, Samples: answer.Samples, Reason: answer.Reason}, now, metricsOnly)
	return envelope, drops, documentVersion, local.Version
}

// exportCache holds the export prepared for serving, one for each of its two
// shapes, with the versions it was built from.
type exportCache struct {
	mu      sync.Mutex
	entries [2]cachedExport
	logged  ExportDrops
}

type cachedExport struct {
	built           bool
	documentVersion int
	metricsVersion  uint64
	payload         cockpit.Payload
	failure         string
}

// exportVersions is the versions the export would be built from now.
func (s *Snapshotter) exportVersions() (documentVersion int, metricsVersion uint64, warming bool) {
	s.mu.RLock()
	documentVersion, warming = s.publishes, s.doc.WarmingUp
	s.mu.RUnlock()
	return documentVersion, s.metricsAnswerFor(localMachineID(s.machine)).Version, warming
}

// ExportPayload is this machine's export prepared for the hub route
// (cockpit-views#req:hub-export-route): the envelope encoded, compressed and
// tagged once for each version of the published document and of the metrics
// history, so a request copies bytes. failure is empty, or ErrorWarmingUp for a
// full export while the first pass has not ended (a partial fleet must not
// replace what a reader holds), or ErrorExportFailed when the envelope would
// not pass its own rules or its size bound. It reads memory only. What the
// export leaves out is logged, as numbers, when it changes.
func (s *Snapshotter) ExportPayload(metricsOnly bool) (payload cockpit.Payload, failure string) {
	slot := 0
	if metricsOnly {
		slot = 1
	}
	documentVersion, metricsVersion, warming := s.exportVersions()
	if warming && !metricsOnly {
		return cockpit.Payload{}, ErrorWarmingUp
	}
	s.exports.mu.Lock()
	held := s.exports.entries[slot]
	s.exports.mu.Unlock()
	// A metrics-only export does not depend on the document.
	if held.built && held.metricsVersion == metricsVersion && (metricsOnly || held.documentVersion == documentVersion) {
		return held.payload, held.failure
	}
	envelope, drops, documentVersion, metricsVersion := s.buildExport(metricsOnly)
	built := cachedExport{built: true, documentVersion: documentVersion, metricsVersion: metricsVersion}
	// The envelope types cannot fail to marshal.
	body, _ := json.Marshal(envelope)
	if len(body) >= MaxEnvelopeBytes || envelope.Validate(metricsOnly, envelope.ExportedAt) != nil {
		built.failure = ErrorExportFailed
	} else {
		built.payload = cockpit.NewPayload(append(body, '\n'), s.compress)
	}
	s.exports.mu.Lock()
	defer s.exports.mu.Unlock()
	s.exports.entries[slot] = built
	if !metricsOnly && drops != s.exports.logged {
		s.exports.logged = drops
		s.logf("cockpit fleet: this machine's export leaves out %d entries its rules refuse (repositories %d, worktrees %d, pull requests %d, agents %d)",
			drops.Total(), drops.Repositories, drops.Worktrees, drops.PullRequests, drops.Agents)
	}
	return built.payload, built.failure
}
