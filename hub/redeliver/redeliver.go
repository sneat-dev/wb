// Package redeliver recovers GitHub App webhook deliveries the hub's own
// downtime lost. GitHub does not redeliver a failed delivery by itself, so a
// hub that stayed down or unreachable for a while would otherwise never see
// the pushes that happened in that window. On start and then hourly, a
// webhook-mode hub asks the App API which of the last 72 hours' deliveries
// still failed and asks GitHub to redeliver each one, retrying up to
// MaxAttempts times before giving up on it for good.
//
// A redelivered event arrives at the hub's normal webhook route and
// deduplicates there by delivery ID exactly like any other delivery: this
// package needs no dedup of its own, only the memory of how many times it
// has already asked.
package redeliver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/hub/narrate"
)

const (
	// DefaultAPIBaseURL is GitHub's public API. A test points this at an
	// httptest server instead.
	DefaultAPIBaseURL = "https://api.github.com"
	// DefaultInterval is how often the sweep repeats once started. The sweep
	// always runs once immediately as well, on daemon start.
	DefaultInterval = time.Hour
	// MaxDeliveryAge bounds how far back the sweep lists and acts on
	// deliveries. An operator down longer than this has a bigger problem
	// than a redelivered push, and the App API listing would only grow.
	MaxDeliveryAge = 72 * time.Hour
	// MaxAttempts bounds how many times the sweep asks GitHub to redeliver
	// the same GUID before narrating it abandoned and never touching it
	// again.
	MaxAttempts = 3
	// Retention is how long a redelivery record survives in the store after
	// its last attempt, whether it is still retrying, succeeded, or was
	// abandoned.
	Retention = 7 * 24 * time.Hour
	// MaxBackoff bounds the exponential retreat after a failed sweep, the
	// same shape hub/poller uses for its own ticks.
	MaxBackoff = 10 * time.Minute

	// eventName is the fixed narrate.Line.Event column value for every line
	// this sweep writes, the way hub/poller always narrates as "poll". The
	// webhook's own event name (push, member, …) and repository or
	// installation id, where the delivery listing provides them, go in the
	// Subject column instead.
	eventName = "redeliver"

	perPage          = 100
	maxResponseBytes = 1 << 20
)

// Options configures a Sweeper. Store, AppID and PrivateKeyPEM are required
// for the sweep to do anything; everything else has a working default.
type Options struct {
	// Client performs the GitHub App API requests.
	Client *http.Client
	// APIBaseURL is GitHub's API origin. Empty means DefaultAPIBaseURL.
	APIBaseURL string
	// AppID and PrivateKeyPEM mint the App JWT the sweep authenticates with,
	// the same key material the hub's installation verifier signs with.
	AppID         int64
	PrivateKeyPEM []byte
	// Store is the sweep's memory of prior attempts per delivery GUID.
	Store hub.WebhookRedeliveryStore
	// Interval is the gap between sweeps. Zero means DefaultInterval.
	Interval time.Duration
	// Now and Sleep exist so a test drives time instead of waiting for it.
	Now   func() time.Time
	Sleep func(context.Context, time.Duration) error
	// Narrate receives one line per redelivery, one per abandonment, and one
	// for a sweep that failed before it could act on anything. Nil discards
	// every line.
	Narrate func(narrate.Line)
}

// Status is the sweep's most recent completed-pass summary, read by
// `wb daemon status`.
type Status struct {
	LastSweepAt time.Time
	Redelivered int
	Abandoned   int
}

// Sweeper is the missed-webhook recovery loop.
type Sweeper struct {
	options Options

	// backoff is the delay the next sweep waits after a failed one. It
	// starts at the configured interval and doubles, capped at MaxBackoff,
	// until a clean sweep resets it.
	backoff time.Duration

	// mu guards last, which `wb daemon status` reads from another goroutine.
	mu   sync.Mutex
	last Status
}

// New returns a Sweeper with every optional field defaulted.
func New(options Options) *Sweeper {
	if strings.TrimSpace(options.APIBaseURL) == "" {
		options.APIBaseURL = DefaultAPIBaseURL
	}
	options.APIBaseURL = strings.TrimSuffix(options.APIBaseURL, "/")
	if options.Interval <= 0 {
		options.Interval = DefaultInterval
	}
	if options.Client == nil {
		options.Client = http.DefaultClient
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Sleep == nil {
		options.Sleep = sleepContext
	}
	if options.Narrate == nil {
		options.Narrate = func(narrate.Line) {}
	}
	return &Sweeper{options: options, backoff: options.Interval}
}

// Status returns the most recent completed sweep's summary, the zero value
// before the first one finishes.
func (sweeper *Sweeper) Status() Status {
	sweeper.mu.Lock()
	defer sweeper.mu.Unlock()
	return sweeper.last
}

func (sweeper *Sweeper) setStatus(status Status) {
	sweeper.mu.Lock()
	sweeper.last = status
	sweeper.mu.Unlock()
}

// Run sweeps once immediately — the "on start" half of the requirement —
// then again after every delay Sweep returns, until ctx ends. It returns nil
// on a cancelled context: a stopped sweeper is a normal shutdown.
func (sweeper *Sweeper) Run(ctx context.Context) error {
	for {
		delay := sweeper.Sweep(ctx)
		if err := sweeper.options.Sleep(ctx, delay); err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return nil
		}
	}
}

