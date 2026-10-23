package fleet

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cockpit"
	"github.com/sneat-dev/wb/internal/cockpit/machinemetrics"
	"github.com/sneat-dev/wb/internal/remotestate"
)

const metricsURL = cockpit.APIPrefix + MetricsRoute + "?machine="

// countingSource is a machinemetrics.Source that counts its readings.
type countingSource struct {
	reads atomic.Int64
	err   error
}

func (c *countingSource) Read() (machinemetrics.Sample, error) {
	n := c.reads.Add(1)
	return machinemetrics.Sample{Load1: ptr(float64(n)), MemoryUsedBytes: ptr(uint64(1)), MemoryTotalBytes: ptr(uint64(2)), DiskFreeBytes: ptr(uint64(3)), DiskTotalBytes: ptr(uint64(4))}, c.err
}

func ptr[T any](value T) *T { return &value }

// fakeMetrics is a MetricsSource that knows some machines.
type fakeMetrics struct {
	answers map[string]MetricsAnswer
	asked   atomic.Int64
}

func (f *fakeMetrics) MachineMetrics(id string) (MetricsAnswer, bool) {
	f.asked.Add(1)
	answer, ok := f.answers[id]
	return answer, ok
}

func machineIDOf(t *testing.T, snapshotter *Snapshotter, name string) string {
	t.Helper()
	for _, machine := range snapshotter.Document().Machines {
		if machine.Machine == name {
			return machine.ID
		}
	}
	t.Fatalf("no machine %q in the document", name)
	return ""
}

// advancing moves the clock on by one interval at every reading, so each sample's
// time is 10 seconds after the last with no race between a test and the loop.
type advancing struct {
	machinemetrics.Source
	clock *manualClock
}

func (a advancing) Read() (machinemetrics.Sample, error) {
	a.clock.advance(machinemetrics.Interval)
	return a.Source.Read()
}

// filledSampler is a sampler holding n samples read from source, 10 s apart.
func filledSampler(t *testing.T, source machinemetrics.Source, n int) *machinemetrics.Sampler {
	clock := newClock()
	clock.advance(-time.Hour) // the samples are all in the snapshotter's past
	ticks := make(chan time.Time)
	sampler := machinemetrics.New(machinemetrics.Options{
		Source: advancing{source, clock}, Now: clock.Now,
		Tick: func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} },
	})
	stop := sampler.Start(t.Context())
	for range n - 1 {
		ticks <- clock.Now()
	}
	stop()
	return sampler
}

