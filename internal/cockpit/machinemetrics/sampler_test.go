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
	mu      sync.Mutex
	clock   *time.Time
	reads   int
	errors  map[int]error
	partial map[int]bool
	panics  map[int]bool
	hang    chan struct{}
}

func (s *scriptSource) Read() (Sample, error) {
	s.mu.Lock()
	s.reads++
	n := s.reads
	*s.clock = s.clock.Add(Interval)
	err, partial, panics, hang := s.errors[n], s.partial[n], s.panics[n], s.hang
	s.mu.Unlock()
	if panics {
		panic("native call failed")
	}
	if hang != nil && n == 1 {
		<-hang
	}
	if err != nil && !partial {
		return Sample{}, err
	}
	return Sample{Load1: ptr(float64(n))}, err
}

func (s *scriptSource) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

func newScripted(source *scriptSource) (*scriptSource, *Sampler, chan time.Time, *[]string) {
	clock := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	source.clock = &clock
	ticks := make(chan time.Time)
	var logs []string
	sampler := New(Options{
		Source:   source,
		Now:      func() time.Time { return clock },
		Tick:     func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} },
		Logf:     func(format string, args ...any) { logs = append(logs, format) },
		StopWait: 50 * time.Millisecond,
	})
	return source, sampler, ticks, &logs
}

// TestRingBufferKeepsTheNewest360SamplesOldestFirst proves
// cockpit-views#ac:sampler-fills-a-ring-buffer: 400 ticks through the real loop
// leave exactly the newest 360 samples, oldest first and 10 seconds apart, and
// asking for them reads memory only.
func TestRingBufferKeepsTheNewest360SamplesOldestFirst(t *testing.T) {
	t.Parallel()
	source, sampler, ticks, _ := newScripted(&scriptSource{})
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
	if len(snapshot.Samples) != Capacity || !snapshot.Supported || snapshot.Failing {
		t.Fatalf("held %d samples, supported %v, failing %v", len(snapshot.Samples), snapshot.Supported, snapshot.Failing)
	}
	if *snapshot.Samples[0].Load1 != 41 || *snapshot.Samples[Capacity-1].Load1 != 400 {
		t.Errorf("window = %v .. %v, want 41 .. 400", *snapshot.Samples[0].Load1, *snapshot.Samples[Capacity-1].Load1)
	}
	for i := 1; i < len(snapshot.Samples); i++ {
		if gap := snapshot.Samples[i].SampledAt.Sub(snapshot.Samples[i-1].SampledAt); gap != Interval {
			t.Fatalf("samples %d and %d are %v apart, want %v", i-1, i, gap, Interval)
		}
	}
	snapshot.Samples[0].SampledAt = time.Time{}
	if sampler.Snapshot().Samples[0].SampledAt.IsZero() {
		t.Error("the snapshot aliases the buffer")
	}
}

