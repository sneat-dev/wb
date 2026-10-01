// Package periodic publishes this machine's snapshot to the remote store from
// the daemon, after a successful local scan, when the owner opted in with
// remote.publish.interval (cockpit-views#req:periodic-remote-publish and
// remote-state#req:remote-publish-periodic).
//
// It adds no publisher of its own: the snapshot is built by the same collector
// as `wb remote publish` and sent through the same remotestate.Provider. What
// it adds is the policy around that: not more often than the interval, not at
// all when nothing changed, one publish at a time, a bounded time for each, and
// a typed diagnostic with a doubling backoff on failure that never reaches the
// caller as an error.
package periodic

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
)

// The diagnostics of the last attempt. They are codes, never the text of an
// error, which could carry a path or a remote's response.
const (
	DiagnosticNone           = ""
	DiagnosticCollectFailed  = "collect_failed"
	DiagnosticOpenFailed     = "store_unavailable"
	DiagnosticPublishFailed  = "publish_failed"
	DiagnosticOptionalFields = "optional_fields_dropped"
)

const (
	// DefaultTimeout bounds one attempt: the scan and the publish together.
	DefaultTimeout = 5 * time.Minute
	// DefaultKeepalive is how long an unchanged machine goes without a publish
	// before one is sent anyway, so a machine that is only idle is never taken
	// for a stale one (`wb remote machines` marks a snapshot stale after 24
	// hours by default).
	DefaultKeepalive = 6 * time.Hour
	// DefaultMaxBackoff is the longest wait after repeated failures.
	DefaultMaxBackoff = time.Hour
)

// Options configures a Publisher.
type Options struct {
	// Every is the least time between two attempts, remotestate.PublishConfig's
	// PublishEvery(); it must be positive.
	Every time.Duration
	// Agents and Metrics are the opt-in flags that put this machine's agents
	// and latest sample into the snapshot.
	Agents, Metrics bool
	// Collect builds the snapshot as `wb remote publish` does, stamped now.
	Collect func(ctx context.Context, now time.Time) (remotestate.Snapshot, error)
	// Open returns the provider; it is asked once, on the first attempt that
	// reaches a publish, and its answer kept.
	Open func() (remotestate.Provider, error)
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Timeout bounds one attempt; zero or less means DefaultTimeout.
	Timeout time.Duration
	// Keepalive is the longest an unchanged snapshot goes unpublished; zero or
	// less means DefaultKeepalive.
	Keepalive time.Duration
	// MaxBackoff caps the wait after failures; zero or less means
	// DefaultMaxBackoff.
	MaxBackoff time.Duration
	// Logf reports a diagnostic when it changes; nil discards.
	Logf func(format string, args ...any)
}

// Status is what the publisher knows of its own attempts.
type Status struct {
	// Diagnostic is the code of the last attempt, DiagnosticNone when it
	// published or found nothing to publish.
	Diagnostic string
	// Attempts counts the attempts that ran a scan, Published the publishes
	// that reached the store and Skipped the attempts that found nothing
	// changed.
	Attempts, Published, Skipped int
	// LastPublished is when the last publish reached the store.
	LastPublished time.Time
}

// Publisher is the periodic publisher. It is safe for concurrent use; a call
// made while an attempt runs returns at once.
type Publisher struct {
	options Options
	now     func() time.Time

	mu           sync.Mutex
	running      bool
	next         time.Time
	failures     int
	lastDigest   string
	provider     remotestate.Provider
	status       Status
	loggedStatus string
}

// New builds a Publisher; a non-positive Every, or a missing Collect or Open,
// makes it publish nothing.
func New(options Options) *Publisher {
	publisher := &Publisher{options: options, now: options.Now}
	if publisher.now == nil {
		publisher.now = time.Now
	}
	if options.Timeout <= 0 {
		publisher.options.Timeout = DefaultTimeout
	}
	if options.Keepalive <= 0 {
		publisher.options.Keepalive = DefaultKeepalive
	}
	if options.MaxBackoff <= 0 {
		publisher.options.MaxBackoff = DefaultMaxBackoff
	}
	if options.Logf == nil {
		publisher.options.Logf = func(string, ...any) {}
	}
	return publisher
}

