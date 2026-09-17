package githubapp

import (
	"context"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
)

// The fleet types mirror the browser contract in hub/web/src/data/worktrees.ts.
// isFleetSnapshot there validates every machine and every worktree and returns
// false for the whole payload when a single field is off-contract; a rejected
// payload renders as no data at all, with no error shown to the operator. Every
// value this file emits is therefore filtered against the enums the browser
// accepts, and an unrecognised value is omitted rather than passed through.
var (
	fleetWorktreeStatuses = map[string]bool{
		"active": true, "idle": true, "ready": true, "blocked": true, "landed": true,
		"cleanup_pending": true, "recovery_needed": true, "orphaned": true, "unknown": true,
	}
	fleetOwnerStates      = map[string]bool{"active": true, "orphaned": true, "unknown": true}
	fleetPullRequestState = map[string]bool{"draft": true, "open": true, "merged": true, "closed": true}
)

// FleetPullRequest is the browser's review link for one worktree. The browser
// requires a positive number, an http(s) URL and one of the four states, so a
// partial link is omitted entirely instead of invalidating the fleet payload.
type FleetPullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	State  string `json:"state"`
}

// FleetWorktree is one published worktree as the browser table renders it.
type FleetWorktree struct {
	Task            string            `json:"task"`
	Repository      string            `json:"repository"`
	Branch          string            `json:"branch"`
	OwnerState      string            `json:"owner_state,omitempty"`
	Stream          string            `json:"stream,omitempty"`
	Status          string            `json:"status,omitempty"`
	LastActivityAt  string            `json:"last_activity_at,omitempty"`
	PullRequest     *FleetPullRequest `json:"pull_request,omitempty"`
	NeedsAttention  bool              `json:"needs_attention,omitempty"`
	AttentionReason string            `json:"attention_reason,omitempty"`
}

// FleetMachine is one machine and the worktrees it published.
type FleetMachine struct {
	Name        string          `json:"name"`
	PublishedAt string          `json:"published_at,omitempty"`
	LastSeenAt  string          `json:"last_seen_at,omitempty"`
	Stale       bool            `json:"stale,omitempty"`
	State       string          `json:"state,omitempty"`
	Worktrees   []FleetWorktree `json:"worktrees"`
}

// FleetSnapshot is the optional fleet projection inside a dashboard or stat
// response.
type FleetSnapshot struct {
	Machines []FleetMachine `json:"machines"`
}

// SnapshotReader lists the latest published snapshot per machine. The read
// models only read, so they depend on this rather than on the store that also
// writes; the host adapts its own store to it. hub imports this package, so the
// adapter lives with the caller instead of dragging hub's record type in here.
type SnapshotReader interface {
	ListLatest(context.Context) ([]machinesnapshot.StoredSnapshot, error)
}

// RemoteStateReadModel serves the dashboard read routes from the WB machine
// snapshots a self-hosted hub already stores, so an operator sees their own
// machine, repositories and worktrees with no configuration and no hosted
// control plane. It is the read-model sibling of
// RemoteStateWorktreeReadModel, and shares its per-machine access rule.
//
// Only what the snapshots genuinely carry is reported. Pull requests, issues,
// releases, token and cost trends and cross-repository merges are GitHub-derived
// projections a self-hosted hub does not compute, so those counters stay zero
// and the corresponding collections stay empty rather than being invented.
type RemoteStateReadModel struct {
	Store      SnapshotReader
	Access     MachineAccessResolver
	Now        func() time.Time
	StaleAfter time.Duration
}

var _ ReadModel = RemoteStateReadModel{}

// Dashboard reports every machine this viewer may inspect.
func (model RemoteStateReadModel) Dashboard(ctx context.Context, viewer Viewer) (Access[Dashboard], error) {
	projection, err := model.project(ctx, viewer, scopeFilter{})
	if err != nil {
		return Access[Dashboard]{}, err
	}
	return Access[Dashboard]{
		Visibility: VisibilityPrivate,
		Value: Dashboard{
			GeneratedAt: projection.GeneratedAt,
			Summary:     Summary{Repositories: projection.Repositories},
			Fleet:       &FleetSnapshot{Machines: projection.Machines},
		},
	}, nil
}