// TestMetricsRouteServesEachSource proves cockpit-views#ac:metrics-route-serves-
// each-source: local history, a live-remote and a cached answer from the sources
// that plug in, none, and unknown, with no session and no request-time read.
func TestMetricsRouteServesEachSource(t *testing.T) {
	t.Parallel()
	source := &countingSource{}
	sampler := filledSampler(t, source, 5)
	fetched := time.Date(2026, 10, 1, 8, 59, 0, 0, time.UTC)
	sources := &fakeSources{remote: []remotestate.Entry{
		{Snapshot: remotestate.Snapshot{Login: "a", Machine: "vm", PublishedAt: remotePublishedAt()}},
		{Snapshot: remotestate.Snapshot{Login: "b", Machine: "old", PublishedAt: remotePublishedAt()}},
		{Snapshot: remotestate.Snapshot{Login: "c", Machine: "bare", PublishedAt: remotePublishedAt()}},
	}}
	snapshotter, _ := newSnapshotter(sources.collectors(), nil)
	refreshAndSettle(t, snapshotter)
	vm, old := machineIDOf(t, snapshotter, "vm"), machineIDOf(t, snapshotter, "old")
	remote := &fakeMetrics{answers: map[string]MetricsAnswer{
		vm:  {Route: RouteLiveRemote, FetchedAt: &fetched, Samples: []machinemetrics.Sample{{Load1: ptr(1.0), SampledAt: fetched.Add(-10 * time.Second)}, {Load1: ptr(2.0), SampledAt: fetched}}, Version: 1},
		old: {Route: RouteCached, Samples: []machinemetrics.Sample{{Load1: ptr(9.0), SampledAt: fetched}}, Version: 1},
	}}
	snapshotter.metricsSources = []MetricsSource{remote}
	snapshotter.sampler = sampler
	server := newCockpitServer(t, snapshotter)
	readsBefore := source.reads.Load()

	decode := func(id string) (MetricsResponse, map[string]json.RawMessage) {
		t.Helper()
		recorder := server.get(metricsURL+id, nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", id, recorder.Code, recorder.Body.String())
		}
		var response MetricsResponse
		var fields map[string]json.RawMessage
		if json.Unmarshal(recorder.Body.Bytes(), &response) != nil || json.Unmarshal(recorder.Body.Bytes(), &fields) != nil {
			t.Fatalf("not JSON: %s", recorder.Body.String())
		}
		return response, fields
	}

	local, fields := decode(machineIDOf(t, snapshotter, testMachine))
	if local.Route != RouteLocal || len(local.Samples) != 5 || local.FetchedAt != nil || local.Machine != machineIDOf(t, snapshotter, testMachine) {
		t.Fatalf("local = %+v", local)
	}
	if _, has := fields["fetched_at"]; has {
		t.Error("a local answer carries fetched_at")
	}
	for i := 1; i < len(local.Samples); i++ {
		if *local.Samples[i].Load1 <= *local.Samples[i-1].Load1 || !local.Samples[i].SampledAt.After(local.Samples[i-1].SampledAt) {
			t.Errorf("samples are not oldest first: %+v", local.Samples)
		}
	}
	liveRemote, _ := decode(vm)
	if liveRemote.Route != RouteLiveRemote || liveRemote.FetchedAt == nil || !liveRemote.FetchedAt.Equal(fetched) || len(liveRemote.Samples) != 2 {
		t.Errorf("live-remote = %+v", liveRemote)
	}
	cached, _ := decode(old)
	if cached.Route != RouteCached || len(cached.Samples) != 1 || !cached.Samples[0].SampledAt.Equal(fetched) {
		t.Errorf("cached = %+v", cached)
	}
	none, noneFields := decode(machineIDOf(t, snapshotter, "bare"))
	if none.Route != RouteNone || none.Reason != ReasonNoSource || string(noneFields["samples"]) != "[]" {
		t.Errorf("none = %+v, samples %s", none, noneFields["samples"])
	}
	if unknown := server.get(metricsURL+"machine-nope", nil); unknown.Code != http.StatusNotFound || !strings.Contains(unknown.Body.String(), "unknown_machine") {
		t.Errorf("unknown = %d %s", unknown.Code, unknown.Body.String())
	}
	if missing := server.get(cockpit.APIPrefix+MetricsRoute, nil); missing.Code != http.StatusNotFound {
		t.Errorf("no machine parameter = %d, want 404", missing.Code)
	}
	if source.reads.Load() != readsBefore {
		t.Error("a request read the machine")
	}
}

// TestSampleFieldsAreExactlyTheSevenNamed pins the sample's closed field list.
func TestSampleFieldsAreExactlyTheSevenNamed(t *testing.T) {
	t.Parallel()
	want := []string{"cpu_percent", "load1", "memory_used_bytes", "memory_total_bytes", "disk_free_bytes", "disk_total_bytes", "sampled_at"}
	if got := jsonFields(machinemetrics.Sample{}); !sameSet(got, want) {
		t.Errorf("sample fields = %v, want %v", got, want)
	}
	if got := jsonFields(MetricsResponse{}); !sameSet(got, []string{"machine", "route", "fetched_at", "samples", "reason"}) {
		t.Errorf("response fields = %v", got)
	}
}