// Status returns a copy of what the publisher knows of its attempts.
func (p *Publisher) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

// Publish makes one attempt if the interval (or, after a failure, the backoff)
// has passed and none is running: it scans, adds the extras the flags allow,
// skips a snapshot that says what the last published one said, and publishes
// through the provider. extras is asked only when an attempt runs. It returns
// nothing: every outcome is a Status and a logged code.
func (p *Publisher) Publish(ctx context.Context, extras func() remotestate.Extras) {
	if p.options.Every <= 0 || p.options.Collect == nil || p.options.Open == nil {
		return
	}
	now := p.now()
	p.mu.Lock()
	if p.running || now.Before(p.next) {
		p.mu.Unlock()
		return
	}
	p.running = true
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, p.options.Timeout)
	defer cancel()
	diagnostic, detail, published, skipped, digest := p.guarded(ctx, now, extras)

	p.mu.Lock()
	defer p.mu.Unlock()
	p.running = false
	p.status.Attempts++
	p.status.Diagnostic = diagnostic
	switch {
	case published:
		p.status.Published++
		p.status.LastPublished = now
		p.lastDigest = digest
	case skipped:
		p.status.Skipped++
	}
	failed := diagnostic != DiagnosticNone && diagnostic != DiagnosticOptionalFields
	if failed {
		p.failures++
		wait := p.options.Every
		for step := 1; step < p.failures && wait < p.options.MaxBackoff; step++ {
			wait *= 2
		}
		p.next = now.Add(min(wait, max(p.options.MaxBackoff, p.options.Every)))
	} else {
		p.failures = 0
		p.next = now.Add(p.options.Every)
	}
	if diagnostic != p.loggedStatus {
		p.loggedStatus = diagnostic
		if diagnostic != DiagnosticNone {
			p.options.Logf("remote publish: %s: %s", diagnostic, detail)
		}
	}
}

// guarded is attempt with a panic in a collaborator turned into a failed
// attempt: a daemon never ends because a publish did.
func (p *Publisher) guarded(ctx context.Context, now time.Time, extras func() remotestate.Extras) (diagnostic, detail string, published, skipped bool, digest string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			diagnostic, detail, published, skipped, digest = DiagnosticCollectFailed, fmt.Sprintf("panic (%T)", recovered), false, false, ""
		}
	}()
	return p.attempt(ctx, now, extras)
}

// attempt runs the scan and the publish and says how it ended.
func (p *Publisher) attempt(ctx context.Context, now time.Time, extras func() remotestate.Extras) (diagnostic, detail string, published, skipped bool, digest string) {
	snapshot, err := p.options.Collect(ctx, now)
	if err != nil {
		return DiagnosticCollectFailed, err.Error(), false, false, ""
	}
	if extras != nil {
		snapshot = snapshot.WithExtras(extras(), p.options.Agents, p.options.Metrics)
	} else {
		snapshot = snapshot.WithExtras(remotestate.Extras{}, false, false)
	}
	digest = snapshot.Digest()
	p.mu.Lock()
	unchanged := digest == p.lastDigest && !p.status.LastPublished.IsZero() && now.Sub(p.status.LastPublished) < p.options.Keepalive
	provider := p.provider
	p.mu.Unlock()
	if unchanged {
		return DiagnosticNone, "", false, true, digest
	}
	if provider == nil {
		if provider, err = p.options.Open(); err != nil {
			return DiagnosticOpenFailed, err.Error(), false, false, ""
		}
		p.mu.Lock()
		p.provider = provider
		p.mu.Unlock()
	}
	_, dropped, err := remotestate.PublishWithFallback(ctx, provider, snapshot)
	if err != nil {
		return DiagnosticPublishFailed, err.Error(), false, false, ""
	}
	if errors.Is(dropped, remotestate.ErrOptionalFieldsDropped) {
		return DiagnosticOptionalFields, fmt.Sprint(dropped), true, false, digest
	}
	return DiagnosticNone, "", true, false, digest
}
