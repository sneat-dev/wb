// Package machinemetrics samples this machine's CPU, load, memory and disk
// into an in-memory ring buffer for the Cockpit's machine-metrics route
// (cockpit-views#req:metrics-sampler). The sampler runs on the daemon's
// lifetime, never on a request: a request reads a copy of the buffer.
package machinemetrics

import (
	"context"
	"errors"
	"sync"
	"time"
)

// The sampling cadence and the buffer size: 360 samples at 10 seconds is one hour.
const (
	Interval = 10 * time.Second
	Capacity = 360
)

// ErrUnsupported is what a Source returns when this platform has no way to
// read metrics; the sampler then reports "unsupported" and stops sampling.
var ErrUnsupported = errors.New("machine metrics are not supported on this platform")

// Sample is one reading. It holds numbers and a time and nothing else. CPUPercent is
// absent where the platform has no reader that can report it correctly, and on the
// first reading of a platform that derives it from two readings.
type Sample struct {
	CPUPercent       *float64  `json:"cpu_percent,omitempty"`
	Load1            float64   `json:"load1"`
	MemoryUsedBytes  uint64    `json:"memory_used_bytes"`
	MemoryTotalBytes uint64    `json:"memory_total_bytes"`
	DiskFreeBytes    uint64    `json:"disk_free_bytes"`
	DiskTotalBytes   uint64    `json:"disk_total_bytes"`
	SampledAt        time.Time `json:"sampled_at"`
}

// Source reads the machine once. It returns a Sample with SampledAt unset (the
// sampler stamps it from its clock), ErrUnsupported where the platform cannot be
// read, or another error for a reading that failed this time.
type Source interface {
	Read() (Sample, error)
}

// Options configures a Sampler.
type Options struct {
	// Source reads the machine; it is required.
	Source Source
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Tick delivers the sampling ticks for an interval and a function that stops
	// them; nil means a time.Ticker. A test supplies its own.
	Tick func(interval time.Duration) (<-chan time.Time, func())
	// Logf reports a source that began failing, once until it recovers; nil discards.
	Logf func(format string, args ...any)
}

// Snapshot is a copy of the sampler's state at one moment. Version changes
// whenever Samples or Supported does, so a caller can cache what it prepared from it.
type Snapshot struct {
	Samples   []Sample
	Supported bool
	Version   uint64
}

// Sampler holds the ring buffer.
type Sampler struct {
	source Source
	now    func() time.Time
	tick   func(time.Duration) (<-chan time.Time, func())
	logf   func(string, ...any)

	mu          sync.RWMutex
	ring        [Capacity]Sample
	next, count int
	unsupported bool
	failing     bool
	version     uint64
}

// New builds a Sampler that has taken no sample.
func New(options Options) *Sampler {
	sampler := &Sampler{source: options.Source, now: options.Now, tick: options.Tick, logf: options.Logf}
	if sampler.now == nil {
		sampler.now = time.Now
	}
	if sampler.tick == nil {
		sampler.tick = tickEvery
	}
	if sampler.logf == nil {
		sampler.logf = func(string, ...any) {}
	}
	return sampler
}

// tickEvery is the production tick source.
func tickEvery(interval time.Duration) (<-chan time.Time, func()) {
	ticker := time.NewTicker(interval)
	return ticker.C, ticker.Stop
}

// Start samples now and then on every Interval until the returned function is
// called or ctx ends; the function stops the loop and waits for it. On a
// platform the source reports unsupported the loop ends after the first reading.
func (s *Sampler) Start(ctx context.Context) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.run(ctx)
	}()
	return func() {
		cancel()
		<-done
	}
}

func (s *Sampler) run(ctx context.Context) {
	ticks, stopTicks := s.tick(Interval)
	defer stopTicks()
	for {
		s.sampleOnce()
		if s.isUnsupported() {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
	}
}

// sampleOnce takes one reading. A failed reading is skipped: the buffer keeps
// what it has and the failure is logged once until a reading succeeds.
func (s *Sampler) sampleOnce() {
	sample, err := s.source.Read()
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case errors.Is(err, ErrUnsupported):
		s.unsupported = true
		s.version++
	case err != nil:
		if !s.failing {
			s.logf("machine metrics: a reading failed: %v", err)
		}
		s.failing = true
	default:
		s.failing = false
		sample.SampledAt = s.now()
		s.ring[s.next] = sample
		s.next = (s.next + 1) % Capacity
		s.count = min(s.count+1, Capacity)
		s.version++
	}
}

func (s *Sampler) isUnsupported() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.unsupported
}

// Snapshot returns a copy of the buffer, oldest first, so its last element is
// the latest sample. It reads memory only and never touches the source.
func (s *Sampler) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	samples := make([]Sample, 0, s.count)
	for i := range s.count {
		samples = append(samples, s.ring[(s.next-s.count+i+Capacity)%Capacity])
	}
	return Snapshot{Samples: samples, Supported: !s.unsupported, Version: s.version}
}
