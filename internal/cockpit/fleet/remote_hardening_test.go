package fleet

import (
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cockpit"
	"github.com/sneat-dev/wb/internal/remotestate"
)

// logRecorder collects what a snapshotter logs.
type logRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (l *logRecorder) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logRecorder) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.lines)
}

func (l *logRecorder) count(part string) int {
	found := 0
	for _, line := range l.all() {
		if strings.Contains(line, part) {
			found++
		}
	}
	return found
}

func publishesOf(snapshotter *Snapshotter) int {
	snapshotter.mu.RLock()
	defer snapshotter.mu.RUnlock()
	return snapshotter.publishes
}

// TestAWarmingRemoteNeverReplacesWhatIsHeldAndCannotHideForEver proves the two
// halves of the warming rule. A remote daemon whose first pass has not ended
// (said by its hub's typed 503 or by a partial fleet in an envelope) never
// replaces a complete live view that is still fresh, sets no error and earns no
// backoff then. But warming is bounded: once nothing fresh is held (the machine
// was never read, or its view went stale) it is shown as remote_warming_up, on
// its published entries or, with none, on a bare machine entry that keeps the
// configured machine visible, and it backs off like any failure.
func TestAWarmingRemoteNeverReplacesWhatIsHeldAndCannotHideForEver(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 2, false)
	partial := copyEnvelope(t, full)
	partial.Fleet.WarmingUp, partial.Fleet.Worktrees, partial.Fleet.Agents, partial.Fleet.PullRequests = true, []Worktree{}, []Agent{}, []PullRequest{}
	exporter := &fakeExporter{answer: failing(fmt.Errorf("the hub: %w", ErrRemoteWarmingUp))}
	logs := &logRecorder{}
	snapshotter, clock := newLive(t, &fakeSources{remote: []remotestate.Entry{cachedVM("alex")}}, exporter, func(options *Options) { options.Logf = logs.logf })
	exporter.clock = clock
	refreshAndSettle(t, snapshotter)
	step := func(seconds int) {
		for range seconds / 5 {
			pollAndSettle(t, snapshotter)
			clock.advance(remoteStep)
		}
	}
	// Never read: warming is shown on the published entries, which stay.
	step(120)
	vm, _ := machineNamed(snapshotter.Document(), vmKey)
	if vm.Route != RouteCached || vm.RemoteError != RemoteErrorWarmingUp || vm.WorktreeCount != 1 {
		t.Fatalf("a never-read machine whose remote is warming = %+v", vm)
	}
	exporter.set(answering(full, full))
	step(60)
	if vm, _ = machineNamed(snapshotter.Document(), vmKey); vm.Route != RouteLiveRemote || vm.WorktreeCount != 3 || vm.RemoteError != "" {
		t.Fatalf("after the remote finished warming = %+v", vm)
	}
	// The remote restarts and warms: the fresh, complete view stays untouched.
	published := publishesOf(snapshotter)
	exporter.set(answering(partial, partial))
	step(60)
	if vm, _ = machineNamed(snapshotter.Document(), vmKey); vm.Route != RouteLiveRemote || vm.WorktreeCount != 3 || vm.RemoteError != "" || len(entriesOf(snapshotter.Document(), vm.ID)["agents"]) != 1 {
		t.Fatalf("a restarted remote that is warming replaced the complete view: %+v", vm)
	}
	if got := publishesOf(snapshotter); got != published {
		t.Errorf("a warming remote with a fresh view caused %d publications", got-published)
	}
	// It keeps warming until the view is two intervals old: now it is an error.
	exporter.set(failing(ErrRemoteWarmingUp))
	step(240)
	if vm, _ = machineNamed(snapshotter.Document(), vmKey); vm.Route != RouteCached || vm.RemoteError != RemoteErrorWarmingUp || vm.WorktreeCount != 1 {
		t.Fatalf("a remote that warms past the view's freshness = %+v", vm)
	}
	// 0: warming, shown, backed off to 120; 120: read; 180: warming with a fresh
	// view; 240: warming with a stale view, backed off to 360.
	if got, want := exporter.callsAt(false), []int{0, 120, 180, 240, 360}; !slices.Equal(got, want) {
		t.Errorf("attempts at %v, want %v", got, want)
	}
	if logs.count("the export of vm failed (remote_warming_up)") != 2 {
		t.Errorf("log = %q, want the bounded warming logged when it starts, twice", logs.all())
	}

	// A configured machine with no published entry stays visible while it warms.
	bare := &fakeExporter{answer: failing(ErrRemoteWarmingUp)}
	lone, _ := newLive(t, oneRepoSources("/repos/widgets"), bare, nil)
	refreshAndSettle(t, lone)
	pollAndSettle(t, lone)
	shown, found := machineNamed(lone.Document(), vmKey)
	if !found || shown.Route != RouteLiveRemote || shown.RemoteError != RemoteErrorWarmingUp || shown.WorktreeCount != 0 {
		t.Fatalf("a machine with no published entry whose remote is warming = %+v (found %v)", shown, found)
	}
}

// TestAMetricsOnlyExportNeverClearsAFailure proves that only a full export
// clears remote_error: a remote that refuses or fails its full export and
// answers its metrics-only one keeps the error, and still gives the metrics.
func TestAMetricsOnlyExportNeverClearsAFailure(t *testing.T) {
	t.Parallel()
	only := exportOf(t, vmOwnName, vmSources(), 3, true)
	snapshotter, clock := newLive(t, oneRepoSources("/repos/widgets"), &fakeExporter{answer: failing(&RemoteError{Code: RemoteErrorHTTPAuthFailed, Fallback: true})}, nil)
	refreshAndSettle(t, snapshotter)
	pollAndSettle(t, snapshotter)
	machine := snapshotter.live[vmKey]
	if publish := snapshotter.recordExport(t.Context(), machine, exportResult{envelope: only, transport: TransportHTTP, ok: true}, true, clock.Now(), clock.Now(), liveView{}, [32]byte{}, 0, "", false); publish {
		t.Error("a metrics-only export asked for a publication")
	}
	snapshotter.mu.RLock()
	kept, samples := machine.remoteError, len(machine.samples)
	snapshotter.mu.RUnlock()
	if kept != RemoteErrorHTTPAuthFailed || samples != 3 {
		t.Fatalf("after a metrics-only success: remote_error %q, %d samples", kept, samples)
	}
	if vm, _ := machineNamed(snapshotter.Document(), vmKey); vm.RemoteError != RemoteErrorHTTPAuthFailed {
		t.Errorf("the machine = %+v", vm)
	}
}

// TestAnHTTPHubSaysWhyThereIsNoEnvelopeAndOnlyThoseWordsAreUnderstood proves the
// typed reasons of the HTTP transport: exactly 503 warming_up is no failure,
// exactly 403 export_refused is export_refused with no fallback, exactly 503
// export_failed is bad_payload with no fallback, and the same statuses with any
// other body, or the same words on another status, are the plain failure of
// the status. The body is never part of an error.
func TestAnHTTPHubSaysWhyThereIsNoEnvelopeAndOnlyThoseWordsAreUnderstood(t *testing.T) {
	t.Parallel()
	exporter := NewHTTPExporter(newClock().Now)
	token := tokenFile(t, vmBearer)
	for name, test := range map[string]struct {
		status  int
		body    string
		warming bool
		want    RemoteError
	}{
		"warming up":                     {503, `{"error":"warming_up"}`, true, RemoteError{}},
		"export refused":                 {403, `{"error":"export_refused"}`, false, RemoteError{Code: RemoteErrorExportRefused}},
		"export failed":                  {503, `{"error":"export_failed"}`, false, RemoteError{Code: RemoteErrorBadPayload}},
		"a 503 with another word":        {503, `{"error":"` + sentinel + `"}`, false, RemoteError{Code: RemoteErrorHTTPUnavailable, Fallback: true}},
		"a 503 with no body":             {503, ``, false, RemoteError{Code: RemoteErrorHTTPUnavailable, Fallback: true}},
		"a 503 that is not JSON":         {503, `<html>warming_up</html>`, false, RemoteError{Code: RemoteErrorHTTPUnavailable, Fallback: true}},
		"a 403 of another identity":      {403, `{"error":"not_the_host_owner"}`, false, RemoteError{Code: RemoteErrorHTTPAuthFailed, Fallback: true}},
		"warming up on a 500":            {500, `{"error":"warming_up"}`, false, RemoteError{Code: RemoteErrorHTTPUnavailable, Fallback: true}},
		"export refused on a 401":        {401, `{"error":"export_refused"}`, false, RemoteError{Code: RemoteErrorHTTPAuthFailed, Fallback: true}},
		"rate limited":                   {429, `{"error":"rate_limited"}`, false, RemoteError{Code: RemoteErrorHTTPUnavailable, Fallback: true}},
		"a megabyte before the word":     {503, strings.Repeat(" ", 1<<20) + `{"error":"warming_up"}`, false, RemoteError{Code: RemoteErrorHTTPUnavailable, Fallback: true}},
		"the word in a field of another": {503, `{"reason":"warming_up"}`, false, RemoteError{Code: RemoteErrorHTTPUnavailable, Fallback: true}},
	} {
		hub := newFakeHub(t, func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(test.status)
			_, _ = writer.Write([]byte(test.body))
		})
		_, err := exporter.Export(t.Context(), httpTarget(hub, token), false)
		var failure *RemoteError
		switch {
		case err == nil || strings.Contains(err.Error(), sentinel):
			t.Errorf("%s: err = %v", name, err)
		case test.warming != errors.Is(err, ErrRemoteWarmingUp):
			t.Errorf("%s: err = %v, want warming %v", name, err, test.warming)
		case !test.warming && (!errors.As(err, &failure) || *failure != test.want):
			t.Errorf("%s: err = %v, want %+v", name, err, test.want)
		}
	}
}

