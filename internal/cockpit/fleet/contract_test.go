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

	"github.com/sneat-dev/wb/internal/cockpit"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/lifecyclehooks"
	"github.com/sneat-dev/wb/internal/remotestate"
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

// TestBranchesAreTheirOwnMetadataRouteForTheHostedOrigin proves
// cockpit-views#ac:hosted-origin-can-revalidate for the branches route.
func TestBranchesAreTheirOwnMetadataRouteForTheHostedOrigin(t *testing.T) {
	t.Parallel()
	snapshotter, _ := newSnapshotter(oneRepoSources(t.TempDir()).collectors(), nil)
	refreshAndSettle(t, snapshotter)
	server := newCockpitServer(t, snapshotter)
	target := branchesURL + localRepositoryOf(t, snapshotter).ID
	preflight := httptest.NewRequest(http.MethodOptions, target, nil)
	preflight.Host = testHost
	preflight.Header.Set("Origin", hostedOrigin)
	preflight.Header.Set("Access-Control-Request-Method", "GET")
	preflight.Header.Set("Access-Control-Request-Headers", "If-None-Match")
	recorder := httptest.NewRecorder()
	server.api.ServeHTTP(recorder, preflight)
	if recorder.Code != http.StatusNoContent || recorder.Header().Get("Access-Control-Allow-Headers") != "if-none-match" || recorder.Header().Get("Access-Control-Allow-Origin") != hostedOrigin {
		t.Fatalf("preflight = %d %v", recorder.Code, recorder.Header())
	}
	first := server.get(target, nil, "Origin", hostedOrigin)
	if first.Code != 200 || first.Header().Get("Access-Control-Expose-Headers") != "ETag" || first.Header().Get("Access-Control-Allow-Origin") != hostedOrigin || first.Header().Get("ETag") == "" {
		t.Fatalf("hosted response = %d %v", first.Code, first.Header())
	}
	repeat := server.get(target, nil, "Origin", hostedOrigin, "If-None-Match", first.Header().Get("ETag"))
	if repeat.Code != http.StatusNotModified || repeat.Header().Get("Access-Control-Allow-Origin") != hostedOrigin {
		t.Errorf("repeat = %d %v", repeat.Code, repeat.Header())
	}
	if foreign := server.get(target, nil, "Origin", "https://elsewhere.example"); foreign.Code != http.StatusForbidden {
		t.Errorf("another origin = %d, want 403", foreign.Code)
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
	for _, worktree := range mapRemote(testMachine, "", "", []remotestate.Entry{{Snapshot: published}}).worktrees {
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
	view := mapRemote(testMachine, "", "", []remotestate.Entry{{Snapshot: remotestate.Snapshot{
		Login: "a", Machine: "desk", PublishedAt: remotePublishedAt(), WBVersion: "v1\x00\u202e.2",
		KnownRepositories: []string{"github.com/Sneat-Co/sneat-go", "acme/widgets", "gitlab.com/group/sub/proj", "a.b/c"},
		Worktrees: []remotestate.WorktreeState{
			{Task: "t\u202ex", Stream: long, Repository: "github.com/Sneat-Co/sneat-go", Branch: "b\x01", Lifecycle: "review", PullRequest: &remotestate.PullRequestState{Number: 1, State: "OPEN", URL: "https://github.com/Sneat-Co/sneat-go/pull/1"}},
			{Task: "u", Repository: "acme/widgets", Branch: "u", Lifecycle: "<img>", PullRequest: &remotestate.PullRequestState{Number: 2, State: "open", URL: "javascript:alert(1)"}},
			{Task: "v", Repository: "acme/widgets", Branch: "v", PullRequest: &remotestate.PullRequestState{Number: 3, State: "open", URL: "https://user@github.com/x"}},
			{Task: "w", Repository: "acme/widgets", Branch: "w", PullRequest: &remotestate.PullRequestState{Number: 4, State: "open", URL: "https://github.com:8443/x"}},
			{Task: "x", Repository: "acme/widgets", Branch: "x", PullRequest: &remotestate.PullRequestState{Number: 5, State: "open", URL: "https://evil.example/a b"}},
			{Task: "y", Repository: "acme/widgets", Branch: "y", PullRequest: &remotestate.PullRequestState{Number: 6, State: "open", URL: "https://%zz/"}},
			{Task: "z", Repository: "acme/widgets", Branch: "z", PullRequest: &remotestate.PullRequestState{Number: 7, State: "merged", URL: "https://github.com/z"}},
		},
	}}})
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
	if repository := names["a.b/c"]; repository.Host != "" {
		t.Errorf("two segments with a dot = %+v", repository)
	}
	if len(view.machines) != 1 || view.machines[0].WBVersion != "v1.2" {
		t.Errorf("version = %+v", view.machines)
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
	if urls[1].URL != "https://github.com/Sneat-Co/sneat-go/pull/1" || urls[1].State != "open" || len(urls) != 6 {
		t.Errorf("pull requests = %+v", urls)
	}
	for number := 2; number <= 6; number++ {
		if urls[number].URL != "" {
			t.Errorf("pull request %d kept the unsafe URL %q", number, urls[number].URL)
		}
	}
}

// TestFleetDocumentFitsTheBudget proves cockpit-views#ac:fleet-document-fits-
// the-budget on the fixture of 500 repositories, 600 worktrees, 4,000 branches
// and 3 machines, with realistic names and code-index statistics.
func TestFleetDocumentFitsTheBudget(t *testing.T) {
	t.Parallel()
	at := newClock().Now()
	document := emptyDocument(time.Minute)
	document.SnapshotAt, document.WarmingUp, document.CodeIndexProvider = at, false, "codegrapher"
	machines := []string{"mbp-alex", "hetzner-vm", "desktop-linux"}
	for _, name := range machines {
		route := RouteCached
		if name == machines[0] {
			route = RouteLocal
		}
		document.Machines = append(document.Machines, Machine{
			Entry:     Entry{ID: entryID(kindMachine, name), Machine: name, MachineID: entryID(kindMachine, name), Route: route, ObservedAt: at},
			WBVersion: "v0.31.4", RepositoryCount: 170, WorktreeCount: 200, OS: "linux", Arch: "arm64", CPUCount: 8, BootTime: at,
		})
	}
	orgs := []string{"sneat-dev", "sneat-co", "strongo", "ingitdb", "dal-go", "bots-go-framework", "datatug", "chatwright"}
	words := []string{"core", "api", "cli", "web", "docs", "bot", "sync", "store", "auth", "ui", "worker", "model", "spec", "kit", "hub"}
	kinds := []KindCount{{"function", 1900}, {"method", 2200}, {"struct", 310}, {"interface", 64}, {"const", 480}, {"var", 120}, {"type", 90}, {"test", 1500}}
	index := func(seed int) []CodeIndex {
		statistics := &CodeStatistics{Indexed: true, Files: 100 + seed%900, Symbols: 5000 + seed*13%40000, Edges: 12000 + seed*37%90000, Kinds: kinds}
		return []CodeIndex{{Indexer: "codegrapher", State: CodeIndexFresh, ReceiptAt: at.Add(-time.Duration(seed) * time.Minute), Statistics: statistics}}
	}
	for number := range 500 {
		machine := machines[number%len(machines)]
		name := fmt.Sprintf("%s/%s-%s-%d", orgs[number%len(orgs)], words[number%len(words)], words[(number*7+3)%len(words)], number)
		local, remoteBranches, open, active := 5, 3, 1, 0
		entry := Entry{ID: entryID(kindRepository, machine, name), Machine: machine, MachineID: entryID(kindMachine, machine), Route: RouteLocal, ObservedAt: at}
		document.Repositories = append(document.Repositories, Repository{
			Entry: entry, Host: "github.com", Name: name, DefaultBranch: "main", WorktreeCount: 1, LocalBranchCount: &local, RemoteBranchCount: &remoteBranches,
			OpenPullRequestCount: &open, ActiveAgentCount: &active, LastActivityAt: at.Add(-time.Duration(number) * time.Hour),
			RemoteURLWeb: "https://github.com/" + name, CodeIndex: index(number),
		})
		if number < 500 {
			ahead, behind, upstream := number%4, number%3, true
			document.Worktrees = append(document.Worktrees, Worktree{
				Entry:      Entry{ID: entryID(kindWorktree, machine, name, "task", fmt.Sprint(number)), Machine: machine, MachineID: entry.MachineID, Route: RouteLocal, ObservedAt: at},
				Repository: entry.ID, Name: fmt.Sprintf("%s-%s-%d", words[number%len(words)], words[(number+5)%len(words)], number), Task: fmt.Sprintf("%s-%s-%d", words[number%len(words)], words[(number+5)%len(words)], number),
				Stream: "stream-" + words[number%len(words)], Branch: fmt.Sprintf("task/%s-%d", words[number%len(words)], number), Lifecycle: "working", OwnerState: OwnerActive,
				LastActivityAt: at.Add(-time.Duration(number) * time.Minute), Ahead: &ahead, Behind: &behind, HasUpstream: &upstream, CodeIndex: index(number + 1),
			})
		}
	}
	for number := range 100 {
		machine := machines[1+number%2]
		name := fmt.Sprintf("%s/%s-%d", orgs[number%len(orgs)], words[number%len(words)], number)
		document.Worktrees = append(document.Worktrees, Worktree{
			Entry:      Entry{ID: entryID(kindWorktree, machine, name, "cached"), Machine: machine, MachineID: entryID(kindMachine, machine), Route: RouteCached, ObservedAt: at},
			Repository: entryID(kindRepository, machine, name), Name: "cached-" + words[number%len(words)], Task: "cached-" + words[number%len(words)], Branch: "task/" + words[number%len(words)], Lifecycle: "review", OwnerState: OwnerActive, LastActivityAt: at,
		})
	}
	body, _ := json.Marshal(document)
	payload := cockpit.NewPayload(body, cockpit.Gzip)
	request := httptest.NewRequest(http.MethodGet, fleetURL, nil)
	request.Header.Set("Accept-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	cockpit.ServePayload(recorder, request, payload)
	if len(document.Repositories) != 500 || len(document.Worktrees) != 600 || len(document.Machines) != 3 {
		t.Fatalf("fixture = %d repositories, %d worktrees, %d machines", len(document.Repositories), len(document.Worktrees), len(document.Machines))
	}
	t.Logf("fleet document: %d bytes identity, %d bytes gzip", len(body), recorder.Body.Len())
	if recorder.Body.Len() > 150*1000 {
		t.Errorf("the gzip body is %d bytes, want at most 150 kB", recorder.Body.Len())
	}
}

// TestHostileHostsAndNamesHaveNoWebLinkInTheDocument is the document-level half
// of cockpit-views#ac:hostile-host-has-no-web-link: five repositories on the
// real mapping path, and only the valid one carries remote_url_web.
func TestHostileHostsAndNamesHaveNoWebLinkInTheDocument(t *testing.T) {
	t.Parallel()
	repos := []discover.Repo{
		{Host: "evil.example/x?y=1", Org: "o", Name: "one", Path: "/p/1"},
		{Host: "evil host", Org: "o", Name: "two", Path: "/p/2"},
		{Host: "github.com", Org: "o", Name: "..", Path: "/p/3"},
		{Host: "github.com", Org: "o", Name: "a b", Path: "/p/4"},
		{Host: "github.com", Org: "o", Name: "x%2Fy", Path: "/p/5"},
		{Host: "github.com", Org: "sneat-dev", Name: "wb", Path: "/p/6"},
	}
	snapshotter, _ := newSnapshotter((&fakeSources{repos: repos}).collectors(), nil)
	refreshAndSettle(t, snapshotter)
	linked := map[string]string{}
	for _, repository := range snapshotter.Document().Repositories {
		if repository.RemoteURLWeb != "" {
			linked[repository.Name] = repository.RemoteURLWeb
		}
	}
	if len(linked) != 1 || linked["sneat-dev/wb"] != "https://github.com/sneat-dev/wb" {
		t.Errorf("repositories with a web link = %v, want only sneat-dev/wb", linked)
	}
}

// TestOwnerStateIsProbedOncePerWorktreePerSnapshot proves the document half of
// cockpit-views#ac:owner-state-mapping: a probe per worktree for the snapshot,
// none for a request, cached values normalised and an out-of-set one dropped.
func TestOwnerStateIsProbedOncePerWorktreePerSnapshot(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources(t.TempDir())
	sources.records["/wt/task-b"] = WorktreeRecord{Task: "task-b", Branch: "feature/b", Owner: worktrees.OwnerUnstated, HeartbeatAt: newClock().Now()}
	sources.remote[0].Snapshot.Worktrees = []remotestate.WorktreeState{
		{Task: "c-idle", Repository: "acme/gadgets", Branch: "i", OwnerState: "idle"},
		{Task: "c-unknown", Repository: "acme/gadgets", Branch: "u", OwnerState: "unknown"},
		{Task: "c-sleepy", Repository: "acme/gadgets", Branch: "s", OwnerState: "sleepy"},
		{Task: "c-none", Repository: "acme/gadgets", Branch: "n"},
	}
	snapshotter, _ := newSnapshotter(sources.collectors(), nil)
	refreshAndSettle(t, snapshotter)
	probes := sources.recordCalls.Load()
	if probes != 2 {
		t.Fatalf("the liveness probe ran %d times for 2 local worktrees in one snapshot", probes)
	}
	server := newCockpitServer(t, snapshotter)
	first, second := server.fleet(), server.fleet()
	if sources.recordCalls.Load() != probes || len(first.Worktrees) != len(second.Worktrees) {
		t.Errorf("requests ran the probe %d more times", sources.recordCalls.Load()-probes)
	}
	got := map[string]string{}
	for _, worktree := range second.Worktrees {
		got[worktree.Task] = worktree.OwnerState
	}
	want := map[string]string{"task-a": "active", "task-b": "unknown", "c-idle": "idle", "c-unknown": "unknown", "c-sleepy": "", "c-none": ""}
	for task, state := range want {
		if got[task] != state {
			t.Errorf("%s: owner_state %q, want %q", task, got[task], state)
		}
	}
}

// fieldTable is REQ:field-tables as data: each field's JSON type and whether
// it may appear on an entry cached from another machine.
type fieldTable map[string]struct {
	kind   string
	cached bool
}

var (
	entryFields   = fieldTable{"id": {"string", true}, "machine": {"string", true}, "machine_id": {"string", true}, "route": {"string", true}, "observed_at": {"time", true}}
	machineFields = fieldTable{
		"wb_version": {"string", true}, "repository_count": {"number", true}, "worktree_count": {"number", true},
		"os": {"string", true}, "arch": {"string", true}, "cpu_count": {"number", true}, "boot_time": {"time", true},
	}
	repositoryFields = fieldTable{
		"host": {"string", true}, "name": {"string", true}, "default_branch": {"string", false}, "worktree_count": {"number", true},
		"local_branch_count": {"number", false}, "remote_branch_count": {"number", false}, "open_pull_request_count": {"number", true}, "active_agent_count": {"number", false},
		"last_activity_at": {"time", false}, "remote_url_web": {"string", false}, "error": {"string", false}, "code_index": {"array", false},
	}
	worktreeFields = fieldTable{
		"repository": {"string", true}, "task": {"string", true}, "name": {"string", true}, "stream": {"string", true}, "branch": {"string", true},
		"lifecycle": {"string", true}, "owner_state": {"string", true}, "last_activity_at": {"time", true},
		"ahead": {"number", false}, "behind": {"number", false}, "upstream_gone": {"bool", false}, "has_upstream": {"bool", false}, "code_index": {"array", false},
	}
	pullRequestFields = fieldTable{
		"repository": {"string", true}, "worktree": {"string", true}, "branch": {"string", true}, "number": {"number", true}, "state": {"string", true}, "url": {"string", true},
	}
	documentFields = fieldTable{
		"schema_version": {"number", false}, "snapshot_at": {"time", false}, "warming_up": {"bool", false}, "repositories_total": {"number", false},
		"repositories_scanned": {"number", false}, "diagnostics": {"number", false}, "error": {"string", false}, "code_index_provider": {"string", false},
		"refresh_interval_seconds": {"number", false}, "agents_truncated": {"bool", false},
		"machines": {"array", false}, "repositories": {"array", false}, "worktrees": {"array", false}, "pull_requests": {"array", false}, "agents": {"array", false},
	}
)

// checkFields requires every key of entry to be in the table (or the entry
// fields), of the table's type, and absent for a cached entry unless the table
// allows it there.
func checkFields(t *testing.T, kind string, entry map[string]any, table fieldTable) {
	t.Helper()
	cached := entry["route"] == RouteCached
	for key, value := range entry {
		spec, known := table[key]
		if !known {
			spec, known = entryFields[key]
		}
		if !known {
			t.Errorf("%s carries the undocumented field %q", kind, key)
			continue
		}
		if cached && !spec.cached {
			t.Errorf("%s: the local-only field %q is on a cached entry", kind, key)
		}
		var got string
		switch typed := value.(type) {
		case string:
			got = "string"
			if _, err := time.Parse(time.RFC3339, typed); err == nil && spec.kind == "time" {
				got = "time"
			}
		case float64:
			got = "number"
		case bool:
			got = "bool"
		case []any:
			got = "array"
		default:
			got = fmt.Sprintf("%T", value)
		}
		if got != spec.kind {
			t.Errorf("%s.%s is a %s, want %s", kind, key, got, spec.kind)
		}
	}
}

// TestDocumentHoldsTheFieldTables proves cockpit-views#ac:field-tables-hold-in-
// the-document for the kinds this task owns, on a fixture with local and cached
// entries of every kind and values outside the closed sets.
func TestDocumentHoldsTheFieldTables(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources(t.TempDir())
	sources.remote[0].Snapshot.OS, sources.remote[0].Snapshot.Arch, sources.remote[0].Snapshot.CPUCount, sources.remote[0].Snapshot.BootTime = "linux", "amd64", 4, remotePublishedAt()
	sources.remote[0].Snapshot.KnownRepositories = append(sources.remote[0].Snapshot.KnownRepositories, "github.com/Sneat-Co/sneat-go")
	sources.remote[0].Snapshot.Worktrees = append(sources.remote[0].Snapshot.Worktrees, remotestate.WorktreeState{
		Task: "odd", Repository: "acme/gadgets", Branch: "odd", Lifecycle: "wandering", OwnerState: "sleepy",
		PullRequest: &remotestate.PullRequestState{Number: 9, State: "open", URL: "ftp://x"},
	})
	snapshotter, _ := newSnapshotter(sources.collectors(), func(options *Options) {
		options.Hardware = Hardware{OS: "darwin", Arch: "arm64", CPUCount: 8, BootTime: remotePublishedAt()}
	})
	refreshAndSettle(t, snapshotter)
	body, _ := snapshotter.Body()
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	checkFields(t, "document", document, documentFields)
	if document["schema_version"] != float64(2) {
		t.Errorf("schema_version = %v", document["schema_version"])
	}
	for _, absent := range []string{"branches", "metrics"} {
		if _, present := document[absent]; present {
			t.Errorf("the document carries %s", absent)
		}
	}
	counts := map[string]int{}
	for collection, table := range map[string]fieldTable{"machines": machineFields, "repositories": repositoryFields, "worktrees": worktreeFields, "pull_requests": pullRequestFields} {
		entries, _ := document[collection].([]any)
		for _, raw := range entries {
			entry := raw.(map[string]any)
			checkFields(t, collection, entry, table)
			if entry["route"] == RouteCached {
				counts[collection+"/cached"]++
			} else {
				counts[collection+"/local"]++
			}
		}
	}
	for _, key := range []string{"machines/local", "machines/cached", "repositories/local", "repositories/cached", "worktrees/local", "worktrees/cached", "pull_requests/local", "pull_requests/cached"} {
		if counts[key] == 0 {
			t.Errorf("the fixture has no %s entry, so the check is vacuous", key)
		}
	}
	for _, worktree := range snapshotter.Document().Worktrees {
		if worktree.Task == "odd" && (worktree.Lifecycle != "" || worktree.OwnerState != "") {
			t.Errorf("out-of-set values were kept: %+v", worktree)
		}
	}
	for _, pull := range snapshotter.Document().PullRequests {
		if pull.Number == 9 && pull.URL != "" {
			t.Errorf("an ftp URL was kept: %+v", pull)
		}
	}
}

// TestCachedRepositoryNamesAreSplitIntoHostAndName proves cockpit-views#ac:
// cached-repository-names-are-split-into-host-and-name through the document.
func TestCachedRepositoryNamesAreSplitIntoHostAndName(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources(t.TempDir())
	sources.remote[0].Snapshot.KnownRepositories = []string{"github.com/Sneat-Co/sneat-go", "sneat-co/sneat-go", "gitlab.example.com/group/sub/proj"}
	sources.remote[0].Snapshot.Worktrees = nil
	snapshotter, _ := newSnapshotter(sources.collectors(), nil)
	refreshAndSettle(t, snapshotter)
	type pair struct{ host, name string }
	got := map[pair]bool{}
	for _, repository := range snapshotter.Document().Repositories {
		got[pair{repository.Host, repository.Name}] = true
		if repository.Route == RouteLocal && (repository.Name != "acme/widgets" || repository.Host != "github.com") {
			t.Errorf("local repository = %+v", repository)
		}
	}
	for _, want := range []pair{{"github.com", "Sneat-Co/sneat-go"}, {"", "sneat-co/sneat-go"}, {"gitlab.example.com", "group/sub/proj"}, {"github.com", "acme/widgets"}} {
		if !got[want] {
			t.Errorf("no repository %+v in %v", want, got)
		}
	}
}
