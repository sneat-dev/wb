package deskpublish

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/desk"
	"github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/deskclient"
)

type scriptedClient struct {
	mu         sync.Mutex
	published  []desk.SnapshotRequest
	beats      []desk.HeartbeatRequest
	publishErr []error
	beatErr    []error
	beaten     chan struct{}
}

func next(errs *[]error) error {
	if len(*errs) == 0 {
		return nil
	}
	err := (*errs)[0]
	*errs = (*errs)[1:]
	return err
}

func (c *scriptedClient) Publish(_ context.Context, request desk.SnapshotRequest) (desk.SnapshotReceipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.published = append(c.published, request)
	return desk.SnapshotReceipt{Hash: request.Hash}, next(&c.publishErr)
}

func (c *scriptedClient) Heartbeat(_ context.Context, request desk.HeartbeatRequest) error {
	c.mu.Lock()
	c.beats = append(c.beats, request)
	err := next(&c.beatErr)
	c.mu.Unlock()
	if c.beaten != nil {
		c.beaten <- struct{}{}
	}
	return err
}

func (c *scriptedClient) sent() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.published) }

type source struct{ envelope fleet.Envelope }

func (s *source) Export(bool) fleet.Envelope         { return s.envelope }
func (s *source) LocalThroughput() *fleet.Throughput { return nil }

// clock is shared with the heartbeat goroutine, so it is locked.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) advance(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.now = c.now.Add(d) }

// stopAfterFirst ends the heartbeat loop after its first beat, so a test that
// only exercises publishing sends exactly one heartbeat.
func stopAfterFirst(context.Context, time.Duration) error { return context.Canceled }

func newTestPublisher(t *testing.T, client *scriptedClient, at *clock) *Publisher {
	t.Helper()
	return NewPublisher(PublisherOptions{Client: client, Detail: desk.LevelRepositories, Key: fixedKey(t, "p"),
		MinInterval: time.Minute, HeartbeatInterval: 5 * time.Minute, WBVersion: "0.211.0", Now: at.Now, Wait: stopAfterFirst})
}

func TestPublisherSendsOnlyChangedSnapshotsAtMostOncePerInterval(t *testing.T) {
	t.Parallel()
	client, at := &scriptedClient{}, &clock{now: time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)}
	publisher := newTestPublisher(t, client, at)
	machine := &source{envelope: publishableEnvelope()}
	publisher.Publish(t.Context(), machine)
	if client.sent() != 1 || publisher.Diagnostic() != "" {
		t.Fatalf("first pass sent %d, diagnostic %q", client.sent(), publisher.Diagnostic())
	}
	if request := client.published[0]; request.Validate() != nil || request.Detail != desk.LevelRepositories || strings.Contains(string(request.Envelope), "task-a") {
		t.Fatalf("published %+v", request)
	}
	machine.envelope.Fleet.Repositories[0].Name = "acme/gadgets"
	at.advance(30 * time.Second)
	publisher.Publish(t.Context(), machine)
	if client.sent() != 1 {
		t.Fatal("a change inside the minimum interval waits for a later pass")
	}
	at.advance(time.Minute)
	publisher.Publish(t.Context(), machine)
	if client.sent() != 2 {
		t.Fatal("a change after the interval is sent")
	}
	at.advance(2 * time.Minute)
	publisher.Publish(t.Context(), machine)
	if client.sent() != 2 {
		t.Fatal("an unchanged snapshot is not sent again")
	}
	warming := publishableEnvelope()
	warming.Fleet.WarmingUp = true
	at.advance(time.Hour)
	publisher.Publish(t.Context(), &source{envelope: warming})
	if client.sent() != 2 {
		t.Fatal("a warming-up daemon publishes nothing")
	}
}

func TestPublisherBacksOffThenRecovers(t *testing.T) {
	t.Parallel()
	unreachable := &deskclient.Error{Code: desk.ErrorUnreachable}
	client := &scriptedClient{publishErr: []error{unreachable, unreachable, errors.New("credential file is readable"), nil}}
	at := &clock{now: time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)}
	publisher := newTestPublisher(t, client, at)
	machine := &source{envelope: publishableEnvelope()}
	publisher.Publish(t.Context(), machine)
	if publisher.Diagnostic() != "unreachable" {
		t.Fatalf("diagnostic = %q", publisher.Diagnostic())
	}
	at.advance(30 * time.Second)
	publisher.Publish(t.Context(), machine)
	if client.sent() != 1 {
		t.Fatal("no retry before the backoff ends")
	}
	at.advance(31 * time.Second)
	publisher.Publish(t.Context(), machine)
	if client.sent() != 2 || publisher.backoff != 2*time.Minute {
		t.Fatalf("second attempt after 1m; backoff now %v", publisher.backoff)
	}
	at.advance(2 * time.Minute)
	publisher.Publish(t.Context(), machine)
	if publisher.Diagnostic() != "unreachable" {
		t.Fatalf("a local credential failure is unreachable, got %q", publisher.Diagnostic())
	}
	at.advance(4 * time.Minute)
	publisher.Publish(t.Context(), machine)
	if client.sent() != 4 || publisher.Diagnostic() != "" || publisher.backoff != 0 {
		t.Fatalf("a success clears the error and the backoff: sent %d, %q, %v", client.sent(), publisher.Diagnostic(), publisher.backoff)
	}
	publisher.mu.Lock()
	publisher.backoff = MaxBackoff
	publisher.mu.Unlock()
	if got := min(max(2*publisher.backoff, time.Minute), MaxBackoff); got != MaxBackoff {
		t.Fatalf("backoff cap = %v", got)
	}
}

