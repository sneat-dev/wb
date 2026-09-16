package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
)

// fixedSnapshots is a SnapshotReader over fixed records.
type fixedSnapshots struct {
	records []machinesnapshot.StoredSnapshot
	err     error
}

func (store fixedSnapshots) ListLatest(context.Context) ([]machinesnapshot.StoredSnapshot, error) {
	return store.records, store.err
}

// localMachineAccess allows every machine, which is what the loopback wiring
// does: the operator owns the listener.
type localMachineAccess struct{}

func (localMachineAccess) CanViewMachine(context.Context, Viewer, string, string) (bool, error) {
	return true, nil
}

func localViewer() Viewer {
	return Viewer{Authenticated: true, Member: true, UserID: "local"}
}

func testSnapshot(publishedAt time.Time, worktrees ...machinesnapshot.Worktree) machinesnapshot.Snapshot {
	return machinesnapshot.Snapshot{
		SchemaVersion: machinesnapshot.SchemaVersion,
		Login:         "alex",
		Machine:       "laptop",
		PublishedAt:   publishedAt,
		LastSeenAt:    publishedAt,
		// Canonical github.com identities, sorted and deduplicated: what
		// Snapshot.Validate requires.
		Repositories: []string{"github.com/acme/gadgets", "github.com/acme/widgets"},
		Worktrees:    worktrees,
	}
}

func stored(snapshot machinesnapshot.Snapshot, receivedAt time.Time) machinesnapshot.StoredSnapshot {
	return machinesnapshot.StoredSnapshot{Snapshot: snapshot, ReceivedAt: receivedAt, Digest: "digest"}
}

func readModel(records ...machinesnapshot.StoredSnapshot) RemoteStateReadModel {
	return RemoteStateReadModel{
		Store:      fixedSnapshots{records: records},
		Access:     localMachineAccess{},
		Now:        func() time.Time { return time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC) },
		StaleAfter: time.Hour,
	}
}

func TestDashboardServesTheLocalFleet(t *testing.T) {
	publishedAt := time.Date(2026, 9, 15, 11, 30, 0, 0, time.UTC)
	snapshot := testSnapshot(publishedAt, machinesnapshot.Worktree{
		Task: "ship-dashboard", Repository: "acme/widgets", Branch: "feat/dashboard",
		Lifecycle: "active", OwnerState: "active", LastActivityAt: publishedAt,
		PullRequest: &machinesnapshot.PullRequest{Number: 7, URL: "https://github.com/acme/widgets/pull/7", State: "open"},
	})

	access, err := readModel(stored(snapshot, publishedAt)).Dashboard(context.Background(), localViewer())
	if err != nil {
		t.Fatalf("Dashboard() error = %v", err)
	}
	if access.Visibility != VisibilityPrivate {
		t.Fatalf("visibility = %q; want private so disclosure stays member-gated", access.Visibility)
	}
	if got := access.Value.Summary.Repositories; got != 2 {
		t.Fatalf("summary.repositories = %d; want 2", got)
	}
	if access.Value.Fleet == nil || len(access.Value.Fleet.Machines) != 1 {
		t.Fatalf("fleet = %+v; want one machine", access.Value.Fleet)
	}
	machine := access.Value.Fleet.Machines[0]
	if machine.Name != "laptop" || machine.State != "online" || len(machine.Worktrees) != 1 {
		t.Fatalf("machine = %+v", machine)
	}
	worktree := machine.Worktrees[0]
	if worktree.Status != "active" || worktree.OwnerState != "active" {
		t.Fatalf("worktree = %+v", worktree)
	}
	// A worktree names its repository "owner/name"; the browser table and the
	// scope ids use the canonical form, so the emitted row must be canonical.
	if worktree.Repository != "github.com/acme/widgets" {
		t.Fatalf("worktree.repository = %q; want the canonical github.com/owner/name", worktree.Repository)
	}
	if worktree.PullRequest == nil || worktree.PullRequest.State != "open" {
		t.Fatalf("pull request = %+v", worktree.PullRequest)
	}
	assertDashboardContract(t, access.Value)
}