// TestMetricsRouteIsCompressedAndRevalidatable proves cockpit-views#ac:metrics-
// route-is-compressed-and-revalidatable, and that the body is prepared once for a
// version, not once for each request.
func TestMetricsRouteIsCompressedAndRevalidatable(t *testing.T) {
	t.Parallel()
	var compressed atomic.Int64
	sampler := filledSampler(t, &countingSource{}, 3)
	snapshotter, _ := newSnapshotter(oneRepoSources(t.TempDir()).collectors(), func(options *Options) {
		options.Sampler = sampler
		options.Compress = func(data []byte) []byte { compressed.Add(1); return cockpit.Gzip(data) }
	})
	refreshAndSettle(t, snapshotter)
	server := newCockpitServer(t, snapshotter)
	target := metricsURL + localMachineID(testMachine)
	compressedBefore := compressed.Load()

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
	zipped := server.get(target, nil, "Origin", hostedOrigin, "Accept-Encoding", "gzip")
	header := zipped.Header()
	if zipped.Code != 200 || header.Get("Content-Encoding") != "gzip" || !strings.HasSuffix(header.Get("ETag"), `-gzip"`) || header.Get("Vary") != "Origin, Accept-Encoding" ||
		header.Get("Access-Control-Expose-Headers") != "ETag, "+cockpit.CheckedAtHeader || header.Get("Access-Control-Allow-Origin") != hostedOrigin {
		t.Fatalf("gzip response = %d %v", zipped.Code, header)
	}
	var response MetricsResponse
	if err := json.Unmarshal(gunzip(t, zipped.Body.Bytes()), &response); err != nil || len(response.Samples) != 3 {
		t.Fatalf("gzip body = %+v, %v", response, err)
	}
	repeat := server.get(target, nil, "Origin", hostedOrigin, "Accept-Encoding", "gzip", "If-None-Match", header.Get("ETag"))
	if repeat.Code != http.StatusNotModified || repeat.Header().Get("Access-Control-Allow-Origin") != hostedOrigin {
		t.Errorf("repeat = %d %v", repeat.Code, repeat.Header())
	}
	plain := server.get(target, nil)
	if plain.Code != 200 || plain.Header().Get("Content-Encoding") != "" || strings.HasSuffix(plain.Header().Get("ETag"), "-gzip\"") {
		t.Errorf("identity = %d %v", plain.Code, plain.Header())
	}
	if again := server.get(target, nil, "If-None-Match", plain.Header().Get("ETag")); again.Code != http.StatusNotModified {
		t.Errorf("identity repeat = %d", again.Code)
	}
	if foreign := server.get(target, nil, "Origin", "https://elsewhere.example"); foreign.Code != http.StatusForbidden {
		t.Errorf("another origin = %d, want 403", foreign.Code)
	}
	if got := compressed.Load() - compressedBefore; got != 1 {
		t.Errorf("the body was compressed %d times for one version, want once", got)
	}
}

// TestMetricsBodyIsPreparedAgainWhenTheHistoryChanges proves a new sample
// replaces the served body, and that a machine with no sampler is `none`.
func TestMetricsBodyIsPreparedAgainWhenTheHistoryChanges(t *testing.T) {
	t.Parallel()
	source := &countingSource{}
	clock := newClock()
	clock.advance(-time.Hour)
	ticks := make(chan time.Time)
	sampler := machinemetrics.New(machinemetrics.Options{Source: advancing{source, clock}, Now: clock.Now, Tick: func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }})
	snapshotter, _ := newSnapshotter(oneRepoSources(t.TempDir()).collectors(), func(options *Options) { options.Sampler = sampler })
	server := newCockpitServer(t, snapshotter)
	target := metricsURL + localMachineID(testMachine)
	first := server.get(target, nil).Header().Get("ETag")
	stop := sampler.Start(t.Context())
	ticks <- clock.Now()
	stop()
	second := server.get(target, nil).Header().Get("ETag")
	third := server.get(target, nil).Header().Get("ETag")
	if first == "" || first == second || second != third {
		t.Errorf("etags %s, %s, %s: the prepared body did not follow the history", first, second, third)
	}

	bare, _ := newSnapshotter(oneRepoSources(t.TempDir()).collectors(), nil)
	bareServer := newCockpitServer(t, bare)
	if recorder := bareServer.get(target, nil); recorder.Code != 200 || !strings.Contains(recorder.Body.String(), `"reason":"no_source"`) {
		t.Errorf("a snapshotter with no sampler answered %d %s", recorder.Code, recorder.Body.String())
	}
}

// unsupportedMetrics reports metrics as unsupported, like a Windows build.
type unsupportedMetrics struct{}

func (unsupportedMetrics) Read() (machinemetrics.Sample, error) {
	return machinemetrics.Sample{}, machinemetrics.ErrUnsupported
}

// TestUnsupportedPlatformServesNoneWithAReason covers the Windows answer: 200,
// `none`, an empty list and the reason.
func TestUnsupportedPlatformServesNoneWithAReason(t *testing.T) {
	t.Parallel()
	sampler := machinemetrics.New(machinemetrics.Options{Source: unsupportedMetrics{}})
	stop := sampler.Start(t.Context())
	stop()
	snapshotter, _ := newSnapshotter(oneRepoSources(t.TempDir()).collectors(), func(options *Options) { options.Sampler = sampler })
	recorder := newCockpitServer(t, snapshotter).get(metricsURL+localMachineID(testMachine), nil)
	var response MetricsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || recorder.Code != 200 || response.Route != RouteNone || response.Reason != ReasonUnsupported || len(response.Samples) != 0 {
		t.Errorf("answer = %d %+v %v", recorder.Code, response, err)
	}
}

