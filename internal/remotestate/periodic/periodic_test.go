package periodic

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeProvider counts publishes and captures what it was given.
type fakeProvider struct {
	mu   sync.Mutex
	seen []remotestate.Snapshot
	errs []error
	hold chan struct{}
}

func (p *fakeProvider) Publish(_ context.Context, snapshot remotestate.Snapshot) (remotestate.PublishResult, error) {
	if p.hold != nil {
		<-p.hold
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = append(p.seen, snapshot)
	if len(p.errs) > 0 {
		err := p.errs[0]
		p.errs = p.errs[1:]
		return remotestate.PublishResult{}, err
	}
	return remotestate.PublishResult{Location: "sha"}, nil
}
func (p *fakeProvider) count() int { p.mu.Lock(); defer p.mu.Unlock(); return len(p.seen) }
func (p *fakeProvider) List(context.Context) ([]remotestate.Entry, error) {
	return nil, nil
}
func (p *fakeProvider) Claim(context.Context, remotestate.Claim, remotestate.ClaimMode, string) (remotestate.ClaimOutcome, error) {
	return remotestate.ClaimOutcome{}, nil
}
func (p *fakeProvider) Release(context.Context, string, string, string, bool) (remotestate.ReleaseOutcome, error) {
	return remotestate.ReleaseOutcome{}, nil
}
func (p *fakeProvider) Claims(context.Context) ([]remotestate.ClaimEntry, error) { return nil, nil }

type harness struct {
	clock     *clock
	provider  *fakeProvider
	publisher *Publisher
	collects  int
	opens     int
	worktree  string
	collectEr error
	openErr   error
	logs      []string
}

// source is a fake PublishSource.
type source struct {
	extras remotestate.Extras
	token  string
}

func (s *source) PublishExtras() remotestate.Extras { return s.extras }
func (s *source) ChangeToken() string               { return s.token }

func newHarness(t *testing.T, change func(*Options)) *harness {
	t.Helper()
	h := &harness{clock: &clock{now: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}, provider: &fakeProvider{}, worktree: "a"}
	options := Options{
		Every: 10 * time.Minute, Now: h.clock.Now,
		Collect: func(_ context.Context, now time.Time) (remotestate.Snapshot, error) {
			h.collects++
			if h.collectEr != nil {
				return remotestate.Snapshot{}, h.collectEr
			}
			return remotestate.Snapshot{SchemaVersion: 1, Login: "alex", Machine: "mac", PublishedAt: now, Worktrees: []remotestate.WorktreeState{{Task: h.worktree}}}, nil
		},
		Open: func() (remotestate.Provider, error) {
			h.opens++
			if h.openErr != nil {
				return nil, h.openErr
			}
			return h.provider, nil
		},
		Logf: func(format string, args ...any) { h.logs = append(h.logs, format+strings.Repeat(" %v", 0)) },
	}
	if change != nil {
		change(&options)
	}
	h.publisher = New(options)
	return h
}

func (h *harness) scan() { h.publisher.Publish(context.Background(), nil) }

func TestNothingIsPublishedWithoutAnInterval(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(o *Options) { o.Every = 0 })
	h.scan()
	if h.collects != 0 || h.opens != 0 || h.provider.count() != 0 {
		t.Fatalf("an unset interval scanned %d, opened %d, published %d", h.collects, h.opens, h.provider.count())
	}
	for _, change := range []func(*Options){func(o *Options) { o.Collect = nil }, func(o *Options) { o.Open = nil }} {
		h = newHarness(t, change)
		h.scan()
		if h.provider.count() != 0 {
			t.Fatal("a publisher missing a collaborator published")
		}
	}
}

func TestPublishesNoMoreOftenThanTheIntervalAndOnlyWhenChanged(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	h.scan()
	if h.provider.count() != 1 || h.opens != 1 {
		t.Fatalf("first scan published %d", h.provider.count())
	}
	// A scan inside the interval does not even read the machine.
	h.clock.advance(9 * time.Minute)
	h.scan()
	if h.collects != 1 {
		t.Fatalf("scanned again inside the interval: %d", h.collects)
	}
	// After the interval an unchanged machine is read and skipped.
	h.clock.advance(2 * time.Minute)
	h.scan()
	if h.collects != 2 || h.provider.count() != 1 || h.publisher.Status().Skipped != 1 {
		t.Fatalf("idle machine: collects=%d published=%d status=%+v", h.collects, h.provider.count(), h.publisher.Status())
	}
	// A change is published at once when the interval has passed.
	h.worktree = "b"
	h.clock.advance(10 * time.Minute)
	h.scan()
	if h.provider.count() != 2 || h.opens != 1 {
		t.Fatalf("change not published: %d (opens %d)", h.provider.count(), h.opens)
	}
	status := h.publisher.Status()
	if status.Published != 2 || status.Attempts != 3 || status.Diagnostic != DiagnosticNone || status.LastPublished.IsZero() {
		t.Fatalf("status = %+v", status)
	}
}