// TestFleetPayloadSurvivesHostileStoredValues is the guard against a silent
// blank page. The browser's isFleetSnapshot rejects the whole payload when one
// value is off-contract, and machine snapshots carry free-form lifecycle,
// owner and pull-request state that the browser does not accept, so an
// unfiltered pass-through would render no data at all with no error shown.
func TestFleetPayloadSurvivesHostileStoredValues(t *testing.T) {
	publishedAt := time.Date(2026, 9, 15, 11, 30, 0, 0, time.UTC)
	hostile := testSnapshot(publishedAt,
		machinesnapshot.Worktree{
			// "working" is a real stored lifecycle and not one of the nine
			// values the browser accepts.
			Task: "keep-working", Repository: "acme/widgets", Branch: "feat/a",
			Lifecycle: "working", OwnerState: "zombie",
			PullRequest: &machinesnapshot.PullRequest{Number: 7, URL: "https://github.com/acme/widgets/pull/7", State: ""},
		},
		machinesnapshot.Worktree{
			// The stored pull-request state is free-form and optional, so an
			// unrecognised value is reachable from valid stored data and must
			// not take the whole fleet down with it.
			Task: "odd-link", Repository: "acme/gadgets", Branch: "feat/b",
			Lifecycle: "blocked", OwnerState: "orphaned",
			PullRequest: &machinesnapshot.PullRequest{Number: 9, URL: "https://github.com/acme/gadgets/pull/9", State: "reopened"},
		},
	)
	// The fixture is a snapshot the hub would accept: the trap is reachable
	// from entirely valid stored data.
	if err := hostile.Validate(); err != nil {
		t.Fatalf("fixture must be a valid snapshot: %v", err)
	}

	access, err := readModel(stored(hostile, publishedAt)).Dashboard(context.Background(), localViewer())
	if err != nil {
		t.Fatalf("Dashboard() error = %v", err)
	}
	machine := access.Value.Fleet.Machines[0]
	if len(machine.Worktrees) != 2 {
		t.Fatalf("worktrees = %d; want both rows kept", len(machine.Worktrees))
	}
	for _, worktree := range machine.Worktrees {
		if worktree.Status == "working" || worktree.OwnerState == "zombie" {
			t.Fatalf("off-contract value passed through: %+v", worktree)
		}
		if worktree.PullRequest != nil && worktree.PullRequest.State != "open" && worktree.PullRequest.State != "draft" {
			t.Fatalf("off-contract pull request passed through: %+v", worktree.PullRequest)
		}
	}
	assertDashboardContract(t, access.Value)

	// And the accepted values still travel: "blocked" and "orphaned" are in
	// both enums, so they survive.
	if got := machine.Worktrees[0].Status; got != "blocked" {
		t.Fatalf("status = %q; want blocked to survive", got)
	}
}

func TestStatsNarrowToTheRequestedScope(t *testing.T) {
	publishedAt := time.Date(2026, 9, 15, 11, 30, 0, 0, time.UTC)
	model := readModel(stored(testSnapshot(publishedAt), publishedAt))

	for _, test := range []struct {
		scope        Scope
		id           string
		repositories int
	}{
		{ScopeRepository, "github.com/acme/widgets", 1},
		{ScopeOrganization, "github.com/acme", 2},
		{ScopeUser, "local", 2},
	} {
		access, err := model.Stats(context.Background(), localViewer(), test.scope, test.id)
		if err != nil {
			t.Fatalf("Stats(%s, %s) error = %v", test.scope, test.id, err)
		}
		if got := access.Value.Summary.Repositories; got != test.repositories {
			t.Fatalf("Stats(%s, %s).repositories = %d; want %d", test.scope, test.id, got, test.repositories)
		}
	}

	// A scope this machine does not publish contributes nothing.
	access, err := model.Stats(context.Background(), localViewer(), ScopeRepository, "github.com/other/thing")
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if got := access.Value.Summary.Repositories; got != 0 {
		t.Fatalf("unrelated scope repositories = %d; want 0", got)
	}
	if access.Value.Fleet == nil || len(access.Value.Fleet.Machines) != 0 {
		t.Fatalf("unrelated scope fleet = %+v; want no machines", access.Value.Fleet)
	}
}

// TestUnavailableSeriesAreWellFormed keeps the page rendering: the browser
// expects arrays, not null, even when a self-hosted hub has no such data.
func TestUnavailableSeriesAreWellFormed(t *testing.T) {
	model := readModel()
	ctx := context.Background()

	series, err := model.Series(ctx, localViewer(), ScopeRepository, "github.com/acme/widgets", "tokens")
	if err != nil {
		t.Fatalf("Series() error = %v", err)
	}
	raw, err := json.Marshal(series.Value)
	if err != nil {
		t.Fatalf("marshal series: %v", err)
	}
	if !strings.Contains(string(raw), `"points":[]`) {
		t.Fatalf("series points = %s; want a JSON array", raw)
	}

	leaderboard, err := model.Leaderboard(ctx, localViewer(), "tokens")
	if err != nil {
		t.Fatalf("Leaderboard() error = %v", err)
	}
	if raw, err = json.Marshal(leaderboard.Value); err != nil {
		t.Fatalf("marshal leaderboard: %v", err)
	}
	if !strings.Contains(string(raw), `"entries":[]`) {
		t.Fatalf("leaderboard entries = %s; want a JSON array", raw)
	}

	merges, err := model.LatestMerges(ctx, localViewer(), 10)
	if err != nil {
		t.Fatalf("LatestMerges() error = %v", err)
	}
	if merges.Value == nil {
		t.Fatal("latest merges = nil; want an empty slice")
	}
}

func TestReadModelRefusesAnUnauthenticatedViewer(t *testing.T) {
	publishedAt := time.Date(2026, 9, 15, 11, 30, 0, 0, time.UTC)
	model := readModel(stored(testSnapshot(publishedAt), publishedAt))

	for name, viewer := range map[string]Viewer{
		"anonymous":    {},
		"not a member": {Authenticated: true, UserID: "local"},
		"no identity":  {Authenticated: true, Member: true},
	} {
		if _, err := model.Dashboard(context.Background(), viewer); !errors.Is(err, ErrPrivateData) {
			t.Fatalf("%s: error = %v; want ErrPrivateData", name, err)
		}
	}
}