// Stats reports one repository, organization, or the whole local fleet. The
// repository id uses the browser's canonical "<host>/<org>/<repo>" form, which
// is also how a machine snapshot names its repositories.
func (model RemoteStateReadModel) Stats(ctx context.Context, viewer Viewer, scope Scope, id string) (Access[Stat], error) {
	projection, err := model.project(ctx, viewer, scopeFilter{scope: scope, id: strings.TrimSpace(id)})
	if err != nil {
		return Access[Stat]{}, err
	}
	return Access[Stat]{
		Visibility: VisibilityPrivate,
		Value: Stat{
			Scope:       scope,
			ID:          strings.TrimSpace(id),
			DisplayName: strings.TrimSpace(id),
			Summary:     Summary{Repositories: projection.Repositories},
			UpdatedAt:   projection.GeneratedAt,
			Fleet:       &FleetSnapshot{Machines: projection.Machines},
		},
	}, nil
}

// Series, Leaderboard and LatestMerges are part of the read-model contract but
// have no local source, so they answer with an empty, well-formed shape instead
// of failing the page.
func (model RemoteStateReadModel) Series(_ context.Context, _ Viewer, scope Scope, id, metric string) (Access[Series], error) {
	return Access[Series]{
		Visibility: VisibilityPrivate,
		Value:      Series{Scope: scope, ID: id, Metric: metric, Points: []SeriesPoint{}},
	}, nil
}

func (model RemoteStateReadModel) Leaderboard(_ context.Context, _ Viewer, metric string) (Access[Leaderboard], error) {
	return Access[Leaderboard]{
		Visibility: VisibilityPrivate,
		Value:      Leaderboard{Metric: metric, Entries: []LeaderboardEntry{}},
	}, nil
}

func (model RemoteStateReadModel) LatestMerges(_ context.Context, _ Viewer, _ int) (Access[[]LatestMerge], error) {
	return Access[[]LatestMerge]{Visibility: VisibilityPrivate, Value: []LatestMerge{}}, nil
}

// scopeFilter narrows the projection to one scope. An empty scope means the
// whole fleet, which is what the dashboard shows.
type scopeFilter struct {
	scope Scope
	id    string
}

type fleetProjection struct {
	Machines     []FleetMachine
	Repositories int
	GeneratedAt  time.Time
}

func (model RemoteStateReadModel) project(ctx context.Context, viewer Viewer, filter scopeFilter) (fleetProjection, error) {
	if model.Store == nil || model.Access == nil {
		return fleetProjection{}, ErrNoReadModel
	}
	if !viewer.Authenticated || !viewer.Member || strings.TrimSpace(viewer.UserID) == "" {
		return fleetProjection{}, ErrPrivateData
	}
	records, err := model.Store.ListLatest(ctx)
	if err != nil {
		return fleetProjection{}, err
	}
	now := time.Now().UTC()
	if model.Now != nil {
		now = model.Now().UTC()
	}
	staleAfter := model.StaleAfter
	if staleAfter <= 0 {
		staleAfter = DefaultMachineStaleAfter
	}
	repositories := map[string]bool{}
	machines := make([]FleetMachine, 0, len(records))
	generatedAt := time.Time{}
	for _, record := range records {
		snapshot := record.Snapshot
		if snapshot.Validate() != nil || record.ReceivedAt.IsZero() || record.Digest == "" {
			continue
		}
		allowed, err := model.Access.CanViewMachine(ctx, viewer, snapshot.Login, snapshot.Machine)
		if err != nil {
			return fleetProjection{}, err
		}
		if !allowed {
			continue
		}
		machine := model.machine(snapshot, record.ReceivedAt, filter, repositories, now, staleAfter)
		if len(machine.Worktrees) == 0 && !filter.matchesMachine(snapshot) {
			// A scope that names repositories the machine does not publish
			// contributes nothing, so it is left out of the fleet entirely.
			continue
		}
		machines = append(machines, machine)
		if heartbeat := machineHeartbeat(snapshot, record.ReceivedAt); heartbeat.After(generatedAt) {
			generatedAt = heartbeat
		}
	}
	sort.Slice(machines, func(i, j int) bool { return machines[i].Name < machines[j].Name })
	if generatedAt.IsZero() {
		generatedAt = now
	}
	return fleetProjection{Machines: machines, Repositories: len(repositories), GeneratedAt: generatedAt.UTC()}, nil
}

