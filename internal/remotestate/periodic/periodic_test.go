package periodic

import (
	"context"
	"errors"
	"fmt"
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
	// entered, when set, is told of each publish as it starts, and hold, when
	// set, keeps each one from ending until it is closed.
	entered chan struct{}
	hold    chan struct{}
}

func (p *fakeProvider) Publish(_ context.Context, snapshot remotestate.Snapshot) (remotestate.PublishResult, error) {
	if p.entered != nil {
		p.entered <- struct{}{}
	}
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
		Logf: func(format string, args ...any) { h.logs = append(h.logs, fmt.Sprintf(format, args...)) },
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
	// The log line names the code and carries the detail, which is the daemon
	// log's alone (the status holds the code only).
	if len(h.logs) != 1 || h.logs[0] != "remote publish: publish_failed: push rejected /Users/alex/secret" {
		t.Fatalf("logs = %q", h.logs)
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
	extras := &source{extras: remotestate.Extras{Agents: []remotestate.AgentState{{Kind: "session", State: "live"}}, Metrics: &remotestate.MetricsSample{SampledAt: time.Now()}}}
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

// TestOnlyOneAttemptRunsAtATime: a call made while an attempt is publishing
// returns at once and starts nothing. The first attempt is held inside the
// store, which says when it is there; a second attempt that ran would reach the
// store too, and that is what fails the test, at once.
func TestOnlyOneAttemptRunsAtATime(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	h.provider.entered, h.provider.hold = make(chan struct{}), make(chan struct{})
	first, second := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(first)
		h.scan()
	}()
	select {
	case <-h.provider.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the attempt never reached the store")
	}
	go func() {
		defer close(second)
		h.scan()
	}()
	select {
	case <-second:
	case <-h.provider.entered:
		close(h.provider.hold)
		t.Fatal("a second attempt ran while the first was publishing")
	}
	close(h.provider.hold)
	<-first
	if status := h.publisher.Status(); h.provider.count() != 1 || status.Attempts != 1 || status.Scans != 1 {
		t.Fatalf("published %d, %+v", h.provider.count(), status)
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
	// A flap of activity alone is not a change.
	src.extras.Agents[0].Activity = "idle"
	h.clock.advance(11 * time.Minute)
	h.publisher.Publish(context.Background(), src)
	if h.collects != 1 {
		t.Fatalf("an activity flap opened the gate: collects %d", h.collects)
	}
	// A membership or state change does, and is held back until max(interval, 15 min) has passed
	// since the last publish.
	src.extras.Agents = append(src.extras.Agents, remotestate.AgentState{Kind: "run", RunID: "agt-2", State: "running"})
	h.clock.advance(11 * time.Minute) // 33 minutes since the last publish
	h.publisher.Publish(context.Background(), src)
	if h.provider.count() != 2 || len(h.provider.seen[1].Agents) != 2 || !h.provider.seen[1].PublishedAt.Equal(h.clock.Now()) {
		t.Fatalf("a new agent: published %d, %+v", h.provider.count(), h.provider.seen)
	}
	// The agents are not read by the scan: the repositories and worktrees are as
	// the last scan found them (the same change token), so it is not run again.
	if h.collects != 1 || h.publisher.Status().Scans != 1 {
		t.Fatalf("a change of the agents alone scanned the repositories again: collects %d, %+v", h.collects, h.publisher.Status())
	}
}

// TestAScanIsTakenAgainOnlyForTheTokenItWasMadeFor is the bound of that reuse:
// an agents-only change that is held back does not scan at each interval while
// it waits; a moved token, a source without a token, a scan as old as the
// keepalive and a scan that failed are each scanned anew.
func TestAScanIsTakenAgainOnlyForTheTokenItWasMadeFor(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(o *Options) { o.Agents, o.Every, o.Keepalive = true, 5*time.Minute, time.Hour })
	src := &source{token: "t", extras: remotestate.Extras{Agents: []remotestate.AgentState{{Kind: "run", RunID: "agt-1", State: "running"}}}}
	run := func(advance time.Duration, from remotestate.PublishSource) Status {
		h.clock.advance(advance)
		h.publisher.Publish(context.Background(), from)
		return h.publisher.Status()
	}
	run(0, src)
	src.extras.Agents = append(src.extras.Agents, remotestate.AgentState{Kind: "run", RunID: "agt-2", State: "running"})
	run(6*time.Minute, src)
	if status := run(6*time.Minute, src); status.Held != 2 || status.Scans != 1 || h.collects != 1 {
		t.Fatalf("a held agents-only change scanned at each interval: collects %d, %+v", h.collects, status)
	}
	if status := run(6*time.Minute, src); status.Published != 2 || status.Scans != 1 {
		t.Fatalf("the held change, once due: %+v", status)
	}
	// A moved token is a new scan.
	src.token, h.worktree = "u", "b"
	if status := run(6*time.Minute, src); status.Scans != 2 || status.Published != 3 {
		t.Fatalf("a moved token: %+v", status)
	}
	// A scan as old as the keepalive is not taken again, whatever the token says.
	src.extras.Agents = src.extras.Agents[:1]
	if status := run(time.Hour, src); status.Scans != 3 || status.Published != 4 {
		t.Fatalf("after the keepalive: %+v", status)
	}
	// A source with no token says nothing of what the scan reads.
	if status := run(6*time.Minute, &source{}); status.Scans != 4 {
		t.Fatalf("a source without a token: %+v", status)
	}
	// A scan that failed is not one to take again.
	src.token, h.collectEr = "v", errors.New("scan")
	run(6*time.Minute, src)
	h.collectEr = nil
	if status := run(6*time.Minute, src); status.Scans != 6 || status.Diagnostic != DiagnosticNone {
		t.Fatalf("after a failed scan: %+v", status)
	}
}

// TestAScanIsNotTakenAgainOnceTheOldestReadItKeptIsAsOldAsTheKeepalive: what a
// scan kept of an earlier read is as old as that read, so reusing the scan is
// bounded by the oldest read in it and not by the scan's own time. Nothing
// published is ever older than one keepalive.
func TestAScanIsNotTakenAgainOnceTheOldestReadItKeptIsAsOldAsTheKeepalive(t *testing.T) {
	t.Parallel()
	var oldestAge time.Duration
	var h *harness
	h = newHarness(t, func(o *Options) {
		o.Agents, o.Every, o.Keepalive = true, 5*time.Minute, time.Hour
		o.OldestRead = func() time.Time { return h.clock.Now().Add(-oldestAge) }
	})
	src := &source{token: "t", extras: remotestate.Extras{Agents: []remotestate.AgentState{{Kind: "run", RunID: "agt-1", State: "running"}}}}
	run := func(advance time.Duration) Status {
		h.clock.advance(advance)
		h.publisher.Publish(context.Background(), src)
		return h.publisher.Status()
	}
	oldestAge = 50 * time.Minute // the scan kept a read made 50 minutes before it
	run(0)
	src.extras.Agents = append(src.extras.Agents, remotestate.AgentState{Kind: "run", RunID: "agt-2", State: "running"})
	if status := run(6 * time.Minute); status.Scans != 1 {
		t.Fatalf("a read 56 minutes old was not reused: %+v", status)
	}
	if status := run(6 * time.Minute); status.Scans != 2 {
		t.Fatalf("a read 62 minutes old was reused though the scan is only 12 minutes old: %+v", status)
	}
}

// TestThePublishedHookIsToldOfEachPublishThatReachedTheStore and of no other
// attempt.
func TestThePublishedHookIsToldOfEachPublishThatReachedTheStore(t *testing.T) {
	t.Parallel()
	told := 0
	h := newHarness(t, func(o *Options) { o.Published = func() { told++ } })
	h.provider.errs = []error{errors.New("down")}
	h.scan() // fails
	if told != 0 {
		t.Fatal("a failed publish was reported as published")
	}
	h.clock.advance(11 * time.Minute)
	h.scan() // publishes
	h.clock.advance(11 * time.Minute)
	h.scan() // nothing changed: skipped
	if told != 1 || h.publisher.Status().Skipped != 1 {
		t.Fatalf("told %d times, %+v", told, h.publisher.Status())
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
	if h.provider.count() != 2 || h.publisher.Status().Gated != 0 {
		t.Fatalf("retry after a failure: published %d, %+v", h.provider.count(), h.publisher.Status())
	}
	none := &source{}
	for range 3 {
		h.clock.advance(11 * time.Minute)
		h.publisher.Publish(context.Background(), none)
	}
	if h.collects != 4 || h.publisher.Status().Gated != 0 {
		t.Fatalf("a source with no token was gated: %d %+v", h.collects, h.publisher.Status())
	}
	if h.publisher.Diagnostic() != DiagnosticNone {
		t.Fatalf("diagnostic = %q", h.publisher.Diagnostic())
	}
}

func TestAnAgentsOnlyChangeIsHeldBackForAtLeastFifteenMinutes(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(o *Options) { o.Agents = true })
	src := &source{token: "t0", extras: remotestate.Extras{Agents: []remotestate.AgentState{{Kind: "run", RunID: "agt-1", State: "running"}}}}
	h.publisher.Publish(context.Background(), src)
	src.extras.Agents = append(src.extras.Agents, remotestate.AgentState{Kind: "run", RunID: "agt-2", State: "running"})
	h.clock.advance(10 * time.Minute) // the interval has passed, 15 minutes have not
	h.publisher.Publish(context.Background(), src)
	if h.provider.count() != 1 || h.publisher.Status().Held != 1 {
		t.Fatalf("an agents-only change was not held: published %d, %+v", h.provider.count(), h.publisher.Status())
	}
	h.clock.advance(10 * time.Minute)
	h.publisher.Publish(context.Background(), src)
	if h.provider.count() != 2 {
		t.Fatalf("the held change was not published after 15 minutes: %d", h.provider.count())
	}
	// A change of anything else is never held.
	h.worktree = "b"
	src.token = "t1"
	h.clock.advance(10 * time.Minute)
	h.publisher.Publish(context.Background(), src)
	if h.provider.count() != 3 {
		t.Fatalf("a worktree change was held: %d", h.provider.count())
	}
}

// TestThePublishErrorLifecycle is the one rule of Diagnostic: a failure's code
// stays until the step that failed has worked again, and no longer. A scan that
// works clears collect_failed whatever the attempt then does; a failure of the
// store is cleared by the next publish that reaches it, and the attempt after
// such a failure is never gated, skipped or held back, so that there is one.
func TestThePublishErrorLifecycle(t *testing.T) {
	t.Parallel()
	type step struct {
		name    string
		advance time.Duration
		edit    func(*harness, *source)
		want    string
	}
	h := newHarness(t, nil)
	src := &source{token: "t0"}
	steps := []step{
		{"first publish succeeds", 0, nil, ""},
		{"a failure sets its code", 11 * time.Minute, func(h *harness, s *source) {
			s.token, h.worktree = "t1", "b"
			h.provider.errs = []error{errors.New("x")}
		}, DiagnosticPublishFailed},
		{"the store still failing keeps it, though the machine is as it was published", 21 * time.Minute, func(h *harness, s *source) {
			s.token, h.worktree = "t0", "a"
			h.provider.errs = []error{errors.New("x")}
		}, DiagnosticPublishFailed},
		{"the store answering clears it, with nothing new to publish and the source's token unchanged", 41 * time.Minute, nil, ""},
		{"a gated attempt after that changes nothing", 11 * time.Minute, nil, ""},
		{"a collect failure sets its code", 11 * time.Minute, func(h *harness, s *source) { s.token = "t3"; h.collectEr = errors.New("scan") }, DiagnosticCollectFailed},
		{"a scan that works clears it, though it finds nothing to publish", 21 * time.Minute, func(h *harness, s *source) { h.collectEr = nil }, ""},
		{"a collect failure on the token that was skipped (the keepalive's scan)", time.Hour * 7, func(h *harness, s *source) { h.collectEr = errors.New("scan") }, DiagnosticCollectFailed},
		{"is not gated: the next scan clears it and publishes the keepalive", 21 * time.Minute, func(h *harness, s *source) { h.collectEr = nil }, ""},
		{"a store failure sets its code", 11 * time.Minute, func(h *harness, s *source) {
			s.token, h.openErr, h.publisher.provider, h.worktree = "t4", errors.New("clone"), nil, "e"
		}, DiagnosticOpenFailed},
		{"a scan that works does not clear a failure of the store", 21 * time.Minute, func(h *harness, s *source) { h.collectEr = nil }, DiagnosticOpenFailed},
		{"recovery clears it", 41 * time.Minute, func(h *harness, s *source) { h.openErr, h.worktree = nil, "d" }, ""},
	}
	for _, st := range steps {
		h.clock.advance(st.advance)
		if st.edit != nil {
			st.edit(h, src)
		}
		h.publisher.Publish(context.Background(), src)
		if got := h.publisher.Diagnostic(); got != st.want {
			t.Fatalf("%s: diagnostic = %q, want %q (%+v)", st.name, got, st.want, h.publisher.Status())
		}
	}
	// The backoff a collect failure started ends with the scan that works: the
	// next attempt is due after the interval.
	if status := h.publisher.Status(); status.Gated != 1 || status.Skipped != 1 {
		t.Fatalf("the lifecycle ran %+v, want its one gated and its one skipped attempt", status)
	}
}

// TestAHeldBackAttemptClearsCollectFailedAndKeepsARememberedRefusal: the scan
// worked, so collect_failed goes whatever the attempt does next; what an older
// hub's refusal of the optional fields says comes back in its place, since no
// publish has carried them.
func TestAHeldBackAttemptClearsCollectFailedAndKeepsARememberedRefusal(t *testing.T) {
	t.Parallel()
	provider := &refusingProvider{fakeProvider: &fakeProvider{}, refuse: true}
	h := newHarness(t, func(o *Options) {
		o.Agents, o.Every = true, 5*time.Minute
		o.Open = func() (remotestate.Provider, error) { return provider, nil }
	})
	src := &source{token: "t0", extras: remotestate.Extras{Agents: []remotestate.AgentState{{Kind: "run", RunID: "agt-1", State: "running"}}}}
	run := func(advance time.Duration) string {
		h.clock.advance(advance)
		h.publisher.Publish(context.Background(), src)
		return h.publisher.Diagnostic()
	}
	if got := run(0); got != DiagnosticOptionalFields {
		t.Fatalf("an older hub's refusal: %q", got)
	}
	src.token, h.collectEr = "t1", errors.New("scan")
	if got := run(6 * time.Minute); got != DiagnosticCollectFailed {
		t.Fatalf("a failed scan: %q", got)
	}
	// The scan works; only the agents changed, three minutes short of the hold.
	h.collectEr = nil
	src.extras.Agents = append(src.extras.Agents, remotestate.AgentState{Kind: "run", RunID: "agt-2", State: "running"})
	if got := run(6 * time.Minute); got != DiagnosticOptionalFields || h.publisher.Status().Held != 1 {
		t.Fatalf("a held-back attempt after a failed scan: %q, %+v", got, h.publisher.Status())
	}
}

func TestOptionalFieldsDroppedStaysUntilAPublishThatCarriedThemSucceeds(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(o *Options) { o.Agents = true })
	src := &source{token: "t0", extras: remotestate.Extras{Agents: []remotestate.AgentState{{Kind: "run", RunID: "a", State: "running"}}}}
	// A provider that refuses optional fields until upgraded and remembers the refusal.
	old := &refusingProvider{fakeProvider: h.provider, refuse: true}
	h.publisher.provider = old
	h.publisher.Publish(context.Background(), src)
	if h.publisher.Diagnostic() != DiagnosticOptionalFields || old.fullAttempts != 1 {
		t.Fatalf("first: %q, full attempts %d", h.publisher.Diagnostic(), old.fullAttempts)
	}
	// Changes within the day publish without the fields and leave the code.
	for i := range 3 {
		h.clock.advance(11 * time.Minute)
		src.token, h.worktree = "t"+string(rune('1'+i)), string(rune('b'+i))
		h.publisher.Publish(context.Background(), src)
		if h.publisher.Diagnostic() != DiagnosticOptionalFields {
			t.Fatalf("change %d cleared the code: %q", i, h.publisher.Diagnostic())
		}
	}
	if old.fullAttempts != 1 {
		t.Fatalf("the refusal was not remembered: %d full attempts", old.fullAttempts)
	}
	// An idle machine: gated and skipped attempts leave it too.
	h.clock.advance(11 * time.Minute)
	h.publisher.Publish(context.Background(), src)
	if h.publisher.Diagnostic() != DiagnosticOptionalFields {
		t.Fatal("a gated attempt changed the code")
	}
	// After a day the next attempt publishes the full payload at once, with no
	// change, bypassing the gate and the digest skip; the upgraded hub takes it and clears the code.
	old.refuse = false
	h.clock.advance(24 * time.Hour)
	published := h.provider.count()
	h.publisher.Publish(context.Background(), src)
	if h.provider.count() != published+1 || h.publisher.Diagnostic() != DiagnosticNone || old.fullAttempts != 2 {
		t.Fatalf("after the day: published %d->%d, diagnostic %q, full attempts %d", published, h.provider.count(), h.publisher.Diagnostic(), old.fullAttempts)
	}
	// And it is not forced again.
	h.clock.advance(11 * time.Minute)
	h.publisher.Publish(context.Background(), src)
	if h.provider.count() != published+1 {
		t.Fatal("a forced publish repeated")
	}
}

