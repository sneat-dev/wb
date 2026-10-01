package fleet

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sneat-dev/wb/internal/cockpit"
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
	// RemoteErrorWarmingUp says the remote daemon keeps answering that its first
	// pass has not ended and nothing fresh is held of it.
	RemoteErrorWarmingUp = "remote_warming_up"
	// RemoteErrorExportTooLarge says the machine's live entries were left out
	// because the fleet document would be over its size bound with them.
	RemoteErrorExportTooLarge = "export_too_large"
	// RemoteErrorSelfExport says the export read from the configured address is
	// this machine's own.
	RemoteErrorSelfExport = "self_export"
)

// remoteErrorCodes is the closed vocabulary of remote_error: a code outside it
// never reaches the document.
var remoteErrorCodes = []string{
	RemoteErrorHTTPUnavailable, RemoteErrorHTTPAuthFailed, RemoteErrorSSHUnavailable, RemoteErrorAuthFailed, RemoteErrorTimeout,
	RemoteErrorWBMissing, RemoteErrorWBTooOld, RemoteErrorDaemonNotRunning, RemoteErrorExportRefused, RemoteErrorBadPayload,
	RemoteErrorWarmingUp, RemoteErrorExportTooLarge, RemoteErrorSelfExport,
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
	// fallbackCoolDown is how long a machine is read over SSH alone after its
	// preferred transport failed and SSH answered.
	fallbackCoolDown = 5 * time.Minute
	// liveIntervals is how many refresh intervals an export stays fresh for.
	liveIntervals = 2
	// The most entries of one remote machine that are kept; what is over is cut
	// and counted in the machine's export_dropped. The agents are capped at
	// agentCap, as this machine's own are.
	maxLiveRepositories = 2000
	maxLiveWorktrees    = 2000
	maxLivePullRequests = 500
	// defaultMaxDocumentBytes is the size of the published document over which
	// the live machines are left out of it.
	defaultMaxDocumentBytes = 32 << 20
)

