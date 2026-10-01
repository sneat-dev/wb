package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/cockpit/machinemetrics"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/periodic"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// fakePublisher counts the hand-offs of the snapshotter and captures what the
// publisher would have been given.
type fakePublisher struct {
	mu     sync.Mutex
	calls  int
	extras []remotestate.Extras
	panics bool
	block  chan struct{}
	diag   string
	tokens []string
}

func (p *fakePublisher) Diagnostic() string { p.mu.Lock(); defer p.mu.Unlock(); return p.diag }

func (p *fakePublisher) Publish(_ context.Context, source remotestate.PublishSource) {
	if p.block != nil {
		<-p.block
	}
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	if p.panics {
		panic("publisher panicked")
	}
	got := source.PublishExtras()
	p.mu.Lock()
	p.tokens = append(p.tokens, source.ChangeToken())
	p.extras = append(p.extras, got)
	p.mu.Unlock()
}

func (p *fakePublisher) count() int { p.mu.Lock(); defer p.mu.Unlock(); return p.calls }

func publishedAt(t *testing.T) time.Time {
	t.Helper()
	published, err := time.Parse(time.RFC3339, remotePublish)
	if err != nil {
		t.Fatal(err)
	}
	return published
}

func remoteWith(t *testing.T, change func(*remotestate.Snapshot)) []remotestate.Entry {
	t.Helper()
	snapshot := remotestate.Snapshot{Login: "someone", Machine: "vm", PublishedAt: publishedAt(t), KnownRepositories: []string{"acme/gadgets"}}
	change(&snapshot)
	return []remotestate.Entry{{Snapshot: snapshot}}
}

func TestPeriodicPublishIsHandedTheEndOfEachSuccessfulPass(t *testing.T) {
	t.Parallel()
	publisher := &fakePublisher{}
	snapshotter, _ := newSnapshotter(oneRepoSources("/repo/widgets").collectors(), func(o *Options) { o.Publisher = publisher })
	refreshAndSettle(t, snapshotter)
	if publisher.count() != 1 {
		t.Fatalf("hand-offs after one pass = %d", publisher.count())
	}
	refreshAndSettle(t, snapshotter)
	if publisher.count() != 2 {
		t.Fatalf("hand-offs after two passes = %d", publisher.count())
	}
}

func TestPeriodicPublishIsNotHandedAFailedOrCancelledPass(t *testing.T) {
	t.Parallel()
	publisher := &fakePublisher{}
	sources := oneRepoSources("/repo/widgets")
	sources.repoErr = errors.New("cannot list")
	snapshotter, _ := newSnapshotter(sources.collectors(), func(o *Options) { o.Publisher = publisher })
	if err := snapshotter.Refresh(t.Context()); err == nil {
		t.Fatal("a failed listing reported no error")
	}
	snapshotter.side.Wait()
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	healthy, _ := newSnapshotter(oneRepoSources("/repo/widgets").collectors(), func(o *Options) { o.Publisher = publisher })
	_ = healthy.Refresh(cancelled)
	healthy.side.Wait()
	if publisher.count() != 0 {
		t.Fatalf("a failed or cancelled pass handed %d times", publisher.count())
	}
}