func TestAHubStillOldAfterADayIsTriedOnceAndRememberedAgain(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(o *Options) { o.Agents = true })
	src := &source{token: "t0", extras: remotestate.Extras{Agents: []remotestate.AgentState{{Kind: "run", RunID: "a", State: "running"}}}}
	old := &refusingProvider{fakeProvider: h.provider, refuse: true}
	h.publisher.provider = old
	h.publisher.Publish(context.Background(), src)
	h.clock.advance(25 * time.Hour)
	h.publisher.Publish(context.Background(), src)
	if old.fullAttempts != 2 || h.publisher.Diagnostic() != DiagnosticOptionalFields {
		t.Fatalf("full attempts %d, diagnostic %q", old.fullAttempts, h.publisher.Diagnostic())
	}
	h.clock.advance(11 * time.Minute)
	h.publisher.Publish(context.Background(), src)
	if old.fullAttempts != 2 {
		t.Fatal("the full payload was retried before the next day")
	}
}

// refusingProvider answers 400 to a payload with optional fields while refuse is
// set, and remembers the refusal as the hub provider does.
type refusingProvider struct {
	*fakeProvider
	refuse       bool
	fullAttempts int
	until        time.Time
}

func (p *refusingProvider) OptionalFieldsRefused(now time.Time) bool { return now.Before(p.until) }
func (p *refusingProvider) RefuseOptionalFields(until time.Time)     { p.until = until }

func (p *refusingProvider) Publish(ctx context.Context, snapshot remotestate.Snapshot) (remotestate.PublishResult, error) {
	if snapshot.HasOptional() {
		p.fullAttempts++
		if p.refuse {
			return remotestate.PublishResult{}, status(400)
		}
	}
	return p.fakeProvider.Publish(ctx, snapshot)
}
