package fleet

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cockpit"
)

// waitFor polls condition until it holds or two seconds pass.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// idsAreUnique requires every id in ids to be non-empty and distinct.
func idsAreUnique(t *testing.T, collection string, ids []string) {
	t.Helper()
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] || strings.ContainsAny(id, "/ ?#%") {
			t.Errorf("%s id %q is empty, repeated or not URL-safe", collection, id)
		}
		seen[id] = true
	}
}

// TestFleetReadModelListsLocalStateAndCachedMachines proves
// cockpit#ac:read-model-lists-local-state: one repository with two worktrees,
// one with an open pull request, a session and a second machine's snapshot.
func TestFleetReadModelListsLocalStateAndCachedMachines(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources(t.TempDir())
	snapshotter, clock := newSnapshotter(sources.collectors(), nil)
	refreshAndSettle(t, snapshotter)
	document := newCockpitServer(t, snapshotter).fleet()

	if document.SchemaVersion != SchemaVersion || document.WarmingUp || !document.SnapshotAt.Equal(clock.Now()) {
		t.Errorf("document header = %d warming %v at %v", document.SchemaVersion, document.WarmingUp, document.SnapshotAt)
	}
	machines := map[string]Machine{}
	for _, machine := range document.Machines {
		machines[machine.Machine] = machine
	}
	if len(machines) != 2 || machines[testMachine].Route != RouteLocal || machines["desktop"].Route != RouteCached {
		t.Fatalf("machines = %+v, want this machine local and desktop cached", document.Machines)
	}
	published, _ := time.Parse(time.RFC3339, remotePublish)
	if !machines["desktop"].ObservedAt.Equal(published) || machines[testMachine].WBVersion != testVersion {
		t.Errorf("machine times/versions = %+v", machines)
	}

	var local Repository
	for _, repository := range document.Repositories {
		if repository.Route == RouteLocal {
			local = repository
		}
	}
	if local.Name != "acme/widgets" || local.Host != "github.com" || local.WorktreeCount != 2 || *local.LocalBranchCount != 2 || *local.RemoteBranchCount != 1 || local.OpenPullRequestCount != nil {
		t.Errorf("local repository = %+v", local)
	}
	var local2, cached int
	worktreeID := ""
	for _, worktree := range document.Worktrees {
		switch worktree.Route {
		case RouteLocal:
			local2++
			if worktree.Task == "task-a" {
				worktreeID = worktree.ID
			}
		case RouteCached:
			cached++
			if !worktree.ObservedAt.Equal(published) {
				t.Errorf("cached worktree observed at %v, want the publish time", worktree.ObservedAt)
			}
		}
	}
	if local2 != 2 || cached != 1 {
		t.Errorf("worktrees: %d local, %d cached", local2, cached)
	}
	for _, branch := range snapshotter.allBranches() {
		if branch.Route != RouteLocal {
			t.Errorf("branch %+v: want a local route", branch)
		}
		if branch.Name == "feature/a" && (branch.Worktree != worktreeID || branch.Task != "task-a" || branch.Upstream != "origin/feature/a" || branch.Ahead != 2) {
			t.Errorf("branch %+v: want its worktree, task and tracking state", branch)
		}
	}
	if len(snapshotter.allBranches()) != 3 {
		t.Errorf("branches = %d, want 3", len(snapshotter.allBranches()))
	}
	prs := map[int]PullRequest{}
	for _, pull := range document.PullRequests {
		prs[pull.Number] = pull
	}
	if prs[7].Worktree != worktreeID || prs[7].State != PullRequestUnknown || prs[7].Repository != local.ID || prs[7].Route != RouteLocal || prs[3].Route != RouteCached {
		t.Errorf("pull requests = %+v, want #7 tied to its worktree and #3 cached", document.PullRequests)
	}
	if len(document.Agents) != 1 || document.Agents[0].SessionID != "wbs-1" || document.Agents[0].Kind != AgentSession {
		t.Errorf("agents = %+v, want the session", document.Agents)
	}
	var ids []string
	for _, item := range document.Machines {
		ids = append(ids, item.ID)
	}
	idsAreUnique(t, "machines", ids)
	ids = nil
	for _, item := range document.Repositories {
		ids = append(ids, item.ID)
	}
	idsAreUnique(t, "repositories", ids)
	ids = nil
	for _, item := range document.Worktrees {
		ids = append(ids, item.ID)
	}
	idsAreUnique(t, "worktrees", ids)
	ids = nil
	for _, item := range snapshotter.allBranches() {
		ids = append(ids, item.ID)
	}
	idsAreUnique(t, "branches", ids)
	ids = nil
	for _, item := range document.PullRequests {
		ids = append(ids, item.ID)
	}
	idsAreUnique(t, "pull requests", ids)
	ids = nil
	for _, item := range document.Agents {
		ids = append(ids, item.ID)
	}
	idsAreUnique(t, "agents", ids)
}