func TestPeriodicPublishNeverDelaysOrBreaksTheLocalSnapshot(t *testing.T) {
	t.Parallel()
	blocked := &fakePublisher{block: make(chan struct{})}
	snapshotter, _ := newSnapshotter(oneRepoSources("/repo/widgets").collectors(), func(o *Options) { o.Publisher = blocked })
	done := make(chan error, 1)
	go func() { done <- snapshotter.Refresh(t.Context()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the pass waited for a publish that is still running")
	}
	if snapshotter.Document().WarmingUp {
		t.Fatal("the local document is not complete while a publish runs")
	}
	// A second pass does not start a second hand-off while one is running.
	if err := snapshotter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	close(blocked.block)
	snapshotter.side.Wait()
	if blocked.count() != 1 {
		t.Fatalf("hand-offs = %d, want one at a time", blocked.count())
	}
	// A publisher that panics is a failed publish, not a failed daemon.
	panicking := &fakePublisher{panics: true}
	another, _ := newSnapshotter(oneRepoSources("/repo/widgets").collectors(), func(o *Options) { o.Publisher = panicking })
	refreshAndSettle(t, another)
	if panicking.count() != 1 {
		t.Fatal("the publisher was not asked")
	}
}

func TestPublishExtrasAreThisMachinesAgentsAndLatestSample(t *testing.T) {
	t.Parallel()
	publisher := &fakePublisher{}
	sources := oneRepoSources("/repo/widgets")
	finished := newClock().Now().Add(-time.Hour)
	dispatched := agents.Result{AgentID: "agt-1", State: agents.StateRunning, Repository: "acme/widgets", Worktree: "task-a", Branch: "feature/a", StartedAt: newClock().Now().Add(-time.Hour), FinishedAt: &finished}
	dispatched.Resolved.Harness, dispatched.Resolved.Model = "codex", "gpt"
	sources.runs = []agents.Result{dispatched}
	sources.remote[0].Snapshot.Agents = []remotestate.AgentState{{Kind: "run", RunID: "remote", State: "running"}}
	snapshotter, _ := newSnapshotter(sources.collectors(), func(o *Options) {
		o.Publisher = publisher
		o.Sampler = filledSampler(t, &countingSource{}, 3)
	})
	refreshAndSettle(t, snapshotter)
	if len(publisher.extras) != 1 {
		t.Fatalf("extras = %d", len(publisher.extras))
	}
	got := publisher.extras[0]
	var run, sess *remotestate.AgentState
	for index := range got.Agents {
		switch got.Agents[index].Kind {
		case "run":
			run = &got.Agents[index]
		case "session":
			sess = &got.Agents[index]
		}
	}
	if run == nil || run.RunID != "agt-1" || run.Runtime != "codex" || run.Repository != "acme/widgets" || run.Task != "task-a" || run.State != "running" {
		t.Fatalf("run = %+v", run)
	}
	if sess == nil || sess.SessionID != "wbs-1" || sess.Runtime != "claude" || sess.Model != "opus" {
		t.Fatalf("session = %+v", sess)
	}
	if got.Metrics == nil || got.Metrics.Load1 == nil || *got.Metrics.Load1 != 3 || got.Metrics.SampledAt.IsZero() {
		t.Fatalf("metrics = %+v", got.Metrics)
	}
	// Other machines' cached agents are never re-published as this machine's.
	for _, agent := range got.Agents {
		if agent.RunID == "remote" {
			t.Fatal("a cached agent was published")
		}
	}
	// With no sampler, or no sample yet, there is no metrics.
	none, _ := newSnapshotter(oneRepoSources("/repo/widgets").collectors(), nil)
	refreshAndSettle(t, none)
	if extras := none.PublishExtras(); extras.Metrics != nil {
		t.Fatalf("no sampler published %+v", extras.Metrics)
	}
	empty, _ := newSnapshotter(oneRepoSources("/repo/widgets").collectors(), func(o *Options) {
		o.Sampler = machinemetrics.New(machinemetrics.Options{Source: &countingSource{}})
	})
	if empty.PublishExtras().Metrics != nil {
		t.Fatal("a sampler without a sample published one")
	}
}

func TestRemoteAgentsAreCachedWithTheSnapshotsAgeAndNoActions(t *testing.T) {
	t.Parallel()
	published := publishedAt(t)
	entries := remoteWith(t, func(s *remotestate.Snapshot) {
		s.Agents = []remotestate.AgentState{
			{Kind: "session", SessionID: "wbs-9", Runtime: "claude", Model: "opus", State: "live", Activity: "blocked", Task: "fix-ci", Repository: "acme/gadgets", StartedAt: published.Add(-time.Hour)},
			{Kind: "run", RunID: "agt-9", State: "completed", Activity: sentinel + "bad", Repository: "acme/unknown", StartedAt: published.Add(48 * time.Hour)},
			{Kind: "daemon", State: "live"},
			{Kind: "session", State: "exploded"},
			{Kind: "run", State: "running"},
		}
	})
	sources := oneRepoSources("/repo/widgets")
	sources.remote = entries
	snapshotter, _ := newSnapshotter(sources.collectors(), nil)
	refreshAndSettle(t, snapshotter)
	var cached []Agent
	for _, agent := range snapshotter.Document().Agents {
		if agent.Route == RouteCached {
			cached = append(cached, agent)
		}
	}
	if len(cached) != 3 {
		t.Fatalf("cached agents = %+v", cached)
	}
	byKey := map[string]Agent{}
	for _, agent := range cached {
		byKey[agent.SessionID+agent.RunID+agent.State] = agent
	}
	session := byKey["wbs-9live"]
	if session.Machine != "vm" || !session.ObservedAt.Equal(published) || session.Runtime != "claude" || session.Task != "fix-ci" || session.Activity != ActivityBlocked ||
		session.Repository == "" || session.MachineID == "" || len(session.Worktrees) != 0 || session.ExitCode != nil || !session.StartedAt.Equal(published.Add(-time.Hour)) {
		t.Fatalf("cached session = %+v", session)
	}
	run := byKey["agt-9completed"]
	if run.Activity != "" || run.Repository != "" || !run.StartedAt.IsZero() {
		t.Fatalf("an out-of-set activity, an unknown repository and a future start must be dropped: %+v", run)
	}
	unnamed := byKey["running"]
	if unnamed.ID == "" || unnamed.ID == run.ID || unnamed.ID == session.ID {
		t.Fatalf("an agent with no identifier needs its own id: %+v", unnamed)
	}
	// The document marshals nothing that could be an action.
	body, _ := json.Marshal(cached)
	for _, forbidden := range []string{"action", "stop", "log", sentinel} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("cached agents carry %q: %s", forbidden, body)
		}
	}
	if snapshotter.Document().AgentsTruncated {
		t.Error("agents_truncated set without a cut")
	}
}

