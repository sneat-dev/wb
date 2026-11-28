package deskpublish

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/sneat-dev/wb/api/desk"
	"github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/deskclient"
)

// MaxBackoff caps the wait after repeated failures
// (desk-connect#req:publishing-never-blocks-local-work).
const MaxBackoff = 15 * time.Minute

// Client is the part of the Desk client the daemon publisher uses.
type Client interface {
	Publish(context.Context, desk.SnapshotRequest) (desk.SnapshotReceipt, error)
	Heartbeat(context.Context, desk.HeartbeatRequest) error
}

// PublisherOptions configures a Publisher.
type PublisherOptions struct {
	Client            Client
	Detail            desk.Level
	Key               TaskKey
	MinInterval       time.Duration
	HeartbeatInterval time.Duration
	WBVersion         string
	Now               func() time.Time
	// Wait waits d or until ctx ends (tests replace it).
	Wait func(ctx context.Context, d time.Duration) error
	Logf func(format string, args ...any)
}

// Publisher is the daemon's Desk publisher: it implements fleet.DeskPublisher,
// publishing the reduced export when it changed, and, from the first pass on,
// sends a heartbeat on its own cadence (desk-connect#req:publish-on-change,
// desk-connect#req:heartbeat).
type Publisher struct {
	options   PublisherOptions
	heartbeat sync.Once

	mu         sync.Mutex
	lastHash   string
	lastSent   time.Time
	nextTry    time.Time
	backoff    time.Duration
	publishErr desk.ErrorCode
	beatErr    desk.ErrorCode
	stopped    bool
}

// NewPublisher returns a Publisher.
func NewPublisher(options PublisherOptions) *Publisher {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Wait == nil {
		options.Wait = wait
	}
	if options.Logf == nil {
		options.Logf = func(string, ...any) {}
	}
	return &Publisher{options: options}
}

func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Diagnostic is the code of the last failed publish, else of the last failed
// heartbeat, else empty.
func (p *Publisher) Diagnostic() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return string(p.lastError())
}

// lastError is the current failure code; the caller holds mu.
func (p *Publisher) lastError() desk.ErrorCode {
	if p.publishErr != "" {
		return p.publishErr
	}
	return p.beatErr
}

// code is err as a desk.ErrorCode; a failure that is not Desk's (a missing or
// unprotected credential file) cannot reach Desk, so it is unreachable.
func code(err error) desk.ErrorCode {
	if typed := deskclient.CodeOf(err); typed != "" {
		return typed
	}
	return desk.ErrorUnreachable
}

// fail records a failure; the caller holds mu. Revoked and an unsupported
// schema stop every further request (desk-connect#req:revocation-is-respected).
func (p *Publisher) fail(err error, now time.Time) desk.ErrorCode {
	failure := code(err)
	switch failure {
	case desk.ErrorRevoked, desk.ErrorSchemaUnsupported:
		p.stopped = true
	}
	p.backoff = min(max(2*p.backoff, p.options.MinInterval), MaxBackoff)
	var typed *deskclient.Error
	if errors.As(err, &typed) && typed.Code == desk.ErrorRateLimited && typed.RetryAfter > p.backoff {
		p.nextTry = now.Add(typed.RetryAfter)
	} else {
		p.nextTry = now.Add(p.backoff)
	}
	return failure
}

// Start begins the heartbeats when the daemon starts, so a machine whose
// passes do not complete is still seen as running (desk-connect#req:heartbeat).
func (p *Publisher) Start(ctx context.Context) {
	p.heartbeat.Do(func() { go p.heartbeats(ctx) })
}

// Publish implements fleet.DeskPublisher. It also starts the heartbeats, for a
// caller that never called Start.
func (p *Publisher) Publish(ctx context.Context, source fleet.DeskSource) {
	p.Start(ctx)
	p.mu.Lock()
	now := p.options.Now()
	if p.stopped || now.Before(p.nextTry) || (!p.lastSent.IsZero() && now.Sub(p.lastSent) < p.options.MinInterval) {
		p.mu.Unlock()
		return
	}
	lastHash := p.lastHash
	p.mu.Unlock()
	snapshot, err := Reduce(source.Export(false), source.LocalThroughput(), p.options.Detail, p.options.Key)
	if errors.Is(err, ErrNothingToPublish) {
		return
	}
	if err != nil {
		p.options.Logf("desk: cannot reduce this machine's snapshot: %v", err)
		return
	}
	if snapshot.Hash == lastHash {
		return
	}
	_, err = p.options.Client.Publish(ctx, snapshot.Request(now))
	if err != nil && ctx.Err() != nil {
		// The daemon is stopping: that is no Desk failure to show or back off from.
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		p.publishErr = p.fail(err, now)
		p.options.Logf("desk: publish failed: %s", p.publishErr)
		return
	}
	p.lastHash, p.lastSent, p.publishErr, p.backoff, p.nextTry = snapshot.Hash, now, "", 0, time.Time{}
}

// beat sends one heartbeat; it reports whether heartbeats should continue.
func (p *Publisher) beat(ctx context.Context) bool {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return false
	}
	now := p.options.Now()
	intervalSeconds := min(max(0, int(p.options.HeartbeatInterval/time.Second)), desk.MaxHeartbeatIntervalSeconds)
	request := desk.HeartbeatRequest{SchemaVersion: desk.SchemaVersion, ObservedAt: now,
		WBVersion: p.options.WBVersion, LastSnapshotHash: p.lastHash,
		IntervalSeconds: intervalSeconds, LastError: p.lastError()}
	p.mu.Unlock()
	err := p.options.Client.Heartbeat(ctx, request)
	p.mu.Lock()
	defer p.mu.Unlock()
	if err == nil {
		p.beatErr = ""
		return true
	}
	if ctx.Err() != nil {
		return false
	}
	switch failure := code(err); failure {
	case desk.ErrorRevoked, desk.ErrorSchemaUnsupported:
		p.beatErr, p.stopped = failure, true
		p.options.Logf("desk: heartbeat refused: %s; publishing stopped", failure)
		return false
	default:
		p.beatErr = failure
	}
	return true
}

func (p *Publisher) heartbeats(ctx context.Context) {
	for p.beat(ctx) {
		if p.options.Wait(ctx, p.options.HeartbeatInterval) != nil {
			return
		}
	}
}