// TestFleetIdsAreStableAcrossRefreshesAndRestarts requires an id to come from
// identity alone: the same state read by a fresh snapshotter at another time
// gives the same ids.
func TestFleetIdsAreStableAcrossRefreshesAndRestarts(t *testing.T) {
	t.Parallel()
	read := func(advance time.Duration) Document {
		snapshotter, clock := newSnapshotter(oneRepoSources(t.TempDir()).collectors(), nil)
		clock.advance(advance)
		refreshAndSettle(t, snapshotter)
		return snapshotter.Document()
	}
	first, second := read(0), read(48*time.Hour)
	if len(first.Worktrees) != 3 || len(first.Repositories) != 3 {
		t.Fatalf("first read = %d worktrees, %d repositories", len(first.Worktrees), len(first.Repositories))
	}
	for index := range first.Worktrees {
		if first.Worktrees[index].ID != second.Worktrees[index].ID {
			t.Errorf("worktree id changed between runs: %q then %q", first.Worktrees[index].ID, second.Worktrees[index].ID)
		}
	}
	for index := range first.Repositories {
		if first.Repositories[index].ID != second.Repositories[index].ID {
			t.Errorf("repository id changed between runs")
		}
	}
}

// TestRequestBeforeTheFirstSnapshotIsEmptyAndWarmingUp proves the cold half of
// cockpit#ac:request-does-not-scan with collectors that fail the test if
// called.
func TestRequestBeforeTheFirstSnapshotIsEmptyAndWarmingUp(t *testing.T) {
	t.Parallel()
	snapshotter, _ := newSnapshotter(forbidden{t}.collectors(), nil)
	recorder := newCockpitServer(t, snapshotter).get(cockpit.APIPrefix+FleetRoute, nil)
	want := `{"schema_version":2,"warming_up":true,"repositories_total":0,"repositories_scanned":0,"diagnostics":0,"refresh_interval_seconds":60,"machines":[],"repositories":[],"worktrees":[],"pull_requests":[],"agents":[]}` + "\n"
	if recorder.Code != http.StatusOK || recorder.Body.String() != want {
		t.Fatalf("cold fleet = %d %q, want %q", recorder.Code, recorder.Body.String(), want)
	}
}

