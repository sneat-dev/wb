package fleet

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/sneat-dev/wb/internal/cockpit/machinemetrics"
)

// This file reads other machines' export envelopes in the background and merges
// them into the fleet document (cockpit-views#req:remote-exporter-transports,
// #req:remote-entries-replace-cached). Its rules:
//
//   - A machine is read only when the local configuration names it, and it is
//     placed by that configured key alone. Nothing a remote sends (its name, its
//     ids, its machine id) decides where its entries go, and nothing is ever
//     applied to this machine's own entries.
//   - Every envelope is held to the strict decoder's rules here, whichever
//     transport produced it, before anything of it is kept.
//   - No fetch runs on a request: a request for a machine's metrics only records
//     that it was asked for.
//   - A failure is shown as one of a closed set of codes, never as the remote's
//     text.

// The transports a machine's export can be read over.
const (
	TransportHTTP = "http"
	TransportSSH  = "ssh"
)

// The codes a machine entry's remote_error can hold
// (cockpit-views#req:remote-error-is-visible). The http_* codes name the HTTP
// transport and the others the SSH transport, except bad_payload, which either
// can produce.
const (
	RemoteErrorHTTPUnavailable  = "http_unavailable"
	RemoteErrorHTTPAuthFailed   = "http_auth_failed"
	RemoteErrorSSHUnavailable   = "ssh_unavailable"
	RemoteErrorAuthFailed       = "auth_failed"
	RemoteErrorTimeout          = "timeout"
	RemoteErrorWBMissing        = "wb_missing"
	RemoteErrorWBTooOld         = "wb_too_old"
	RemoteErrorDaemonNotRunning = "daemon_not_running"
	RemoteErrorExportRefused    = "export_refused"
	RemoteErrorBadPayload       = "bad_payload"
)

// remoteErrorCodes is the closed vocabulary of remote_error: a code outside it
// never reaches the document.
var remoteErrorCodes = []string{
	RemoteErrorHTTPUnavailable, RemoteErrorHTTPAuthFailed, RemoteErrorSSHUnavailable, RemoteErrorAuthFailed, RemoteErrorTimeout,
	RemoteErrorWBMissing, RemoteErrorWBTooOld, RemoteErrorDaemonNotRunning, RemoteErrorExportRefused, RemoteErrorBadPayload,
}

const (
	// remoteStep is how often the background loop looks for an export that is due.
	remoteStep = 5 * time.Second
	// metricsOnlyInterval is the time between two metrics-only exports of a machine
	// whose metrics a client asked for within metricsDemandWindow.
	metricsOnlyInterval = 30 * time.Second
	metricsDemandWindow = 60 * time.Second
	// maxRemoteBackoff caps the delay between attempts on a machine that fails.
	maxRemoteBackoff = 5 * time.Minute
	// remoteExportTimeout bounds one machine's export over all its transports,
	// whatever a transport's own timeout is.
	remoteExportTimeout = 30 * time.Second
	// liveIntervals is how many refresh intervals an export stays fresh for.
	liveIntervals = 2
)

// RemoteTarget is one other machine the local configuration names. Machine is
// its key in session_move.targets, and the only thing that places its entries.
// HTTP is its HTTP route, nil when it has none.
type RemoteTarget struct {
	Machine string
	HTTP    *HTTPRoute
}

// RemoteExporter reads one machine's export envelope over one transport. It is
// given only what the local configuration holds for the target, and returns
// ErrNoRoute for a target it has no route to, a *RemoteError for a failure, and
// a *BadEnvelopeError for an envelope it refused. Whatever it returns is checked
// again by the caller: an exporter is not trusted to have validated.
type RemoteExporter interface {
	Export(ctx context.Context, target RemoteTarget, metricsOnly bool) (Envelope, error)
}

// RemoteTransport is an exporter and the name its entries carry as `transport`.
type RemoteTransport struct {
	Name     string
	Exporter RemoteExporter
}

// ErrNoRoute is what an exporter returns for a target the local configuration
// gives it no route to. It is not a failure: the next transport is asked.
var ErrNoRoute = errors.New("the machine has no route for this transport")