// brokenDocument is a document of this machine with one sound repository and
// its entries, and beside them every kind of entry the strict decoder would
// refuse for a rule between entries.
func brokenDocument(now time.Time) Document {
	const machine = "mach-00000000000000000000"
	local := func(id string) Entry {
		return Entry{ID: id, Machine: testMachine, MachineID: machine, Route: RouteLocal, ObservedAt: now}
	}
	id := func(kind string, n int) string { return fmt.Sprintf("%s-%020d", kind, n) }
	document := emptyDocument(time.Minute)
	document.WarmingUp, document.SnapshotAt = false, now
	document.Machines = []Machine{{Entry: local(machine), WBVersion: testVersion}}
	document.Repositories = []Repository{
		{Entry: local(id("repo", 1)), Host: "github.com", Name: "acme/sound", RemoteURLWeb: "https://github.com/acme/sound"},
		{Entry: local(id("repo", 2)), Host: "github.com", Name: "acme/forged", RemoteURLWeb: "https://evil.example/acme/forged"},
		{Entry: local(id("repo", 3)), Host: "github.com", Name: "acme/null-kinds", CodeIndex: []CodeIndex{{Indexer: "codegrapher", State: CodeIndexFresh, Statistics: &CodeStatistics{}}}},
		{Entry: local(id("repo", 1)), Host: "github.com", Name: "acme/same-id"},
	}
	document.Worktrees = []Worktree{
		{Entry: local(id("wt", 1)), Repository: id("repo", 1), Name: "kept", Task: "kept", Branch: "b"},
		{Entry: local(id("wt", 2)), Repository: id("repo", 2), Name: "of-forged", Task: "of-forged", Branch: "b"},
		{Entry: local(id("wt", 3)), Repository: id("repo", 1), Name: "long", Task: strings.Repeat("t", 400), Branch: "b"},
		{Entry: Entry{ID: id("wt", 4), Machine: testMachine, MachineID: "mach-11111111111111111111", Route: RouteLocal}, Repository: id("repo", 1), Name: "elsewhere", Task: "elsewhere", Branch: "b"},
		{Entry: local(id("wt", 1)), Repository: id("repo", 1), Name: "same-id", Task: "same-id", Branch: "b"},
		{Entry: local(id("wt", 5)), Repository: id("repo", 1), Name: "null-kinds", Task: "null-kinds", Branch: "b", CodeIndex: []CodeIndex{{Indexer: "codegrapher", State: CodeIndexFresh, Statistics: &CodeStatistics{}}}},
	}
	document.PullRequests = []PullRequest{
		{Entry: local(id("pr", 1)), Repository: id("repo", 1), Worktree: id("wt", 1), Number: 1, URL: "https://github.com/acme/sound/pull/1"},
		{Entry: local(id("pr", 2)), Repository: id("repo", 1), Worktree: id("wt", 3), Number: 2, URL: "https://github.com/acme/sound/pull/2"},
		{Entry: local(id("pr", 3)), Repository: id("repo", 1), Number: 3, URL: "https://evil.example/acme/sound/pull/3"},
		{Entry: local(id("pr", 4)), Repository: id("repo", 2), Number: 4},
		{Entry: local(id("pr", 5)), Number: 5, URL: "https://elsewhere.example/a/b/pull/5"},
	}
	document.Agents = []Agent{
		{Entry: local(id("ag", 1)), Kind: AgentRun, State: "running", Repository: id("repo", 1)},
		{Entry: local(id("ag", 2)), Kind: AgentRun, State: "running", Repository: id("repo", 3)},
		{Entry: local(id("ag", 3)), Kind: AgentRun, State: "not-a-state"},
	}
	return document
}

// TestExportLeavesOutEveryEntryThatWouldFailTheWholeEnvelope proves that the
// rules between entries drop one entry and not the export: a repository whose
// web address is not built from its host and name, one whose statistics have a
// null kinds list and one that repeats an id are left out, with everything of
// them (worktrees, pull requests and agents, each counted); so are a pull
// request whose address is off its repository's host, an entry of another
// machine id and a repeated id; a pull request whose worktree was left out
// stays without the reference; and what remains passes the strict decoder.
func TestExportLeavesOutEveryEntryThatWouldFailTheWholeEnvelope(t *testing.T) {
	t.Parallel()
	now := newClock().Now()
	document := brokenDocument(now)
	if err := validateDocument(&document); err == nil {
		t.Fatal("the document is valid as it is: the test would be vacuous")
	}
	envelope, drops := NewEnvelope(document, MetricsResponse{Route: RouteNone, Reason: ReasonNoSource}, now, false)
	if want := (ExportDrops{Repositories: 3, Worktrees: 5, PullRequests: 2, Agents: 2}); drops != want || envelope.Dropped != want.Total() {
		t.Fatalf("drops = %+v (dropped %d), want %+v", drops, envelope.Dropped, want)
	}
	body, _ := json.Marshal(envelope)
	decoded, err := DecodeEnvelope(strings.NewReader(string(body)), false, now)
	if err != nil {
		t.Fatalf("what remains is refused: %v", err)
	}
	names := func() string {
		var kept []string
		for _, repository := range decoded.Fleet.Repositories {
			kept = append(kept, repository.Name)
		}
		for _, worktree := range decoded.Fleet.Worktrees {
			kept = append(kept, worktree.Task)
		}
		for _, pull := range decoded.Fleet.PullRequests {
			kept = append(kept, fmt.Sprintf("pr%d:%s", pull.Number, pull.Worktree))
		}
		for _, agent := range decoded.Fleet.Agents {
			kept = append(kept, agent.ID)
		}
		return strings.Join(kept, " ")
	}()
	if want := "acme/sound kept pr1:wt-00000000000000000001 pr2: pr5: ag-00000000000000000001"; names != want {
		t.Errorf("kept %q, want %q", names, want)
	}
	if strings.Contains(string(body), "evil.example") {
		t.Errorf("the export carries a forged address: %s", body)
	}
	// A metrics-only export has no fleet and so nothing to leave out.
	if only, none := NewEnvelope(document, MetricsResponse{Route: RouteNone, Reason: ReasonNoSource}, now, true); only.Dropped != 0 || none.Total() != 0 || only.Fleet != nil {
		t.Errorf("a metrics-only export dropped %d", only.Dropped)
	}
}