func TestRemoteAgentsAreCappedAndTheCutIsOnThatMachinesEntry(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 5000)
	entries := remoteWith(t, func(s *remotestate.Snapshot) {
		for index := range 500 {
			s.Agents = append(s.Agents, remotestate.AgentState{Kind: "run", RunID: "agt-" + strconv.Itoa(index), Runtime: long, Model: long, State: "running", Task: long, Repository: long})
		}
	})
	sources := oneRepoSources("/repo/widgets")
	sources.remote = entries
	snapshotter, _ := newSnapshotter(sources.collectors(), nil)
	refreshAndSettle(t, snapshotter)
	count := 0
	for _, agent := range snapshotter.Document().Agents {
		if agent.Route != RouteCached {
			continue
		}
		count++
		// Strings that fail the rules are blanked, never kept long.
		if agent.Runtime != "" || agent.Model != "" || agent.Task != "" || agent.Repository != "" {
			t.Fatalf("an over-long string was kept: %+v", agent)
		}
	}
	vm, _ := machineNamed(snapshotter.Document(), "vm")
	// The cut is on the machine it cut, and the document-level flag is this machine's own.
	if count != 200 || !vm.AgentsTruncated || snapshotter.Document().AgentsTruncated {
		t.Fatalf("cached agents = %d, vm truncated = %v, document truncated = %v", count, vm.AgentsTruncated, snapshotter.Document().AgentsTruncated)
	}
}

func TestAPublishedTruncationMarkerAndInvalidAgentsBeyondTheCap(t *testing.T) {
	t.Parallel()
	run := func(change func(*remotestate.Snapshot)) Machine {
		sources := oneRepoSources("/repo/widgets")
		sources.remote = remoteWith(t, change)
		snapshotter, _ := newSnapshotter(sources.collectors(), nil)
		refreshAndSettle(t, snapshotter)
		vm, _ := machineNamed(snapshotter.Document(), "vm")
		return vm
	}
	// The publisher's own marker is carried.
	if vm := run(func(s *remotestate.Snapshot) {
		s.Agents, s.AgentsTruncated = []remotestate.AgentState{{Kind: "run", RunID: "a", State: "running"}}, true
	}); !vm.AgentsTruncated {
		t.Error("a published truncation marker was lost")
	}
	// Exactly the cap, then invalid agents: not a cut.
	if vm := run(func(s *remotestate.Snapshot) {
		for index := range 200 {
			s.Agents = append(s.Agents, remotestate.AgentState{Kind: "run", RunID: "a" + strconv.Itoa(index), State: "running"})
		}
		s.Agents = append(s.Agents, remotestate.AgentState{Kind: "run", State: "bogus"})
	}); vm.AgentsTruncated {
		t.Error("an invalid 201st agent counted as a cut")
	}
	if vm := run(func(*remotestate.Snapshot) {}); vm.AgentsTruncated {
		t.Error("no agents, yet truncated")
	}
}