// TestSnapshotterStartsAndStopsItsSampler proves the sampler lives and dies with
// the snapshotter's own lifecycle.
func TestSnapshotterStartsAndStopsItsSampler(t *testing.T) {
	t.Parallel()
	source := &countingSource{}
	// The sampler's loop ends a little after the snapshotter's own, so a stop that
	// did not wait for the sampler returns while it still runs.
	loopEnded := make(chan struct{})
	var samplerEnded atomic.Bool
	sampler := machinemetrics.New(machinemetrics.Options{Source: source, Tick: func(time.Duration) (<-chan time.Time, func()) {
		return make(chan time.Time), func() {
			<-loopEnded
			time.Sleep(20 * time.Millisecond)
			samplerEnded.Store(true)
		}
	}})
	snapshotter, _ := newSnapshotter(oneRepoSources(t.TempDir()).collectors(), func(options *Options) {
		options.Sampler = sampler
		options.Tick = func(time.Duration) (<-chan time.Time, func()) {
			return make(chan time.Time), func() { close(loopEnded) }
		}
	})
	stop := snapshotter.Start(t.Context())
	stop()
	if !samplerEnded.Load() {
		t.Fatal("the snapshotter's stop returned while its sampler was still running")
	}
	if source.reads.Load() != 1 || len(sampler.Snapshot().Samples) != 1 {
		t.Errorf("the sampler took %d readings, want its first", source.reads.Load())
	}
	withoutSampler, _ := newSnapshotter(oneRepoSources(t.TempDir()).collectors(), func(options *Options) {
		options.Tick = func(time.Duration) (<-chan time.Time, func()) { return make(chan time.Time), func() {} }
	})
	withoutSampler.Start(t.Context())()
}