// TestExportPayloadIsPreparedOncePerVersionAndSaysWhatItLeftOut proves the hub
// route's source: the envelope is encoded and compressed once for each version
// of the published document and of the metrics, not once per request; a full
// export is warming_up until the first pass ends while the metrics-only one is
// served; an envelope that fails its own rules is export_failed; and what was
// left out is logged once, as numbers by kind and never as names.
func TestExportPayloadIsPreparedOncePerVersionAndSaysWhatItLeftOut(t *testing.T) {
	t.Parallel()
	sources := vmSources()
	secret := strings.Repeat("secret-name-", 40)
	sources.records["/vm/wt-2"] = WorktreeRecord{Task: secret, Branch: "feature/vm-2", CreatedAt: newClock().Now()}
	var compressed atomic.Int64
	logs := &logRecorder{}
	snapshotter, _ := newSnapshotter(sources.collectors(), func(options *Options) {
		options.Sampler = filledSampler(t, &countingSource{}, 3)
		options.Logf = logs.logf
		options.Compress = func(body []byte) []byte { compressed.Add(1); return body }
	})
	if _, failure := snapshotter.ExportPayload(false); failure != ErrorWarmingUp {
		t.Fatalf("a full export before the first pass = %q, want warming_up", failure)
	}
	if payload, failure := snapshotter.ExportPayload(true); failure != "" || payload.Size() == 0 {
		t.Fatalf("a metrics-only export before the first pass = %q", failure)
	}
	refreshAndSettle(t, snapshotter)
	before := compressed.Load()
	var first []byte
	for range 5 {
		for _, metricsOnly := range []bool{false, true} {
			payload, failure := snapshotter.ExportPayload(metricsOnly)
			if failure != "" || payload.Size() == 0 {
				t.Fatalf("export (metrics-only %v) = %q", metricsOnly, failure)
			}
			if !metricsOnly && first == nil {
				recorder := httptest.NewRecorder()
				cockpit.ServePayload(recorder, httptest.NewRequest(http.MethodGet, "/", nil), payload)
				first = recorder.Body.Bytes()
			}
		}
	}
	if got := compressed.Load() - before; got != 1 {
		t.Errorf("ten requests compressed %d bodies, want 1 (the metrics-only one was prepared before the pass)", got)
	}
	envelope, err := DecodeEnvelope(strings.NewReader(string(first)), false, newClock().Now())
	if err != nil || envelope.Dropped != 1 || len(envelope.Fleet.Worktrees) != 2 {
		t.Fatalf("the served envelope: %v, dropped %d", err, envelope.Dropped)
	}
	want := "cockpit fleet: this machine's export leaves out 1 entries its rules refuse (repositories 0, worktrees 1, pull requests 0, agents 0)"
	if lines := logs.all(); len(lines) != 1 || lines[0] != want || strings.Contains(lines[0], "secret") {
		t.Errorf("log = %q, want %q once", lines, want)
	}
	// A new publication is a new version: the full export is prepared again, the
	// metrics-only one is not, and the same drops are not logged twice.
	refreshAndSettle(t, snapshotter)
	before = compressed.Load()
	snapshotter.ExportPayload(false)
	snapshotter.ExportPayload(true)
	if got := compressed.Load() - before; got != 1 {
		t.Errorf("after a publication %d bodies were compressed, want 1", got)
	}
	if lines := logs.all(); len(lines) != 1 {
		t.Errorf("the same drops were logged again: %q", lines)
	}

	// A machine whose own entry is not valid has no export, and that is cached too.
	invalid, _ := newSnapshotter(vmSources().collectors(), func(options *Options) { options.Machine = strings.Repeat("m", 300) })
	refreshAndSettle(t, invalid)
	for range 2 {
		if _, failure := invalid.ExportPayload(false); failure != ErrorExportFailed {
			t.Fatalf("an export with an invalid machine entry = %q, want export_failed", failure)
		}
	}
}

// TestAReaderShowsWhatAnExportLeftOutAndNeverTakesTheCountFromAnExportItself
// proves export_dropped: the reader's machine entry for a live machine carries
// the number the export left out, and an export that carries export_dropped or
// agents_truncated on its own machine entry (which only a reader sets) is
// refused.
func TestAReaderShowsWhatAnExportLeftOutAndNeverTakesTheCountFromAnExportItself(t *testing.T) {
	t.Parallel()
	sources := vmSources()
	sources.records["/vm/wt-2"] = WorktreeRecord{Task: strings.Repeat("long-", 80), Branch: "feature/vm-2", CreatedAt: newClock().Now()}
	full := exportOf(t, vmOwnName, sources, 2, false)
	if full.Dropped != 1 {
		t.Fatalf("the export dropped %d", full.Dropped)
	}
	snapshotter, clock := newLive(t, oneRepoSources("/repos/widgets"), &fakeExporter{answer: answering(full, full)}, nil)
	refreshAndSettle(t, snapshotter)
	pollAndSettle(t, snapshotter)
	vm, _ := machineNamed(snapshotter.Document(), vmKey)
	body, _ := json.Marshal(snapshotter.Document())
	if vm.ExportDropped != 1 || vm.WorktreeCount != 2 || !strings.Contains(string(body), `"export_dropped":1`) {
		t.Fatalf("the live machine = %+v", vm)
	}
	if local, _ := machineNamed(snapshotter.Document(), testMachine); local.ExportDropped != 0 || local.AgentsTruncated {
		t.Errorf("this machine's entry = %+v", local)
	}
	for name, change := range map[string]func(*Machine){
		"export_dropped":   func(machine *Machine) { machine.ExportDropped = 3 },
		"agents_truncated": func(machine *Machine) { machine.AgentsTruncated = true },
	} {
		forged := copyEnvelope(t, full)
		change(&forged.Fleet.Machines[0])
		if err := forged.Validate(false, clock.Now()); err == nil || !strings.Contains(err.Error(), "only a reader sets") {
			t.Errorf("an export with %s on its machine entry = %v, want it refused", name, err)
		}
	}
}

// TestALinkAnotherMachineSentIsNeverRendered proves the link rule for entries of
// other machines, live and published: no address a remote sent is rendered. A
// pull request's link is built here from its repository's host and name and its
// number, and kept only where the host is the host of a repository of this
// machine and the link built is exactly the address sent (so another forge's
// address shape, or a path the sender chose, has no link at all); a
// repository's web address is kept under the same host rule.
func TestALinkAnotherMachineSentIsNeverRendered(t *testing.T) {
	t.Parallel()
	// The live machine: one repository on github.com, where this machine has
	// repositories, and one on a host only it names.
	evil := vmSources()
	evil.repos[0].Host = "evil.example"
	evil.bindings[0].URL = "https://evil.example/acme/engine/pull/41"
	onEvil := exportOf(t, vmOwnName, evil, 1, false)
	onGitHub := exportOf(t, vmOwnName, vmSources(), 1, false)
	// A consistent envelope whose pull request names a path of the sender's
	// choosing on the right host: the path is not used.
	onGitHub.Fleet.PullRequests[0].URL = "https://github.com/attacker/phish/pull/1?x=" + sentinel
	if err := onGitHub.Validate(false, newClock().Now()); err != nil {
		t.Fatal(err)
	}
	published := []remotestate.Entry{{Snapshot: remotestate.Snapshot{
		Login: "someone", Machine: "desktop", PublishedAt: remotePublishedAt(),
		KnownRepositories: []string{"acme/gadgets", "github.com/acme/hosted", "evil.example/acme/other"},
		Worktrees: []remotestate.WorktreeState{
			{Task: "t1", Repository: "acme/gadgets", Branch: "b1", PullRequest: &remotestate.PullRequestState{Number: 3, State: "OPEN", URL: "https://evil.example/acme/gadgets/pull/3"}},
			{Task: "t2", Repository: "acme/gadgets", Branch: "b2", PullRequest: &remotestate.PullRequestState{Number: 4, State: "OPEN", URL: "https://github.com/attacker/phish/pull/9999"}},
			{Task: "t3", Repository: "github.com/acme/hosted", Branch: "b3", PullRequest: &remotestate.PullRequestState{Number: 5, State: "OPEN", URL: "https://evil.example/x/y/pull/5"}},
			{Task: "t4", Repository: "evil.example/acme/other", Branch: "b4", PullRequest: &remotestate.PullRequestState{Number: 6, State: "OPEN", URL: "https://evil.example/acme/other/pull/6"}},
			{Task: "t5", Repository: "github.com/acme/hosted", Branch: "b5", PullRequest: &remotestate.PullRequestState{Number: 8, State: "OPEN", URL: "https://github.com/acme/hosted/pull/8"}},
			{Task: "t6", Repository: "github.com/acme/hosted", Branch: "b6", PullRequest: &remotestate.PullRequestState{Number: 9, State: "OPEN", URL: "https://github.com/acme/hosted/-/merge_requests/9"}},
			{Task: "t7", Repository: "acme/gadgets", Branch: "b7", PullRequest: &remotestate.PullRequestState{Number: 10, State: "OPEN", URL: "https://github.com/acme/gadgets/pull/10"}},
		},
	}}}
	for name, test := range map[string]struct {
		envelope  Envelope
		localRepo bool
		wantLinks []string
	}{
		"a host only the remote names": {onEvil, true, []string{
			"https://github.com/acme/gadgets/pull/10", "https://github.com/acme/hosted/pull/8", "https://github.com/acme/widgets", "https://github.com/acme/widgets/pull/7",
		}},
		"a path the sender chose": {onGitHub, true, []string{
			"https://github.com/acme/engine",
			"https://github.com/acme/gadgets/pull/10", "https://github.com/acme/hosted/pull/8", "https://github.com/acme/widgets", "https://github.com/acme/widgets/pull/7",
		}},
		"the address the link would have": {exportOf(t, vmOwnName, vmSources(), 1, false), true, []string{
			"https://github.com/acme/engine", "https://github.com/acme/engine/pull/41",
			"https://github.com/acme/gadgets/pull/10", "https://github.com/acme/hosted/pull/8", "https://github.com/acme/widgets", "https://github.com/acme/widgets/pull/7",
		}},
		"this machine has no repository": {onGitHub, false, nil},
	} {
		sources := &fakeSources{}
		if test.localRepo {
			sources = oneRepoSources("/repos/widgets")
		}
		sources.remote = published
		snapshotter, _ := newLive(t, sources, &fakeExporter{answer: answering(test.envelope, test.envelope)}, nil)
		refreshAndSettle(t, snapshotter)
		pollAndSettle(t, snapshotter)
		document := snapshotter.Document()
		var links []string
		for _, repository := range document.Repositories {
			if repository.RemoteURLWeb != "" {
				links = append(links, repository.RemoteURLWeb)
			}
		}
		for _, pull := range document.PullRequests {
			if pull.URL != "" {
				links = append(links, pull.URL)
			}
		}
		slices.Sort(links)
		if !slices.Equal(links, test.wantLinks) {
			t.Errorf("%s: links = %q, want %q", name, links, test.wantLinks)
		}
		body, _ := json.Marshal(document)
		for _, never := range []string{"https://evil.example", "attacker", "phish", "merge_requests", sentinel} {
			if strings.Contains(string(body), never) {
				t.Errorf("%s: the document renders %q", name, never)
			}
		}
		if vm, _ := machineNamed(document, vmKey); vm.Route != RouteLiveRemote || len(entriesOf(document, vm.ID)["pull_requests"]) != 1 {
			t.Errorf("%s: the live machine = %+v (the test would be vacuous)", name, vm)
		}
	}
	// The stored entries are not changed by the linking: it works on copies.
	if got := relink(nil, nil, nil); got != nil {
		t.Errorf("no pull requests relink to %v", got)
	}
}

