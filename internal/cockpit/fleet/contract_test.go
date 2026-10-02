package fleet

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/cockpit"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/lifecyclehooks"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktrees"
)

const (
	fleetURL    = cockpit.APIPrefix + FleetRoute
	branchesURL = cockpit.APIPrefix + BranchesRoute + "?repository="
)

func gunzip(t *testing.T, data []byte) []byte {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return plain
}

// localRepositoryOf is the id of the document's first local repository.
func localRepositoryOf(t *testing.T, snapshotter *Snapshotter) Repository {
	t.Helper()
	for _, repository := range snapshotter.Document().Repositories {
		if repository.Route == RouteLocal {
			return repository
		}
	}
	t.Fatal("no local repository")
	return Repository{}
}

// TestFleetAndBranchesAreGzipWithAnEncodingSpecificETag proves
// cockpit-views#ac:responses-are-gzip-with-etag for the fleet document and the
// branches route (the metrics route is task 2's, the static assets web's).
func TestFleetAndBranchesAreGzipWithAnEncodingSpecificETag(t *testing.T) {
	t.Parallel()
	snapshotter, _ := newSnapshotter(oneRepoSources(t.TempDir()).collectors(), nil)
	refreshAndSettle(t, snapshotter)
	server := newCockpitServer(t, snapshotter)
	id := localRepositoryOf(t, snapshotter).ID
	for _, target := range []string{fleetURL, branchesURL + id} {
		zipped := server.get(target, nil, "Accept-Encoding", "gzip")
		tag := zipped.Header().Get("ETag")
		if zipped.Code != 200 || zipped.Header().Get("Content-Encoding") != "gzip" || !strings.HasSuffix(tag, `-gzip"`) || zipped.Header().Get("Vary") != "Origin, Accept-Encoding" {
			t.Fatalf("%s gzip = %d %v", target, zipped.Code, zipped.Header())
		}
		plain := server.get(target, nil)
		identity := plain.Header().Get("ETag")
		if plain.Code != 200 || plain.Header().Get("Content-Encoding") != "" || identity == "" || strings.HasSuffix(identity, `-gzip"`) || plain.Header().Get("Vary") != "Origin, Accept-Encoding" {
			t.Fatalf("%s identity = %d %v", target, plain.Code, plain.Header())
		}
		if !bytes.Equal(gunzip(t, zipped.Body.Bytes()), plain.Body.Bytes()) || !json.Valid(plain.Body.Bytes()) {
			t.Errorf("%s: the gzip body is not the identity body", target)
		}
		for name, candidate := range map[string]string{"the gzip ETag": tag, "the identity ETag": identity, "a weak form": "W/" + tag, "a list": `"stale", ` + identity, "any": "*"} {
			if again := server.get(target, nil, "Accept-Encoding", "gzip", "If-None-Match", candidate); again.Code != http.StatusNotModified || again.Body.Len() != 0 || again.Header().Get("ETag") != tag {
				t.Errorf("%s with %s = %d (%d bytes), want 304", target, name, again.Code, again.Body.Len())
			}
		}
		if stale := server.get(target, nil, "Accept-Encoding", "gzip", "If-None-Match", `"stale"`); stale.Code != 200 {
			t.Errorf("%s with a stale ETag = %d", target, stale.Code)
		}
	}
}

// TestGzipBytesAreComputedOncePerStoredSnapshot proves
// cockpit-views#ac:gzip-bytes-are-computed-once: the compressor runs once for
// each snapshot stored (and for the initial empty one) and never for a request.
func TestGzipBytesAreComputedOncePerStoredSnapshot(t *testing.T) {
	t.Parallel()
	var compressed atomic.Int64
	snapshotter, _ := newSnapshotter(oneRepoSources(t.TempDir()).collectors(), func(options *Options) {
		options.Compress = func(data []byte) []byte {
			compressed.Add(1)
			return cockpit.Gzip(data)
		}
	})
	refreshAndSettle(t, snapshotter)
	stored := func() int64 {
		snapshotter.mu.RLock()
		defer snapshotter.mu.RUnlock()
		return int64(snapshotter.publishes) + 1 // the empty document New stores
	}
	if compressed.Load() != stored() {
		t.Fatalf("the compressor ran %d times for %d stored snapshots", compressed.Load(), stored())
	}
	server := newCockpitServer(t, snapshotter)
	before := compressed.Load()
	for range 100 {
		if recorder := server.get(fleetURL, nil, "Accept-Encoding", "gzip"); recorder.Code != 200 {
			t.Fatal(recorder.Code)
		}
	}
	if compressed.Load() != before {
		t.Fatalf("100 requests ran the compressor %d times", compressed.Load()-before)
	}
	refreshAndSettle(t, snapshotter)
	if compressed.Load() != stored() || compressed.Load() == before {
		t.Errorf("after a new snapshot the compressor ran %d times in all for %d stored", compressed.Load(), stored())
	}
}

