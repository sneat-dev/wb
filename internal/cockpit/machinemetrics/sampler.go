// Package machinemetrics samples this machine's CPU, load, memory and disk
// into an in-memory ring buffer for the Cockpit's machine-metrics route
// (cockpit-views#req:metrics-sampler). The sampler runs on the daemon's
// lifetime, never on a request: a request reads a copy of the buffer.
package machinemetrics

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// The sampling cadence and the buffer size: 360 samples at 10 seconds is one
// hour. A source that reads nothing at all FailureLimit times in a row makes the
// sampler report itself failing; StopWait bounds how long stopping waits for a
// reading that is still in flight.
const (
	Interval     = 10 * time.Second
	Capacity     = 360
	FailureLimit = 3
	StopWait     = 2 * time.Second
)

// ErrUnsupported is what a Source returns when this platform has no way to
// read metrics; the sampler then reports "unsupported" and stops sampling.
var ErrUnsupported = errors.New("machine metrics are not supported on this platform")

// Sample is one reading: numbers and a time, nothing else. Every measurement
// is optional: a part that could not be read is absent, never zero. CPUPercent
// is derived from two readings, so it is absent on the first sample, when the
// counters did not advance or went backwards, and where it is unsupported.
type Sample struct {
	CPUPercent       *float64  `json:"cpu_percent,omitempty"`
	Load1            *float64  `json:"load1,omitempty"`
	MemoryUsedBytes  *uint64   `json:"memory_used_bytes,omitempty"`
	MemoryTotalBytes *uint64   `json:"memory_total_bytes,omitempty"`
	DiskFreeBytes    *uint64   `json:"disk_free_bytes,omitempty"`
	DiskTotalBytes   *uint64   `json:"disk_total_bytes,omitempty"`
	SampledAt        time.Time `json:"sampled_at"`
}

// HasData reports whether the sample holds any measurement.
func (s Sample) HasData() bool {
	return s.CPUPercent != nil || s.Load1 != nil || s.MemoryUsedBytes != nil || s.MemoryTotalBytes != nil || s.DiskFreeBytes != nil || s.DiskTotalBytes != nil
}

// Source reads the machine once. It returns the parts it could read in a Sample
// with SampledAt unset (the sampler stamps it from its clock), and an error for
// the parts it could not: a Sample with data and an error is a partial reading
// that is kept, a Sample with no data and an error is a failed one.
// ErrUnsupported means the platform cannot be read at all.
type Source interface {
	Read() (Sample, error)
}

// Options configures a Sampler.
type Options struct {
	// Source reads the machine; nil means a platform with no reader (unsupported).
	Source Source
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Tick delivers the sampling ticks for an interval and a function that stops
	// them; nil means a time.Ticker. A test supplies its own.
	Tick func(interval time.Duration) (<-chan time.Time, func())
	// StopWait bounds how long the stop function waits for a reading in flight;
	// zero or less means StopWait.
	StopWait time.Duration
	// Logf reports a source that began failing, once until it recovers; nil discards.
	Logf func(format string, args ...any)
}

// Snapshot is a copy of the sampler's state at one moment. Version changes
// whenever anything else in it does, so a caller can cache what it prepared from it.
// Failing is set after FailureLimit consecutive readings that gave nothing.
type Snapshot struct {
	Samples   []Sample
	Supported bool
	Failing   bool
	Version   uint64
}

// Sampler holds the ring buffer.
type Sampler struct {
	source   Source
	now      func() time.Time
	tick     func(time.Duration) (<-chan time.Time, func())
	logf     func(string, ...any)
	stopWait time.Duration

	startOnce sync.Once
	stopFn    func()

	mu          sync.RWMutex
	ring        [Capacity]Sample
	next, count int
	unsupported bool
	failures    int
	unhealthy   bool
	version     uint64
}

// New builds a Sampler that has taken no sample.
func New(options Options) *Sampler {
	sampler := &Sampler{source: options.Source, now: options.Now, tick: options.Tick, logf: options.Logf, stopWait: options.StopWait}
	if sampler.source == nil {
		sampler.source = unsupportedSource{}
	}
	if sampler.now == nil {
		sampler.now = time.Now
	}
	if sampler.tick == nil {
		sampler.tick = tickEvery
	}
	if sampler.logf == nil {
		sampler.logf = func(string, ...any) {}
	}
	if sampler.stopWait <= 0 {
		sampler.stopWait = StopWait
	}
	return sampler
}

// tickEvery is the production tick source.
func tickEvery(interval time.Duration) (<-chan time.Time, func()) {
	ticker := time.NewTicker(interval)
	return ticker.C, ticker.Stop
}

// Start samples now and then on every Interval until the returned function is
// called or ctx ends. It starts once: a second call returns the first call's stop
// function and starts nothing. The stop function cancels the loop and waits for it
// at most StopWait, so a reading hung on a dead mount cannot hold up the daemon's
// shutdown. On a platform the source reports unsupported the loop ends after the
// first reading.
func (s *Sampler) Start(ctx context.Context) (stop func()) {
	s.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			s.run(ctx)
		}()
		s.stopFn = func() {
			cancel()
			select {
			case <-done:
			case <-time.After(s.stopWait):
			}
		}
	})
	return s.stopFn
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

// read calls the source, turning a panic in it (a native call through purego, say)
// into a failed reading, so a bad reading never ends the daemon.
func (s *Sampler) read() (sample Sample, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			sample, err = Sample{}, fmt.Errorf("the source panicked: %v", recovered)
		}
	}()
	return s.source.Read()
}

// sampleOnce takes one reading. A partial reading is kept; a reading that gave
// nothing is skipped and, FailureLimit times in a row, makes the sampler failing.
// A source that began failing, or giving less than all, is logged once until a
// reading is whole again.
func (s *Sampler) sampleOnce() {
	sample, err := s.read()
	s.mu.Lock()
	defer s.mu.Unlock()
	if errors.Is(err, ErrUnsupported) && !sample.HasData() {
		s.unsupported = true
		s.version++
		return
	}
	if err != nil && !s.unhealthy {
		s.logf("machine metrics: a reading failed in whole or in part: %v", err)
	}
	s.unhealthy = err != nil
	if !sample.HasData() {
		if s.failures++; s.failures == FailureLimit {
			s.version++
		}
		return
	}
	s.failures = 0
	sample.SampledAt = s.now()
	s.ring[s.next] = sample
	s.next = (s.next + 1) % Capacity
	s.count = min(s.count+1, Capacity)
	s.version++
}

func (s *Sampler) isUnsupported() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.unsupported
}

// Version is the version a Snapshot taken now would carry, read without copying
// the buffer: a caller that holds what it prepared from a snapshot asks this to
// learn whether that is still current.
func (s *Sampler) Version() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version
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
	return Snapshot{Samples: samples, Supported: !s.unsupported, Failing: s.failures >= FailureLimit, Version: s.version}
}