// TestAPanicWhileMappingARemoteIsBadPayloadAndNeverTheDaemons proves the panic
// safety of the remote path: a mapper that panics on an export marks that
// machine bad_payload, logged once, keeps its published entries and leaves the
// daemon and the other machines alone; and a panic when the entries are mapped
// again during a publication drops that machine's export the same way.
func TestAPanicWhileMappingARemoteIsBadPayloadAndNeverTheDaemons(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 2, false)
	logs := &logRecorder{}
	sources := &fakeSources{remote: []remotestate.Entry{cachedVM("alex")}}
	snapshotter, clock := newSnapshotter(sources.collectors(), func(options *Options) {
		options.Login = testLogin
		options.Remotes = []RemoteTarget{{Machine: vmKey}, {Machine: "other"}}
		options.Transports = []RemoteTransport{{Name: TransportHTTP, Exporter: &fakeExporter{answer: answering(full, full)}}}
		options.Logf = logs.logf
	})
	var panics atomic.Bool
	panics.Store(true)
	snapshotter.mapper = func(key, machineID string, fleet *Document, observed time.Time, dropped int) liveView {
		if panics.Load() && key == vmKey {
			panic("a mapper panicked on " + sentinel)
		}
		return mapLive(key, machineID, fleet, observed, dropped)
	}
	refreshAndSettle(t, snapshotter)
	for range 3 {
		pollAndSettle(t, snapshotter)
		clock.advance(maxRemoteBackoff)
	}
	document := snapshotter.Document()
	vm, _ := machineNamed(document, vmKey)
	other, _ := machineNamed(document, "other")
	if vm.Route != RouteCached || vm.RemoteError != RemoteErrorBadPayload || other.Route != RouteLiveRemote || other.WorktreeCount != 3 {
		t.Fatalf("after a panicking mapper: vm %+v, other %+v", vm, other)
	}
	if lines := logs.all(); logs.count("the export of vm failed (bad_payload)") != 1 || strings.Contains(strings.Join(lines, "\n"), sentinel) {
		t.Errorf("log = %q, want the failure once and no panic text", lines)
	}

	// The mapper works, the machine goes live; then its entries must be mapped
	// again at a publication (as when its id changes because the login was
	// learned), while the mapper panics: the publication survives.
	panics.Store(false)
	pollAndSettle(t, snapshotter)
	if vm, _ = machineNamed(snapshotter.Document(), vmKey); vm.Route != RouteLiveRemote || vm.RemoteError != "" {
		t.Fatalf("with a working mapper = %+v", vm)
	}
	panics.Store(true)
	snapshotter.mu.Lock()
	snapshotter.live[vmKey].mappedFor = ""
	snapshotter.mu.Unlock()
	sources.change(func(f *fakeSources) { f.remote = nil })
	refreshAndSettle(t, snapshotter)
	document = snapshotter.Document()
	vm, _ = machineNamed(document, vmKey)
	other, _ = machineNamed(document, "other")
	if vm.Route != RouteLiveRemote || vm.RemoteError != RemoteErrorBadPayload || vm.WorktreeCount != 0 || !vm.ObservedAt.IsZero() || other.WorktreeCount != 3 {
		t.Fatalf("after a panic during a publication: vm %+v, other %+v", vm, other)
	}
	if logs.count("the export of vm could not be mapped (bad_payload)") != 1 {
		t.Errorf("log = %q", logs.all())
	}
	requireUniqueIDs(t, document)
}

// TestAnExportThatChangesNothingPublishesNothing proves that the document is not
// encoded and compressed again for an export whose entries are the ones already
// shown: only a changed view, transport or failure publishes.
func TestAnExportThatChangesNothingPublishesNothing(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 2, false)
	exporter := &fakeExporter{answer: answering(full, full)}
	var compressed atomic.Int64
	snapshotter, clock := newLive(t, oneRepoSources("/repos/widgets"), exporter, func(options *Options) {
		options.Compress = func(body []byte) []byte { compressed.Add(1); return body }
	})
	refreshAndSettle(t, snapshotter)
	pollAndSettle(t, snapshotter)
	published, bodies := publishesOf(snapshotter), compressed.Load()
	for range 3 {
		clock.advance(DefaultInterval)
		pollAndSettle(t, snapshotter)
	}
	if exporter.count() != 4 || publishesOf(snapshotter) != published || compressed.Load() != bodies {
		t.Fatalf("three unchanged exports: %d exports, %d publications, %d bodies compressed", exporter.count(), publishesOf(snapshotter)-published, compressed.Load()-bodies)
	}
	// The unchanged export still counts as fresh: two intervals after the first
	// one the machine is live, because the last was received just now.
	refreshAndSettle(t, snapshotter)
	if vm, _ := machineNamed(snapshotter.Document(), vmKey); vm.Route != RouteLiveRemote {
		t.Fatalf("an unchanged export let the machine go stale: %+v", vm)
	}
	published = publishesOf(snapshotter)
	changed := copyEnvelope(t, full)
	changed.Fleet.Worktrees[0].OwnerState = OwnerIdle
	exporter.set(answering(changed, changed))
	clock.advance(DefaultInterval)
	pollAndSettle(t, snapshotter)
	if publishesOf(snapshotter) != published+1 {
		t.Errorf("a changed export caused %d publications, want 1", publishesOf(snapshotter)-published)
	}
}