// TestBranchesRouteServesFromTheLastSnapshotWithNoGit proves
// cockpit-views#ac:branches-leave-the-document.
func TestBranchesRouteServesFromTheLastSnapshotWithNoGit(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources(t.TempDir())
	var twelve []BranchRef
	for index := range 12 {
		twelve = append(twelve, BranchRef{Name: fmt.Sprintf("topic-%02d", 11-index), Scope: BranchLocal, Upstream: "origin/main"})
	}
	sources.branches["acme/widgets"] = twelve
	snapshotter, _ := newSnapshotter(sources.collectors(), nil)
	refreshAndSettle(t, snapshotter)
	server := newCockpitServer(t, snapshotter)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(server.get(fleetURL, nil).Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, present := raw["branches"]; present {
		t.Error("the document carries a branches collection")
	}
	document := snapshotter.Document()
	local := localRepositoryOf(t, snapshotter)
	if *local.LocalBranchCount != 12 || *local.RemoteBranchCount != 0 || document.SchemaVersion != 2 {
		t.Errorf("counts = %d local, %d remote at schema %d", *local.LocalBranchCount, *local.RemoteBranchCount, document.SchemaVersion)
	}
	var cached Repository
	for _, repository := range document.Repositories {
		if repository.Route == RouteCached {
			cached = repository
		}
	}
	calls := sources.calls.Load()

	response := server.get(branchesURL+local.ID, nil)
	var listed BranchesResponse
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || len(listed.Branches) != 12 || listed.Repository != local.ID || listed.Reason != "" || listed.Branches[0].Name != "topic-00" {
		t.Fatalf("local branches = %d %+v", response.Code, listed)
	}
	empty := server.get(branchesURL+cached.ID, nil)
	var none BranchesResponse
	if err := json.Unmarshal(empty.Body.Bytes(), &none); err != nil {
		t.Fatal(err)
	}
	if empty.Code != 200 || none.Branches == nil || len(none.Branches) != 0 || none.Reason != ReasonCachedRepository || !strings.Contains(empty.Body.String(), `"branches":[]`) {
		t.Errorf("cached repository = %d %s", empty.Code, empty.Body.String())
	}
	again := server.get(branchesURL+cached.ID, nil)
	if again.Header().Get("ETag") != empty.Header().Get("ETag") {
		t.Error("the cached repository's body is not prepared once")
	}
	unknown := server.get(branchesURL+"repo-nope", nil)
	if unknown.Code != http.StatusNotFound || strings.Contains(unknown.Body.String(), "branches") {
		t.Errorf("unknown id = %d %s", unknown.Code, unknown.Body.String())
	}
	if missing := server.get(cockpit.APIPrefix+BranchesRoute, nil); missing.Code != http.StatusNotFound {
		t.Errorf("no id = %d", missing.Code)
	}
	if sources.calls.Load() != calls {
		t.Errorf("the branches requests ran %d collector calls, want none", sources.calls.Load()-calls)
	}

	// Two branches of one name (a local and a remote) are ordered by id.
	sources.change(func(f *fakeSources) {
		f.branches["acme/widgets"] = []BranchRef{{Name: "same", Scope: BranchLocal}, {Name: "same", Scope: BranchRemote}}
	})
	if err := snapshotter.RefreshRepository(t.Context(), local.ID); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(server.get(branchesURL+local.ID, nil).Body.Bytes(), &listed); err != nil || len(listed.Branches) != 2 || listed.Branches[0].ID >= listed.Branches[1].ID {
		t.Errorf("equal names: %+v, %v", listed.Branches, err)
	}

	// A new scan replaces the prepared list.
	sources.change(func(f *fakeSources) { f.branches["acme/widgets"] = twelve[:3] })
	if err := snapshotter.RefreshRepository(t.Context(), local.ID); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(server.get(branchesURL+local.ID, nil).Body.Bytes(), &listed); err != nil || len(listed.Branches) != 3 {
		t.Errorf("after a rescan: %d branches, %v", len(listed.Branches), err)
	}
}

// TestBranchesOfARepositoryNotYetScannedAreUnknown covers an id that is held
// but whose first read has not finished: the document does not list it yet.
func TestBranchesOfARepositoryNotYetScannedAreUnknown(t *testing.T) {
	t.Parallel()
	snapshotter, _ := newSnapshotter(oneRepoSources(t.TempDir()).collectors(), nil)
	snapshotter.track([]discover.Repo{{Host: "github.com", Org: "acme", Name: "widgets", Path: "/x"}})
	if _, found := snapshotter.Branches(localRepositoryID(testMachine, discover.Repo{Host: "github.com", Org: "acme", Name: "widgets"})); found {
		t.Error("a repository with no scan has branches")
	}
}

// TestFleetAndBranchesAreMetadataRoutesForTheHostedOrigin proves
// cockpit-views#ac:hosted-origin-can-revalidate for the fleet document and the
// branches route.
func TestFleetAndBranchesAreMetadataRoutesForTheHostedOrigin(t *testing.T) {
	t.Parallel()
	snapshotter, _ := newSnapshotter(oneRepoSources(t.TempDir()).collectors(), nil)
	refreshAndSettle(t, snapshotter)
	server := newCockpitServer(t, snapshotter)
	for _, target := range []string{fleetURL, branchesURL + localRepositoryOf(t, snapshotter).ID} {
		preflight := httptest.NewRequest(http.MethodOptions, target, nil)
		preflight.Host = testHost
		preflight.Header.Set("Origin", hostedOrigin)
		preflight.Header.Set("Access-Control-Request-Method", "GET")
		preflight.Header.Set("Access-Control-Request-Headers", "If-None-Match")
		recorder := httptest.NewRecorder()
		server.api.ServeHTTP(recorder, preflight)
		if recorder.Code != http.StatusNoContent || recorder.Header().Get("Access-Control-Allow-Headers") != "if-none-match" || recorder.Header().Get("Access-Control-Allow-Origin") != hostedOrigin {
			t.Fatalf("%s preflight = %d %v", target, recorder.Code, recorder.Header())
		}
		first := server.get(target, nil, "Origin", hostedOrigin)
		if first.Code != 200 || first.Header().Get("Access-Control-Expose-Headers") != "ETag" || first.Header().Get("Access-Control-Allow-Origin") != hostedOrigin || first.Header().Get("ETag") == "" {
			t.Fatalf("%s hosted response = %d %v", target, first.Code, first.Header())
		}
		repeat := server.get(target, nil, "Origin", hostedOrigin, "If-None-Match", first.Header().Get("ETag"))
		if repeat.Code != http.StatusNotModified || repeat.Header().Get("Access-Control-Allow-Origin") != hostedOrigin {
			t.Errorf("%s repeat = %d %v", target, repeat.Code, repeat.Header())
		}
		if foreign := server.get(target, nil, "Origin", "https://elsewhere.example"); foreign.Code != http.StatusForbidden {
			t.Errorf("%s from another origin = %d, want 403", target, foreign.Code)
		}
	}
}

// TestBranchesNeedASessionWhenForwardedOrNotLoopback proves the branches route
// has the fleet document's access class: a proxied request or one for a
// non-loopback host gets no anonymous reading.
func TestBranchesNeedASessionWhenForwardedOrNotLoopback(t *testing.T) {
	t.Parallel()
	snapshotter, _ := newSnapshotter(oneRepoSources(t.TempDir()).collectors(), nil)
	refreshAndSettle(t, snapshotter)
	server := newCockpitServer(t, snapshotter)
	target := branchesURL + localRepositoryOf(t, snapshotter).ID
	if recorder := server.get(target, nil); recorder.Code != 200 {
		t.Fatalf("anonymous loopback = %d", recorder.Code)
	}
	for name, headers := range map[string][]string{
		"forwarded for": {"X-Forwarded-For", "203.0.113.9"}, "forwarded https": {"X-Forwarded-Proto", "https"}, "via a proxy": {"Via", "1.1 proxy"},
	} {
		if recorder := server.get(target, nil, headers...); recorder.Code != http.StatusUnauthorized || strings.Contains(recorder.Body.String(), "branches") {
			t.Errorf("%s = %d %s, want 401 with no data", name, recorder.Code, recorder.Body.String())
		}
		if recorder := server.get(target, server.login(), headers...); recorder.Code != 200 {
			t.Errorf("%s with a session = %d", name, recorder.Code)
		}
	}
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Host = "wb.example.test"
	recorder := httptest.NewRecorder()
	server.api.ServeHTTP(recorder, request)
	if recorder.Code == 200 || strings.Contains(recorder.Body.String(), `"branches"`) {
		t.Errorf("a non-loopback host got %d %s", recorder.Code, recorder.Body.String())
	}
}

// originReader is an IdentityCollector that holds an origin URL carrying a
// credential and gives back only the identity, as the production one does.
type originReader struct{ origin string }

func (o originReader) Identity(context.Context, discover.Repo) (string, error) {
	identity, _ := lifecyclehooks.IdentityFromOrigin(o.origin)
	return identity, nil
}

// TestRepositoriesCarryActivityAndAWebURLButNoOrigin proves
// cockpit-views#ac:repository-entries-carry-activity-and-web-url.
func TestRepositoriesCarryActivityAndAWebURLButNoOrigin(t *testing.T) {
	t.Parallel()
	newest := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	sources := oneRepoSources(t.TempDir())
	sources.repos[0] = discover.Repo{Host: "github.com", Org: "sneat-dev", Name: "wb", Path: sources.repos[0].Path}
	sources.worktrees = map[string][]LinkedWorktree{}
	sources.branches = map[string][]BranchRef{"sneat-dev/wb": {
		{Name: "old", Scope: BranchLocal, CommittedAt: newest.Add(-48 * time.Hour)},
		{Name: "new", Scope: BranchLocal, CommittedAt: newest},
		{Name: "origin/newer", Scope: BranchRemote, CommittedAt: newest.Add(time.Hour)},
	}}
	collectors := sources.collectors()
	collectors.Identity = originReader{origin: "https://someone:hunter2@github.com/sneat-dev/wb.git"}
	snapshotter, _ := newSnapshotter(collectors, nil)
	refreshAndSettle(t, snapshotter)
	bodyBytes, _ := snapshotter.Body()
	body := string(bodyBytes)
	local := localRepositoryOf(t, snapshotter)
	if !local.LastActivityAt.Equal(newest) || local.RemoteURLWeb != "https://github.com/sneat-dev/wb" || local.Host != "github.com" {
		t.Errorf("local repository = %+v", local)
	}
	for _, repository := range snapshotter.Document().Repositories {
		if repository.Route == RouteCached && (!repository.LastActivityAt.IsZero() || repository.RemoteURLWeb != "") {
			t.Errorf("cached repository = %+v", repository)
		}
	}
	for _, leaked := range []string{"hunter2", "someone", ".git\"", "@github.com"} {
		if strings.Contains(body, leaked) {
			t.Errorf("the document carries %q: %s", leaked, body)
		}
	}
}

// TestHostileHostsAndNamesHaveNoWebLink proves
// cockpit-views#ac:hostile-host-has-no-web-link.
func TestHostileHostsAndNamesHaveNoWebLink(t *testing.T) {
	t.Parallel()
	at := newClock().Now()
	for name, repo := range map[string]discover.Repo{
		"slash in the host":    {Host: "evil.example/x?y=1", Org: "o", Name: "r"},
		"space in the host":    {Host: "evil host", Org: "o", Name: "r"},
		"a port":               {Host: "evil.example:8080", Org: "o", Name: "r"},
		"a user":               {Host: "u@evil.example", Org: "o", Name: "r"},
		"a leading hyphen":     {Host: "-evil.example", Org: "o", Name: "r"},
		"a long host":          {Host: strings.Repeat("a", 250) + ".example", Org: "o", Name: "r"},
		"dot dot as the name":  {Host: "github.com", Org: "o", Name: ".."},
		"dot as the owner":     {Host: "github.com", Org: ".", Name: "r"},
		"a space in the name":  {Host: "github.com", Org: "o", Name: "a b"},
		"an escape in a name":  {Host: "github.com", Org: "o", Name: "x%2Fy"},
		"an empty host":        {Org: "o", Name: "r"},
		"a name with no owner": {Host: "github.com", Name: "r"},
	} {
		if link := mapLocalRepository(testMachine, repo, "", nil, nil, "", localCodeIndex{}, "", at).repository.RemoteURLWeb; link != "" {
			t.Errorf("%s: remote_url_web = %q", name, link)
		}
	}
	valid := discover.Repo{Host: "github.com", Org: "sneat-dev", Name: "wb"}
	if link := mapLocalRepository(testMachine, valid, "", nil, nil, "", localCodeIndex{}, "", at).repository.RemoteURLWeb; link != "https://github.com/sneat-dev/wb" {
		t.Errorf("valid repository: %q", link)
	}
	// A flat-layout clone takes its host from its origin, which must pass the
	// same rule.
	flat := discover.Repo{Org: "sneat-dev", Name: "wb"}
	if link := mapLocalRepository(testMachine, flat, "", nil, nil, "", localCodeIndex{}, "evil.example/x", at).repository.RemoteURLWeb; link != "" {
		t.Errorf("hostile origin host: %q", link)
	}
}

// TestOwnerStateFollowsTheOwnerProcessOnEveryRoute proves
// cockpit-views#ac:owner-state-mapping as the review resolved it: liveness, no
// heartbeat fallback, and a published value outside the vocabulary dropped.
func TestOwnerStateFollowsTheOwnerProcessOnEveryRoute(t *testing.T) {
	t.Parallel()
	at := newClock().Now()
	repo := discover.Repo{Host: "github.com", Org: "o", Name: "r"}
	recorded := []recordedWorktree{
		{linked: LinkedWorktree{Path: "/alive", Branch: "alive"}, record: WorktreeRecord{Task: "alive", Owner: worktrees.OwnerLive, HeartbeatAt: at.Add(-72 * time.Hour)}},
		{linked: LinkedWorktree{Path: "/dead", Branch: "dead"}, record: WorktreeRecord{Task: "dead", Owner: worktrees.OwnerGone, HeartbeatAt: at}},
		{linked: LinkedWorktree{Path: "/none", Branch: "none"}, record: WorktreeRecord{Task: "none", Owner: worktrees.OwnerUnstated, HeartbeatAt: at}},
		{linked: LinkedWorktree{Path: "/blank", Branch: "blank"}, record: WorktreeRecord{Task: "blank"}},
	}
	got := map[string]string{}
	for _, worktree := range mapLocalRepository(testMachine, repo, "", recorded, nil, "", localCodeIndex{}, "", at).worktrees {
		got[worktree.Task] = worktree.OwnerState
	}
	if got["alive"] != OwnerActive || got["dead"] != OwnerOrphaned || got["none"] != OwnerUnknown || got["blank"] != OwnerUnknown {
		t.Errorf("local owner states = %v", got)
	}
	published := remotestate.Snapshot{Login: "a", Machine: "desk", PublishedAt: remotePublishedAt(), Worktrees: []remotestate.WorktreeState{
		{Task: "unknown", Repository: "o/r", Branch: "u", OwnerState: "unknown"},
		{Task: "idle", Repository: "o/r", Branch: "i", OwnerState: "idle"},
		{Task: "hostile", Repository: "o/r", Branch: "h", OwnerState: "<script>"},
		{Task: "empty", Repository: "o/r", Branch: "e"},
	}}
	cached := map[string]string{}
	for _, worktree := range mapRemoteForTest("", "", []remotestate.Entry{{Snapshot: published}}).worktrees {
		cached[worktree.Task] = worktree.OwnerState
	}
	if cached["unknown"] != OwnerUnknown || cached["idle"] != OwnerIdle || cached["hostile"] != "" || cached["empty"] != "" {
		t.Errorf("cached owner states = %v", cached)
	}
}

// TestWorktreesCarryTheirNameAndSyncFacts proves
// cockpit-views#ac:worktree-entries-carry-name-and-sync.
func TestWorktreesCarryTheirNameAndSyncFacts(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources(t.TempDir())
	now := newClock().Now()
	sources.worktrees["acme/widgets"] = []LinkedWorktree{
		{Path: "/wt/ahead", Branch: "ahead"}, {Path: "/wt/gone", Branch: "gone"}, {Path: "/wt/bare", Branch: "bare"}, {Path: "/wt/detached"},
	}
	sources.records = map[string]WorktreeRecord{
		"/wt/ahead":    {Task: "task-ahead", Branch: "ahead", CreatedAt: now},
		"/wt/gone":     {Task: "task-gone", Branch: "gone", CreatedAt: now},
		"/wt/bare":     {Task: "task-bare", Branch: "bare", CreatedAt: now},
		"/wt/detached": {Task: "task-detached", Branch: "elsewhere", CreatedAt: now},
	}
	sources.branches["acme/widgets"] = []BranchRef{
		{Name: "ahead", Scope: BranchLocal, Upstream: "origin/ahead", Ahead: 2, Behind: 1},
		{Name: "gone", Scope: BranchLocal, Upstream: "origin/gone", UpstreamGone: true},
		{Name: "bare", Scope: BranchLocal},
	}
	snapshotter, _ := newSnapshotter(sources.collectors(), nil)
	refreshAndSettle(t, snapshotter)
	byTask := map[string]Worktree{}
	for _, worktree := range snapshotter.Document().Worktrees {
		byTask[worktree.Task] = worktree
		if worktree.Name != worktree.Task || strings.ContainsAny(worktree.Name, `/\`) {
			t.Errorf("worktree %+v: name must be its task, with no separator", worktree)
		}
	}
	ahead, gone, bare, detached, cached := byTask["task-ahead"], byTask["task-gone"], byTask["task-bare"], byTask["task-detached"], byTask["task-x"]
	if ahead.Ahead == nil || *ahead.Ahead != 2 || ahead.Behind == nil || *ahead.Behind != 1 || ahead.HasUpstream == nil || !*ahead.HasUpstream || ahead.UpstreamGone {
		t.Errorf("ahead = %+v", ahead)
	}
	if !gone.UpstreamGone || gone.Ahead != nil || gone.Behind != nil || gone.HasUpstream == nil || !*gone.HasUpstream {
		t.Errorf("gone = %+v", gone)
	}
	if bare.Ahead != nil || bare.Behind != nil || bare.UpstreamGone || bare.HasUpstream == nil || *bare.HasUpstream {
		t.Errorf("bare = %+v, want no counts and has_upstream false", bare)
	}
	if detached.Ahead != nil || detached.Behind != nil || detached.UpstreamGone || detached.HasUpstream != nil {
		t.Errorf("a branch with no ref = %+v, want none of the sync facts", detached)
	}
	if cached.Route != RouteCached || cached.Ahead != nil || cached.Behind != nil || cached.UpstreamGone || cached.HasUpstream != nil || cached.Name != "task-x" {
		t.Errorf("cached = %+v", cached)
	}
}

// TestMachinesCarryHardwareAndNoMetrics proves
// cockpit-views#ac:machine-entries-carry-hardware-and-no-metrics.
func TestMachinesCarryHardwareAndNoMetrics(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources(t.TempDir())
	booted := time.Date(2026, 9, 20, 7, 30, 0, 0, time.UTC)
	sources.remote = []remotestate.Entry{
		{Snapshot: remotestate.Snapshot{Login: "a", Machine: "with", PublishedAt: remotePublishedAt(), OS: "linux", Arch: "amd64", CPUCount: 16, BootTime: booted}},
		{Snapshot: remotestate.Snapshot{Login: "a", Machine: "without", PublishedAt: remotePublishedAt()}},
		{Snapshot: remotestate.Snapshot{Login: "a", Machine: "odd", PublishedAt: remotePublishedAt(), OS: "li nux/../", Arch: strings.Repeat("x", 40), CPUCount: 1 << 30}},
	}
	snapshotter, _ := newSnapshotter(sources.collectors(), func(options *Options) {
		options.Hardware = Hardware{OS: "darwin", Arch: "arm64", CPUCount: 10, BootTime: booted.Add(time.Hour)}
	})
	refreshAndSettle(t, snapshotter)
	machines := map[string]Machine{}
	for _, machine := range snapshotter.Document().Machines {
		machines[machine.Machine] = machine
	}
	local, with := machines[testMachine], machines["with"]
	if local.OS != "darwin" || local.Arch != "arm64" || local.CPUCount != 10 || !local.BootTime.Equal(booted.Add(time.Hour)) {
		t.Errorf("local = %+v", local)
	}
	if with.OS != "linux" || with.Arch != "amd64" || with.CPUCount != 16 || !with.BootTime.Equal(booted) {
		t.Errorf("with hardware = %+v", with)
	}
	for _, name := range []string{"without", "odd"} {
		if machine := machines[name]; machine.OS != "" || machine.Arch != "" || machine.CPUCount != 0 || !machine.BootTime.IsZero() {
			t.Errorf("%s = %+v, want no hardware", name, machine)
		}
	}
	body, _ := snapshotter.Body()
	if strings.Contains(string(body), "metrics") {
		t.Error("the document carries metrics")
	}
	var bare Machine
	if data, _ := json.Marshal(bare); strings.Contains(string(data), "boot_time") || strings.Contains(string(data), "cpu_count") {
		t.Errorf("an empty machine marshals hardware: %s", data)
	}
}

// TestDocumentCarriesTheRefreshInterval proves
// cockpit-views#ac:document-carries-refresh-interval, also before the first
// snapshot.
func TestDocumentCarriesTheRefreshInterval(t *testing.T) {
	t.Parallel()
	snapshotter, _ := newSnapshotter(oneRepoSources(t.TempDir()).collectors(), func(options *Options) { options.Interval = 45 * time.Second })
	if document := snapshotter.Document(); document.RefreshIntervalSeconds != 45 {
		t.Errorf("warming up: refresh_interval_seconds = %d", document.RefreshIntervalSeconds)
	}
	refreshAndSettle(t, snapshotter)
	if document := newCockpitServer(t, snapshotter).fleet(); document.RefreshIntervalSeconds != 45 {
		t.Errorf("refresh_interval_seconds = %d, want 45", document.RefreshIntervalSeconds)
	}
}

// TestRemoteEntriesAreEnumOrOmittedAndForgeNamesSplit covers the untrusted
// strings of another machine's snapshot and the host split of
// cockpit-views#req:repository-identity.
func TestRemoteEntriesAreEnumOrOmittedAndForgeNamesSplit(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("é", 300)
	view := mapRemoteForTest("", "", []remotestate.Entry{{Snapshot: remotestate.Snapshot{
		Login: "a", Machine: "desk", PublishedAt: remotePublishedAt(), WBVersion: "v1\x00\u202e.2",
		KnownRepositories: []string{"github.com/Sneat-Co/sneat-go", "acme/widgets", "gitlab.com/group/sub/proj", "a.b/c", "github.com//x/y", "1.2/a/b"},
		Worktrees: []remotestate.WorktreeState{
			{Task: "t\u202ex", Stream: long, Repository: "github.com/Sneat-Co/sneat-go", Branch: "b\x01", Lifecycle: "review", PullRequest: &remotestate.PullRequestState{Number: 1, State: "OPEN", URL: "https://github.com/Sneat-Co/sneat-go/pull/1"}},
			{Task: "u", Repository: "acme/widgets", Branch: "u", Lifecycle: "<img>", PullRequest: &remotestate.PullRequestState{Number: 2, State: "open", URL: "javascript:alert(1)"}},
			{Task: "v", Repository: "acme/widgets", Branch: "v", PullRequest: &remotestate.PullRequestState{Number: 3, State: "open", URL: "https://user@github.com/x"}},
			{Task: "w", Repository: "acme/widgets", Branch: "w", PullRequest: &remotestate.PullRequestState{Number: 4, State: "open", URL: "https://github.com:8443/x"}},
			{Task: "x", Repository: "acme/widgets", Branch: "x", PullRequest: &remotestate.PullRequestState{Number: 5, State: "open", URL: "https://evil.example/a b"}},
			{Task: "y", Repository: "acme/widgets", Branch: "y", PullRequest: &remotestate.PullRequestState{Number: 6, State: "open", URL: "https://%zz/"}},
			{Task: "z", Repository: "acme/widgets", Branch: "z", PullRequest: &remotestate.PullRequestState{Number: 7, State: "merged", URL: "https://github.com/z"}},
			{Task: "ip", Repository: "acme/widgets", Branch: "ip", PullRequest: &remotestate.PullRequestState{Number: 10, State: "open", URL: "https://127.0.0.1/x"}},
			{Task: "lh", Repository: "acme/widgets", Branch: "lh", PullRequest: &remotestate.PullRequestState{Number: 11, State: "open", URL: "https://localhost/x"}},
			{Task: "lh2", Repository: "acme/widgets", Branch: "lh2", PullRequest: &remotestate.PullRequestState{Number: 12, State: "open", URL: "https://a.localhost/x"}},
			{Task: "num", Repository: "acme/widgets", Branch: "num", PullRequest: &remotestate.PullRequestState{Number: 13, State: "open", URL: "https://1.2/x"}},
			{Task: "long", Repository: "acme/widgets", Branch: "long", PullRequest: &remotestate.PullRequestState{Number: 14, State: "open", URL: "https://github.com/" + strings.Repeat("a", 2100)}},
			{Task: "upper", Repository: "acme/widgets", Branch: "upper", PullRequest: &remotestate.PullRequestState{Number: 15, State: "open", URL: "HTTPS://github.com/u"}},
		},
	}}, {Snapshot: remotestate.Snapshot{
		Login: "a", Machine: "hos\u2028tile\x00" + strings.Repeat("m", 1<<20), PublishedAt: remotePublishedAt(),
		BootTime: time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC), OS: "linux",
	}}, {Snapshot: remotestate.Snapshot{Login: "a", Machine: "\u202e\x00", PublishedAt: remotePublishedAt()}},
		{Snapshot: remotestate.Snapshot{Login: "a", Machine: "future", PublishedAt: remotePublishedAt(), BootTime: newClock().Now().Add(time.Hour)}},
	})
	names := map[string]Repository{}
	for _, repository := range view.repositories {
		names[repository.Name] = repository
	}
	if repository := names["Sneat-Co/sneat-go"]; repository.Host != "github.com" || repository.RemoteURLWeb != "" {
		t.Errorf("split forge name = %+v", repository)
	}
	if repository := names["acme/widgets"]; repository.Host != "" {
		t.Errorf("a name with no host = %+v", repository)
	}
	if repository := names["group/sub/proj"]; repository.Host != "gitlab.com" {
		t.Errorf("nested name = %+v", names)
	}
	for _, name := range []string{"github.com//x/y", "1.2/a/b"} {
		if repository, found := names[name]; !found || repository.Host != "" {
			t.Errorf("%q must keep its whole name and have no host: %+v", name, names)
		}
	}
	if repository := names["a.b/c"]; repository.Host != "" {
		t.Errorf("two segments with a dot = %+v", repository)
	}
	// A version that is not one by the export decoder's rule is dropped, not repaired.
	if len(view.machines) != 3 || view.machines[0].WBVersion != "" {
		t.Fatalf("machines = %+v, want the hostile-named one kept (sanitised), the empty-named one skipped", view.machines)
	}
	for _, machine := range view.machines {
		if len([]rune(machine.Machine)) > maxRemoteText || strings.ContainsAny(machine.Machine, "\x00\u2028") || machine.Machine == "" || !machine.BootTime.IsZero() {
			t.Errorf("machine %q boot %v: name not sanitised or boot time out of range", machine.Machine[:min(20, len(machine.Machine))], machine.BootTime)
		}
	}
	byTask := map[string]Worktree{}
	for _, worktree := range view.worktrees {
		byTask[worktree.Task] = worktree
	}
	if first := byTask["tx"]; first.Name != "tx" || first.Branch != "b" || first.Lifecycle != "review" {
		t.Errorf("sanitised worktree = %+v", first)
	}
	if len([]rune(byTask["tx"].Stream)) != maxRemoteText || byTask["u"].Lifecycle != "" {
		t.Errorf("stream %d runes, lifecycle %q", len([]rune(byTask["tx"].Stream)), byTask["u"].Lifecycle)
	}
	urls := map[int]PullRequest{}
	for _, pull := range view.pullRequests {
		urls[pull.Number] = pull
	}
	if urls[1].URL != "https://github.com/Sneat-Co/sneat-go/pull/1" || urls[1].State != "open" || urls[15].URL != "https://github.com/u" || len(urls) != 12 {
		t.Errorf("pull requests = %+v", urls)
	}
	for _, number := range []int{2, 3, 4, 5, 6, 10, 11, 12, 13, 14} {
		if urls[number].URL != "" {
			t.Errorf("pull request %d kept the unsafe URL %q", number, urls[number].URL)
		}
	}
}

// sizedProvider is a code-index provider that reports the same realistic
// statistics for every checkout.
type sizedProvider struct{}

func (sizedProvider) Name() string    { return "codegrapher" }
func (sizedProvider) Indexer() string { return "index" }
func (sizedProvider) Statistics(_ context.Context, checkout string) (ProviderStatistics, error) {
	size := len(checkout)
	return ProviderStatistics{Indexed: true, Files: 100 + size*7, Symbols: 5000 + size*131, Edges: 12000 + size*377, Kinds: map[string]int{
		"function": 1900 + size, "method": 2200 + size, "struct": 310, "interface": 64, "const": 480, "var": 120, "type": 90, "test": 1500,
	}}, nil
}

// TestFleetDocumentFitsTheBudget proves cockpit-views#ac:fleet-document-fits-
// the-budget on the fixture of 500 repositories, 600 worktrees, 4,000 branches
// and 3 machines, with realistic names, code-index statistics, pull requests,
// agents and local and cached entries, stored through the snapshotter and
// requested with gzip.
func TestFleetDocumentFitsTheBudget(t *testing.T) {
	t.Parallel()
	orgs := []string{"sneat-dev", "sneat-co", "strongo", "ingitdb", "dal-go", "bots-go-framework", "datatug", "chatwright"}
	words := []string{"core", "api", "cli", "web", "docs", "bot", "sync", "store", "auth", "ui", "worker", "model", "spec", "kit", "hub"}
	now := newClock().Now()
	sources := &fakeSources{
		branch: "main", worktrees: map[string][]LinkedWorktree{}, records: map[string]WorktreeRecord{}, branches: map[string][]BranchRef{},
	}
	var slugs []string
	for number := range 500 {
		repo := discover.Repo{Host: "github.com", Org: orgs[number%len(orgs)], Name: fmt.Sprintf("%s-%s-%d", words[number%len(words)], words[(number*7+3)%len(words)], number), Path: fmt.Sprintf("/p/%d", number)}
		sources.repos = append(sources.repos, repo)
		slugs = append(slugs, repo.Slug())
		for index := range 8 {
			scope, name := BranchLocal, fmt.Sprintf("task/%s-%d-%d", words[(number+index)%len(words)], number, index)
			if index >= 5 {
				scope, name = BranchRemote, "origin/"+name
			}
			sources.branches[repo.Slug()] = append(sources.branches[repo.Slug()], BranchRef{
				Name: name, Scope: scope, Upstream: "origin/main", Ahead: index % 3, Behind: index % 2, CommittedAt: now.Add(-time.Duration(number+index) * time.Hour),
			})
		}
		for index := range 1 + number/(500-100) { // 600 worktrees: 100 repositories have two
			task := fmt.Sprintf("%s-%s-%d-%d", words[number%len(words)], words[(number+5)%len(words)], number, index)
			path := fmt.Sprintf("/wt/%d-%d", number, index)
			branch := fmt.Sprintf("task/%s-%d-%d", words[(number+index)%len(words)], number, index)
			sources.worktrees[repo.Slug()] = append(sources.worktrees[repo.Slug()], LinkedWorktree{Path: path, Branch: branch})
			sources.records[path] = WorktreeRecord{Task: task, Branch: branch, CreatedAt: now.Add(-48 * time.Hour), HeartbeatAt: now.Add(-time.Hour), Owner: "live"}
			if number%4 == 0 {
				sources.bindings = append(sources.bindings, worktrees.RegisteredPullRequestBinding{
					Task: task, Repository: repo.Slug(), PullRequest: 100 + number, URL: fmt.Sprintf("https://github.com/%s/pull/%d", repo.Slug(), 100+number),
				})
			}
		}
	}
	for number := range 40 {
		sources.sessions = append(sources.sessions, session.View{Record: session.Record{WBSessionID: fmt.Sprintf("wbs-%d", number), Runtime: "claude", Model: "opus"}, State: session.StateLive})
	}
	for number := range 100 {
		run := agents.Result{AgentID: fmt.Sprintf("agt-%d", number), State: agents.StateRunning, Repository: slugs[number], StartedAt: now}
		run.Resolved.Harness, run.Resolved.Model = "codex", "gpt-5"
		sources.runs = append(sources.runs, run)
	}
	for _, name := range []string{"desktop-linux", "hetzner-vm"} {
		snapshot := remotestate.Snapshot{
			Login: "alex", Machine: name, PublishedAt: now.Add(-time.Hour), WBVersion: "v0.31.4", OS: "linux", Arch: "amd64", CPUCount: 8, BootTime: now.Add(-72 * time.Hour),
		}
		for number := range 100 {
			repository := fmt.Sprintf("github.com/%s/%s-%d", orgs[number%len(orgs)], words[number%len(words)], number)
			snapshot.KnownRepositories = append(snapshot.KnownRepositories, repository)
			snapshot.Worktrees = append(snapshot.Worktrees, remotestate.WorktreeState{
				Task: fmt.Sprintf("remote-%s-%d", words[number%len(words)], number), Stream: "stream-" + words[number%len(words)], Repository: repository,
				Branch: fmt.Sprintf("task/remote-%d", number), Lifecycle: "review", OwnerState: "active", LastActivityAt: now,
				PullRequest: &remotestate.PullRequestState{Number: number, State: "OPEN", URL: fmt.Sprintf("https://github.com/%s/pull/%d", repository, number)},
			})
		}
		sources.remote = append(sources.remote, remotestate.Entry{Snapshot: snapshot})
	}
	collectors := sources.collectors()
	receipt := now.Add(-time.Hour)
	collectors.CodeIndex = &fakeCodeIndex{states: func(_ string, checkouts []string) map[string][]CodeIndex {
		states := map[string][]CodeIndex{}
		for _, checkout := range checkouts {
			states[checkout] = []CodeIndex{{Indexer: "index", State: CodeIndexFresh, ReceiptAt: receipt}}
		}
		return states
	}}
	collectors.CodeIndexProvider = sizedProvider{}
	snapshotter, _ := newSnapshotter(collectors, func(options *Options) {
		options.Fingerprint = newConstFingerprint("one").get
		options.Hardware = Hardware{OS: "darwin", Arch: "arm64", CPUCount: 10, BootTime: now.Add(-24 * time.Hour)}
	})
	refreshAndSettle(t, snapshotter)
	document := snapshotter.Document()
	if len(document.Repositories) != 500+200 || len(document.Worktrees) != 600+200 || len(document.Machines) != 3 || len(snapshotter.allBranches()) != 4000 ||
		len(document.PullRequests) < 100 || len(document.Agents) != 140 {
		t.Fatalf("fixture = %d repositories, %d worktrees, %d machines, %d branches, %d pull requests, %d agents",
			len(document.Repositories), len(document.Worktrees), len(document.Machines), len(snapshotter.allBranches()), len(document.PullRequests), len(document.Agents))
	}
	withStatistics := 0
	for _, repository := range document.Repositories {
		for _, index := range repository.CodeIndex {
			if index.Statistics != nil && index.Statistics.Indexed {
				withStatistics++
			}
		}
	}
	if withStatistics != 500 {
		t.Fatalf("%d repositories carry code-index statistics, want 500", withStatistics)
	}
	recorder := newCockpitServer(t, snapshotter).get(fleetURL, nil, "Accept-Encoding", "gzip")
	body, identity := recorder.Body.Len(), len(gunzip(t, recorder.Body.Bytes()))
	t.Logf("fleet document: %d bytes identity, %d bytes gzip", identity, body)
	if recorder.Code != 200 || body > 150*1000 {
		t.Errorf("status %d, the gzip body is %d bytes, want at most 150 kB", recorder.Code, body)
	}
}

// isBranchList tells a branch list's body from the fleet document's.
func isBranchList(data []byte) bool { return bytes.HasPrefix(data, []byte(`{"repository"`)) }

// TestPollingBranchesAcrossUnchangedPassesRecompressesNothing proves the
// prepared branch list outlives a scan that leaves the branches as they were,
// and is rebuilt when they change.
func TestPollingBranchesAcrossUnchangedPassesRecompressesNothing(t *testing.T) {
	t.Parallel()
	var lists atomic.Int64
	sources := oneRepoSources(t.TempDir())
	snapshotter, clock := newSnapshotter(sources.collectors(), func(options *Options) {
		options.Fingerprint = newConstFingerprint("one").get
		options.Compress = func(data []byte) []byte {
			if isBranchList(data) {
				lists.Add(1)
			}
			return cockpit.Gzip(data)
		}
	})
	refreshAndSettle(t, snapshotter)
	server := newCockpitServer(t, snapshotter)
	id := localRepositoryOf(t, snapshotter).ID
	for range 3 {
		server.get(branchesURL+id, nil, "Accept-Encoding", "gzip")
		clock.advance(time.Minute) // a later observation time, the same branches
		refreshAndSettle(t, snapshotter)
	}
	if lists.Load() != 1 {
		t.Errorf("the branch list was compressed %d times across 3 passes with unchanged branches, want 1", lists.Load())
	}
	sources.change(func(f *fakeSources) { f.branches["acme/widgets"] = f.branches["acme/widgets"][:1] })
	if err := snapshotter.RefreshRepository(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	server.get(branchesURL+id, nil)
	server.get(branchesURL+id, nil)
	if lists.Load() != 2 {
		t.Errorf("after the branches changed the list was compressed %d times in all, want 2", lists.Load())
	}
}

// TestASlowBranchBuildDoesNotBlockTheFleetDocument proves a branch list is
// built outside the snapshotter's lock.
func TestASlowBranchBuildDoesNotBlockTheFleetDocument(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	snapshotter, _ := newSnapshotter(oneRepoSources(t.TempDir()).collectors(), func(options *Options) {
		options.Compress = func(data []byte) []byte {
			if isBranchList(data) {
				close(entered)
				<-release
			}
			return cockpit.Gzip(data)
		}
	})
	refreshAndSettle(t, snapshotter)
	id := localRepositoryOf(t, snapshotter).ID
	answered := make(chan bool)
	go func() {
		_, found := snapshotter.Branches(id)
		answered <- found
	}()
	<-entered
	served := make(chan struct{})
	go func() {
		snapshotter.Payload()
		close(served)
	}()
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Error("Payload was blocked by a branch build")
	}
	close(release)
	if !<-answered {
		t.Error("the branches were not found")
	}
}

// TestBranchesBuiltWhileAScanLandsAreStillServed covers a lost race: a scan
// stores newer entries while a list is being built.
func TestBranchesBuiltWhileAScanLandsAreStillServed(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	sources := oneRepoSources(t.TempDir())
	snapshotter, _ := newSnapshotter(sources.collectors(), func(options *Options) {
		options.Compress = func(data []byte) []byte {
			if isBranchList(data) {
				close(entered)
				<-release
			}
			return cockpit.Gzip(data)
		}
	})
	refreshAndSettle(t, snapshotter)
	id := localRepositoryOf(t, snapshotter).ID
	answered := make(chan branchAnswer)
	go func() {
		payload, found := snapshotter.Branches(id)
		answered <- branchAnswer{payload, found}
	}()
	<-entered
	if err := snapshotter.RefreshRepository(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	close(release)
	if result := <-answered; !result.found {
		t.Error("the list was not served")
	}
	if snapshotter.repos[id].branches != nil {
		t.Error("a list built from superseded entries was stored")
	}
}

// branchAnswer is a branch list and whether it was found.
type branchAnswer struct {
	payload cockpit.Payload
	found   bool
}