// RemoteError is a failed export as a code of remoteErrorCodes. Fallback says
// whether the next transport may be tried after it
// (cockpit-views#req:remote-exporter-transports). It carries no text of the
// remote, no address and no credential.
type RemoteError struct {
	Code     string
	Fallback bool
}

func (e *RemoteError) Error() string { return "remote export failed: " + e.Code }

// remoteFailure is the code of a failed export over transport, and whether the
// next transport may be tried. A refused envelope is bad_payload on either
// transport and never falls back; an exporter's own RemoteError is taken when
// its code is one of the vocabulary; anything else (an exporter that broke its
// contract or panicked) is that transport being unavailable.
func remoteFailure(transport string, err error) RemoteError {
	var refused *BadEnvelopeError
	var typed *RemoteError
	switch {
	case errors.As(err, &refused):
		return RemoteError{Code: RemoteErrorBadPayload}
	case errors.As(err, &typed) && slices.Contains(remoteErrorCodes, typed.Code):
		return *typed
	case transport == TransportSSH:
		return RemoteError{Code: RemoteErrorSSHUnavailable, Fallback: true}
	}
	return RemoteError{Code: RemoteErrorHTTPUnavailable, Fallback: true}
}

// exportFrom reads target's envelope from the first transport that yields a
// valid one, in order. It returns the envelope and the name of the transport
// that produced it, and failure, the code of the first transport that failed
// before it (the preferred transport's failure, shown while a fallback supplies
// the data). When none succeeds, failure is the code of the last one tried, and
// it is empty when no transport has a route. A failure that is not
// fallback-class ends the attempt. Each envelope is validated here, so the
// boundary does not depend on a transport having done it.
func exportFrom(ctx context.Context, transports []RemoteTransport, target RemoteTarget, metricsOnly bool, now func() time.Time) (envelope Envelope, transport, failure string, ok bool) {
	last := ""
	for _, candidate := range transports {
		var exported Envelope
		err := catch(func() (err error) {
			exported, err = candidate.Exporter.Export(ctx, target, metricsOnly)
			return err
		})
		if errors.Is(err, ErrNoRoute) {
			continue
		}
		if err == nil {
			err = exported.Validate(metricsOnly, now())
		}
		if err == nil {
			return exported, candidate.Name, failure, true
		}
		failed := remoteFailure(candidate.Name, err)
		last = failed.Code
		if failure == "" {
			failure = failed.Code
		}
		if !failed.Fallback {
			break
		}
	}
	return Envelope{}, "", last, false
}

// remoteSchedule is when one machine is next read. It is a value so that the
// rule is a pure function of it and the time.
type remoteSchedule struct {
	// nextFleet is when the next fleet export is due; the zero time is at once.
	nextFleet time.Time
	// nextMetrics is the earliest time of the next metrics-only export.
	nextMetrics time.Time
	// asked is when a client last asked for this machine's metrics.
	asked time.Time
	// failures counts the attempts that failed since the last success.
	failures int
}

// due reports whether an export is due at now, and whether it is the
// metrics-only one: a fleet export when its time has come; else a metrics-only
// export while a client asked for the machine's metrics within
// metricsDemandWindow, at most every metricsOnlyInterval, and only while the
// machine is not failing (a failing machine is retried by its fleet export's
// backoff alone).
func (r remoteSchedule) due(now time.Time) (fetch, metricsOnly bool) {
	if !now.Before(r.nextFleet) {
		return true, false
	}
	if r.failures == 0 && !r.asked.IsZero() && now.Sub(r.asked) <= metricsDemandWindow && !now.Before(r.nextMetrics) {
		return true, true
	}
	return false, false
}

// after is the schedule once an export started at started has ended. A success
// clears the failures and sets the next fleet export one interval on (for a
// fleet export) and the next metrics-only export metricsOnlyInterval on (every
// export carries the metrics). A failed fleet export is tried again after a
// delay that doubles with each failure up to maxRemoteBackoff, and never sooner
// than the interval; a failed metrics-only export leaves the fleet export's time
// alone and stops the metrics-only exports until a success.
func (r remoteSchedule) after(started time.Time, metricsOnly, ok bool, interval time.Duration) remoteSchedule {
	if ok {
		r.failures = 0
		r.nextMetrics = started.Add(metricsOnlyInterval)
		if !metricsOnly {
			r.nextFleet = started.Add(interval)
		}
		return r
	}
	r.failures++
	if !metricsOnly {
		r.nextFleet = started.Add(remoteBackoff(interval, r.failures))
	}
	return r
}