func TestACutCachedMachineDoesNotMarkThisMachinesDocumentAndHidesWithItsMachine(t *testing.T) {
	t.Parallel()
	published := cachedVM("alex")
	for index := range 250 {
		published.Snapshot.Agents = append(published.Snapshot.Agents, remotestate.AgentState{Kind: "run", RunID: "a" + strconv.Itoa(index), State: "running"})
	}
	sources := oneRepoSources("/repos/widgets")
	sources.remote = append(sources.remote, published)
	full := exportOf(t, vmOwnName, vmSources(), 4, false)
	live, clock := newLive(t, sources, &fakeExporter{answer: answering(full, full)}, nil)
	refreshAndSettle(t, live)
	if vm, _ := machineNamed(live.Document(), vmKey); !vm.AgentsTruncated || live.Document().AgentsTruncated {
		t.Fatalf("cached: machine %v, document %v", vm.AgentsTruncated, live.Document().AgentsTruncated)
	}
	clock.advance(7 * time.Second)
	pollAndSettle(t, live)
	vm, _ := machineNamed(live.Document(), vmKey)
	if vm.Route != RouteLiveRemote || vm.AgentsTruncated || live.Document().AgentsTruncated {
		t.Fatalf("behind a live read the cut stayed: machine %+v, document %v", vm, live.Document().AgentsTruncated)
	}
}

func TestOnlyLocalEntriesAreExportedWhateverIsCached(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources("/repo/widgets")
	sources.remote = remoteWith(t, func(s *remotestate.Snapshot) {
		s.Agents = []remotestate.AgentState{{Kind: "run", RunID: "agt-remote", State: "running", Runtime: sentinel + "rt"}}
		s.Metrics = &remotestate.MetricsSample{Load1: ptr(1.0), SampledAt: publishedAt(t)}
	})
	snapshotter, _ := newSnapshotter(sources.collectors(), func(o *Options) { o.Sampler = filledSampler(t, &countingSource{}, 2) })
	refreshAndSettle(t, snapshotter)
	for _, metricsOnly := range []bool{false, true} {
		body, err := json.Marshal(snapshotter.Export(metricsOnly))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "agt-remote") || strings.Contains(string(body), sentinel) || strings.Contains(string(body), `"cached"`) {
			t.Fatalf("the export carries cached data: %s", body)
		}
	}
}