func TestPublisherHonoursRetryAfterAndStopsWhenRevoked(t *testing.T) {
	t.Parallel()
	client := &scriptedClient{publishErr: []error{&deskclient.Error{Code: desk.ErrorRateLimited, RetryAfter: 10 * time.Minute},
		&deskclient.Error{Code: desk.ErrorRevoked}}}
	at := &clock{now: time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)}
	publisher := newTestPublisher(t, client, at)
	machine := &source{envelope: publishableEnvelope()}
	publisher.Publish(t.Context(), machine)
	at.advance(5 * time.Minute)
	publisher.Publish(t.Context(), machine)
	if client.sent() != 1 || publisher.Diagnostic() != "rate_limited" {
		t.Fatalf("Retry-After is honoured: sent %d, %q", client.sent(), publisher.Diagnostic())
	}
	at.advance(6 * time.Minute)
	publisher.Publish(t.Context(), machine)
	at.advance(time.Hour)
	publisher.Publish(t.Context(), machine)
	if client.sent() != 2 || publisher.Diagnostic() != "revoked" {
		t.Fatalf("revoked stops publishing: sent %d, %q", client.sent(), publisher.Diagnostic())
	}
	if publisher.beat(t.Context()) {
		t.Fatal("a revoked publisher sends no heartbeat")
	}
}

func TestPublisherLogsAReductionFailureAndSendsNothing(t *testing.T) {
	t.Parallel()
	var logged []string
	client := &scriptedClient{}
	publisher := NewPublisher(PublisherOptions{Client: client, Detail: desk.LevelRepositories, Wait: stopAfterFirst,
		Logf: func(format string, args ...any) { logged = append(logged, format) }})
	publisher.Publish(t.Context(), &source{envelope: publishableEnvelope()})
	if client.sent() != 0 || len(logged) != 1 || publisher.Diagnostic() != "" {
		t.Fatalf("sent %d, logged %v", client.sent(), logged)
	}
}