// TestARemotesEntriesAreCappedAndTheCutIsCounted proves the caps of one remote
// machine: at most 2000 repositories, 2000 worktrees, 500 pull requests and 200
// agents are kept, what is cut is added to export_dropped, and cut agents set
// agents_truncated, as the remote's own truncation does.
func TestARemotesEntriesAreCappedAndTheCutIsCounted(t *testing.T) {
	t.Parallel()
	own := func(kind string, n int) Entry {
		return Entry{ID: fmt.Sprintf("%s-%d", kind, n), MachineID: "mach-own", Route: RouteLocal}
	}
	document := emptyDocument(time.Minute)
	document.Machines = []Machine{{Entry: Entry{ID: "mach-own", MachineID: "mach-own", Route: RouteLocal}}}
	for n := range maxLiveRepositories + 7 {
		document.Repositories = append(document.Repositories, Repository{Entry: own("repo", n), Name: fmt.Sprintf("acme/r%d", n)})
	}
	for n := range maxLiveWorktrees + 5 {
		document.Worktrees = append(document.Worktrees, Worktree{Entry: own("wt", n), Repository: "repo-0", Task: fmt.Sprintf("t%d", n)})
	}
	for n := range maxLivePullRequests + 3 {
		document.PullRequests = append(document.PullRequests, PullRequest{Entry: own("pr", n), Number: n + 1})
	}
	for n := range agentCap {
		document.Agents = append(document.Agents, Agent{Entry: own("ag", n), Kind: AgentRun, State: "running"})
	}
	view := mapLive(vmKey, "mach-vm", &document, newClock().Now(), 2)
	if len(view.repositories) != maxLiveRepositories || len(view.worktrees) != maxLiveWorktrees || len(view.pullRequests) != maxLivePullRequests || len(view.agents) != agentCap {
		t.Fatalf("kept %d repositories, %d worktrees, %d pull requests, %d agents", len(view.repositories), len(view.worktrees), len(view.pullRequests), len(view.agents))
	}
	if view.machine.ExportDropped != 2+7+5+3 || view.machine.AgentsTruncated || view.repositories[0].WorktreeCount != maxLiveWorktrees {
		t.Errorf("the machine = %+v", view.machine)
	}
	document.AgentsTruncated = true
	if !mapLive(vmKey, "mach-vm", &document, newClock().Now(), -5).machine.AgentsTruncated {
		t.Error("the remote's own truncation is not carried")
	}
	if got := mapLive(vmKey, "mach-vm", &document, newClock().Now(), maxCount*2).machine.ExportDropped; got != maxCount {
		t.Errorf("an absurd dropped count is shown as %d", got)
	}
}

// TestADocumentOverItsSizeBoundLeavesTheLiveMachinesOut proves the size guard on
// both publication paths: a document that is over the bound with the live
// machines' entries is published without them, with their published entries
// shown instead, one more diagnostic and one log line; and when it fits again
// they return.
func TestADocumentOverItsSizeBoundLeavesTheLiveMachinesOut(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 2, false)
	logs := &logRecorder{}
	snapshotter, clock := newLive(t, &fakeSources{remote: []remotestate.Entry{cachedVM("alex")}}, &fakeExporter{answer: answering(full, full)}, func(options *Options) { options.Logf = logs.logf })
	refreshAndSettle(t, snapshotter)
	without := snapshotter.Payload().Size()
	diagnostics := snapshotter.Document().Diagnostics
	snapshotter.mu.Lock()
	snapshotter.maxDocument = without + 200
	snapshotter.mu.Unlock()

	pollAndSettle(t, snapshotter) // the export publishes outside the lock
	check := func(when string) {
		t.Helper()
		document := snapshotter.Document()
		vm, _ := machineNamed(document, vmKey)
		if vm.Route != RouteCached || vm.RemoteError != RemoteErrorExportTooLarge || vm.WorktreeCount != 1 || document.Diagnostics != diagnostics+1 || snapshotter.Payload().Size() > without+200 {
			t.Fatalf("%s: vm %+v, diagnostics %d, %d bytes", when, vm, document.Diagnostics, snapshotter.Payload().Size())
		}
	}
	check("after the export")
	refreshAndSettle(t, snapshotter) // the pass publishes under the lock
	check("after a pass")
	if logs.count("the fleet document would be over") != 1 {
		t.Errorf("log = %q, want the guard logged once", logs.all())
	}
	snapshotter.mu.Lock()
	snapshotter.maxDocument = defaultMaxDocumentBytes
	snapshotter.mu.Unlock()
	clock.advance(time.Second)
	refreshAndSettle(t, snapshotter)
	document := snapshotter.Document()
	if vm, _ := machineNamed(document, vmKey); vm.Route != RouteLiveRemote || vm.RemoteError != "" || vm.WorktreeCount != 3 || document.Diagnostics != diagnostics {
		t.Fatalf("when it fits again: vm %+v, diagnostics %d", vm, document.Diagnostics)
	}
	// Over the bound again, found by the path that prepares outside the lock.
	snapshotter.mu.Lock()
	snapshotter.maxDocument = without + 200
	snapshotter.mu.Unlock()
	snapshotter.publishUnlocked()
	check("published outside the lock")
	if logs.count("the fleet document would be over") != 2 {
		t.Errorf("log = %q, want the guard logged again", logs.all())
	}
}

// TestAMachineLeftOutForSizeIsNeverDroppedSilently proves that a live machine
// with no published entry stays visible when the size guard leaves its entries
// out: a bare machine entry that says export_too_large. It also proves that a
// document discarded for a later one changes nothing of the guard's state.
func TestAMachineLeftOutForSizeIsNeverDroppedSilently(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 2, false)
	logs := &logRecorder{}
	var snapshotter *Snapshotter
	var interleave atomic.Bool
	snapshotter, _ = newLive(t, oneRepoSources("/repos/widgets"), &fakeExporter{answer: answering(full, full)}, func(options *Options) {
		options.Logf = logs.logf
		options.Compress = func(body []byte) []byte {
			if interleave.CompareAndSwap(true, false) {
				// A later document, which fits, is published while this one,
				// which left the live machines out, is being prepared.
				snapshotter.mu.Lock()
				snapshotter.maxDocument = defaultMaxDocumentBytes
				snapshotter.publishLocked()
				snapshotter.mu.Unlock()
			}
			return body
		}
	})
	refreshAndSettle(t, snapshotter)
	without := snapshotter.Payload().Size()
	pollAndSettle(t, snapshotter)
	if vm, _ := machineNamed(snapshotter.Document(), vmKey); vm.WorktreeCount != 3 || vm.RemoteError != "" {
		t.Fatalf("the fixture's live machine = %+v", vm)
	}
	snapshotter.mu.Lock()
	snapshotter.maxDocument = without + 200
	snapshotter.mu.Unlock()
	interleave.Store(true)
	snapshotter.publishUnlocked()
	snapshotter.mu.RLock()
	leftOut := snapshotter.leftOut
	snapshotter.mu.RUnlock()
	if vm, _ := machineNamed(snapshotter.Document(), vmKey); leftOut || vm.WorktreeCount != 3 || logs.count("the fleet document would be over") != 0 {
		t.Fatalf("a discarded document changed the guard: left out %v, vm %+v, log %q", leftOut, vm, logs.all())
	}

	snapshotter.mu.Lock()
	snapshotter.maxDocument = without + 200
	snapshotter.mu.Unlock()
	snapshotter.publishUnlocked()
	document := snapshotter.Document()
	vm, found := machineNamed(document, vmKey)
	if !found || vm.RemoteError != RemoteErrorExportTooLarge || vm.Route != RouteLiveRemote || vm.WorktreeCount != 0 || len(entriesOf(document, vm.ID)) != 0 {
		t.Fatalf("a live machine left out for size, with no published entry = %+v (found %v), entries %v", vm, found, entriesOf(document, vm.ID))
	}
	if snapshotter.Payload().Size() > without+200 || logs.count("the fleet document would be over") != 1 {
		t.Errorf("%d bytes, log %q", snapshotter.Payload().Size(), logs.all())
	}
}

// TestAPublicationPreparedOutsideTheLockGivesWayToALaterOne proves the two rules
// of publishUnlocked: during a pass it keeps to the pass's publication rate, and
// a document assembled while its body was being prepared wins over it.
func TestAPublicationPreparedOutsideTheLockGivesWayToALaterOne(t *testing.T) {
	t.Parallel()
	var snapshotter *Snapshotter
	var interleave atomic.Bool
	snapshotter, clock := newSnapshotter(oneRepoSources("/repos/widgets").collectors(), func(options *Options) {
		options.Compress = func(body []byte) []byte {
			if interleave.CompareAndSwap(true, false) {
				// Another publication happens while this body is prepared.
				snapshotter.mu.Lock()
				snapshotter.agents = nil
				snapshotter.publishLocked()
				snapshotter.mu.Unlock()
			}
			return body
		}
	})
	refreshAndSettle(t, snapshotter)
	if len(snapshotter.Document().Agents) == 0 {
		t.Fatal("the fixture has no agent")
	}
	published := publishesOf(snapshotter)
	interleave.Store(true)
	snapshotter.publishUnlocked()
	if got := publishesOf(snapshotter); got != published+1 || len(snapshotter.Document().Agents) != 0 {
		t.Fatalf("%d publications and %d agents, want the later document alone", got-published, len(snapshotter.Document().Agents))
	}
	// During a pass that has just published, nothing is published.
	snapshotter.mu.Lock()
	snapshotter.passing, snapshotter.lastPublish = true, clock.Now()
	snapshotter.mu.Unlock()
	published = publishesOf(snapshotter)
	snapshotter.publishUnlocked()
	if got := publishesOf(snapshotter); got != published {
		t.Errorf("a publication inside the pass's rate: %d", got-published)
	}
	clock.advance(publishInterval)
	snapshotter.publishUnlocked()
	if got := publishesOf(snapshotter); got != published+1 {
		t.Errorf("after the rate's interval: %d publications, want 1", got-published)
	}
}