// remoteBackoff is the delay before the attempt that follows failures failed
// ones: the interval doubled for each, at most maxRemoteBackoff and never less
// than the interval.
func remoteBackoff(interval time.Duration, failures int) time.Duration {
	limit := max(maxRemoteBackoff, interval)
	delay := interval
	for range failures {
		if delay >= limit {
			break
		}
		delay *= 2
	}
	return min(delay, limit)
}

// liveView is one machine's live-remote entries, mapped.
type liveView struct {
	machine      Machine
	repositories []Repository
	worktrees    []Worktree
	pullRequests []PullRequest
	agents       []Agent
}

// liveMachine is what the snapshotter holds of one configured machine: its
// schedule, the last accepted export and its last failure. It is guarded by the
// snapshotter's mu.
type liveMachine struct {
	target   RemoteTarget
	schedule remoteSchedule
	busy     bool

	// fleet is the last accepted fleet, observedAt the remote snapshot's time,
	// receivedAt when this daemon received it (which freshness is measured from,
	// so a remote's clock cannot keep stale data live) and transport what
	// produced it. view is fleet mapped under the machine id mappedFor.
	fleet      *Document
	observedAt time.Time
	receivedAt time.Time
	transport  string
	mappedFor  string
	view       liveView

	// remoteError is the code of the last failure, empty after a success on the
	// preferred transport.
	remoteError string

	// samples is the last accepted metrics history, received at metricsAt;
	// metricsVersion changes with every one.
	samples        []machinemetrics.Sample
	metricsAt      time.Time
	metricsVersion uint64
}

// fresh reports whether the machine's last export is young enough to replace its
// cached entries: received less than liveIntervals refresh intervals ago.
func (m *liveMachine) fresh(now time.Time, interval time.Duration) bool {
	return m.fleet != nil && now.Sub(m.receivedAt) < liveIntervals*interval
}

// observedTime is the time a live-remote entry was observed at: the remote
// snapshot's time, or the export's when the snapshot has none, and never later
// than when this daemon received it.
func observedTime(envelope Envelope, received time.Time) time.Time {
	observed := envelope.ExportedAt
	if envelope.Fleet != nil && !envelope.Fleet.SnapshotAt.IsZero() {
		observed = envelope.Fleet.SnapshotAt
	}
	if observed.After(received) {
		return received
	}
	return observed
}