func TestAnIdleMachineIsStillPublishedOnTheKeepalive(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(o *Options) { o.Keepalive = time.Hour })
	h.scan()
	published := 1
	for range 12 { // two hours of 10-minute intervals
		h.clock.advance(10 * time.Minute)
		h.scan()
	}
	if got := h.provider.count(); got != published+2 {
		t.Fatalf("an idle machine published %d times in two hours, want 3 (first, +1h, +2h)", got)
	}
}

func TestAnIntervalBelowTheMinimumIsTheCallersToRaise(t *testing.T) {
	t.Parallel()
	// The publisher takes the already-raised value, PublishConfig.PublishEvery.
	if got := (remotestate.PublishConfig{Interval: time.Minute}).PublishEvery(); got != remotestate.MinPublishInterval {
		t.Fatalf("1m = %s", got)
	}
	h := newHarness(t, func(o *Options) { o.Every = remotestate.PublishConfig{Interval: time.Minute}.PublishEvery() })
	h.scan()
	h.worktree = "b"
	h.clock.advance(4 * time.Minute)
	h.scan()
	h.clock.advance(time.Minute)
	h.scan()
	if h.provider.count() != 2 || h.collects != 2 {
		t.Fatalf("published %d, scanned %d: a 1-minute interval must act as 5 minutes", h.provider.count(), h.collects)
	}
}

func TestAFailedPublishIsRetriedWithBackoffAndIsATypedDiagnostic(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(o *Options) { o.MaxBackoff = 25 * time.Minute })
	h.provider.errs = []error{errors.New("push rejected /Users/alex/secret"), errors.New("again"), errors.New("again")}
	h.scan()
	if st := h.publisher.Status(); st.Diagnostic != DiagnosticPublishFailed || st.Published != 0 {
		t.Fatalf("status = %+v", st)
	}
	if len(h.logs) != 1 {
		t.Fatalf("logs = %v", h.logs)
	}
	// Retried at the next interval.
	h.clock.advance(9 * time.Minute)
	h.scan()
	if h.provider.count() != 1 {
		t.Fatal("retried before the interval")
	}
	h.clock.advance(time.Minute)
	h.scan() // second failure: wait doubles to 20 minutes
	h.clock.advance(19 * time.Minute)
	h.scan()
	if h.provider.count() != 2 {
		t.Fatalf("published %d, want the backoff to hold the third attempt", h.provider.count())
	}
	h.clock.advance(time.Minute)
	h.scan() // third failure: the wait would be 40 minutes, capped at 25
	h.clock.advance(24 * time.Minute)
	h.scan()
	if h.provider.count() != 3 {
		t.Fatalf("published %d before the capped backoff passed", h.provider.count())
	}
	h.clock.advance(time.Minute)
	h.scan() // succeeds
	st := h.publisher.Status()
	if h.provider.count() != 4 || st.Diagnostic != DiagnosticNone || st.Published != 1 {
		t.Fatalf("recovery: count=%d status=%+v", h.provider.count(), st)
	}
	// The failure repeated three times was logged once, not three times.
	if len(h.logs) != 1 {
		t.Fatalf("logs = %v", h.logs)
	}
	h.clock.advance(10 * time.Minute)
	h.worktree = "z"
	h.scan()
	if h.provider.count() != 5 {
		t.Fatal("a recovered publisher stopped publishing")
	}
}

func TestEveryFailureKindHasItsOwnCode(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	h.collectEr = errors.New("scan failed")
	h.scan()
	if h.publisher.Status().Diagnostic != DiagnosticCollectFailed || h.opens != 0 {
		t.Fatalf("collect: %+v opens %d", h.publisher.Status(), h.opens)
	}
	h.collectEr, h.openErr = nil, errors.New("no clone")
	h.clock.advance(10 * time.Minute)
	h.scan()
	if h.publisher.Status().Diagnostic != DiagnosticOpenFailed {
		t.Fatalf("open: %+v", h.publisher.Status())
	}
	// Panics in a collaborator are a failed attempt, and later attempts still run.
	p := newHarness(t, func(o *Options) {
		o.Collect = func(context.Context, time.Time) (remotestate.Snapshot, error) { panic("boom") }
	})
	p.scan()
	if p.publisher.Status().Diagnostic != DiagnosticCollectFailed {
		t.Fatalf("panic: %+v", p.publisher.Status())
	}
	p.clock.advance(10 * time.Minute)
	p.scan()
	if p.publisher.Status().Attempts != 2 {
		t.Fatal("a panic stopped the publisher")
	}
}

