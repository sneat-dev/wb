package fleet

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	return machinemetrics.Sample{Load1: float64(n), MemoryUsedBytes: 1, MemoryTotalBytes: 2, DiskFreeBytes: 3, DiskTotalBytes: 4}, c.err
}

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
		vm:  {Route: RouteLiveRemote, FetchedAt: &fetched, Samples: []machinemetrics.Sample{{Load1: 1, SampledAt: fetched}, {Load1: 2, SampledAt: fetched}}, Version: 1},
		old: {Route: RouteCached, Samples: []machinemetrics.Sample{{Load1: 9, SampledAt: fetched}}, Version: 1},
	}}
	snapshotter.metricsSources = []MetricsSource{remote, localMetrics{machineID: localMachineID(testMachine), sampler: sampler}}
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
		if local.Samples[i].Load1 <= local.Samples[i-1].Load1 || !local.Samples[i].SampledAt.After(local.Samples[i-1].SampledAt) {
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
		header.Get("Access-Control-Expose-Headers") != "ETag" || header.Get("Access-Control-Allow-Origin") != hostedOrigin {
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

// TestMetricsBodyIsPreparedAgainWhenTheHistoryChanges proves a new sample (a new
// version) replaces the prepared body, and that a machine with no sampler is `none`.
func TestMetricsBodyIsPreparedAgainWhenTheHistoryChanges(t *testing.T) {
	t.Parallel()
	source := &countingSource{}
	clock := newClock()
	ticks := make(chan time.Time)
	sampler := machinemetrics.New(machinemetrics.Options{Source: source, Now: clock.Now, Tick: func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }})
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
	sampler := machinemetrics.New(machinemetrics.Options{Source: source, Tick: func(time.Duration) (<-chan time.Time, func()) { return make(chan time.Time), func() {} }})
	snapshotter, _ := newSnapshotter(oneRepoSources(t.TempDir()).collectors(), func(options *Options) {
		options.Sampler = sampler
		options.Tick = func(time.Duration) (<-chan time.Time, func()) { return make(chan time.Time), func() {} }
	})
	stop := snapshotter.Start(t.Context())
	stop()
	if source.reads.Load() != 1 || len(sampler.Snapshot().Samples) != 1 {
		t.Errorf("the sampler took %d readings, want its first", source.reads.Load())
	}
	withoutSampler, _ := newSnapshotter(oneRepoSources(t.TempDir()).collectors(), func(options *Options) {
		options.Tick = func(time.Duration) (<-chan time.Time, func()) { return make(chan time.Time), func() {} }
	})
	withoutSampler.Start(t.Context())()
}