// TestAnHTTPSHubIsVerifiedAndRedirectsNeverCarryTheBearer covers the HTTP
// transport on real connections: an https hub whose certificate does not chain
// to a trusted root is refused and receives no bearer; with its root trusted it
// is read; a 307 or 308, which a client would follow with the same headers, is
// not followed and the other host sees nothing; and a body that stalls is cut
// off by the total timeout.
func TestAnHTTPSHubIsVerifiedAndRedirectsNeverCarryTheBearer(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 2, false)
	now := newClock().Now
	token := tokenFile(t, vmBearer)
	var mu sync.Mutex
	var bearers []string
	secure := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		bearers = append(bearers, request.Header.Get("Authorization"))
		mu.Unlock()
		_, _ = writer.Write(marshalled(t, full))
	}))
	t.Cleanup(secure.Close)
	target := RemoteTarget{Machine: vmKey, HTTP: &HTTPRoute{URL: secure.URL, TokenFile: token}}
	var failure *RemoteError
	if _, err := NewHTTPExporter(now).Export(t.Context(), target, false); !errors.As(err, &failure) || failure.Code != RemoteErrorHTTPUnavailable {
		t.Fatalf("a hub with an untrusted certificate = %v, want it refused", err)
	}
	mu.Lock()
	if len(bearers) != 0 {
		t.Fatalf("the bearer was sent to an unverified hub: %d requests", len(bearers))
	}
	mu.Unlock()
	roots := x509.NewCertPool()
	roots.AddCert(secure.Certificate())
	envelope, err := newHTTPExporter(remoteConnectTimeout, remoteTotalTimeout, now, roots).Export(t.Context(), target, false)
	if err != nil || envelope.Machine != vmOwnName {
		t.Fatalf("a hub with a trusted certificate = %v", err)
	}
	mu.Lock()
	if len(bearers) != 1 || bearers[0] != "Bearer "+vmBearer {
		t.Errorf("the verified hub saw %q", bearers)
	}
	mu.Unlock()

	other := newFakeHub(t, serving(marshalled(t, full), nil))
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect, http.StatusMovedPermanently, http.StatusSeeOther} {
		redirecting := newFakeHub(t, func(writer http.ResponseWriter, request *http.Request) {
			http.Redirect(writer, request, other.server.URL+MachineExportPath, status)
		})
		if _, err := NewHTTPExporter(now).Export(t.Context(), httpTarget(redirecting, token), false); !errors.As(err, &failure) || failure.Code != RemoteErrorHTTPUnavailable || !failure.Fallback {
			t.Errorf("a %d = %v, want http_unavailable", status, err)
		}
	}
	if got := other.seen(); len(got) != 0 {
		t.Fatalf("a redirect was followed: the other host saw %+v", got)
	}

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	stalling := newFakeHub(t, func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`{"schema_version":1,`))
		writer.(http.Flusher).Flush()
		select {
		case <-release:
		case <-request.Context().Done():
		}
	})
	started := time.Now()
	if _, err := newHTTPExporter(time.Second, 100*time.Millisecond, now, nil).Export(t.Context(), httpTarget(stalling, token), false); !errors.As(err, &failure) || failure.Code != RemoteErrorHTTPUnavailable {
		t.Errorf("a body that stalls = %v, want http_unavailable", err)
	}
	if waited := time.Since(started); waited > 5*time.Second {
		t.Errorf("the total timeout took %v", waited)
	}
}

// hostileText is text a remote may put in a free-text field: markup, quotes and
// shell and SQL punctuation. It is text, and must only ever be text.
const hostileText = `<img src=x onerror=alert(1)>"';--$(id)` + "`id`"

// TestHostileTextInEveryFreeTextFieldArrivesOnlyAsText sends, over the HTTP
// path, an envelope with hostile text in every free-text field an export may
// carry. The name the machine gives itself goes nowhere; the others arrive as
// the same text, JSON-escaped in the served body (no raw markup), a failing
// check's name cut at 100 characters; and the same fields holding a control or
// bidirectional character refuse the whole envelope.
func TestHostileTextInEveryFreeTextFieldArrivesOnlyAsText(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 2, false)
	// The free-text fields are the ones with the isText rule.
	fields := map[string]func(*Envelope, string){
		"Envelope.machine": func(e *Envelope, text string) { e.Machine = text },
		"Entry.machine":    func(e *Envelope, text string) { e.Fleet.Worktrees[0].Machine, e.Fleet.Machines[0].Machine = text, text },
		"Repository.name": func(e *Envelope, text string) {
			e.Fleet.Repositories[0].Name, e.Fleet.Repositories[0].RemoteURLWeb = text, ""
		},
		"Repository.default_branch": func(e *Envelope, text string) { e.Fleet.Repositories[0].DefaultBranch = text },
		"Worktree.name":             func(e *Envelope, text string) { e.Fleet.Worktrees[0].Name = text },
		"Worktree.task":             func(e *Envelope, text string) { e.Fleet.Worktrees[0].Task = text },
		"Worktree.stream":           func(e *Envelope, text string) { e.Fleet.Worktrees[0].Stream = text },
		"Worktree.branch":           func(e *Envelope, text string) { e.Fleet.Worktrees[0].Branch = text },
		"PullRequest.branch":        func(e *Envelope, text string) { e.Fleet.PullRequests[0].Branch = text },
		"PullRequest.failed_check":  func(e *Envelope, text string) { e.Fleet.PullRequests[0].FailedCheck = text },
		"Agent.task":                func(e *Envelope, text string) { e.Fleet.Agents[0].Task = text },
	}
	for key, rule := range stringRules {
		if _, covered := fields[key]; reflect.ValueOf(rule).Pointer() == reflect.ValueOf(textRule(isText)).Pointer() && !covered {
			t.Errorf("the free-text field %s is not covered by this test", key)
		}
	}
	read := func(envelope Envelope) (Document, string) {
		hub := newFakeHub(t, serving(marshalled(t, envelope), nil))
		var clock *manualClock
		snapshotter, clock := newSnapshotter(oneRepoSources("/repos/widgets").collectors(), func(options *Options) {
			options.Login = testLogin
			options.Remotes = []RemoteTarget{httpTarget(hub, tokenFile(t, vmBearer))}
			options.Transports = []RemoteTransport{{Name: TransportHTTP, Exporter: NewHTTPExporter(func() time.Time { return clock.Now() })}}
		})
		refreshAndSettle(t, snapshotter)
		pollAndSettle(t, snapshotter)
		body, _ := snapshotter.Body()
		return snapshotter.Document(), string(body)
	}
	hostile := copyEnvelope(t, full)
	for _, set := range fields {
		set(&hostile, hostileText)
	}
	hostile.Fleet.PullRequests[0].FailedCheck = strings.Repeat("c", 150)
	document, body := read(hostile)
	vm, found := machineNamed(document, vmKey)
	if !found || vm.Route != RouteLiveRemote || vm.RemoteError != "" {
		t.Fatalf("the envelope with hostile text = %+v", vm)
	}
	if strings.Contains(body, "<img") || strings.Contains(body, "onerror=alert(1)>") {
		t.Errorf("the served body carries raw markup: %s", body)
	}
	if _, named := machineNamed(document, hostileText); named {
		t.Error("a machine is named as the response named it")
	}
	arrived := 0
	for _, worktree := range document.Worktrees {
		if worktree.MachineID != vm.ID {
			continue
		}
		if worktree.Machine != vmKey {
			t.Errorf("a worktree's machine = %q", worktree.Machine)
		}
		if worktree.Task == hostileText && worktree.Name == hostileText && worktree.Stream == hostileText && worktree.Branch == hostileText {
			arrived++
		}
	}
	for _, repository := range document.Repositories {
		if repository.MachineID == vm.ID && repository.Name == hostileText && repository.DefaultBranch == hostileText && repository.RemoteURLWeb == "" {
			arrived++
		}
	}
	for _, pull := range document.PullRequests {
		if pull.MachineID == vm.ID && pull.Branch == hostileText && pull.FailedCheck == strings.Repeat("c", maxFailedCheckText) && pull.URL == "" {
			arrived++
		}
	}
	for _, agent := range document.Agents {
		if agent.MachineID == vm.ID && agent.Task == hostileText {
			arrived++
		}
	}
	if arrived != 4 {
		t.Errorf("the hostile text arrived intact in %d of 4 entries", arrived)
	}
	for key, set := range fields {
		for name, text := range map[string]string{"a bidirectional override": "task\u202Egnp.exe", "a NUL": "task\x00", "a line break": "task\nSet-Cookie: x", "an escape": "task\x1b[2J"} {
			bad := copyEnvelope(t, full)
			set(&bad, text)
			document, body := read(bad)
			if vm, _ := machineNamed(document, vmKey); vm.RemoteError != RemoteErrorBadPayload || strings.Contains(body, "vm-task") {
				t.Errorf("%s with %s was accepted: %+v", key, name, vm)
			}
		}
	}
}