func TestAnOlderHubRefusalIsOneRetryAndADiagnostic(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(o *Options) { o.Agents, o.Metrics = true, true })
	h.provider.errs = []error{status(400)}
	extras := &source{extras: remotestate.Extras{Agents: []remotestate.AgentState{{Kind: "run", State: "running"}}, Metrics: &remotestate.MetricsSample{SampledAt: h.clock.Now()}}}
	h.publisher.Publish(context.Background(), extras)
	st := h.publisher.Status()
	if h.provider.count() != 2 || st.Diagnostic != DiagnosticOptionalFields || st.Published != 1 {
		t.Fatalf("count=%d status=%+v", h.provider.count(), st)
	}
	if !h.provider.seen[0].HasOptional() || h.provider.seen[1].HasOptional() {
		t.Fatal("the retry did not drop the optional fields")
	}
	// The diagnostic is not a failure: the next interval is the normal one.
	h.clock.advance(10 * time.Minute)
	h.publisher.Publish(context.Background(), extras)
	if h.publisher.Status().Skipped != 1 {
		t.Fatalf("status = %+v", h.publisher.Status())
	}
}

type status int

func (s status) Error() string   { return "status" }
func (s status) HTTPStatus() int { return int(s) }

func TestExtrasAreAddedOnlyUnderTheirFlags(t *testing.T) {
	t.Parallel()
	extras := &source{extras: remotestate.Extras{Agents: []remotestate.AgentState{{Kind: "session", State: "live"}}, Metrics: &remotestate.MetricsSample{}}}
	for _, test := range []struct{ agents, metrics bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		h := newHarness(t, func(o *Options) { o.Agents, o.Metrics = test.agents, test.metrics })
		h.publisher.Publish(context.Background(), extras)
		got := h.provider.seen[0]
		if (len(got.Agents) == 1) != test.agents || (got.Metrics != nil) != test.metrics {
			t.Errorf("flags %+v published agents=%d metrics=%v", test, len(got.Agents), got.Metrics)
		}
	}
	// With no extras function the flags have nothing to add.
	h := newHarness(t, func(o *Options) { o.Agents, o.Metrics = true, true })
	h.scan()
	if got := h.provider.seen[0]; got.Agents != nil || got.Metrics != nil {
		t.Errorf("published %+v", got)
	}
}

func TestOnlyOneAttemptRunsAtATime(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	h.provider.hold = make(chan struct{})
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(started)
		h.scan()
		close(done)
	}()
	<-started
	deadline := time.After(5 * time.Second)
	for {
		h.publisher.mu.Lock()
		running := h.publisher.running
		h.publisher.mu.Unlock()
		if running {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the attempt never started")
		case <-time.After(time.Millisecond):
		}
	}
	h.scan() // returns at once: single-flight
	close(h.provider.hold)
	<-done
	if h.provider.count() != 1 {
		t.Fatalf("published %d", h.provider.count())
	}
}

func TestDefaultsAndConfiguredBounds(t *testing.T) {
	t.Parallel()
	p := New(Options{Every: time.Minute})
	if p.options.Timeout != DefaultTimeout || p.options.Keepalive != DefaultKeepalive || p.options.MaxBackoff != DefaultMaxBackoff || p.now == nil {
		t.Fatalf("defaults = %+v", p.options)
	}
	p.options.Logf("discarded %d", 1)
	// The time bound reaches the collaborators.
	var deadline bool
	bounded := New(Options{
		Every: time.Minute, Timeout: time.Second,
		Collect: func(ctx context.Context, now time.Time) (remotestate.Snapshot, error) {
			_, deadline = ctx.Deadline()
			return remotestate.Snapshot{}, errors.New("stop")
		},
		Open: func() (remotestate.Provider, error) { return nil, errors.New("unused") },
	})
	bounded.Publish(context.Background(), nil)
	if !deadline {
		t.Error("the attempt has no time bound")
	}
}

