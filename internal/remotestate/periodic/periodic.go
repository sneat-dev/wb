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
	// hours by default). The keepalive in force is the larger of this and the
	// interval.
	DefaultKeepalive = 6 * time.Hour
	// DefaultMaxBackoff is the longest wait after repeated failures.
	DefaultMaxBackoff = time.Hour
	// AgentsMinSpacing is the least time between two publishes that differ only
	// in the agents (an agent started or ended, a state changed): with the
	// interval it bounds the commits an agents-only churn can add to a git store
	// to one per max(interval, 15 minutes).
	AgentsMinSpacing = 15 * time.Minute
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
	// less means DefaultKeepalive, and it is never less than Every.
	Keepalive time.Duration
	// MaxBackoff caps the wait after failures; zero or less means
	// DefaultMaxBackoff.
	MaxBackoff time.Duration
	// Logf reports a diagnostic when it changes; nil discards.
	Logf func(format string, args ...any)
	// Published is called after each publish that reached the store, outside the
	// publisher's lock; nil means nothing.
	Published func()
}

// Status is what the publisher knows of its own attempts.
type Status struct {
	// Diagnostic is the code of the last attempt, DiagnosticNone when it
	// published or found nothing to publish.
	Diagnostic string
	// Attempts counts the attempts that were not gated, Published the publishes
	// that reached the store, Skipped the attempts that found nothing changed,
	// Held the attempts that found only the agents changed and held them back,
	// and Gated the attempts that did nothing at all because the source reported
	// nothing changed (they are not Attempts). Scans counts the attempts that
	// ran the scan (Collect): an attempt whose source reports the repositories
	// and worktrees as they were at the last scan reuses that scan.
	Attempts, Published, Skipped, Held, Gated, Scans int
	// LastPublished is when the last publish reached the store.
	LastPublished time.Time
}

// Publisher is the periodic publisher. It is safe for concurrent use; a call
// made while an attempt runs returns at once.
type Publisher struct {
	options Options
	now     func() time.Time

	mu         sync.Mutex
	running    bool
	next       time.Time
	failures   int
	lastDigest string
	lastCore   string
	lastToken  string
	// scanned is the snapshot of the last scan, before the extras, scannedToken
	// the source's change token read before that scan ("" when it had none) and
	// scannedAt its time: an attempt for the same token reuses it.
	scanned      remotestate.Snapshot
	scannedToken string
	scannedAt    time.Time
	retryFullAt  time.Time
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
	publisher.options.Keepalive = max(publisher.options.Keepalive, options.Every)
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

// Diagnostic is the code of the last completed outcome against the store, ""
// when it is healthy, none has run or publishing never failed.
func (p *Publisher) Diagnostic() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status.Diagnostic
}