// mapLive maps an accepted fleet to the entries of the machine configured as
// key, whose machine entry has the id machineID. It reads the document field by
// field, as every mapping does, and:
//
//   - names the machine by key, never by what the document says;
//   - keeps only the entries of the document's own machine entry (an entry of a
//     third machine, or one with any other route, is dropped);
//   - derives every id anew from the key and the entry's id, which it never uses
//     as received: an id a remote chose can therefore never equal an id of this
//     machine or of another one, and a reference to an entry that is not in the
//     document is cleared (a worktree whose repository is not is dropped).
func mapLive(key, machineID string, fleet *Document, observed time.Time) liveView {
	entry := func(kind, received string) Entry {
		return Entry{ID: entryID(kind, RouteLiveRemote, key, received), Machine: key, MachineID: machineID, Route: RouteLiveRemote, ObservedAt: observed}
	}
	var source Machine
	found := false
	for _, machine := range fleet.Machines {
		if machine.Route == RouteLocal && machine.ID != "" {
			source, found = machine, true
			break
		}
	}
	own := func(candidate Entry) bool {
		return found && candidate.Route == RouteLocal && candidate.MachineID == source.ID && candidate.ID != ""
	}
	var view liveView
	repositoryIDs, worktreeIDs := map[string]string{}, map[string]string{}
	for _, repository := range fleet.Repositories {
		if !own(repository.Entry) {
			continue
		}
		mapped := entry(kindRepository, repository.ID)
		repositoryIDs[repository.ID] = mapped.ID
		view.repositories = append(view.repositories, Repository{
			Entry: mapped, Host: repository.Host, Name: repository.Name, DefaultBranch: repository.DefaultBranch,
			LocalBranchCount: repository.LocalBranchCount, RemoteBranchCount: repository.RemoteBranchCount,
			OpenPullRequestCount: repository.OpenPullRequestCount, ActiveAgentCount: repository.ActiveAgentCount,
			Error: repository.Error, LastActivityAt: repository.LastActivityAt,
			RemoteURLWeb: webURL(repository.Host, repository.Name), CodeIndex: liveCodeIndex(repository.CodeIndex),
		})
	}
	for _, worktree := range fleet.Worktrees {
		repository := repositoryIDs[worktree.Repository]
		if !own(worktree.Entry) || repository == "" {
			continue
		}
		mapped := entry(kindWorktree, worktree.ID)
		worktreeIDs[worktree.ID] = mapped.ID
		view.worktrees = append(view.worktrees, Worktree{
			Entry: mapped, Repository: repository, Name: worktree.Task, Task: worktree.Task, Stream: worktree.Stream, Branch: worktree.Branch,
			Lifecycle: worktree.Lifecycle, OwnerState: worktree.OwnerState, LastActivityAt: worktree.LastActivityAt,
			Ahead: worktree.Ahead, Behind: worktree.Behind, UpstreamGone: worktree.UpstreamGone, HasUpstream: worktree.HasUpstream,
			CodeIndex: liveCodeIndex(worktree.CodeIndex),
		})
	}
	for _, pull := range fleet.PullRequests {
		if !own(pull.Entry) {
			continue
		}
		view.pullRequests = append(view.pullRequests, PullRequest{
			Entry: entry(kindPR, pull.ID), Repository: repositoryIDs[pull.Repository], Worktree: worktreeIDs[pull.Worktree],
			Branch: pull.Branch, Number: pull.Number, State: pull.State, URL: safeHTTPSURL(pull.URL),
		})
	}
	for _, agent := range fleet.Agents {
		if !own(agent.Entry) || len(view.agents) == agentCap {
			continue
		}
		view.agents = append(view.agents, Agent{
			Entry: entry(kindAgent, agent.ID), Kind: agent.Kind, SessionID: agent.SessionID, RunID: agent.RunID,
			Runtime: agent.Runtime, Model: agent.Model, State: agent.State, Repository: repositoryIDs[agent.Repository],
		})
	}
	view.repositories = uniqueByID(view.repositories, func(item Repository) string { return item.ID })
	view.worktrees = uniqueByID(view.worktrees, func(item Worktree) string { return item.ID })
	view.pullRequests = uniqueByID(view.pullRequests, func(item PullRequest) string { return item.ID })
	view.agents = uniqueByID(view.agents, func(item Agent) string { return item.ID })
	for index := range view.repositories {
		id := view.repositories[index].ID
		view.repositories[index].WorktreeCount = countWhere(view.worktrees, func(worktree Worktree) bool { return worktree.Repository == id })
	}
	view.machine = Machine{
		Entry:     Entry{ID: machineID, Machine: key, MachineID: machineID, Route: RouteLiveRemote, ObservedAt: observed},
		WBVersion: source.WBVersion, RepositoryCount: len(view.repositories), WorktreeCount: len(view.worktrees),
		OS: source.OS, Arch: source.Arch, CPUCount: cpuCount(source.CPUCount), BootTime: source.BootTime,
	}
	return view
}

// liveCodeIndex rebuilds a code-index list field by field.
func liveCodeIndex(indexes []CodeIndex) []CodeIndex {
	if len(indexes) == 0 {
		return nil
	}
	mapped := make([]CodeIndex, 0, len(indexes))
	for _, index := range indexes {
		item := CodeIndex{Indexer: index.Indexer, State: index.State, Behind: index.Behind, ReceiptAt: index.ReceiptAt}
		if statistics := index.Statistics; statistics != nil {
			kinds := make([]KindCount, 0, len(statistics.Kinds))
			for _, kind := range statistics.Kinds {
				kinds = append(kinds, KindCount{Kind: kind.Kind, Count: kind.Count})
			}
			item.Statistics = &CodeStatistics{
				Indexed: statistics.Indexed, Files: statistics.Files, Symbols: statistics.Symbols, Edges: statistics.Edges, Kinds: kinds, Error: statistics.Error,
			}
		}
		mapped = append(mapped, item)
	}
	return mapped
}