// Sweep performs one pass and returns how long to wait before the next one:
// the configured interval normally, or the current backoff after a failure.
// It never returns an error and never panics on one: a sweep that cannot
// reach GitHub is narrated in one line and retried later, which is the whole
// point of a background recovery loop outliving a transient GitHub outage.
func (sweeper *Sweeper) Sweep(ctx context.Context) time.Duration {
	if sweeper.options.Store == nil || sweeper.options.AppID <= 0 || len(sweeper.options.PrivateKeyPEM) == 0 {
		return sweeper.options.Interval
	}
	if err := sweeper.sweepOnce(ctx); err != nil {
		sweeper.options.Narrate(narrate.Line{At: sweeper.options.Now(), Event: eventName, Subject: "github.com", Action: "sweep failed: " + err.Error()})
		return sweeper.transientDelay()
	}
	sweeper.backoff = sweeper.options.Interval
	return sweeper.options.Interval
}

func (sweeper *Sweeper) sweepOnce(ctx context.Context) error {
	token, err := hub.BuildAppJWT(sweeper.options.AppID, sweeper.options.PrivateKeyPEM, sweeper.options.Now)
	if err != nil {
		return fmt.Errorf("mint github app jwt: %w", err)
	}
	cutoff := sweeper.options.Now().Add(-MaxDeliveryAge)
	failed, err := sweeper.listRecentFailedDeliveries(ctx, token, cutoff)
	if err != nil {
		return err
	}
	redelivered, abandoned, err := sweeper.actOn(ctx, token, failed)
	if err != nil {
		return err
	}
	if err := sweeper.prune(ctx); err != nil {
		return err
	}
	sweeper.setStatus(Status{LastSweepAt: sweeper.options.Now(), Redelivered: redelivered, Abandoned: abandoned})
	return nil
}

// deliveryItem is the subset of one GET /app/hook/deliveries entry the sweep
// needs. GitHub lists each attempt separately; a redelivery keeps the GUID
// and marks Redelivery true.
type deliveryItem struct {
	ID          int64     `json:"id"`
	GUID        string    `json:"guid"`
	DeliveredAt time.Time `json:"delivered_at"`
	Redelivery  bool      `json:"redelivery"`
	StatusCode  int       `json:"status_code"`
	Event       string    `json:"event"`
	Action      string    `json:"action,omitempty"`
	// InstallationID and RepositoryID name the delivery's subject when the
	// listing provides them. Neither is a secret: both are already visible
	// on the App's own Advanced tab.
	InstallationID int64 `json:"installation_id"`
	RepositoryID   int64 `json:"repository_id"`
}

func (item deliveryItem) succeeded() bool {
	return item.StatusCode >= 200 && item.StatusCode < 300
}

// subject is the narrate.Line.Subject column: the webhook's own event name
// plus whichever identifier the listing provided, never a payload.
func (item deliveryItem) subject() string {
	event := strings.TrimSpace(item.Event)
	if event == "" {
		event = "unknown"
	}
	switch {
	case item.RepositoryID > 0:
		return event + " repository:" + strconv.FormatInt(item.RepositoryID, 10)
	case item.InstallationID > 0:
		return event + " installation:" + strconv.FormatInt(item.InstallationID, 10)
	default:
		return event
	}
}

// listRecentFailedDeliveries walks the App's delivery list newest-first,
// following cursor pagination via the Link header, and stops as soon as a
// page's deliveries cross the 72-hour boundary: everything after that is
// even older. Because the list is newest-first, the first time a GUID is
// seen is by construction its latest attempt, so grouping needs no sorting.
func (sweeper *Sweeper) listRecentFailedDeliveries(ctx context.Context, token string, cutoff time.Time) ([]deliveryItem, error) {
	url := sweeper.options.APIBaseURL + "/app/hook/deliveries?per_page=" + strconv.Itoa(perPage)
	seen := make(map[string]bool)
	var latest []deliveryItem
	for url != "" {
		var page []deliveryItem
		next, err := sweeper.get(ctx, url, token, &page)
		if err != nil {
			return nil, err
		}
		stop := false
		for _, item := range page {
			if item.DeliveredAt.Before(cutoff) {
				stop = true
				break
			}
			if strings.TrimSpace(item.GUID) == "" || seen[item.GUID] {
				continue
			}
			seen[item.GUID] = true
			latest = append(latest, item)
		}
		if stop {
			break
		}
		url = next
	}
	failed := make([]deliveryItem, 0, len(latest))
	for _, item := range latest {
		if !item.succeeded() {
			failed = append(failed, item)
		}
	}
	return failed, nil
}

