package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/cockpit/machinemetrics"
	"github.com/sneat-dev/wb/internal/remotestate"
)

// fakePublisher counts the hand-offs of the snapshotter and captures what the
// publisher would have been given.
type fakePublisher struct {
	mu     sync.Mutex
	calls  int
	extras []remotestate.Extras
	panics bool
	block  chan struct{}
}

func (p *fakePublisher) Publish(_ context.Context, extras func() remotestate.Extras) {
	if p.block != nil {
		<-p.block
	}
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	if p.panics {
		panic("publisher panicked")
	}
	got := extras()
	p.mu.Lock()
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
	if extras := none.publishExtras(); extras.Metrics != nil {
		t.Fatalf("no sampler published %+v", extras.Metrics)
	}
	empty, _ := newSnapshotter(oneRepoSources("/repo/widgets").collectors(), func(o *Options) {
		o.Sampler = machinemetrics.New(machinemetrics.Options{Source: &countingSource{}})
	})
	if empty.publishExtras().Metrics != nil {
		t.Fatal("a sampler without a sample published one")
	}
}

func TestRemoteAgentsAreCachedWithTheSnapshotsAgeAndNoActions(t *testing.T) {
	t.Parallel()
	published := publishedAt(t)
	entries := remoteWith(t, func(s *remotestate.Snapshot) {
		s.Agents = []remotestate.AgentState{
			{Kind: "session", SessionID: "wbs-9", Runtime: "claude\u202e", Model: "opus", State: "live", Activity: "blocked", Task: "t\n1", Repository: "acme/gadgets", StartedAt: published.Add(-time.Hour)},
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
	if session.Machine != "vm" || !session.ObservedAt.Equal(published) || session.Runtime != "claude" || session.Task != "t1" || session.Activity != ActivityBlocked ||
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

func TestRemoteAgentsAreCappedAndTheDocumentSaysSo(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 5000)
	entries := remoteWith(t, func(s *remotestate.Snapshot) {
		for index := range 500 {
			s.Agents = append(s.Agents, remotestate.AgentState{Kind: "run", RunID: "agt-" + strings.Repeat("0", index%3) + string(rune('a'+index%26)) + long[:index%7], Runtime: long, Model: long, State: "running", Task: long, Repository: long, SessionID: ""})
			s.Agents[index].RunID += "-" + string(rune('0'+index/100)) + string(rune('0'+(index/10)%10)) + string(rune('0'+index%10))
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
		for _, text := range []string{agent.Runtime, agent.Model, agent.Task, agent.RunID} {
			if len([]rune(text)) > maxRemoteText {
				t.Fatalf("an uncapped string of %d characters", len([]rune(text)))
			}
		}
	}
	if count != 200 || !snapshotter.Document().AgentsTruncated {
		t.Fatalf("cached agents = %d, truncated = %v", count, snapshotter.Document().AgentsTruncated)
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
	// The remote entry of the sentinel sources carries a sentinel in every field
	// of its agent and sample; only the closed, enum-checked ones may arrive.
	sources := sentinelSources()
	entry := &sources.remote[0].Snapshot
	entry.Agents = []remotestate.AgentState{filled[remotestate.AgentState]()}
	entry.Agents[0].Kind, entry.Agents[0].State = "run", "running"
	entry.Metrics = &remotestate.MetricsSample{SampledAt: publishedAt(t)}
	snapshotter, _ := newSnapshotter(sources.collectors(), nil)
	refreshAndSettle(t, snapshotter)
	for _, agent := range snapshotter.Document().Agents {
		if agent.Route != RouteCached {
			continue
		}
		// Free text that the document may carry (runtime, model, task and the ids)
		// arrives plain and capped; the activity and the repository, which are
		// closed or resolved, do not carry the sentinel.
		if strings.Contains(agent.Activity, sentinel) || strings.Contains(agent.Repository, sentinel) {
			t.Fatalf("a closed field carries a sentinel: %+v", agent)
		}
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