// cachedMachinesOf is the ids of the published-store machine entries that are
// the machine configured as key: the ones published under that machine name,
// and, when this machine's login is known, under that login.
func (s *Snapshotter) cachedMachinesOf(key string) []string {
	var ids []string
	for _, published := range s.remote.named[key] {
		if s.login == "" || published.login == s.login {
			ids = append(ids, published.id)
		}
	}
	return ids
}

// liveMachineID is the id of the machine entry of the machine configured as
// key. When exactly one published-store entry is that machine it is that
// entry's id, so the machine keeps one id whether its entries are shown live or
// cached; otherwise it is derived from this machine's login and the key, as a
// published entry's id is. It is never taken from an export.
func (s *Snapshotter) liveMachineID(key string, cached []string) string {
	if len(cached) == 1 {
		return cached[0]
	}
	return entryID(kindMachine, s.login+"/"+key)
}

// overlayLive adds the configured machines to document and says which of the
// published-store entries they replace or annotate. A machine whose last export
// is fresh contributes its live-remote entries, with the transport and any
// failure of the preferred transport, and hides its published-store entries.
// One whose export is stale or missing keeps its published-store entries, which
// then carry the failure code; with no such entry and a failure, a bare machine
// entry says so. It also records which machine ids stand for which configured
// key, for the metrics source. The caller holds s.mu.
func (s *Snapshotter) overlayLive(document *Document, now time.Time) (hidden map[string]bool, failures map[string]string) {
	s.liveIDs = map[string]string{}
	for _, key := range s.liveKeys {
		machine := s.live[key]
		cached := s.cachedMachinesOf(key)
		id := s.liveMachineID(key, cached)
		s.liveIDs[id] = key
		for _, published := range cached {
			s.liveIDs[published] = key
		}
		switch {
		case machine.fresh(now, s.interval):
			if machine.mappedFor != id {
				machine.view, machine.mappedFor = mapLive(key, id, machine.fleet, machine.observedAt), id
			}
			entry := machine.view.machine
			entry.Transport, entry.RemoteError = machine.transport, machine.remoteError
			document.Machines = append(document.Machines, entry)
			document.Repositories = append(document.Repositories, machine.view.repositories...)
			document.Worktrees = append(document.Worktrees, machine.view.worktrees...)
			document.PullRequests = append(document.PullRequests, machine.view.pullRequests...)
			document.Agents = append(document.Agents, machine.view.agents...)
			for _, published := range cached {
				if hidden == nil {
					hidden = map[string]bool{}
				}
				hidden[published] = true
			}
		case machine.remoteError == "":
		case len(cached) > 0:
			for _, published := range cached {
				if failures == nil {
					failures = map[string]string{}
				}
				failures[published] = machine.remoteError
			}
		default:
			document.Machines = append(document.Machines, Machine{
				Entry:       Entry{ID: id, Machine: key, MachineID: id, Route: RouteLiveRemote},
				RemoteError: machine.remoteError,
			})
		}
	}
	return hidden, failures
}

// appendCached adds the published-store entries to document, less the machines
// in hidden, and with the failure code of failures on a machine entry it names.
// The caller holds s.mu.
func (s *Snapshotter) appendCached(document *Document, hidden map[string]bool, failures map[string]string) {
	if len(hidden) == 0 && len(failures) == 0 {
		document.Machines = append(document.Machines, s.remote.machines...)
		document.Repositories = append(document.Repositories, s.remote.repositories...)
		document.Worktrees = append(document.Worktrees, s.remote.worktrees...)
		document.PullRequests = append(document.PullRequests, s.remote.pullRequests...)
		return
	}
	for _, machine := range s.remote.machines {
		if hidden[machine.ID] {
			continue
		}
		machine.RemoteError = failures[machine.ID]
		document.Machines = append(document.Machines, machine)
	}
	for _, repository := range s.remote.repositories {
		if !hidden[repository.MachineID] {
			document.Repositories = append(document.Repositories, repository)
		}
	}
	for _, worktree := range s.remote.worktrees {
		if !hidden[worktree.MachineID] {
			document.Worktrees = append(document.Worktrees, worktree)
		}
	}
	for _, pull := range s.remote.pullRequests {
		if !hidden[pull.MachineID] {
			document.PullRequests = append(document.PullRequests, pull)
		}
	}
}