// actOn redelivers each failed GUID that has not yet exhausted MaxAttempts,
// and abandons each one that has. It stops and returns an error as soon as a
// GitHub call fails, leaving every GUID it has not reached yet to be picked
// up, unrecorded, on the next sweep.
func (sweeper *Sweeper) actOn(ctx context.Context, token string, failed []deliveryItem) (redelivered, abandoned int, err error) {
	now := sweeper.options.Now().UTC()
	for _, item := range failed {
		record, found, loadErr := sweeper.options.Store.LoadWebhookRedelivery(ctx, item.GUID)
		if loadErr != nil {
			return redelivered, abandoned, loadErr
		}
		if found && record.Abandoned {
			continue
		}
		attempts := 0
		if found {
			attempts = record.Attempts
		}
		if attempts >= MaxAttempts {
			if saveErr := sweeper.options.Store.SaveWebhookRedelivery(ctx, hub.WebhookRedeliveryRecord{GUID: item.GUID, Attempts: attempts, Abandoned: true, LastAttemptAt: now}); saveErr != nil {
				return redelivered, abandoned, saveErr
			}
			sweeper.narrate(item, now, fmt.Sprintf("abandoned after %d attempts", attempts))
			abandoned++
			continue
		}
		if redeliverErr := sweeper.redeliver(ctx, token, item.ID); redeliverErr != nil {
			return redelivered, abandoned, redeliverErr
		}
		attempts++
		if saveErr := sweeper.options.Store.SaveWebhookRedelivery(ctx, hub.WebhookRedeliveryRecord{GUID: item.GUID, Attempts: attempts, Abandoned: false, LastAttemptAt: now}); saveErr != nil {
			return redelivered, abandoned, saveErr
		}
		sweeper.narrate(item, now, fmt.Sprintf("redelivered (attempt %d of %d)", attempts, MaxAttempts))
		redelivered++
	}
	return redelivered, abandoned, nil
}

func (sweeper *Sweeper) narrate(item deliveryItem, at time.Time, action string) {
	sweeper.options.Narrate(narrate.Line{At: at, Event: eventName, Subject: item.subject(), Action: action})
}

// prune deletes every stored record whose last attempt is older than
// Retention, regardless of whether it is still retrying, succeeded, or was
// abandoned.
func (sweeper *Sweeper) prune(ctx context.Context) error {
	records, err := sweeper.options.Store.ListWebhookRedeliveries(ctx)
	if err != nil {
		return err
	}
	cutoff := sweeper.options.Now().Add(-Retention)
	expired := make([]string, 0, len(records))
	for _, record := range records {
		if record.LastAttemptAt.Before(cutoff) {
			expired = append(expired, record.GUID)
		}
	}
	if len(expired) == 0 {
		return nil
	}
	return sweeper.options.Store.DeleteWebhookRedeliveries(ctx, expired)
}

// redeliver asks GitHub to redeliver one delivery attempt by its numeric id
// (not its GUID — GitHub keys this route by attempt id).
func (sweeper *Sweeper) redeliver(ctx context.Context, token string, deliveryID int64) error {
	url := sweeper.options.APIBaseURL + "/app/hook/deliveries/" + strconv.FormatInt(deliveryID, 10) + "/attempts"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return fmt.Errorf("build github redeliver request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := sweeper.options.Client.Do(request)
	if err != nil {
		return fmt.Errorf("reach github: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusAccepted && response.StatusCode != http.StatusOK {
		return fmt.Errorf("github redeliver responded %d", response.StatusCode)
	}
	return nil
}

// transientDelay returns the current backoff and doubles it for next time.
func (sweeper *Sweeper) transientDelay() time.Duration {
	delay := sweeper.backoff
	if delay <= 0 {
		delay = sweeper.options.Interval
	}
	sweeper.backoff = min(delay*2, MaxBackoff)
	return delay
}

// get performs one authenticated GitHub App API request, decodes its JSON
// body into out, and returns the next page URL from the Link header, or "".
func (sweeper *Sweeper) get(ctx context.Context, url, token string, out any) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build github request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := sweeper.options.Client.Do(request)
	if err != nil {
		return "", fmt.Errorf("reach github: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github responded %d", response.StatusCode)
	}
	next := nextPageURL(response.Header.Get("Link"))
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(out); err != nil {
		return "", fmt.Errorf("decode github response: %w", err)
	}
	return next, nil
}

// nextPageURL extracts the rel="next" target from a GitHub Link header, or
// "" when there is no next page.
func nextPageURL(header string) string {
	if strings.TrimSpace(header) == "" {
		return ""
	}
	for _, part := range strings.Split(header, ",") {
		segments := strings.Split(part, ";")
		if len(segments) < 2 {
			continue
		}
		url := strings.Trim(strings.TrimSpace(segments[0]), "<>")
		for _, attribute := range segments[1:] {
			if strings.TrimSpace(attribute) == `rel="next"` {
				return url
			}
		}
	}
	return ""
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