func TestHeartbeatsStartWithTheFirstPassAndReportTheirOwnErrors(t *testing.T) {
	t.Parallel()
	client := &scriptedClient{beatErr: []error{&deskclient.Error{Code: desk.ErrorTimeout}}, beaten: make(chan struct{}, 4)}
	at := &clock{now: time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)}
	waits := make(chan struct{})
	publisher := NewPublisher(PublisherOptions{Client: client, Detail: desk.LevelFull, Key: fixedKey(t, "h"), MinInterval: time.Minute,
		HeartbeatInterval: 5 * time.Minute, WBVersion: "0.211.0", Now: at.Now,
		Wait: func(ctx context.Context, d time.Duration) error {
			if d != 5*time.Minute {
				t.Errorf("heartbeat interval = %v", d)
			}
			select {
			case <-waits:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	publisher.Publish(ctx, &source{envelope: publishableEnvelope()})
	publisher.Publish(ctx, &source{envelope: publishableEnvelope()})
	<-client.beaten
	waitFor(t, func() bool { return publisher.Diagnostic() == "timeout" })
	publisher.mu.Lock()
	publisher.publishErr = desk.ErrorTooLarge
	publisher.mu.Unlock()
	if publisher.Diagnostic() != "too_large" {
		t.Fatalf("a failed publish is reported before a heartbeat error: %q", publisher.Diagnostic())
	}
	publisher.mu.Lock()
	publisher.publishErr = ""
	publisher.mu.Unlock()
	waits <- struct{}{}
	<-client.beaten
	waitFor(t, func() bool { return publisher.Diagnostic() == "" })
	client.mu.Lock()
	beats := append([]desk.HeartbeatRequest(nil), client.beats...)
	published := append([]desk.SnapshotRequest(nil), client.published...)
	client.mu.Unlock()
	if len(beats) != 2 || beats[1].LastSnapshotHash == "" || beats[1].IntervalSeconds != 300 || beats[1].LastError != desk.ErrorTimeout {
		t.Fatalf("two heartbeats, carrying interval and prior heartbeat error: %+v", beats)
	}
	if len(published) != 1 || published[0].Validate() != nil || len(published[0].RepositoriesEnvelope) == 0 {
		t.Fatalf("full publish should carry a valid repositories envelope: %+v", published)
	}
}

func TestHeartbeatsCarryIntervalAndClearLastErrorAfterSuccess(t *testing.T) {
	t.Parallel()
	client := &scriptedClient{
		publishErr: []error{&deskclient.Error{Code: desk.ErrorTooLarge}, nil},
		beatErr:    []error{nil, nil, &deskclient.Error{Code: desk.ErrorTimeout}, nil, nil},
	}
	at := &clock{now: time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)}
	publisher := NewPublisher(PublisherOptions{
		Client:            client,
		Detail:            desk.LevelFull,
		Key:               fixedKey(t, "e"),
		MinInterval:       time.Minute,
		HeartbeatInterval: 3 * time.Minute,
		WBVersion:         "0.211.0",
		Now:               at.Now,
		Wait:              stopAfterFirst,
	})
	// Prevent Publish from starting the background heartbeat goroutine so we
	// drive each beat synchronously in exact order.
	publisher.heartbeat.Do(func() {})
	machine := &source{envelope: publishableEnvelope()}

	// Failed publish sets last_error on the next heartbeat.
	publisher.Publish(t.Context(), machine)
	if !publisher.beat(t.Context()) {
		t.Fatal("beat after publish failure should continue")
	}
	// Successful publish clears last_error on the following heartbeat.
	at.advance(2 * time.Minute)
	publisher.Publish(t.Context(), machine)
	if !publisher.beat(t.Context()) {
		t.Fatal("beat after publish recovery should continue")
	}
	// Failed heartbeat sets last_error on the next heartbeat, which succeeds and clears it.
	if !publisher.beat(t.Context()) {
		t.Fatal("transient heartbeat failure should continue")
	}
	if !publisher.beat(t.Context()) {
		t.Fatal("recovering heartbeat should continue")
	}
	if !publisher.beat(t.Context()) {
		t.Fatal("post-recovery heartbeat should continue")
	}

	client.mu.Lock()
	beats := append([]desk.HeartbeatRequest(nil), client.beats...)
	client.mu.Unlock()
	wantErrors := []desk.ErrorCode{desk.ErrorTooLarge, "", "", desk.ErrorTimeout, ""}
	if len(beats) != len(wantErrors) {
		t.Fatalf("got %d beats, want %d", len(beats), len(wantErrors))
	}
	for index, want := range wantErrors {
		beat := beats[index]
		if err := beat.Validate(); err != nil {
			t.Fatalf("beat[%d] invalid: %v", index, err)
		}
		if beat.IntervalSeconds != 180 || beat.LastError != want {
			t.Fatalf("beat[%d] = interval %d, last_error %q; want 180, %q", index, beat.IntervalSeconds, beat.LastError, want)
		}
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestABeatStopsOnRefusalAndOnCancellation(t *testing.T) {
	t.Parallel()
	refused := NewPublisher(PublisherOptions{Client: &scriptedClient{beatErr: []error{&deskclient.Error{Code: desk.ErrorSchemaUnsupported}}}})
	if refused.beat(t.Context()) || refused.Diagnostic() != "schema_unsupported" || !refused.stopped {
		t.Fatalf("an unsupported schema stops heartbeats: %q", refused.Diagnostic())
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	interrupted := NewPublisher(PublisherOptions{Client: &scriptedClient{beatErr: []error{context.Canceled}}})
	if interrupted.beat(cancelled) || interrupted.Diagnostic() != "" {
		t.Fatalf("a cancelled beat records nothing: %q", interrupted.Diagnostic())
	}
	loop := NewPublisher(PublisherOptions{Client: &scriptedClient{}, Wait: func(context.Context, time.Duration) error { return context.Canceled }})
	loop.heartbeats(t.Context())
	if err := wait(t.Context(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := wait(cancelled, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait on a cancelled context = %v", err)
	}
}

func TestStartSendsHeartbeatsBeforeAnyPublish(t *testing.T) {
	t.Parallel()
	client := &scriptedClient{beaten: make(chan struct{}, 1)}
	publisher := NewPublisher(PublisherOptions{Client: client, Wait: stopAfterFirst})
	publisher.Start(t.Context())
	<-client.beaten
	if client.sent() != 0 {
		t.Fatal("starting sends a heartbeat and no snapshot")
	}
}

func TestAPublishCutOffByShutdownIsNoDeskError(t *testing.T) {
	t.Parallel()
	client := &scriptedClient{publishErr: []error{context.Canceled}}
	at := &clock{now: time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)}
	publisher := newTestPublisher(t, client, at)
	ctx, cancel := context.WithCancel(t.Context())
	publisher.Start(ctx)
	cancel()
	publisher.Publish(ctx, &source{envelope: publishableEnvelope()})
	if publisher.Diagnostic() != "" || publisher.backoff != 0 {
		t.Fatalf("shutdown recorded %q with backoff %v", publisher.Diagnostic(), publisher.backoff)
	}
}