// Publish makes one attempt if the interval (or, after a failure, the backoff)
// has passed and none is running. If source reports the same change token (and
// the same agents, when they are published) as at the last publish or the last
// attempt that found nothing changed, and the keepalive has not passed, it
// does not even scan: the scan reads the git status of every repository, which
// the snapshotter's fingerprints already tell is unchanged. Otherwise it scans
// (or, when the source's change token is the one the last scan was made for and
// that scan is younger than the keepalive, takes that scan again: only the
// agents moved, which the scan does not read),
// adds the extras the flags allow, skips a snapshot that says what the last
// published one said, holds back a change of the agents alone until
// max(interval, AgentsMinSpacing) has passed since the last publish, and
// publishes through the provider. When an older hub's refusal of the optional
// fields has been remembered for a day, the first attempt after that day is
// forced: no gate and no digest skip, so an upgraded hub is noticed within 24
// hours. source may be nil (no gate, no extras). It returns nothing: every
// outcome is a Status and a logged code.
//
// The diagnostic (Status.Diagnostic) is one rule: a failure's code stays until
// the step that failed has worked again, and no longer. A failure sets its code.
// collect_failed is cleared by the next scan that works, whatever the attempt
// then does (publish, skip or hold back). store_unavailable and publish_failed
// are cleared by the next publish that reaches the store, and so that there is
// one, an attempt made while either stands is never gated, skipped or held
// back: it publishes. A publish that carried the optional fields clears every
// code; one that had to leave them out sets optional_fields_dropped, which
// stays until a publish that carried them succeeds (a failure's code takes its
// place while the failure stands). A gated, skipped or held-back attempt never
// changes a code other than collect_failed.
func (p *Publisher) Publish(ctx context.Context, source remotestate.PublishSource) {
	if p.options.Every <= 0 || p.options.Collect == nil || p.options.Open == nil {
		return
	}
	now := p.now()
	p.mu.Lock()
	if p.running || now.Before(p.next) {
		p.mu.Unlock()
		return
	}
	var extras func() remotestate.Extras
	token, scanToken := "", ""
	if source != nil {
		extras = source.PublishExtras
		scanToken = source.ChangeToken()
		token = p.gateKey(source, scanToken)
	}
	forced := !p.retryFullAt.IsZero() && !now.Before(p.retryFullAt)
	// A failure that stands is cleared only by the step that failed working
	// again, so the attempt that would show it is never gated; and after a
	// failure of the store it goes to the store.
	failing := p.status.Diagnostic == DiagnosticCollectFailed || p.status.Diagnostic == DiagnosticOpenFailed || p.status.Diagnostic == DiagnosticPublishFailed
	toStore := forced || (failing && p.status.Diagnostic != DiagnosticCollectFailed)
	if !forced && !failing && token != "" && token == p.lastToken && !p.status.LastPublished.IsZero() && now.Sub(p.status.LastPublished) < p.options.Keepalive {
		p.status.Gated++
		p.next = p.after(now)
		p.mu.Unlock()
		return
	}
	p.running = true
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, p.options.Timeout)
	defer cancel()
	result := p.guarded(ctx, now, extras, scanToken, toStore)
	if result.published && p.options.Published != nil {
		p.options.Published()
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.running = false
	p.status.Attempts++
	if (result.skipped || result.held) && p.status.Diagnostic == DiagnosticCollectFailed {
		// The scan works again. What a remembered refusal of the optional
		// fields says stands until a publish carries them.
		p.status.Diagnostic, p.failures = DiagnosticNone, 0
		if !p.retryFullAt.IsZero() {
			p.status.Diagnostic = DiagnosticOptionalFields
		}
	}
	switch {
	case result.failure != DiagnosticNone:
		p.status.Diagnostic = result.failure
		p.failures++
	case result.published:
		p.status.Published++
		p.status.LastPublished = now
		p.lastDigest, p.lastCore, p.lastToken = result.digest, result.core, token
		p.failures = 0
		switch {
		case result.dropped:
			p.status.Diagnostic = DiagnosticOptionalFields
			if p.retryFullAt.IsZero() || forced {
				p.retryFullAt = now.Add(remotestate.OptionalRefusalMemoryFor)
			}
		case result.carriedOptional:
			p.status.Diagnostic, p.retryFullAt = DiagnosticNone, time.Time{}
		case p.status.Diagnostic != DiagnosticOptionalFields:
			p.status.Diagnostic = DiagnosticNone
		}
	case result.skipped:
		p.status.Skipped++
		p.lastToken = token
	case result.held:
		p.status.Held++
	}
	p.next = p.after(now)
	if p.status.Diagnostic != p.loggedStatus {
		p.loggedStatus = p.status.Diagnostic
		if p.status.Diagnostic != DiagnosticNone {
			p.options.Logf("remote publish: %s: %s", p.status.Diagnostic, result.detail)
		}
	}
}

// after is the time of the next attempt that is allowed: the interval, or while
// publishes keep failing a wait that doubles up to MaxBackoff. The caller holds
// p.mu.
func (p *Publisher) after(now time.Time) time.Time {
	wait := p.options.Every
	for step := 1; step < p.failures && wait < p.options.MaxBackoff; step++ {
		wait *= 2
	}
	return now.Add(min(wait, max(p.options.MaxBackoff, p.options.Every)))
}

// gateKey is the source's change token with the digest of the agents it would
// publish, so that a change of agents opens the gate when agents are published.
// It is empty when the source has no token yet.
func (p *Publisher) gateKey(source remotestate.PublishSource, token string) string {
	if token == "" {
		return ""
	}
	if p.options.Agents {
		token += "|" + remotestate.Snapshot{Agents: source.PublishExtras().Agents}.Digest()
	}
	return token
}