// setAll gives every settable field under value a value that is not its zero
// value, allocating pointers and one-element slices.
func setAll(value reflect.Value, now time.Time) {
	switch value.Kind() {
	case reflect.String:
		value.SetString("x")
	case reflect.Bool:
		value.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value.SetInt(3)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value.SetUint(3)
	case reflect.Float32, reflect.Float64:
		value.SetFloat(3)
	case reflect.Pointer:
		value.Set(reflect.New(value.Type().Elem()))
		setAll(value.Elem(), now)
	case reflect.Slice:
		value.Set(reflect.MakeSlice(value.Type(), 1, 1))
		setAll(value.Index(0), now)
	case reflect.Struct:
		if value.Type() == timeType {
			value.Set(reflect.ValueOf(now))
			return
		}
		for index := range value.NumField() {
			if field := value.Field(index); field.CanSet() {
				setAll(field, now)
			}
		}
	}
}

// unset lists the exported fields under value that hold their zero value, by
// path.
func unset(value reflect.Value, path string) []string {
	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return []string{path}
		}
		return unset(value.Elem(), path)
	case reflect.Slice:
		if value.Len() == 0 {
			return []string{path}
		}
		return unset(value.Index(0), path+"[0]")
	case reflect.Struct:
		if value.Type() == timeType {
			if value.IsZero() {
				return []string{path}
			}
			return nil
		}
		var missing []string
		for index := range value.NumField() {
			if field := value.Type().Field(index); field.IsExported() {
				missing = append(missing, unset(value.Field(index), path+"."+field.Name)...)
			}
		}
		return missing
	}
	if value.IsZero() {
		return []string{path}
	}
	return nil
}

// TestMapLiveCarriesEveryFieldOfEveryEntryOrSaysWhyNot is the completeness check
// of the live mapping. mapLive copies field by field, so a field added to an
// entry type later is dropped by it unless it is added there too: this test
// gives every field of every entry kind a value, maps the document, and fails
// for any field of a mapped entry that is still zero and is not listed below
// with its reason. It also pins the document's own fields, each of which the
// mapping either uses or deliberately ignores, so a new one needs a decision.
//
// When this fails after a merge: add the field to mapLive under the rule its
// string rule or its sanitiser gives it (and to the decoder's rules), or list
// it here with the reason it must not be carried from another machine.
func TestMapLiveCarriesEveryFieldOfEveryEntryOrSaysWhyNot(t *testing.T) {
	t.Parallel()
	now := newClock().Now()
	notCarried := map[string]string{
		".machine.Transport":    "set at publication, from the transport that produced the entries",
		".machine.RemoteError":  "set at publication, from the last failure",
		".machine.PublishError": "the diagnostic of this machine's own periodic publish: local only, never read from another machine's document",
	}
	var document Document
	setAll(reflect.ValueOf(&document).Elem(), now)
	own := func(id string) Entry {
		return Entry{ID: id, Machine: "theirs", MachineID: "mach-own", Route: RouteLocal, ObservedAt: now}
	}
	document.Machines[0].Entry = own("mach-own")
	document.Repositories[0].Entry, document.Repositories[0].Host, document.Repositories[0].Name = own("repo-1"), "github.com", "acme/x"
	document.Worktrees[0].Entry, document.Worktrees[0].Repository = own("wt-1"), "repo-1"
	pull := &document.PullRequests[0]
	pull.Entry, pull.Repository, pull.Worktree, pull.URL, pull.Mergeable = own("pr-1"), "repo-1", "wt-1", "https://github.com/acme/x/pull/3", "clean"
	// An agent names its worktrees by id: one of them is carried here, one is not.
	document.Agents[0].Entry, document.Agents[0].Repository, document.Agents[0].Worktrees = own("ag-1"), "repo-1", []string{"wt-gone", "wt-1"}

	view := mapLive(vmKey, "mach-vm", &document, now, 1)
	if len(view.repositories) != 1 || len(view.worktrees) != 1 || len(view.pullRequests) != 1 || len(view.agents) != 1 {
		t.Fatalf("the mapping kept %d, %d, %d and %d entries of one each", len(view.repositories), len(view.worktrees), len(view.pullRequests), len(view.agents))
	}
	// An agent's worktree ids are re-derived under the configured key, and one
	// that is no worktree carried here is dropped, never kept as received.
	if agent, source := view.agents[0], document.Agents[0]; !slices.Equal(agent.Worktrees, []string{view.worktrees[0].ID}) || view.worktrees[0].ID == "wt-1" ||
		agent.ExitCode == source.ExitCode || *agent.ExitCode != *source.ExitCode || agent.Activity != source.Activity || agent.Task != source.Task ||
		!agent.StartedAt.Equal(source.StartedAt) || !agent.FinishedAt.Equal(source.FinishedAt) {
		t.Errorf("the agent is mapped as %+v", agent)
	}
	for _, code := range []int{-1, maxCount + 1} {
		odd := document
		odd.Agents = []Agent{document.Agents[0]}
		odd.Agents[0].ExitCode = &code
		if mapped := mapLive(vmKey, "mach-vm", &odd, now, 0); mapped.agents[0].ExitCode != nil {
			t.Errorf("an exit code of %d is carried", code)
		}
	}
	var missing []string
	for path, entry := range map[string]any{
		".machine": view.machine, ".repository": view.repositories[0], ".worktree": view.worktrees[0], ".pull_request": view.pullRequests[0], ".agent": view.agents[0],
	} {
		missing = append(missing, unset(reflect.ValueOf(entry), path)...)
	}
	slices.Sort(missing)
	for _, path := range missing {
		if _, listed := notCarried[path]; !listed {
			t.Errorf("mapLive does not carry %s: copy it there under its rule, or list it in notCarried with the reason", path)
		}
	}
	for path, reason := range notCarried {
		if !slices.Contains(missing, path) {
			t.Errorf("%s is listed as not carried and is carried", path)
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("%s is listed as not carried with no reason", path)
		}
	}
	// The list is pinned: a field added to it is a reviewed change of this number.
	if len(notCarried) != 3 {
		t.Errorf("%d fields are listed as not carried by mapLive, want 3", len(notCarried))
	}
	if document.Throughput == nil {
		t.Fatal("the filled document has no throughput: the check below would be vacuous")
	}

	// The same check on the published document, where the links are rebuilt
	// (relink, locallyLinked): every field of a live machine's entries arrives
	// unless it is listed here with its reason.
	notPublished := map[string]string{
		".machine.RemoteError":  "empty: the last export succeeded",
		".machine.PublishError": "local only: the diagnostic of this machine's own publish, never carried for another machine",
	}
	snapshotter, _ := newLive(t, oneRepoSources("/repos/widgets"), &fakeExporter{answer: failing(errBoom)}, nil)
	refreshAndSettle(t, snapshotter)
	snapshotter.mu.Lock()
	machine := snapshotter.live[vmKey]
	id := snapshotter.liveMachineID(vmKey, nil)
	machine.fleet, machine.receivedAt, machine.observedAt, machine.transport, machine.dropped = &document, now, now, TransportHTTP, 1
	machine.view, machine.mappedFor = mapLive(vmKey, id, &document, now, 1), id
	snapshotter.publishLocked()
	published := snapshotter.doc
	snapshotter.mu.Unlock()
	entries := map[string]any{}
	for _, item := range published.Machines {
		if item.ID == id {
			entries[".machine"] = item
		}
	}
	for _, item := range published.Repositories {
		if item.MachineID == id {
			entries[".repository"] = item
		}
	}
	for _, item := range published.Worktrees {
		if item.MachineID == id {
			entries[".worktree"] = item
		}
	}
	for _, item := range published.PullRequests {
		if item.MachineID == id {
			entries[".pull_request"] = item
		}
	}
	for _, item := range published.Agents {
		if item.MachineID == id {
			entries[".agent"] = item
		}
	}
	if len(entries) != 5 {
		t.Fatalf("the published document has %d of the live machine's 5 kinds of entry", len(entries))
	}
	missing = nil
	for path, entry := range entries {
		missing = append(missing, unset(reflect.ValueOf(entry), path)...)
	}
	slices.Sort(missing)
	for _, path := range missing {
		if strings.TrimSpace(notPublished[path]) == "" {
			t.Errorf("the published document does not carry %s of a live machine: carry it, or list it in notPublished with the reason", path)
		}
	}
	for path := range notPublished {
		if !slices.Contains(missing, path) {
			t.Errorf("%s is listed as not published and is published", path)
		}
	}
	if len(notPublished) != 2 {
		t.Errorf("%d fields are listed as not published, want 2", len(notPublished))
	}

	// The document's own fields: the collections are mapped, and each other field
	// is named here with what the mapping does with it.
	handled := map[string]string{
		"machines": "mapped", "repositories": "mapped", "worktrees": "mapped", "pull_requests": "mapped", "agents": "mapped",
		"snapshot_at":              "the entries' observed_at",
		"warming_up":               "a warming fleet is never taken",
		"agents_truncated":         "the machine entry's agents_truncated",
		"schema_version":           "checked by the decoder",
		"repositories_total":       "ignored: a count of the remote's own scan",
		"repositories_scanned":     "ignored: a count of the remote's own scan",
		"diagnostics":              "ignored: the remote's own diagnostics",
		"error":                    "ignored: the remote's own scan error",
		"code_index_provider":      "ignored: the remote's own configuration",
		"refresh_interval_seconds": "ignored: freshness is measured by this daemon's interval",
		"pull_requests_throttled":  "ignored: the remote's own observation budget",
		"throughput":               "not carried: local only, never exported (NewEnvelope clears it) and never merged",
	}
	for _, field := range jsonFields(Document{}) {
		if _, known := handled[field]; !known {
			t.Errorf("the document gained %q: decide what a live-remote machine's value of it does in mapLive, and name it in this test", field)
		}
	}
	if len(handled) != len(jsonFields(Document{})) {
		t.Errorf("this test names %d document fields and the document has %d", len(handled), len(jsonFields(Document{})))
	}
}