func TestPartlyFilledBufferIsOldestFirst(t *testing.T) {
	t.Parallel()
	_, sampler, _, _ := newScripted(&scriptSource{})
	for range 3 {
		sampler.sampleOnce()
	}
	got := sampler.Snapshot()
	if len(got.Samples) != 3 || *got.Samples[0].Load1 != 1 || *got.Samples[2].Load1 != 3 {
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

// TestPartialReadingsAreKeptAndTotalFailuresMakeTheSamplerFail covers a source that
// gives only some of its parts, and one that gives nothing three times in a row.
func TestPartialReadingsAreKeptAndTotalFailuresMakeTheSamplerFail(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	_, sampler, _, logs := newScripted(&scriptSource{errors: map[int]error{1: boom, 2: boom, 3: boom, 4: boom, 5: boom, 6: boom, 8: boom}, partial: map[int]bool{1: true, 8: true}})
	for range 6 {
		sampler.sampleOnce()
	}
	snapshot := sampler.Snapshot()
	if len(snapshot.Samples) != 1 || !snapshot.Failing {
		t.Fatalf("after one partial and five failures: %d samples, failing %v; want 1 and failing", len(snapshot.Samples), snapshot.Failing)
	}
	version := snapshot.Version
	sampler.sampleOnce() // reading 7 succeeds
	sampler.sampleOnce() // reading 8 is partial again
	recovered := sampler.Snapshot()
	if len(recovered.Samples) != 3 || recovered.Failing || recovered.Version <= version {
		t.Errorf("after recovery: %d samples, failing %v", len(recovered.Samples), recovered.Failing)
	}
	if len(*logs) != 2 {
		t.Errorf("logged %d times, want once for each run of trouble (2)", len(*logs))
	}
}

func TestAPanickingSourceIsAFailedReadingNotACrash(t *testing.T) {
	t.Parallel()
	_, sampler, _, logs := newScripted(&scriptSource{panics: map[int]bool{1: true, 2: true}})
	sampler.sampleOnce()
	sampler.sampleOnce()
	sampler.sampleOnce()
	if got := len(sampler.Snapshot().Samples); got != 1 {
		t.Errorf("held %d samples, want the 1 after the panics", got)
	}
	if len(*logs) != 1 {
		t.Errorf("logged %d times, want once", len(*logs))
	}
}

func TestStartingTwiceStartsOnce(t *testing.T) {
	t.Parallel()
	source, sampler, _, _ := newScripted(&scriptSource{})
	var wait sync.WaitGroup
	for range 4 {
		wait.Go(func() { sampler.Start(t.Context())() })
	}
	wait.Wait()
	if source.count() != 1 {
		t.Errorf("source read %d times by four starts, want 1 (one loop)", source.count())
	}
}

// TestStopDoesNotWaitForAHungReading proves a reading stuck on a dead mount cannot
// hold up the daemon's shutdown beyond the bounded wait.
func TestStopDoesNotWaitForAHungReading(t *testing.T) {
	t.Parallel()
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	_, sampler, _, _ := newScripted(&scriptSource{hang: hang})
	stop := sampler.Start(t.Context())
	began := time.Now()
	stop()
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("stop took %v with a hung reading", took)
	}
}

// TestCancelStopsTheLoop: the context the sampler was started with ending is
// enough to end its loop, without its stop function: the loop gives its ticker
// back, and takes no reading after that.
func TestCancelStopsTheLoop(t *testing.T) {
	t.Parallel()
	source := &scriptSource{}
	clock := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	source.clock = &clock
	ticks, ended := make(chan time.Time), make(chan struct{})
	sampler := New(Options{Source: source, Now: func() time.Time { return clock }, Tick: func(time.Duration) (<-chan time.Time, func()) {
		return ticks, func() { close(ended) }
	}})
	ctx, cancel := context.WithCancel(t.Context())
	stop := sampler.Start(ctx)
	ticks <- time.Time{} // the loop is running: it took its first reading and this tick
	cancel()
	select {
	case <-ended:
	case <-time.After(30 * time.Second):
		t.Fatal("the loop did not end when its context did")
	}
	if got := source.count(); got != 2 {
		t.Fatalf("the source was read %d times, want the first reading and the one tick", got)
	}
	read := source.count()
	stop() // a stop after the loop ended returns and starts nothing
	if source.count() != read {
		t.Fatal("a reading was taken after the loop ended")
	}
}

// TestASamplerWithOnlyASourceSamplesWithTheRealClock proves the defaults: the
// real clock stamps the first sample, which is taken at once.
func TestASamplerWithOnlyASourceSamplesWithTheRealClock(t *testing.T) {
	t.Parallel()
	sampler := New(Options{Source: &scriptSource{clock: new(time.Time)}})
	began := time.Now()
	stop := sampler.Start(t.Context())
	deadline := time.Now().Add(5 * time.Second)
	for len(sampler.Snapshot().Samples) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	stop()
	samples := sampler.Snapshot().Samples
	if len(samples) != 1 || samples[0].SampledAt.Before(began) || samples[0].SampledAt.After(time.Now()) {
		t.Errorf("samples = %+v, want one stamped by the real clock", samples)
	}
}

func TestUnsupportedSourceReportsUnsupported(t *testing.T) {
	t.Parallel()
	if _, err := (unsupportedSource{}).Read(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("unsupported source = %v", err)
	}
}

// TestVersionIsTheSnapshotsVersionWithoutACopy: a reader that only wants to know
// whether what it prepared is still current asks Version, which is the version
// a snapshot carries, at every state of the sampler.
func TestVersionIsTheSnapshotsVersionWithoutACopy(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	sampler := New(Options{Source: &scriptSource{clock: &clock}, Now: func() time.Time { return clock }})
	if sampler.Version() != sampler.Snapshot().Version {
		t.Fatal("the versions differ before any sample")
	}
	for range 3 {
		before := sampler.Version()
		sampler.sampleOnce()
		if got := sampler.Version(); got == before || got != sampler.Snapshot().Version {
			t.Fatalf("after a sample the version = %d (was %d), the snapshot's %d", got, before, sampler.Snapshot().Version)
		}
	}
}