// result is how one attempt ended.
type result struct {
	// failure is the diagnostic of a failed attempt, DiagnosticNone otherwise.
	failure string
	detail  string
	// published: reached the store; dropped: without the optional fields;
	// carriedOptional: with them. skipped: nothing changed; held: only the agents
	// changed and it is too soon to publish them.
	published, dropped, carriedOptional, skipped, held bool
	digest, core                                       string
}

// guarded is attempt with a panic in a collaborator turned into a failed
// attempt: a daemon never ends because a publish did.
func (p *Publisher) guarded(ctx context.Context, now time.Time, extras func() remotestate.Extras, scanToken string, toStore bool) (outcome result) {
	defer func() {
		if recovered := recover(); recovered != nil {
			outcome = result{failure: DiagnosticCollectFailed, detail: fmt.Sprintf("panic (%T)", recovered)}
		}
	}()
	return p.attempt(ctx, now, extras, scanToken, toStore)
}

// scan is the snapshot of this attempt before the extras: the last scan's,
// stamped now, when the source's change token is the one that scan was made for
// (nothing the scan reads has moved) and the scan is younger than the
// keepalive, and a new scan otherwise. The token was read before the scan, so a
// change during it is a different token at the next attempt.
func (p *Publisher) scan(ctx context.Context, now time.Time, scanToken string) (remotestate.Snapshot, error) {
	p.mu.Lock()
	held, reuse := p.scanned, scanToken != "" && scanToken == p.scannedToken && now.Sub(p.scannedAt) < p.options.Keepalive
	p.mu.Unlock()
	if reuse {
		held.PublishedAt = now
		return held, nil
	}
	snapshot, err := p.options.Collect(ctx, now)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.status.Scans++
	if err != nil {
		p.scanned, p.scannedToken = remotestate.Snapshot{}, ""
		return remotestate.Snapshot{}, err
	}
	p.scanned, p.scannedToken, p.scannedAt = snapshot, scanToken, now
	return snapshot, nil
}

// attempt runs the scan and the publish and says how it ended. toStore says
// the attempt must reach the store whatever it finds: it is neither skipped nor
// held back.
func (p *Publisher) attempt(ctx context.Context, now time.Time, extras func() remotestate.Extras, scanToken string, toStore bool) result {
	snapshot, err := p.scan(ctx, now, scanToken)
	if err != nil {
		return result{failure: DiagnosticCollectFailed, detail: err.Error()}
	}
	if extras != nil {
		snapshot = snapshot.WithExtras(extras(), p.options.Agents, p.options.Metrics)
	} else {
		snapshot = snapshot.WithExtras(remotestate.Extras{}, false, false)
	}
	outcome := result{digest: snapshot.Digest(), core: snapshot.CoreDigest()}
	p.mu.Lock()
	published := !p.status.LastPublished.IsZero()
	age := now.Sub(p.status.LastPublished)
	unchanged := published && outcome.digest == p.lastDigest && age < p.options.Keepalive
	held := published && outcome.core == p.lastCore && age < max(p.options.Every, AgentsMinSpacing) && age < p.options.Keepalive
	provider := p.provider
	p.mu.Unlock()
	if !toStore {
		if unchanged {
			outcome.skipped = true
			return outcome
		}
		if held {
			outcome.held = true
			return outcome
		}
	}
	if provider == nil {
		if provider, err = p.options.Open(); err != nil {
			return result{failure: DiagnosticOpenFailed, detail: err.Error()}
		}
		p.mu.Lock()
		p.provider = provider
		p.mu.Unlock()
	}
	_, dropped, err := remotestate.PublishWithFallback(ctx, provider, snapshot, now)
	if err != nil {
		return result{failure: DiagnosticPublishFailed, detail: err.Error()}
	}
	outcome.published = true
	if errors.Is(dropped, remotestate.ErrOptionalFieldsDropped) {
		outcome.dropped, outcome.detail = true, fmt.Sprint(dropped)
		return outcome
	}
	outcome.carriedOptional = snapshot.HasOptional()
	return outcome
}