// TestMetricsNeedASessionWhenForwardedOrNotLoopback proves the metrics route has
// the fleet document's access class: a proxied request or one for a non-loopback
// host gets no anonymous reading and no data.
func TestMetricsNeedASessionWhenForwardedOrNotLoopback(t *testing.T) {
	t.Parallel()
	snapshotter, _ := newSnapshotter(oneRepoSources(t.TempDir()).collectors(), func(options *Options) {
		options.Sampler = filledSampler(t, &countingSource{}, 2)
	})
	refreshAndSettle(t, snapshotter)
	server := newCockpitServer(t, snapshotter)
	target := metricsURL + localMachineID(testMachine)
	if recorder := server.get(target, nil); recorder.Code != 200 {
		t.Fatalf("anonymous loopback = %d", recorder.Code)
	}
	for name, headers := range map[string][]string{
		"forwarded for": {"X-Forwarded-For", "203.0.113.9"}, "forwarded https": {"X-Forwarded-Proto", "https"}, "via a proxy": {"Via", "1.1 proxy"},
	} {
		if recorder := server.get(target, nil, headers...); recorder.Code != http.StatusUnauthorized || strings.Contains(recorder.Body.String(), "samples") {
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
	if recorder.Code == 200 || strings.Contains(recorder.Body.String(), `"samples"`) {
		t.Errorf("a non-loopback host got %d %s", recorder.Code, recorder.Body.String())
	}
}

// switchable is a MetricsSource whose answer a test changes between requests.
type switchable struct {
	mu     sync.Mutex
	answer *MetricsAnswer
}

func (s *switchable) set(answer *MetricsAnswer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answer = answer
}

func (s *switchable) MachineMetrics(string) (MetricsAnswer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.answer == nil {
		return MetricsAnswer{}, false
	}
	return *s.answer, true
}

func routeOf(t *testing.T, server *cockpitServer, id string) MetricsResponse {
	t.Helper()
	recorder := server.get(metricsURL+id, nil)
	var response MetricsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || recorder.Code != 200 {
		t.Fatalf("%s = %d %s %v", id, recorder.Code, recorder.Body.String(), err)
	}
	return response
}

// TestMetricsFallThroughTheSourcesInOrderAndFollowARouteChange proves the order
// live remote, then cached, then none: when the live source stops knowing a machine
// the next source answers, even with a lower version number than the live one had,
// and when both stop the machine has none.
func TestMetricsFallThroughTheSourcesInOrderAndFollowARouteChange(t *testing.T) {
	t.Parallel()
	sources := &fakeSources{remote: []remotestate.Entry{{Snapshot: remotestate.Snapshot{Login: "a", Machine: "vm", PublishedAt: remotePublishedAt()}}}}
	snapshotter, _ := newSnapshotter(sources.collectors(), nil)
	refreshAndSettle(t, snapshotter)
	vm := machineIDOf(t, snapshotter, "vm")
	past := newClock().Now().Add(-time.Hour)
	live, cached := &switchable{}, &switchable{}
	snapshotter.metricsSources = []MetricsSource{live, cached}
	server := newCockpitServer(t, snapshotter)

	live.set(&MetricsAnswer{Route: RouteLiveRemote, FetchedAt: &past, Samples: []machinemetrics.Sample{{Load1: ptr(1.0), SampledAt: past}}, Version: 9})
	cached.set(&MetricsAnswer{Route: RouteCached, Samples: []machinemetrics.Sample{{Load1: ptr(2.0), SampledAt: past}}, Version: 1})
	if got := routeOf(t, server, vm); got.Route != RouteLiveRemote || *got.Samples[0].Load1 != 1 {
		t.Fatalf("both known = %+v, want the live one", got)
	}
	live.set(nil)
	if got := routeOf(t, server, vm); got.Route != RouteCached || *got.Samples[0].Load1 != 2 {
		t.Errorf("live gone = %+v, want the cached one though its version is lower", got)
	}
	cached.set(nil)
	if got := routeOf(t, server, vm); got.Route != RouteNone || got.Reason != ReasonNoSource {
		t.Errorf("both gone = %+v, want none", got)
	}
}

// TestTheLocalMachineIsAlwaysAnsweredByItsOwnSampler proves no other source can
// shadow this machine's history, and that with no sampler it has none.
func TestTheLocalMachineIsAlwaysAnsweredByItsOwnSampler(t *testing.T) {
	t.Parallel()
	past := newClock().Now().Add(-time.Hour)
	shadow := &switchable{}
	shadow.set(&MetricsAnswer{Route: RouteCached, Samples: []machinemetrics.Sample{{Load1: ptr(99.0), SampledAt: past}}, Version: 1})
	local := localMachineID(testMachine)

	snapshotter, _ := newSnapshotter(oneRepoSources(t.TempDir()).collectors(), func(options *Options) {
		options.Sampler = filledSampler(t, &countingSource{}, 2)
		options.Metrics = []MetricsSource{shadow}
	})
	if got := routeOf(t, newCockpitServer(t, snapshotter), local); got.Route != RouteLocal || len(got.Samples) != 2 || *got.Samples[0].Load1 == 99 {
		t.Errorf("with a sampler = %+v", got)
	}
	bare, _ := newSnapshotter(oneRepoSources(t.TempDir()).collectors(), func(options *Options) { options.Metrics = []MetricsSource{shadow} })
	if got := routeOf(t, newCockpitServer(t, bare), local); got.Route != RouteNone || got.Reason != ReasonNoSource {
		t.Errorf("with no sampler = %+v, want none however the other sources answer", got)
	}
}

// TestEverySourcesAnswerIsSanitizedBeforeItIsServed proves the answer a source gives
// is untrusted: a bad route, a missing fetch time, a future or out-of-order sample,
// numbers out of range and more than 360 samples are all dealt with.
func TestEverySourcesAnswerIsSanitizedBeforeItIsServed(t *testing.T) {
	t.Parallel()
	now := newClock().Now()
	at := func(seconds int) time.Time { return now.Add(time.Duration(seconds) * time.Second) }
	fetched, future := at(-5), at(600)
	nan := math.NaN()
	many := make([]machinemetrics.Sample, 0, 400)
	for i := range 400 {
		many = append(many, machinemetrics.Sample{Load1: ptr(float64(i)), SampledAt: at(-4000 + i)})
	}
	for name, test := range map[string]struct {
		in   MetricsAnswer
		want func(MetricsAnswer) bool
	}{
		"unknown route": {MetricsAnswer{Route: "teleport", Samples: many}, func(a MetricsAnswer) bool {
			return a.Route == RouteNone && a.Reason == ReasonUnavailable && len(a.Samples) == 0
		}},
		"live without fetch time":  {MetricsAnswer{Route: RouteLiveRemote, Samples: many}, func(a MetricsAnswer) bool { return a.Route == RouteNone }},
		"live fetched in future":   {MetricsAnswer{Route: RouteLiveRemote, FetchedAt: &future}, func(a MetricsAnswer) bool { return a.Route == RouteNone }},
		"cached drops fetch time":  {MetricsAnswer{Route: RouteCached, FetchedAt: &fetched, Reason: "free text"}, func(a MetricsAnswer) bool { return a.FetchedAt == nil && a.Reason == "" }},
		"none with free text":      {MetricsAnswer{Route: RouteNone, Reason: "/home/alex/secret", Samples: many}, func(a MetricsAnswer) bool { return a.Reason == ReasonNoSource && len(a.Samples) == 0 }},
		"none keeps known reasons": {MetricsAnswer{Route: RouteNone, Reason: ReasonUnsupported}, func(a MetricsAnswer) bool { return a.Reason == ReasonUnsupported }},
		"caps at the newest 360": {MetricsAnswer{Route: RouteLocal, Samples: many}, func(a MetricsAnswer) bool {
			return len(a.Samples) == 360 && *a.Samples[0].Load1 == 40 && *a.Samples[359].Load1 == 399
		}},
		"future and zero times": {MetricsAnswer{Route: RouteLocal, Samples: []machinemetrics.Sample{{SampledAt: future}, {}, {Load1: ptr(1.0), SampledAt: at(-1)}}}, func(a MetricsAnswer) bool { return len(a.Samples) == 1 }},
		"small skew allowed":    {MetricsAnswer{Route: RouteLocal, Samples: []machinemetrics.Sample{{Load1: ptr(1.0), SampledAt: at(2)}}}, func(a MetricsAnswer) bool { return len(a.Samples) == 1 }},
		"out of order after a clock step": {MetricsAnswer{Route: RouteLocal, Samples: []machinemetrics.Sample{
			{Load1: ptr(1.0), SampledAt: at(-30)}, {Load1: ptr(2.0), SampledAt: at(-50)}, {Load1: ptr(3.0), SampledAt: at(-30)}, {Load1: ptr(4.0), SampledAt: at(-20)},
		}}, func(a MetricsAnswer) bool { return len(a.Samples) == 2 && *a.Samples[1].Load1 == 4 }},
		"numbers out of range": {MetricsAnswer{Route: RouteLocal, Samples: []machinemetrics.Sample{
			{CPUPercent: ptr(101.0), Load1: ptr(-1.0), SampledAt: at(-40)},
			{CPUPercent: ptr(nan), Load1: ptr(nan), SampledAt: at(-30)},
			{CPUPercent: ptr(-0.5), Load1: ptr(math.Inf(1)), SampledAt: at(-20)},
			{CPUPercent: ptr(100.0), Load1: ptr(0.0), SampledAt: at(-10)},
		}}, func(a MetricsAnswer) bool {
			return a.Samples[0].CPUPercent == nil && a.Samples[0].Load1 == nil && a.Samples[1].CPUPercent == nil && a.Samples[1].Load1 == nil &&
				a.Samples[2].CPUPercent == nil && a.Samples[2].Load1 == nil && *a.Samples[3].CPUPercent == 100 && *a.Samples[3].Load1 == 0
		}},
		"pairs": {MetricsAnswer{Route: RouteLocal, Samples: []machinemetrics.Sample{
			{MemoryUsedBytes: ptr(uint64(5)), MemoryTotalBytes: ptr(uint64(4)), DiskFreeBytes: ptr(uint64(5)), DiskTotalBytes: ptr(uint64(4)), SampledAt: at(-30)},
			{MemoryUsedBytes: ptr(uint64(1)), DiskTotalBytes: ptr(uint64(4)), SampledAt: at(-20)},
			{MemoryUsedBytes: ptr(uint64(4)), MemoryTotalBytes: ptr(uint64(4)), DiskFreeBytes: ptr(uint64(0)), DiskTotalBytes: ptr(uint64(4)), SampledAt: at(-10)},
		}}, func(a MetricsAnswer) bool {
			return a.Samples[0].MemoryUsedBytes == nil && a.Samples[0].DiskFreeBytes == nil && a.Samples[1].MemoryUsedBytes == nil && a.Samples[1].DiskTotalBytes == nil &&
				*a.Samples[2].MemoryUsedBytes == 4 && *a.Samples[2].DiskFreeBytes == 0
		}},
	} {
		if got := sanitizeMetrics(test.in, now); !test.want(got) {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

// TestAFailingSamplerServesNoneWithAReason covers a sampler whose source has given
// nothing for the failure limit: the route says none and why.
func TestAFailingSamplerServesNoneWithAReason(t *testing.T) {
	t.Parallel()
	ticks := make(chan time.Time)
	sampler := machinemetrics.New(machinemetrics.Options{
		Source: failingSource{},
		Tick:   func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} },
	})
	stop := sampler.Start(t.Context())
	for range machinemetrics.FailureLimit - 1 {
		ticks <- time.Time{}
	}
	stop()
	snapshotter, _ := newSnapshotter(oneRepoSources(t.TempDir()).collectors(), func(options *Options) { options.Sampler = sampler })
	if got := routeOf(t, newCockpitServer(t, snapshotter), localMachineID(testMachine)); got.Route != RouteNone || got.Reason != ReasonUnavailable {
		t.Errorf("a failing sampler = %+v", got)
	}
}

// failingSource reads nothing, with an error.
type failingSource struct{}

func (failingSource) Read() (machinemetrics.Sample, error) {
	return machinemetrics.Sample{}, errors.New("boom")
}

// TestAMarshalFailureServesNone covers the impossible: the body cannot be marshalled.
func TestAMarshalFailureServesNone(t *testing.T) { //nolint:paralleltest // it replaces a package variable
	snapshotter, _ := newSnapshotter(oneRepoSources(t.TempDir()).collectors(), func(options *Options) {
		options.Sampler = filledSampler(t, &countingSource{}, 2)
	})
	server := newCockpitServer(t, snapshotter)
	marshal := marshalMetrics
	t.Cleanup(func() { marshalMetrics = marshal })
	calls := 0
	marshalMetrics = func(value any) ([]byte, error) {
		if calls++; calls == 1 {
			return nil, errors.New("cannot marshal")
		}
		return marshal(value)
	}
	if got := routeOf(t, server, localMachineID(testMachine)); got.Route != RouteNone || got.Reason != ReasonUnavailable || len(got.Samples) != 0 {
		t.Errorf("after a marshal failure = %+v", got)
	}
}

// TestMetricsBodiesOfMachinesThatLeftTheFleetAreForgotten bounds the prepared bodies
// to the machines the fleet document lists.
func TestMetricsBodiesOfMachinesThatLeftTheFleetAreForgotten(t *testing.T) {
	t.Parallel()
	sources := &fakeSources{remote: []remotestate.Entry{{Snapshot: remotestate.Snapshot{Login: "a", Machine: "vm", PublishedAt: remotePublishedAt()}}}}
	snapshotter, _ := newSnapshotter(sources.collectors(), nil)
	refreshAndSettle(t, snapshotter)
	server := newCockpitServer(t, snapshotter)
	vm := machineIDOf(t, snapshotter, "vm")
	routeOf(t, server, vm)
	routeOf(t, server, localMachineID(testMachine))
	sources.change(func(f *fakeSources) { f.remote = nil })
	refreshAndSettle(t, snapshotter)
	if recorder := server.get(metricsURL+vm, nil); recorder.Code != http.StatusNotFound {
		t.Fatalf("a machine that left = %d", recorder.Code)
	}
	snapshotter.metrics.mu.Lock()
	snapshotter.metrics.entries["machine-stale"] = cachedMetrics{}
	snapshotter.metrics.mu.Unlock()
	routeOf(t, server, localMachineID(testMachine)) // a hit builds nothing...
	snapshotter.metricsSources = nil
	snapshotter.sampler = filledSampler(t, &countingSource{}, 2) // ...so a new answer is built to prune
	routeOf(t, server, localMachineID(testMachine))
	snapshotter.metrics.mu.Lock()
	held := len(snapshotter.metrics.entries)
	snapshotter.metrics.mu.Unlock()
	if held != 1 {
		t.Errorf("%d bodies held, want only the local machine's", held)
	}
}