// liveRepository reports whether id is a repository of a machine read live. The
// caller holds s.mu.
func (s *Snapshotter) liveRepository(id string) bool {
	for _, machine := range s.live {
		if slices.ContainsFunc(machine.view.repositories, func(repository Repository) bool { return repository.ID == id }) {
			return true
		}
	}
	return false
}

// runRemotes looks for a due export of each configured machine every remoteStep
// until ctx ends. It is the only caller of an exporter, and no request reaches
// it.
func (s *Snapshotter) runRemotes(ctx context.Context) {
	ticks, stopTicks := s.remoteTick(remoteStep)
	defer stopTicks()
	for {
		s.pollRemotes(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
	}
}

// pollRemotes starts the export that is due for each configured machine, each in
// a goroutine of its own, so a slow machine delays neither another machine nor
// the local snapshot. A machine has at most one export running.
func (s *Snapshotter) pollRemotes(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, key := range s.liveKeys {
		machine := s.live[key]
		if machine.busy {
			continue
		}
		fetch, metricsOnly := machine.schedule.due(now)
		if !fetch {
			continue
		}
		machine.busy = true
		s.side.Add(1)
		go func() {
			defer s.side.Done()
			s.exportRemote(ctx, machine, metricsOnly, now)
		}()
	}
}

// exportRemote reads one machine's export and merges it. A full export replaces
// the machine's fleet, its metrics and its failure; a metrics-only one its
// metrics. A failure keeps what the machine last gave and records the code. An
// export that was cut short by the daemon stopping records nothing.
func (s *Snapshotter) exportRemote(parent context.Context, machine *liveMachine, metricsOnly bool, started time.Time) {
	ctx, cancel := context.WithTimeout(parent, remoteExportTimeout)
	defer cancel()
	key := machine.target.Machine
	envelope, transport, failure, ok := exportFrom(ctx, s.transports, machine.target, metricsOnly, s.now)
	received := s.now()
	var view liveView
	id := ""
	if ok && !metricsOnly {
		// The entries are mapped outside the lock, under the id the machine has now;
		// a publication that finds the id changed maps them again.
		s.mu.RLock()
		id = s.liveMachineID(key, s.cachedMachinesOf(key))
		s.mu.RUnlock()
		view = mapLive(key, id, envelope.Fleet, observedTime(envelope, received))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	machine.busy = false
	if parent.Err() != nil {
		return
	}
	machine.schedule = machine.schedule.after(started, metricsOnly, ok, s.interval)
	changed := machine.remoteError != failure
	machine.remoteError = failure
	if !ok {
		if changed {
			s.logf("cockpit fleet: the export of %s failed (%s)", key, failure)
			s.publishMaybeLocked()
		}
		return
	}
	machine.samples, machine.metricsAt = nil, received
	if envelope.Metrics.Route == RouteLocal {
		machine.samples = envelope.Metrics.Samples
	}
	machine.metricsVersion++
	if !metricsOnly {
		machine.fleet, machine.observedAt, machine.receivedAt, machine.transport = envelope.Fleet, observedTime(envelope, received), received, transport
		machine.view, machine.mappedFor = view, id
		s.remoteBranches = nil
		changed = true
	}
	if changed {
		s.publishMaybeLocked()
	}
}

// liveMetrics is the live-remote source of the machine-metrics route: the last
// history a configured machine's export carried. Asking it records that the
// machine's metrics are wanted, which is what makes the background loop read
// them every metricsOnlyInterval; it never reads anything itself.
type liveMetrics struct{ snapshotter *Snapshotter }

func (l liveMetrics) MachineMetrics(id string) (MetricsAnswer, bool) {
	s := l.snapshotter
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	machine, configured := s.live[s.liveIDs[id]]
	if !configured {
		return MetricsAnswer{}, false
	}
	machine.schedule.asked = now
	if machine.samples == nil || now.Sub(machine.metricsAt) >= liveIntervals*s.interval {
		return MetricsAnswer{}, false
	}
	fetched := machine.metricsAt
	return MetricsAnswer{Route: RouteLiveRemote, FetchedAt: &fetched, Samples: machine.samples, Version: machine.metricsVersion}, true
}