func TestUnconfiguredAndUnreadableStoresAreReported(t *testing.T) {
	if _, err := (RemoteStateReadModel{}).Dashboard(context.Background(), localViewer()); !errors.Is(err, ErrNoReadModel) {
		t.Fatalf("error = %v; want ErrNoReadModel", err)
	}
	failing := readModel()
	failing.Store = fixedSnapshots{err: errors.New("store unavailable")}
	if _, err := failing.Dashboard(context.Background(), localViewer()); err == nil {
		t.Fatal("error = nil; want the store failure")
	}
}

func TestMalformedRecordsAreSkippedRatherThanServed(t *testing.T) {
	publishedAt := time.Date(2026, 9, 15, 11, 30, 0, 0, time.UTC)
	invalid := testSnapshot(publishedAt)
	invalid.SchemaVersion = 99 // Validate rejects it.
	model := readModel(
		stored(invalid, publishedAt),
		machinesnapshot.StoredSnapshot{Snapshot: testSnapshot(publishedAt)}, // no ReceivedAt or Digest
	)

	access, err := model.Dashboard(context.Background(), localViewer())
	if err != nil {
		t.Fatalf("Dashboard() error = %v", err)
	}
	if len(access.Value.Fleet.Machines) != 0 {
		t.Fatalf("machines = %+v; want none", access.Value.Fleet.Machines)
	}
}

// assertDashboardContract enforces the browser's acceptance rules
// (hub/web/src/data/worktrees.ts isFleetSnapshot and dashboard.ts) on the JSON
// this package emits. A violation here is a blank page in production, which no
// server-side error would reveal.
func assertDashboardContract(t *testing.T, dashboard Dashboard) {
	t.Helper()
	raw, err := json.Marshal(dashboard)
	if err != nil {
		t.Fatalf("marshal dashboard: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal dashboard: %v", err)
	}
	if _, ok := body["generated_at"].(string); !ok {
		t.Fatalf("generated_at = %v; want a string", body["generated_at"])
	}
	summary, ok := body["summary"].(map[string]any)
	if !ok {
		t.Fatalf("summary = %v; want an object", body["summary"])
	}
	for _, field := range []string{"repositories", "open_pulls", "merged_pulls", "open_issues", "releases"} {
		if _, ok := summary[field].(float64); !ok {
			t.Fatalf("summary.%s = %v; want a number", field, summary[field])
		}
	}
	fleet, ok := body["fleet"].(map[string]any)
	if !ok {
		t.Fatalf("fleet = %v; want an object", body["fleet"])
	}
	machines, ok := fleet["machines"].([]any)
	if !ok {
		t.Fatalf("fleet.machines = %v; want an array", fleet["machines"])
	}
	for _, entry := range machines {
		machine, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("machine = %v; want an object", entry)
		}
		if name, _ := machine["name"].(string); name == "" {
			t.Fatalf("machine.name = %v; want a non-empty string", machine["name"])
		}
		if state, present := machine["state"]; present && !oneOf(state, "online", "stale", "offline", "unknown") {
			t.Fatalf("machine.state = %v; not accepted", state)
		}
		worktrees, ok := machine["worktrees"].([]any)
		if !ok {
			t.Fatalf("machine.worktrees = %v; want an array", machine["worktrees"])
		}
		for _, item := range worktrees {
			worktree, ok := item.(map[string]any)
			if !ok {
				t.Fatalf("worktree = %v; want an object", item)
			}
			for _, required := range []string{"task", "repository", "branch"} {
				if value, _ := worktree[required].(string); value == "" {
					t.Fatalf("worktree.%s = %v; want a non-empty string", required, worktree[required])
				}
			}
			if status, present := worktree["status"]; present && !oneOf(status,
				"active", "idle", "ready", "blocked", "landed", "cleanup_pending", "recovery_needed", "orphaned", "unknown") {
				t.Fatalf("worktree.status = %v; not accepted", status)
			}
			if owner, present := worktree["owner_state"]; present && !oneOf(owner, "active", "orphaned", "unknown") {
				t.Fatalf("worktree.owner_state = %v; not accepted", owner)
			}
			if request, present := worktree["pull_request"]; present {
				link, ok := request.(map[string]any)
				if !ok {
					t.Fatalf("pull_request = %v; want an object", request)
				}
				if number, ok := link["number"].(float64); !ok || number <= 0 {
					t.Fatalf("pull_request.number = %v; want a positive number", link["number"])
				}
				raw, _ := link["url"].(string)
				parsed, err := url.Parse(raw)
				if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
					t.Fatalf("pull_request.url = %q; want an absolute http(s) URL", raw)
				}
				if !oneOf(link["state"], "draft", "open", "merged", "closed") {
					t.Fatalf("pull_request.state = %v; not accepted", link["state"])
				}
			}
		}
	}
}

func oneOf(value any, allowed ...string) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	for _, candidate := range allowed {
		if text == candidate {
			return true
		}
	}
	return false
}