// RemoteTarget is one other machine the local configuration names. Machine is
// its key in session_move.targets, and the only thing that places its entries.
// HTTP is its HTTP route and SSH its SSH route, each nil when it has none.
type RemoteTarget struct {
	Machine string
	HTTP    *HTTPRoute
	SSH     *SSHRoute
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

// ErrRemoteWarmingUp is what an exporter returns when the remote daemon says
// its first pass has not ended. It is not a failure and not a reason to try
// another transport: the remote is healthy and has nothing complete to give
// yet, so what is held of it is kept.
var ErrRemoteWarmingUp = errors.New("the remote daemon is warming up")

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

// exportResult is the outcome of one machine's export over its transports.
type exportResult struct {
	envelope  Envelope
	transport string
	// failure is, with ok, the code of the preferred transport that failed
	// before the one that worked; without ok, the code of the last one tried
	// (empty when no transport has a route).
	failure string
	ok      bool
	// warming says the remote answered that it is warming up: nothing new is
	// held, and nothing failed.
	warming bool
}

// exportFrom reads target's envelope from the first transport that yields a
// valid one, in order. A failure that is not fallback-class ends the attempt,
// and so does a remote that is warming up. Each envelope is validated here, so
// the boundary does not depend on a transport having done it, and a panic in an
// exporter or in the validation is that transport's failure, never the
// daemon's.
func exportFrom(ctx context.Context, transports []RemoteTransport, target RemoteTarget, metricsOnly bool, now func() time.Time) exportResult {
	result := exportResult{}
	last := ""
	for _, candidate := range transports {
		var exported Envelope
		err := catch(func() (err error) {
			if exported, err = candidate.Exporter.Export(ctx, target, metricsOnly); err != nil {
				return err
			}
			return exported.Validate(metricsOnly, now())
		})
		switch {
		case errors.Is(err, ErrNoRoute):
			continue
		case errors.Is(err, ErrRemoteWarmingUp), err == nil && exported.Fleet != nil && exported.Fleet.WarmingUp:
			// A partial fleet from a warming daemon is never taken, whatever sent it.
			return exportResult{warming: true}
		case err == nil:
			result.envelope, result.transport, result.ok = exported, candidate.Name, true
			return result
		}
		failed := remoteFailure(candidate.Name, err)
		last = failed.Code
		if result.failure == "" {
			result.failure = failed.Code
		}
		if !failed.Fallback {
			break
		}
	}
	return exportResult{failure: last}
}

// fallback is one machine's cool-down on the SSH transport
// (cockpit-views#req:remote-exporter-transports): until is when the preferred
// transport is tried again and failure the code it failed with, which the
// machine entry carries while SSH supplies its entries. It is a value, so the
// rule is a pure function of it, the time and an export's outcome.
type fallback struct {
	until   time.Time
	failure string
}

// active reports whether the machine is read over SSH alone at now.
func (f fallback) active(now time.Time) bool {
	return f.failure != "" && now.Before(f.until)
}

// after is the cool-down once an export started at started has ended as result:
//
//   - the preferred transport failed and SSH answered: the cool-down starts, for
//     fallbackCoolDown from started, with the preferred transport's failure;
//   - SSH answered within a cool-down (the preferred transport was not asked):
//     the cool-down goes on, unchanged;
//   - a remote that is warming up says nothing of either: unchanged;
//   - anything else ends it: a failed export (so the next attempt asks the
//     preferred transport again), an answer of the preferred transport, or SSH
//     answering a machine that has no other route.
func (f fallback) after(started time.Time, result exportResult) fallback {
	switch {
	case result.warming:
		return f
	case !result.ok || result.transport != TransportSSH:
		return fallback{}
	case result.failure != "":
		return fallback{until: started.Add(fallbackCoolDown), failure: result.failure}
	case f.active(started):
		return f
	}
	return fallback{}
}

// routed is the transports an export may use: all of them, in order, or SSH
// alone within a cool-down.
func routed(transports []RemoteTransport, cooling bool) []RemoteTransport {
	if !cooling {
		return transports
	}
	var kept []RemoteTransport
	for _, transport := range transports {
		if transport.Name == TransportSSH {
			kept = append(kept, transport)
		}
	}
	return kept
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
	// cooling is the machine's cool-down on SSH.
	cooling fallback
	busy    bool
	// asked is when a client last asked for this machine's metrics, in
	// nanoseconds since the epoch, and zero for never. It is set on a request
	// path, so it is atomic and needs no exclusive lock.
	asked atomic.Int64

	// fleet is the last accepted fleet, observedAt the remote snapshot's time,
	// receivedAt when this daemon received it (which freshness is measured from,
	// so a remote's clock cannot keep stale data live) and transport what
	// produced it. view is fleet mapped under the machine id mappedFor.
	fleet      *Document
	dropped    int
	observedAt time.Time
	receivedAt time.Time
	transport  string
	mappedFor  string
	view       liveView
	// digest is the digest of view, by which an export that changed nothing is
	// recognised and not published again, and viewBytes its encoded size, which
	// the document's size guard counts.
	digest    [sha256.Size]byte
	viewBytes int

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
//
// At most maxLiveRepositories, maxLiveWorktrees, maxLivePullRequests and
// agentCap entries are kept; what is cut is added to dropped, the number the
// export itself left out, and shown as the machine's export_dropped.
func mapLive(key, machineID string, fleet *Document, observed time.Time, dropped int) liveView {
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
	cut := 0
	for _, repository := range fleet.Repositories {
		if !own(repository.Entry) {
			continue
		}
		if len(view.repositories) == maxLiveRepositories {
			cut++
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
		if len(view.worktrees) == maxLiveWorktrees {
			cut++
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
		if len(view.pullRequests) == maxLivePullRequests {
			cut++
			continue
		}
		mergeable := ""
		if slices.Contains(mergeableStates, pull.Mergeable) {
			mergeable = pull.Mergeable
		}
		// The address is kept only as a candidate host for relink, which rebuilds
		// the link at publication: an address a remote sent is never rendered.
		view.pullRequests = append(view.pullRequests, PullRequest{
			Entry: entry(kindPR, pull.ID), Repository: repositoryIDs[pull.Repository], Worktree: worktreeIDs[pull.Worktree],
			Branch: pull.Branch, Number: pull.Number, State: pull.State, URL: safeHTTPSURL(pull.URL),
			Mergeable: mergeable, ChecksTotal: pull.ChecksTotal, ChecksPassed: pull.ChecksPassed, ChecksFailed: pull.ChecksFailed,
			ChecksSkipped: pull.ChecksSkipped, ChecksPending: pull.ChecksPending, ChecksGreen: pull.ChecksGreen,
			FailedCheck: plainTextMax(pull.FailedCheck, maxFailedCheckText), CheckedAt: pull.CheckedAt,
		})
	}
	truncated := fleet.AgentsTruncated
	for _, agent := range fleet.Agents {
		if !own(agent.Entry) {
			continue
		}
		if len(view.agents) == agentCap {
			cut++
			truncated = true
			continue
		}
		// An agent's worktrees are the ids of this view's worktrees, re-derived
		// like every other id: one that is not a worktree carried here is left out.
		var linked []string
		for _, id := range agent.Worktrees {
			if mapped := worktreeIDs[id]; mapped != "" {
				linked = append(linked, mapped)
			}
		}
		var exitCode *int
		if agent.ExitCode != nil && *agent.ExitCode >= 0 && *agent.ExitCode <= maxCount {
			code := *agent.ExitCode
			exitCode = &code
		}
		view.agents = append(view.agents, Agent{
			Entry: entry(kindAgent, agent.ID), Kind: agent.Kind, SessionID: agent.SessionID, RunID: agent.RunID,
			Runtime: agent.Runtime, Model: agent.Model, State: agent.State, Activity: agent.Activity,
			Repository: repositoryIDs[agent.Repository], Task: agent.Task, Worktrees: boundedIDs(linked),
			StartedAt: agent.StartedAt, FinishedAt: agent.FinishedAt, ExitCode: exitCode,
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
		ExportDropped: min(max(dropped, 0)+cut, maxCount), AgentsTruncated: truncated,
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

// digestOf is a digest of everything a view contributes to the document, and
// its encoded size.
func digestOf(view liveView) ([sha256.Size]byte, int) {
	// The document types cannot fail to marshal.
	body, _ := json.Marshal([]any{view.machine, view.repositories, view.worktrees, view.pullRequests, view.agents})
	return sha256.Sum256(body), len(body)
}

// pullRequestPath is the path of a pull request under a repository's web
// address, as GitHub has it.
const pullRequestPath = "/pull/"

// relink gives the pull requests of another machine the only links they may
// have (cockpit-views#req:pull-request-fields): an address a remote sent is
// never rendered. The link is built here from the host of the pull request's
// repository (or, for a repository published with no host, the host of the
// address that was sent), the repository's owner/name and the number, and kept
// only when that host is the host of a repository of THIS machine and the link
// built is exactly the address that was sent: a forge whose pull request
// addresses have another shape (GitLab, Bitbucket) then has no link, rather
// than a wrong one, and a sender cannot choose a path. Otherwise the pull
// request has no link. It returns new entries and leaves pulls untouched.
func relink(pulls []PullRequest, repositories []Repository, localHosts map[string]bool) []PullRequest {
	if len(pulls) == 0 {
		return nil
	}
	byID := make(map[string]Repository, len(repositories))
	for _, repository := range repositories {
		byID[repository.ID] = repository
	}
	linked := make([]PullRequest, len(pulls))
	for index, pull := range pulls {
		repository := byID[pull.Repository]
		host := repository.Host
		if host == "" && pull.URL != "" {
			host = urlHost(pull.URL)
		}
		sent := pull.URL
		pull.URL = ""
		if base := webURL(host, repository.Name); base != "" && localHosts[strings.ToLower(host)] && pull.Number > 0 {
			if built := base + pullRequestPath + strconv.Itoa(pull.Number); built == sent {
				pull.URL = built
			}
		}
		linked[index] = pull
	}
	return linked
}

// locallyLinked is another machine's repositories with a web address only where
// its host is the host of a repository of THIS machine: a link to a host that
// only a remote names is not rendered. It returns new entries.
func locallyLinked(repositories []Repository, localHosts map[string]bool) []Repository {
	linked := make([]Repository, len(repositories))
	for index, repository := range repositories {
		if !localHosts[strings.ToLower(repository.Host)] {
			repository.RemoteURLWeb = ""
		}
		linked[index] = repository
	}
	return linked
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
// entry says so. With withLive false no live entry is added (the document would
// be over its size bound with them) and each machine that has a fresh export is
// shown that way with export_too_large. It also records which machine ids stand for
// which configured key, for the metrics source. A panic while a machine's
// entries are mapped drops that machine's export as bad_payload and never
// reaches the daemon. The caller holds s.mu.
func (s *Snapshotter) overlayLive(document *Document, now time.Time, withLive bool, localHosts map[string]bool) (hidden map[string]bool, failures map[string]string) {
	s.liveIDs = map[string]string{}
	for _, key := range s.liveKeys {
		machine := s.live[key]
		cached := s.cachedMachinesOf(key)
		id := s.liveMachineID(key, cached)
		s.liveIDs[id] = key
		for _, published := range cached {
			s.liveIDs[published] = key
		}
		fresh, failure := machine.fresh(now, s.interval), machine.remoteError
		if fresh && !withLive {
			// Left out for the document's size: shown by its published entries, or
			// a bare entry, with the reason.
			fresh, failure = false, RemoteErrorExportTooLarge
		}
		if fresh && machine.mappedFor != id {
			if err := catch(func() error {
				machine.view, machine.mappedFor = s.mapper(key, id, machine.fleet, machine.observedAt, machine.dropped), id
				machine.digest, machine.viewBytes = digestOf(machine.view)
				return nil
			}); err != nil {
				failure = RemoteErrorBadPayload
				s.logf("cockpit fleet: the export of %s could not be mapped (%s)", key, RemoteErrorBadPayload)
				machine.fleet, machine.view, machine.mappedFor, machine.remoteError, fresh = nil, liveView{}, "", RemoteErrorBadPayload, false
			}
		}
		switch {
		case fresh:
			entry := machine.view.machine
			entry.Transport, entry.RemoteError = machine.transport, machine.remoteError
			document.Machines = append(document.Machines, entry)
			document.Repositories = append(document.Repositories, locallyLinked(machine.view.repositories, localHosts)...)
			document.Worktrees = append(document.Worktrees, machine.view.worktrees...)
			document.PullRequests = append(document.PullRequests, relink(machine.view.pullRequests, machine.view.repositories, localHosts)...)
			document.Agents = append(document.Agents, machine.view.agents...)
			for _, published := range cached {
				if hidden == nil {
					hidden = map[string]bool{}
				}
				hidden[published] = true
			}
		case failure == "":
		case len(cached) > 0:
			for _, published := range cached {
				if failures == nil {
					failures = map[string]string{}
				}
				failures[published] = failure
			}
		default:
			document.Machines = append(document.Machines, Machine{
				Entry:       Entry{ID: id, Machine: key, MachineID: id, Route: RouteLiveRemote},
				RemoteError: failure,
			})
		}
	}
	return hidden, failures
}

// appendCached adds the published-store entries to document, less the machines
// in hidden, with the failure code of failures on a machine entry it names, and
// with the pull requests' links rebuilt (relink). The caller holds s.mu.
func (s *Snapshotter) appendCached(document *Document, hidden map[string]bool, failures map[string]string, localHosts map[string]bool) {
	pulls := relink(s.remote.pullRequests, s.remote.repositories, localHosts)
	if len(hidden) == 0 && len(failures) == 0 {
		document.Machines = append(document.Machines, s.remote.machines...)
		document.Repositories = append(document.Repositories, s.remote.repositories...)
		document.Worktrees = append(document.Worktrees, s.remote.worktrees...)
		document.PullRequests = append(document.PullRequests, pulls...)
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
	for _, pull := range pulls {
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
		schedule := machine.schedule
		if asked := machine.asked.Load(); asked != 0 {
			schedule.asked = time.Unix(0, asked)
		}
		fetch, metricsOnly := schedule.due(now)
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

// MachineRoutes is the SSH routes of the configured machines, by the ids their
// machine entries have in the document: the id of each published entry of the
// machine and, for one that is read live, its live id. It is what an owner
// session's "Copy command" entries are built from
// (cockpit-views#req:copy-the-command), and it is never part of the document,
// of an export or of anything an anonymous reader is sent.
func (s *Snapshotter) MachineRoutes() []cockpit.MachineRoute {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var routes []cockpit.MachineRoute
	for _, key := range s.sshKeys {
		route := s.sshRoutes[key]
		ids := s.cachedMachinesOf(key)
		if live := s.liveMachineID(key, ids); s.live[key] != nil && !slices.Contains(ids, live) {
			ids = append(ids, live)
		}
		for _, id := range ids {
			routes = append(routes, cockpit.MachineRoute{MachineID: id, SSH: cockpit.SSHRoute{Host: route.Host, User: route.User, WBPath: route.command()}})
		}
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].MachineID < routes[j].MachineID })
	return routes
}

// exportRemote reads one machine's export and merges it. A full export replaces
// the machine's fleet, its metrics and its failure; a metrics-only one its
// metrics. A failure keeps what the machine last gave and records the code. A
// remote that is warming up changes nothing and is asked again at the usual
// time. An export that was cut short by the daemon stopping records nothing.
//
// The entries are mapped and digested before the lock is taken, the lock is
// held only to record the outcome, and the document is published after it is
// released (publishUnlocked), and only when something a reader sees changed.
func (s *Snapshotter) exportRemote(parent context.Context, machine *liveMachine, metricsOnly bool, started time.Time) {
	ctx, cancel := context.WithTimeout(parent, remoteExportTimeout)
	defer cancel()
	key := machine.target.Machine
	s.mu.RLock()
	cooling := machine.cooling.active(started)
	s.mu.RUnlock()
	result := exportFrom(ctx, routed(s.transports, cooling), machine.target, metricsOnly, s.now)
	received := s.now()
	var view liveView
	var digest [sha256.Size]byte
	size, id := 0, ""
	if result.ok && s.ownExport(result.envelope) {
		// The export is this machine's own, whatever address it came from (a
		// tunnel or proxy that leads back here): reading it would show this
		// machine a second time under another name.
		result = exportResult{failure: RemoteErrorSelfExport}
	}
	if result.ok && !metricsOnly {
		// A publication that finds the machine's id changed maps the entries again.
		s.mu.RLock()
		id = s.liveMachineID(key, s.cachedMachinesOf(key))
		s.mu.RUnlock()
		if err := catch(func() error {
			view = s.mapper(key, id, result.envelope.Fleet, observedTime(result.envelope, received), result.envelope.Dropped)
			digest, size = digestOf(view)
			return nil
		}); err != nil {
			result = exportResult{failure: RemoteErrorBadPayload}
		}
	}
	if s.recordExport(parent, machine, result, metricsOnly, started, received, view, digest, size, id) {
		s.publishUnlocked()
	}
}

// ownExport reports whether envelope is this machine's own export: it names
// this machine, or its machine entry has this machine's id.
func (s *Snapshotter) ownExport(envelope Envelope) bool {
	if envelope.Machine == s.machine {
		return true
	}
	return envelope.Fleet != nil && len(envelope.Fleet.Machines) == 1 && envelope.Fleet.Machines[0].ID == localMachineID(s.machine)
}

// recordExport records the outcome of an export under the lock and reports
// whether the document must be published again.
//
// A warming remote keeps what is held while that is fresh: nothing changes and
// it is asked again one interval on. Once nothing fresh is held (never read, or
// the view went stale) warming is shown as remote_warming_up and backs off like
// a failure, so a machine cannot say "warming up" for ever and show no error,
// and a configured machine with no published entry stays visible.
//
// A metrics-only export never clears the failure: only a full export on the
// preferred transport does. An export that SSH answered after the preferred
// transport failed starts the machine's cool-down (fallback.after), within
// which SSH alone is asked and the entry keeps the preferred transport's
// failure.
func (s *Snapshotter) recordExport(parent context.Context, machine *liveMachine, result exportResult, metricsOnly bool, started, received time.Time, view liveView, digest [sha256.Size]byte, size int, id string) (publish bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	machine.busy = false
	if parent.Err() != nil {
		return false
	}
	held := machine.cooling
	machine.cooling = held.after(started, result)
	if result.ok && result.failure == "" && result.transport == TransportSSH && held.active(started) {
		// SSH was asked alone: the entry still carries what the preferred transport
		// failed with.
		result.failure = held.failure
	}
	if result.warming {
		if machine.fresh(received, s.interval) {
			machine.schedule = machine.schedule.after(started, metricsOnly, true, s.interval)
			return false
		}
		result = exportResult{failure: RemoteErrorWarmingUp}
	}
	machine.schedule = machine.schedule.after(started, metricsOnly, result.ok, s.interval)
	if !result.ok {
		publish = machine.remoteError != result.failure
		machine.remoteError = result.failure
		if publish {
			s.logf("cockpit fleet: the export of %s failed (%s)", machine.target.Machine, result.failure)
		}
		return publish
	}
	machine.samples, machine.metricsAt = nil, received
	if result.envelope.Metrics.Route == RouteLocal {
		machine.samples = result.envelope.Metrics.Samples
	}
	machine.metricsVersion++
	if metricsOnly {
		return false
	}
	publish = machine.remoteError != result.failure
	machine.remoteError = result.failure
	// An export whose entries are the ones already shown, by the same transport,
	// publishes nothing: only the time it was received at moves on.
	unchanged := machine.fresh(received, s.interval) && machine.mappedFor == id && machine.digest == digest && machine.transport == result.transport
	machine.fleet, machine.dropped, machine.observedAt, machine.receivedAt, machine.transport = result.envelope.Fleet, result.envelope.Dropped, observedTime(result.envelope, received), received, result.transport
	machine.view, machine.mappedFor, machine.digest, machine.viewBytes = view, id, digest, size
	if !unchanged {
		s.remoteBranches = nil
	}
	return publish || !unchanged
}

// liveMetrics is the live-remote source of the machine-metrics route: the last
// history a configured machine's export carried. Asking it records that the
// machine's metrics are wanted, which is what makes the background loop read
// them every metricsOnlyInterval; it never reads anything itself, and it takes
// no exclusive lock.
type liveMetrics struct{ snapshotter *Snapshotter }

func (l liveMetrics) MachineMetrics(id string) (MetricsAnswer, bool) {
	s := l.snapshotter
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	machine, configured := s.live[s.liveIDs[id]]
	if !configured {
		return MetricsAnswer{}, false
	}
	machine.asked.Store(now.UnixNano())
	if machine.samples == nil || now.Sub(machine.metricsAt) >= liveIntervals*s.interval {
		return MetricsAnswer{}, false
	}
	fetched := machine.metricsAt
	return MetricsAnswer{Route: RouteLiveRemote, FetchedAt: &fetched, Samples: machine.samples, Version: machine.metricsVersion}, true
}