// TestRequestsReadTheLastSnapshotAndRunNoCollector proves
// cockpit#ac:request-does-not-scan end to end: cold, warm and refreshed after a
// worktree is added and one interval passes, with the collectors' call count
// unchanged by every request.
func TestRequestsReadTheLastSnapshotAndRunNoCollector(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources(t.TempDir())
	ticks := make(chan time.Time)
	snapshotter, _ := newSnapshotter(sources.collectors(), func(options *Options) {
		options.Tick = func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }
		options.Fingerprint = func(string) (string, error) { return "", errBoom }
	})
	server := newCockpitServer(t, snapshotter)
	request := func() Document {
		t.Helper()
		before := sources.calls.Load()
		document := server.fleet()
		if after := sources.calls.Load(); after != before {
			t.Fatalf("a request ran %d collector calls", after-before)
		}
		return document
	}

	if cold := request(); !cold.WarmingUp || len(cold.Worktrees) != 0 || !cold.SnapshotAt.IsZero() {
		t.Fatalf("cold response = %+v, want empty and warming up", cold)
	}
	stop := snapshotter.Start(context.Background())
	t.Cleanup(stop)
	waitFor(t, "the first snapshot", func() bool { return !snapshotter.Document().WarmingUp })
	waitFor(t, "the first refresh to finish", func() bool { return sources.calls.Load() == passCalls })
	warm := request()
	if warm.WarmingUp || warm.SnapshotAt.IsZero() || len(localWorktrees(warm)) != 2 {
		t.Fatalf("warm response = %+v, want a snapshot with two worktrees", warm)
	}

	sources.change(func(f *fakeSources) {
		f.worktrees["acme/widgets"] = append(f.worktrees["acme/widgets"], LinkedWorktree{Path: "/wt/task-c", Branch: "feature/c"})
		f.records["/wt/task-c"] = WorktreeRecord{Task: "task-c", Branch: "feature/c"}
	})
	ticks <- time.Time{}
	waitFor(t, "the interval's refresh to finish", func() bool { return sources.calls.Load() == 2*passCalls+1 })
	waitFor(t, "the new worktree", func() bool { return len(localWorktrees(snapshotter.Document())) == 3 })
	refreshed := request()
	if len(localWorktrees(refreshed)) != 3 || !refreshed.SnapshotAt.Equal(snapshotter.Document().SnapshotAt) {
		t.Fatalf("refreshed response lists %d worktrees", len(localWorktrees(refreshed)))
	}
}

func localWorktrees(document Document) []Worktree {
	var local []Worktree
	for _, worktree := range document.Worktrees {
		if worktree.Route == RouteLocal {
			local = append(local, worktree)
		}
	}
	return local
}

// TestAnonymousLocalGetsMetadataOnly proves
// cockpit#ac:anonymous-local-gets-metadata-only: a worktree and a remote
// snapshot that carry sensitive fields, requested with no session cookie.
func TestAnonymousLocalGetsMetadataOnly(t *testing.T) {
	t.Parallel()
	sources := sentinelSources()
	// The same sensitive values the Feature names: an untracked file name, a
	// commit subject, a path and a task summary.
	sources.bindings[0].ClaimID = "secret-claim"
	sources.remote[0].Snapshot.Repositories[0].Untracked = []string{"secret-untracked-file.txt"}
	sources.remote[0].Snapshot.Repositories[0].Unpushed = []string{"abc123 secret subject"}
	sources.remote[0].Snapshot.Worktrees[0].Dir = "/home/other/secret/dir"
	sources.remote[0].Snapshot.Worktrees[0].TaskSummary = "secret remote summary"
	snapshotter, _ := newSnapshotter(sources.collectors(), nil)
	refreshAndSettle(t, snapshotter)
	server := newCockpitServer(t, snapshotter)

	fleet := server.get(cockpit.APIPrefix+FleetRoute, nil)
	if fleet.Code != http.StatusOK {
		t.Fatalf("fleet without a session = %d", fleet.Code)
	}
	for _, forbidden := range []string{"secret", sentinel, "/home/other", "untracked-file", "SENTINEL"} {
		if strings.Contains(fleet.Body.String(), forbidden) {
			t.Errorf("the read model contains %q: %s", forbidden, fleet.Body.String())
		}
	}
	var repositoryID string
	for _, repository := range snapshotter.Document().Repositories {
		if repository.Route == RouteLocal {
			repositoryID = repository.ID
		}
	}
	readme := server.get(ReadmePath+"?repository="+repositoryID, nil)
	if readme.Code != http.StatusUnauthorized {
		t.Fatalf("README without a session = %d %s, want 401", readme.Code, readme.Body.String())
	}
}