func TestPublishedSampleServesTheCachedMetricsSource(t *testing.T) {
	t.Parallel()
	published := publishedAt(t)
	sources := oneRepoSources("/repo/widgets")
	sources.remote = remoteWith(t, func(s *remotestate.Snapshot) {
		s.Metrics = &remotestate.MetricsSample{CPUPercent: ptr(85.0), Load1: ptr(2.5), MemoryUsedBytes: ptr(uint64(10)), MemoryTotalBytes: ptr(uint64(20)), DiskFreeBytes: ptr(uint64(5)), DiskTotalBytes: ptr(uint64(50)), SampledAt: published}
	})
	snapshotter, clock := newSnapshotter(sources.collectors(), nil)
	refreshAndSettle(t, snapshotter)
	server := newCockpitServer(t, snapshotter)
	id := machineIDOf(t, snapshotter, "vm")
	var body MetricsResponse
	recorder := server.get(metricsURL+id, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d %s", recorder.Code, recorder.Body.String())
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Route != RouteCached || body.FetchedAt != nil || len(body.Samples) != 1 || !body.Samples[0].SampledAt.Equal(published) ||
		body.Samples[0].CPUPercent == nil || *body.Samples[0].CPUPercent != 85 || body.Reason != "" {
		t.Fatalf("cached answer = %+v", body)
	}
	// A new publication replaces the sample and the body is built again.
	sources.change(func(f *fakeSources) {
		f.remote[0].Snapshot.Metrics = &remotestate.MetricsSample{Load1: ptr(9.0), SampledAt: published.Add(time.Minute)}
	})
	clock.advance(time.Minute)
	refreshAndSettle(t, snapshotter)
	if err := json.Unmarshal(server.get(metricsURL+id, nil).Body.Bytes(), &body); err != nil || *body.Samples[0].Load1 != 9 {
		t.Fatalf("after a new publication = %+v %v", body, err)
	}
	// A machine whose snapshot carries no sample, or only an unusable one, has none.
	for name, sample := range map[string]*remotestate.MetricsSample{"absent": nil, "no time": {Load1: ptr(1.0)}, "future": {Load1: ptr(1.0), SampledAt: clock.Now().Add(time.Hour)}, "no data": {SampledAt: published}} {
		sources.change(func(f *fakeSources) { f.remote[0].Snapshot.Metrics = sample })
		refreshAndSettle(t, snapshotter)
		if err := json.Unmarshal(server.get(metricsURL+id, nil).Body.Bytes(), &body); err != nil || body.Route != RouteNone || body.Reason != ReasonNoSource || len(body.Samples) != 0 {
			t.Errorf("%s: answer = %+v %v", name, body, err)
		}
	}
}

func TestACachedSampleAnswersAfterALiveRemoteOne(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources("/repo/widgets")
	sources.remote = remoteWith(t, func(s *remotestate.Snapshot) {
		s.Metrics = &remotestate.MetricsSample{Load1: ptr(2.5), SampledAt: publishedAt(t)}
	})
	live := &fakeMetrics{answers: map[string]MetricsAnswer{}}
	snapshotter, _ := newSnapshotter(sources.collectors(), func(o *Options) { o.Metrics = []MetricsSource{live} })
	refreshAndSettle(t, snapshotter)
	id := machineIDOf(t, snapshotter, "vm")
	server := newCockpitServer(t, snapshotter)
	route := func() string {
		var body MetricsResponse
		if err := json.Unmarshal(server.get(metricsURL+id, nil).Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Route
	}
	if route() != RouteCached {
		t.Fatalf("with no live source answering the route is %s", route())
	}
	fetched := newClock().Now()
	live.answers[id] = MetricsAnswer{Route: RouteLiveRemote, FetchedAt: &fetched, Samples: []machinemetrics.Sample{{Load1: ptr(1.0), SampledAt: fetched}}, Version: 1}
	if route() != RouteLiveRemote {
		t.Fatalf("a live answer lost to the cached one: %s", route())
	}
}

func TestPublishedAgentAndSampleStringsAreSentinelFree(t *testing.T) {
	t.Parallel()
	// Every string of the agent is a sentinel that fails its field's rule (a space,
	// a path and an environment-looking value): the identifying fields are valid so
	// the agent is kept, and none of the sentinel may reach the document.
	sources := sentinelSources()
	entry := &sources.remote[0].Snapshot
	hostile := sentinel + "x /Users/x HOME=/y"
	entry.Agents = []remotestate.AgentState{{
		Kind: "run", State: "running", RunID: "agt-hostile", Runtime: hostile, Model: hostile, Activity: hostile, Task: hostile, Repository: hostile,
	}}
	entry.Metrics = &remotestate.MetricsSample{SampledAt: publishedAt(t)}
	snapshotter, _ := newSnapshotter(sources.collectors(), nil)
	refreshAndSettle(t, snapshotter)
	body, _ := json.Marshal(snapshotter.Document())
	if strings.Contains(string(body), sentinel+"x") || strings.Contains(string(body), "/Users/x") || strings.Contains(string(body), "HOME=") {
		t.Fatalf("a hostile agent string reached the document: %s", body)
	}
	kept := false
	for _, agent := range snapshotter.Document().Agents {
		if agent.RunID == "agt-hostile" {
			kept = true
			if agent.Runtime != "" || agent.Model != "" || agent.Activity != "" || agent.Task != "" || agent.Repository != "" {
				t.Fatalf("a failing field was kept: %+v", agent)
			}
		}
	}
	if !kept {
		t.Fatal("the agent with valid identifying fields was dropped: the test would be vacuous")
	}
	if answer, ok := (cachedMetricsSource{snapshotter}).MachineMetrics(machineIDOf(t, snapshotter, "desktop")); ok {
		t.Fatalf("a sample with no measurement was served: %+v", answer)
	}
}

func TestPublishedSampleIsMadeFit(t *testing.T) {
	t.Parallel()
	now := newClock().Now()
	sample, ok := publishedSample(&remotestate.MetricsSample{CPUPercent: ptr(150.0), Load1: ptr(-1.0), MemoryUsedBytes: ptr(uint64(9)), MemoryTotalBytes: ptr(uint64(2)), DiskFreeBytes: ptr(uint64(1)), DiskTotalBytes: ptr(uint64(2)), SampledAt: now}, now)
	if !ok || sample.CPUPercent != nil || sample.Load1 != nil || sample.MemoryUsedBytes != nil || sample.DiskFreeBytes == nil {
		t.Fatalf("sample = %+v ok=%v", sample, ok)
	}
	if _, ok := publishedSample(nil, now); ok {
		t.Error("a missing sample was usable")
	}
}

func TestADaemonWithoutAPublisherPublishesNothing(t *testing.T) {
	t.Parallel()
	snapshotter, _ := newSnapshotter(oneRepoSources("/repo/widgets").collectors(), nil)
	refreshAndSettle(t, snapshotter)
	if snapshotter.publisher != nil {
		t.Fatal("a publisher appeared without being configured")
	}
	// startPublish with none is a no-op.
	snapshotter.startPublish(t.Context())
}

func TestChangeTokenMovesWithWhatThePublisherMustSeeAndWithNothingElse(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources("/repo/widgets")
	snapshotter, clock := newSnapshotter(sources.collectors(), func(o *Options) { o.Fingerprint = newConstFingerprint("fp-1").get })
	if snapshotter.ChangeToken() != "" {
		t.Fatal("a token before the first pass")
	}
	refreshAndSettle(t, snapshotter)
	first := snapshotter.ChangeToken()
	if first == "" {
		t.Fatal("no token after a pass")
	}
	clock.advance(time.Hour)
	refreshAndSettle(t, snapshotter)
	if again := snapshotter.ChangeToken(); again != first {
		t.Fatal("the token moved with the clock alone")
	}
	// A moved fingerprint, a worktree whose owner went away and a pull request
	// that went away each move it.
	snapshotter.fingerprint = newConstFingerprint("fp-2").get
	refreshAndSettle(t, snapshotter)
	second := snapshotter.ChangeToken()
	if second == first {
		t.Fatal("a moved fingerprint did not move the token")
	}
	sources.change(func(f *fakeSources) {
		record := f.records["/wt/task-a"]
		record.Owner = worktrees.OwnerGone
		f.records["/wt/task-a"] = record
	})
	refreshAndSettle(t, snapshotter)
	third := snapshotter.ChangeToken()
	if third == second {
		t.Fatal("a worktree whose owner went away did not move the token")
	}
	sources.change(func(f *fakeSources) { f.bindings = nil })
	refreshAndSettle(t, snapshotter)
	if snapshotter.ChangeToken() == third {
		t.Fatal("a pull request that went away did not move the token")
	}
}

func TestThePublisherIsHandedTheSnapshotterAsItsSource(t *testing.T) {
	t.Parallel()
	publisher := &fakePublisher{}
	snapshotter, _ := newSnapshotter(oneRepoSources("/repo/widgets").collectors(), func(o *Options) {
		o.Publisher = publisher
		o.Fingerprint = newConstFingerprint("fp").get
	})
	refreshAndSettle(t, snapshotter)
	if len(publisher.tokens) != 1 || publisher.tokens[0] == "" {
		t.Fatalf("tokens = %v", publisher.tokens)
	}
}

func TestPublishErrorIsShownOnThisMachinesEntryOnlyWhileUnhealthy(t *testing.T) {
	t.Parallel()
	publisher := &fakePublisher{diag: "publish_failed"}
	snapshotter, _ := newSnapshotter(oneRepoSources("/repo/widgets").collectors(), func(o *Options) { o.Publisher = publisher })
	refreshAndSettle(t, snapshotter)
	local := func() Machine {
		for _, machine := range snapshotter.Document().Machines {
			if machine.Route == RouteLocal {
				return machine
			}
		}
		t.Fatal("no local machine")
		return Machine{}
	}
	if got := local().PublishError; got != "publish_failed" {
		t.Fatalf("publish_error = %q", got)
	}
	for _, machine := range snapshotter.Document().Machines {
		if machine.Route != RouteLocal && machine.PublishError != "" {
			t.Fatalf("a cached machine carries publish_error: %+v", machine)
		}
	}
	// Healthy again: absent.
	publisher.mu.Lock()
	publisher.diag = ""
	publisher.mu.Unlock()
	refreshAndSettle(t, snapshotter)
	if got := local().PublishError; got != "" {
		t.Fatalf("a healthy publisher leaves publish_error %q", got)
	}
	// Every code of the closed list is shown; anything else is dropped.
	for _, code := range publishErrorCodes {
		publisher.mu.Lock()
		publisher.diag = code
		publisher.mu.Unlock()
		refreshAndSettle(t, snapshotter)
		if got := local().PublishError; got != code {
			t.Errorf("code %q shown as %q", code, got)
		}
	}
	publisher.mu.Lock()
	publisher.diag = sentinel + "/Users/alex/secret"
	publisher.mu.Unlock()
	refreshAndSettle(t, snapshotter)
	if got := local().PublishError; got != "" {
		t.Fatalf("an unknown code reached the document: %q", got)
	}
	// With publishing off there is none.
	off, _ := newSnapshotter(oneRepoSources("/repo/widgets").collectors(), nil)
	refreshAndSettle(t, off)
	for _, machine := range off.Document().Machines {
		if machine.PublishError != "" {
			t.Fatalf("publishing off shows %q", machine.PublishError)
		}
	}
}

func TestThePublishErrorCodesAreThePublishersDiagnostics(t *testing.T) {
	t.Parallel()
	want := []string{periodic.DiagnosticCollectFailed, periodic.DiagnosticOpenFailed, periodic.DiagnosticPublishFailed, periodic.DiagnosticOptionalFields}
	if !slices.Equal(publishErrorCodes, want) {
		t.Fatalf("codes %v, want %v", publishErrorCodes, want)
	}
}

func TestACachedSampleOlderThanADayIsNotServed(t *testing.T) {
	t.Parallel()
	sampled := newClock().Now().Add(-MaxCachedSampleAge - time.Minute)
	sources := oneRepoSources("/repo/widgets")
	sources.remote = remoteWith(t, func(s *remotestate.Snapshot) {
		s.Metrics = &remotestate.MetricsSample{Load1: ptr(2.5), SampledAt: sampled}
	})
	snapshotter, clock := newSnapshotter(sources.collectors(), nil)
	refreshAndSettle(t, snapshotter)
	id := machineIDOf(t, snapshotter, "vm")
	server := newCockpitServer(t, snapshotter)
	var body MetricsResponse
	if err := json.Unmarshal(server.get(metricsURL+id, nil).Body.Bytes(), &body); err != nil || body.Route != RouteNone || body.Reason != ReasonStale || len(body.Samples) != 0 {
		t.Fatalf("an expired sample = %+v %v", body, err)
	}
	// A younger one is served with its own time; time passing expires it.
	sources.change(func(f *fakeSources) {
		f.remote[0].Snapshot.Metrics = &remotestate.MetricsSample{Load1: ptr(2.5), SampledAt: clock.Now().Add(-time.Hour)}
	})
	refreshAndSettle(t, snapshotter)
	if err := json.Unmarshal(server.get(metricsURL+id, nil).Body.Bytes(), &body); err != nil || body.Route != RouteCached || len(body.Samples) != 1 {
		t.Fatalf("a young sample = %+v %v", body, err)
	}
	clock.advance(MaxCachedSampleAge)
	if err := json.Unmarshal(server.get(metricsURL+id, nil).Body.Bytes(), &body); err != nil || body.Route != RouteNone || body.Reason != ReasonStale {
		t.Fatalf("a sample that aged out = %+v %v", body, err)
	}
}

func cachedAgentsOf(document Document, machine string) []Agent {
	var agents []Agent
	for _, agent := range document.Agents {
		if agent.Route == RouteCached && agent.Machine == machine {
			agents = append(agents, agent)
		}
	}
	return agents
}

// TestCachedAgentsFollowTheirMachinesLiveReplacement: a machine read live has
// its cached entries replaced, agents included, and a machine whose live read
// failed keeps showing its cached agents (with the entry's remote_error).
func TestCachedAgentsFollowTheirMachinesLiveReplacement(t *testing.T) {
	t.Parallel()
	published := cachedVM("alex")
	published.Snapshot.Agents = []remotestate.AgentState{{Kind: "run", RunID: "agt-vm", State: "running"}}
	sources := oneRepoSources("/repos/widgets")
	sources.remote = append(sources.remote, published)

	failed, clock := newLive(t, sources, &fakeExporter{answer: failing(errBoom)}, nil)
	refreshAndSettle(t, failed)
	clock.advance(7 * time.Second)
	pollAndSettle(t, failed)
	if got := cachedAgentsOf(failed.Document(), vmKey); len(got) != 1 {
		t.Fatalf("after a failed live read the cached agents = %+v", got)
	}

	full := exportOf(t, vmOwnName, vmSources(), 4, false)
	live, clock := newLive(t, sources, &fakeExporter{answer: answering(full, full)}, nil)
	refreshAndSettle(t, live)
	if got := cachedAgentsOf(live.Document(), vmKey); len(got) != 1 {
		t.Fatalf("before the live read the cached agents = %+v", got)
	}
	clock.advance(7 * time.Second)
	pollAndSettle(t, live)
	if got := cachedAgentsOf(live.Document(), vmKey); len(got) != 0 {
		t.Fatalf("a machine read live still shows its cached agents: %+v", got)
	}
}

// TestChangeTokenMovesExactlyWhenTheDigestWouldForTheFieldsItTakes is the
// invariant of ChangeToken for its fields: a heartbeat inside a
// remotestate.ActivityBucket moves neither the token nor the published digest, one
// across a bucket moves both. (An edit that no fingerprint and none of these facts
// sees moves neither, which the spec says waits for the keepalive.)
func TestChangeTokenMovesExactlyWhenTheDigestWouldForTheFieldsItTakes(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources("/repo/widgets")
	snapshotter, clock := newSnapshotter(sources.collectors(), func(o *Options) { o.Fingerprint = newConstFingerprint("fp").get })
	refreshAndSettle(t, snapshotter)
	start := newClock().Now().Add(-time.Hour) // the record's heartbeat, 08:00
	digestAt := func(heartbeat time.Time) string {
		return remotestate.Snapshot{Worktrees: []remotestate.WorktreeState{{Task: "task-a", LastActivityAt: heartbeat}}}.Digest()
	}
	token, digest := snapshotter.ChangeToken(), digestAt(start)
	for name, test := range map[string]struct {
		heartbeat time.Time
		moves     bool
	}{
		"inside the bucket": {start.Add(5 * time.Minute), false},
		"across the bucket": {start.Add(20 * time.Minute), true},
		"a day later":       {start.Add(24 * time.Hour), true},
	} {
		sources.change(func(f *fakeSources) {
			record := f.records["/wt/task-a"]
			record.HeartbeatAt = test.heartbeat
			f.records["/wt/task-a"] = record
		})
		clock.advance(time.Minute)
		refreshAndSettle(t, snapshotter)
		tokenMoved := snapshotter.ChangeToken() != token
		digestMoved := digestAt(test.heartbeat) != digest
		if tokenMoved != test.moves || digestMoved != test.moves {
			t.Errorf("%s: token moved %v, digest moved %v, want both %v", name, tokenMoved, digestMoved, test.moves)
		}
	}
}

func TestAnUncomputableFingerprintOpensTheGate(t *testing.T) {
	t.Parallel()
	snapshotter, _ := newSnapshotter(oneRepoSources("/repo/widgets").collectors(), func(o *Options) {
		o.Fingerprint = func(string) (string, error) { return "", errors.New("no .git") }
	})
	refreshAndSettle(t, snapshotter)
	if token := snapshotter.ChangeToken(); token != "" {
		t.Fatalf("a repository with no fingerprint still gave a token %q", token)
	}
}

func TestAnUnchangedRemoteReadDoesNotBumpTheSampleVersion(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources("/repo/widgets")
	sources.remote = remoteWith(t, func(s *remotestate.Snapshot) {
		s.Metrics = &remotestate.MetricsSample{CPUPercent: ptr(10.0), Load1: ptr(1.0), SampledAt: publishedAt(t)}
	})
	snapshotter, _ := newSnapshotter(sources.collectors(), nil)
	refreshAndSettle(t, snapshotter)
	first := snapshotter.remoteSampleVersion
	if first == 0 {
		t.Fatal("the first sample did not set a version")
	}
	// A second read of the same data builds new pointers and must not bump.
	refreshAndSettle(t, snapshotter)
	refreshAndSettle(t, snapshotter)
	if snapshotter.remoteSampleVersion != first {
		t.Fatalf("version %d after unchanged reads, want %d", snapshotter.remoteSampleVersion, first)
	}
	sources.change(func(f *fakeSources) { f.remote[0].Snapshot.Metrics.CPUPercent = ptr(11.0) })
	refreshAndSettle(t, snapshotter)
	if snapshotter.remoteSampleVersion == first {
		t.Fatal("a changed sample did not bump the version")
	}
	if sameSample(machinemetrics.Sample{SampledAt: time.Unix(1, 0)}, machinemetrics.Sample{SampledAt: time.Unix(2, 0)}) {
		t.Fatal("samples of different times are the same")
	}
}

func TestPublishErrorIsNeverExportedAndAnExportCarryingItIsRefused(t *testing.T) {
	t.Parallel()
	publisher := &fakePublisher{diag: "publish_failed"}
	snapshotter, _ := newSnapshotter(oneRepoSources("/repo/widgets").collectors(), func(o *Options) { o.Publisher = publisher })
	refreshAndSettle(t, snapshotter)
	body, _ := json.Marshal(snapshotter.Export(false))
	if strings.Contains(string(body), "publish_error") {
		t.Fatalf("the export carries publish_error: %s", body)
	}
	if rule := stringRules["Machine.publish_error"]; rule("publish_failed") || !rule("") {
		t.Fatal("the decoder accepts publish_error in an export")
	}
}
