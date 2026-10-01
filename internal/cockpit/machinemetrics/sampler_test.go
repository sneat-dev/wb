package machinemetrics

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// scriptSource gives each reading the next scripted error (or none) and a load1
// equal to the reading's number, and advances a manual clock by one interval.
type scriptSource struct {
	mu     sync.Mutex
	clock  *time.Time
	reads  int
	errors map[int]error
}

func (s *scriptSource) Read() (Sample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	*s.clock = s.clock.Add(Interval)
	if err := s.errors[s.reads]; err != nil {
		return Sample{}, err
	}
	return Sample{Load1: float64(s.reads)}, nil
}

func (s *scriptSource) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

func newScripted(errs map[int]error) (*scriptSource, *Sampler, chan time.Time, *[]string) {
	clock := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	source := &scriptSource{clock: &clock, errors: errs}
	ticks := make(chan time.Time)
	var logs []string
	sampler := New(Options{
		Source: source,
		Now:    func() time.Time { return clock },
		Tick:   func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} },
		Logf:   func(format string, args ...any) { logs = append(logs, format) },
	})
	return source, sampler, ticks, &logs
}

// TestRingBufferKeepsTheNewest360SamplesOldestFirst proves
// cockpit-views#ac:sampler-fills-a-ring-buffer: 400 ticks through the real loop
// leave exactly the newest 360 samples, oldest first and 10 seconds apart, and
// asking for them reads memory only.
func TestRingBufferKeepsTheNewest360SamplesOldestFirst(t *testing.T) {
	t.Parallel()
	source, sampler, ticks, _ := newScripted(nil)
	stop := sampler.Start(t.Context())
	for range 399 {
		ticks <- time.Time{}
	}
	stop()
	if source.count() != 400 {
		t.Fatalf("source read %d times, want 400", source.count())
	}
	before := source.count()
	snapshot := sampler.Snapshot()
	_ = sampler.Snapshot()
	if source.count() != before {
		t.Error("asking for the samples read the machine")
	}
	if len(snapshot.Samples) != Capacity || !snapshot.Supported {
		t.Fatalf("held %d samples, supported %v", len(snapshot.Samples), snapshot.Supported)
	}
	if snapshot.Samples[0].Load1 != 41 || snapshot.Samples[Capacity-1].Load1 != 400 {
		t.Errorf("window = %v .. %v, want 41 .. 400", snapshot.Samples[0].Load1, snapshot.Samples[Capacity-1].Load1)
	}
	for i := 1; i < len(snapshot.Samples); i++ {
		if gap := snapshot.Samples[i].SampledAt.Sub(snapshot.Samples[i-1].SampledAt); gap != Interval {
			t.Fatalf("samples %d and %d are %v apart, want %v", i-1, i, gap, Interval)
		}
	}
	snapshot.Samples[0].Load1 = -1
	if sampler.Snapshot().Samples[0].Load1 == -1 {
		t.Error("the snapshot aliases the buffer")
	}
}

func TestPartlyFilledBufferIsOldestFirst(t *testing.T) {
	t.Parallel()
	_, sampler, _, _ := newScripted(nil)
	for range 3 {
		sampler.sampleOnce()
	}
	got := sampler.Snapshot()
	if len(got.Samples) != 3 || got.Samples[0].Load1 != 1 || got.Samples[2].Load1 != 3 {
		t.Errorf("samples = %+v", got.Samples)
	}
	if empty := New(Options{}).Snapshot(); len(empty.Samples) != 0 || empty.Samples == nil {
		t.Errorf("an empty sampler holds %v, want an empty non-nil list", empty.Samples)
	}
}

func TestUnsupportedPlatformStopsSamplingWithoutAnError(t *testing.T) {
	t.Parallel()
	sampler := New(Options{})
	stop := sampler.Start(t.Context())
	stop() // the loop has already ended; stopping still returns
	snapshot := sampler.Snapshot()
	if snapshot.Supported || len(snapshot.Samples) != 0 || snapshot.Version == 0 {
		t.Errorf("snapshot = %+v, want unsupported, empty and versioned", snapshot)
	}
}

func TestFailedReadingIsSkippedAndLoggedOnce(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	_, sampler, _, logs := newScripted(map[int]error{1: boom, 2: boom, 4: boom})
	for range 5 {
		sampler.sampleOnce()
	}
	if got := len(sampler.Snapshot().Samples); got != 2 {
		t.Errorf("held %d samples, want the 2 that succeeded", got)
	}
	if len(*logs) != 2 {
		t.Errorf("logged %d times, want once per run of failures (2)", len(*logs))
	}
}

func TestCancelStopsTheLoop(t *testing.T) {
	t.Parallel()
	_, sampler, _, _ := newScripted(nil)
	ctx, cancel := context.WithCancel(t.Context())
	stop := sampler.Start(ctx)
	cancel()
	stop()
}

func TestDefaultsUseTheRealClockAndTicker(t *testing.T) {
	t.Parallel()
	sampler := New(Options{})
	if _, err := sampler.source.Read(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("a sampler with no source reads %v, want unsupported", err)
	}
	if sampler.now == nil || sampler.tick == nil || sampler.logf == nil {
		t.Fatal("defaults missing")
	}
	sampler.logf("ignored")
	channel, stopTicker := tickEvery(time.Hour)
	stopTicker()
	if channel == nil {
		t.Error("no tick channel")
	}
	var zero Sample
	if _, err := (unsupportedSource{}).Read(); !errors.Is(err, ErrUnsupported) || zero.SampledAt != (time.Time{}) {
		t.Errorf("unsupported source = %v", err)
	}
}