// TestNoEntryOfAnotherMachineCarriesAnIDOfThisMachine pins the one marker a
// client has of whose word a field is. Another machine sends, live, this
// machine's own entries back under its own machine entry (every id but the
// machine's is one of this machine's), with sync facts and a merged pull
// request of its own choosing, and publishes a snapshot that names this
// machine's repository and task. In the document every entry of this machine
// has route `local` and this machine's id, every other entry has another route
// and another machine id, no id of this machine's entries appears on an entry
// of another route, and the facts the other machine reported are carried only
// on entries whose route says they are reported.
func TestNoEntryOfAnotherMachineCarriesAnIDOfThisMachine(t *testing.T) {
	t.Parallel()
	local, _ := newSnapshotter(vmSources().collectors(), nil)
	refreshAndSettle(t, local)
	own := local.Document()
	localIDs := map[string]bool{}
	for _, id := range allIDs(own) {
		// allIDs names each id with its kind.
		localIDs[id[strings.LastIndex(id, " ")+1:]] = true
	}
	ownWorktrees, ownPulls := map[string]Worktree{}, map[string]PullRequest{}
	for _, worktree := range own.Worktrees {
		ownWorktrees[worktree.ID] = worktree
	}
	for _, pull := range own.PullRequests {
		ownPulls[pull.ID] = pull
	}
	// The hostile export: this machine's own export, under another machine entry.
	hostile := local.Export(false)
	foreignMachine := entryID(kindMachine, "somebody-else")
	hostile.Machine = "somebody-else"
	fleet := *hostile.Fleet
	hostile.Fleet = &fleet
	fleet.Machines = []Machine{fleet.Machines[0]}
	fleet.Machines[0].ID, fleet.Machines[0].MachineID = foreignMachine, foreignMachine
	ahead, green, upstream := 0, true, true
	fleet.Repositories, fleet.Worktrees, fleet.PullRequests, fleet.Agents = slices.Clone(fleet.Repositories), slices.Clone(fleet.Worktrees), slices.Clone(fleet.PullRequests), slices.Clone(fleet.Agents)
	for index := range fleet.Repositories {
		fleet.Repositories[index].MachineID = foreignMachine
	}
	for index := range fleet.Worktrees {
		fleet.Worktrees[index].MachineID = foreignMachine
		fleet.Worktrees[index].Ahead, fleet.Worktrees[index].Behind, fleet.Worktrees[index].HasUpstream, fleet.Worktrees[index].Lifecycle = &ahead, &ahead, &upstream, "merged"
	}
	for index := range fleet.PullRequests {
		fleet.PullRequests[index].MachineID = foreignMachine
		fleet.PullRequests[index].State, fleet.PullRequests[index].Mergeable, fleet.PullRequests[index].ChecksGreen = "merged", "clean", &green
	}
	for index := range fleet.Agents {
		fleet.Agents[index].MachineID = foreignMachine
	}
	if err := hostile.Validate(false, newClock().Now()); err != nil {
		t.Fatalf("the hostile export is refused at the boundary, so it tests nothing: %v", err)
	}
	published := remotestate.Entry{Snapshot: remotestate.Snapshot{
		Login: "mallory", Machine: testMachine, PublishedAt: newClock().Now().Add(-time.Hour), KnownRepositories: []string{"acme/engine"},
		Worktrees: []remotestate.WorktreeState{{Task: "vm-task-1", Repository: "acme/engine", Branch: "feature/vm-1", Lifecycle: "merged"}},
	}}
	sources := vmSources()
	sources.remote = []remotestate.Entry{published}
	reader, _ := newLive(t, sources, &fakeExporter{answer: answering(hostile, hostile)}, nil)
	refreshAndSettle(t, reader)
	pollAndSettle(t, reader)
	document := reader.Document()
	requireUniqueIDs(t, document)
	machineID := localMachineID(testMachine)
	routes := map[string]int{}
	check := func(kind string, entry Entry) {
		t.Helper()
		routes[entry.Route]++
		isLocal := entry.Route == RouteLocal
		if isLocal != (entry.MachineID == machineID) || isLocal != localIDs[entry.ID] {
			t.Errorf("%s %s has route %s, machine id %s (this machine's: %v), an id of this machine's entries: %v", kind, entry.ID, entry.Route, entry.MachineID, entry.MachineID == machineID, localIDs[entry.ID])
		}
	}
	for _, machine := range document.Machines {
		check("machine", machine.Entry)
	}
	for _, repository := range document.Repositories {
		check("repository", repository.Entry)
	}
	for _, worktree := range document.Worktrees {
		check("worktree", worktree.Entry)
		// What this machine observed of its own worktree is untouched by what the
		// other machine said of it.
		if observed := ownWorktrees[worktree.ID]; worktree.Route == RouteLocal && (worktree.Lifecycle != observed.Lifecycle || !reflect.DeepEqual(worktree.Ahead, observed.Ahead) || !reflect.DeepEqual(worktree.HasUpstream, observed.HasUpstream)) {
			t.Errorf("a local worktree carries what another machine reported: %+v, observed %+v", worktree, observed)
		}
	}
	for _, pull := range document.PullRequests {
		check("pull request", pull.Entry)
		if observed := ownPulls[pull.ID]; pull.Route == RouteLocal && (pull.State != observed.State || pull.State == "merged" || pull.ChecksGreen != nil || pull.Mergeable != "") {
			t.Errorf("a local pull request carries what another machine reported: %+v, observed %+v", pull, observed)
		}
	}
	for _, agent := range document.Agents {
		check("agent", agent.Entry)
	}
	if routes[RouteLocal] == 0 || routes[RouteLiveRemote] < 5 || routes[RouteCached] < 3 {
		t.Fatalf("entries by route = %v, want this machine's, the live machine's and the published one's (the test would be vacuous)", routes)
	}
	// The reported facts are carried, on the entries whose route says so.
	reported := 0
	for _, pull := range document.PullRequests {
		if pull.Route == RouteLiveRemote && pull.State == "merged" && pull.ChecksGreen != nil && pull.Mergeable == "clean" {
			reported++
		}
	}
	if reported != 1 {
		t.Errorf("%d live-remote pull requests carry the other machine's report, want 1", reported)
	}
}