func (model RemoteStateReadModel) machine(snapshot machinesnapshot.Snapshot, receivedAt time.Time, filter scopeFilter, repositories map[string]bool, now time.Time, staleAfter time.Duration) FleetMachine {
	heartbeat := machineHeartbeat(snapshot, receivedAt)
	machine := FleetMachine{
		Name:        strings.TrimSpace(snapshot.Machine),
		PublishedAt: snapshot.PublishedAt.UTC().Format(time.RFC3339),
		LastSeenAt:  heartbeat.UTC().Format(time.RFC3339),
		Worktrees:   []FleetWorktree{},
	}
	if stale := now.Sub(heartbeat) > staleAfter; stale {
		machine.Stale = true
		machine.State = "stale"
	} else {
		machine.State = "online"
	}
	for _, repository := range snapshot.Repositories {
		if filter.matchesRepository(repository) {
			repositories[repository] = true
		}
	}
	for _, worktree := range snapshot.Worktrees {
		repository := canonicalWorktreeRepository(worktree.Repository)
		if !filter.matchesRepository(repository) {
			continue
		}
		row, ok := fleetWorktree(worktree)
		if !ok {
			continue
		}
		repositories[row.Repository] = true
		machine.Worktrees = append(machine.Worktrees, row)
	}
	sort.Slice(machine.Worktrees, func(i, j int) bool {
		left, right := machine.Worktrees[i], machine.Worktrees[j]
		if left.Repository != right.Repository {
			return left.Repository < right.Repository
		}
		return left.Task < right.Task
	})
	return machine
}

func (filter scopeFilter) matchesMachine(snapshot machinesnapshot.Snapshot) bool {
	if filter.scope == "" || filter.id == "" {
		return true
	}
	for _, repository := range snapshot.Repositories {
		if filter.matchesRepository(repository) {
			return true
		}
	}
	return false
}

func (filter scopeFilter) matchesRepository(repository string) bool {
	if filter.scope == "" || filter.id == "" {
		return true
	}
	repository = strings.TrimSpace(repository)
	id := strings.TrimSpace(filter.id)
	switch filter.scope {
	case ScopeRepository:
		return strings.EqualFold(repository, id)
	case ScopeOrganization:
		return strings.HasPrefix(strings.ToLower(repository), strings.ToLower(id)+"/")
	case ScopeUser:
		return true
	default:
		return false
	}
}

// fleetWorktree maps one stored worktree into the browser contract, refusing
// anything the browser would reject. It reports false when a required field is
// missing, so the caller drops the row rather than invalidating the payload.
func fleetWorktree(worktree machinesnapshot.Worktree) (FleetWorktree, bool) {
	task := strings.TrimSpace(worktree.Task)
	repository := canonicalWorktreeRepository(worktree.Repository)
	branch := strings.TrimSpace(worktree.Branch)
	if task == "" || repository == "" || branch == "" {
		return FleetWorktree{}, false
	}
	row := FleetWorktree{Task: task, Repository: repository, Branch: branch}
	if stream := strings.TrimSpace(worktree.Stream); stream != "" {
		row.Stream = stream
	}
	if status := strings.ToLower(strings.TrimSpace(worktree.Lifecycle)); fleetWorktreeStatuses[status] {
		row.Status = status
	}
	if owner := strings.ToLower(strings.TrimSpace(worktree.OwnerState)); fleetOwnerStates[owner] {
		row.OwnerState = owner
	}
	if !worktree.LastActivityAt.IsZero() {
		row.LastActivityAt = worktree.LastActivityAt.UTC().Format(time.RFC3339)
	}
	if request := fleetPullRequest(worktree.PullRequest); request != nil {
		row.PullRequest = request
	}
	row.NeedsAttention = worktree.NeedsAttention || strings.TrimSpace(worktree.AttentionReason) != ""
	if reason := safeAttentionReason(worktree.AttentionReason); reason != "" {
		row.AttentionReason = reason
	}
	return row, true
}

// canonicalWorktreeRepository normalises the two repository spellings a
// snapshot legitimately carries. A worktree names its repository as
// "owner/name" (Snapshot.Validate refuses a second slash there), while the
// top-level Repositories list and every dashboard scope id use the canonical
// "github.com/owner/name". The browser table and the scope filters both need
// the canonical form, so the bare spelling is prefixed here.
func canonicalWorktreeRepository(repository string) string {
	repository = strings.TrimSpace(repository)
	if repository == "" || strings.HasPrefix(repository, "github.com/") {
		return repository
	}
	return "github.com/" + repository
}

// fleetPullRequest keeps only a link the browser accepts: a positive number, an
// absolute http(s) URL, and one of the four recognised states. The stored state
// is free-form and optional, so an empty or unexpected value drops the whole
// link instead of failing validation downstream.
func fleetPullRequest(request *machinesnapshot.PullRequest) *FleetPullRequest {
	if request == nil || request.Number <= 0 {
		return nil
	}
	state := strings.ToLower(strings.TrimSpace(request.State))
	if !fleetPullRequestState[state] {
		return nil
	}
	raw := strings.TrimSpace(request.URL)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return nil
	}
	return &FleetPullRequest{Number: request.Number, URL: raw, State: state}
}