func TestNoScanRunsWhileTheSourceReportsNothingChanged(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	src := &source{token: "t1"}
	run := func(advance time.Duration) {
		h.clock.advance(advance)
		h.publisher.Publish(context.Background(), src)
	}
	run(0) // first publish: scans
	if h.collects != 1 || h.provider.count() != 1 {
		t.Fatalf("first: collects %d published %d", h.collects, h.provider.count())
	}
	for range 5 {
		run(11 * time.Minute)
	}
	if h.collects != 1 || h.publisher.Status().Gated != 5 || h.publisher.Status().Attempts != 1 {
		t.Fatalf("an unchanged source scanned: collects %d, %+v", h.collects, h.publisher.Status())
	}
	// The interval still holds back a gated attempt as it does a scan.
	run(time.Minute)
	if h.publisher.Status().Gated != 5 {
		t.Fatal("the interval did not apply to the gate")
	}
	// A moved token opens the gate: the scan runs, finds the same digest and skips the publish.
	src.token = "t2"
	run(11 * time.Minute)
	if h.collects != 2 || h.provider.count() != 1 || h.publisher.Status().Skipped != 1 {
		t.Fatalf("moved token: collects %d published %d %+v", h.collects, h.provider.count(), h.publisher.Status())
	}
	// ... and the new token is the one now remembered.
	run(11 * time.Minute)
	if h.collects != 2 {
		t.Fatalf("scanned again for the same token: %d", h.collects)
	}
	// A change that publishes.
	src.token, h.worktree = "t3", "b"
	run(11 * time.Minute)
	if h.provider.count() != 2 {
		t.Fatalf("a change was not published: %d", h.provider.count())
	}
}

func TestTheKeepaliveOpensTheGateAndIsAtLeastTheInterval(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(o *Options) { o.Keepalive = time.Hour })
	src := &source{token: "t"}
	h.publisher.Publish(context.Background(), src)
	h.clock.advance(61 * time.Minute)
	h.publisher.Publish(context.Background(), src)
	if h.collects != 2 || h.publisher.Status().Published != 2 {
		t.Fatalf("keepalive did not open the gate and publish: %d %+v", h.collects, h.publisher.Status())
	}
	if got := New(Options{Every: 12 * time.Hour}).options.Keepalive; got != 12*time.Hour {
		t.Errorf("keepalive for a 12h interval = %s, want 12h", got)
	}
	if got := New(Options{Every: time.Hour}).options.Keepalive; got != DefaultKeepalive {
		t.Errorf("keepalive for a 1h interval = %s, want %s", got, DefaultKeepalive)
	}
	if got := New(Options{Every: time.Hour, Keepalive: time.Minute}).options.Keepalive; got != time.Hour {
		t.Errorf("a keepalive below the interval = %s", got)
	}
}

func TestAChangeOfPublishedAgentsOpensTheGate(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(o *Options) { o.Agents = true })
	src := &source{token: "t", extras: remotestate.Extras{Agents: []remotestate.AgentState{{Kind: "run", State: "running"}}}}
	h.publisher.Publish(context.Background(), src)
	h.clock.advance(11 * time.Minute)
	h.publisher.Publish(context.Background(), src)
	if h.collects != 1 {
		t.Fatal("unchanged agents opened the gate")
	}
	src.extras.Agents[0].Activity = "idle"
	h.clock.advance(11 * time.Minute)
	h.publisher.Publish(context.Background(), src)
	if h.collects != 2 || h.provider.count() != 2 {
		t.Fatalf("changed agents: collects %d published %d", h.collects, h.provider.count())
	}
}

func TestAFailedAttemptAndATokenlessSourceNeverGate(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	src := &source{token: "t"}
	h.provider.errs = []error{errors.New("down")}
	h.publisher.Publish(context.Background(), src)
	h.clock.advance(11 * time.Minute)
	h.publisher.Publish(context.Background(), src) // retried, not gated
	if h.collects != 2 || h.provider.count() != 2 {
		t.Fatalf("retry after a failure: collects %d published %d", h.collects, h.provider.count())
	}
	none := &source{}
	for range 3 {
		h.clock.advance(11 * time.Minute)
		h.publisher.Publish(context.Background(), none)
	}
	if h.collects != 5 || h.publisher.Status().Gated != 0 {
		t.Fatalf("a source with no token was gated: %d %+v", h.collects, h.publisher.Status())
	}
	if h.publisher.Diagnostic() != DiagnosticNone {
		t.Fatalf("diagnostic = %q", h.publisher.Diagnostic())
	}
}
