// Package redeliver recovers GitHub App webhook deliveries the hub's own
// downtime lost. GitHub keeps the record of a failed delivery — an operator
// can always redeliver one by hand from the App's Advanced tab — but it never
// retries one automatically, so a hub that stayed down or unreachable for a
// while would otherwise never see the pushes that happened in that window.
// On start and then hourly, a webhook-mode hub asks the App API which of the
// last 72 hours' deliveries still failed and asks GitHub to redeliver each
// one.
//
// Redelivering costs nothing extra when the operator's own endpoint is the
// thing that is down: GitHub still accepts the redeliver request and simply
// fails it again. What must be bounded is how many of MaxAttempts a GUID
// spends while that is true, because "endpoint has been down for a week"
// must not look identical to "GitHub keeps failing this one delivery for
// some other reason" — the former should retry forever at the sweep's normal
// cadence and never reach abandonment on that account alone. spentAttempt
// answers that question from evidence already in the same listing: some
// other delivery, any GUID, that reached the endpoint with a 2xx more
// recently than this GUID's own last attempt.
//
// A redelivered event arrives at the hub's normal webhook route and
// deduplicates there by delivery ID exactly like any other delivery: this
// package needs no dedup of its own, only the memory of how many counted
// attempts it has already spent.
package redeliver

import (
	"context"
	"encoding/json"
	"errors"
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
	// DefaultInterval is how often the sweep repeats once started, and also
	// the minimum gap this package enforces between two counted or
	// uncounted redeliver calls for the same GUID. The sweep always runs
	// once immediately as well, on daemon start.
	DefaultInterval = time.Hour
	// MaxDeliveryAge bounds how far back the sweep lists and acts on
	// deliveries. An operator down longer than this has a bigger problem
	// than a redelivered push, and the App API listing would only grow. This
	// is a hard bound: nothing in this package ever widens it.
	MaxDeliveryAge = 72 * time.Hour
	// MaxAttempts bounds how many counted attempts the sweep spends asking
	// GitHub to redeliver the same GUID before narrating it abandoned and
	// never touching it again. An attempt only counts while there is
	// evidence the endpoint is reachable; see spentAttempt.
	MaxAttempts = 3
	// Retention is how long a redelivery record survives in the store after
	// its last attempt, whether it is still retrying, succeeded, or was
	// abandoned.
	Retention = 7 * 24 * time.Hour
	// MaxBackoff bounds the exponential retreat after a failed sweep. It
	// must exceed Interval — a shorter cap would make a persistent failure
	// retry more often than a healthy sweep does, hammering an outage.
	MaxBackoff = 6 * time.Hour
	// jwtRefreshAfter re-mints the App JWT partway through a long sweep: the
	// JWT hub.BuildAppJWT signs is valid for about 9 minutes, and a sweep
	// paginating through a large 72-hour window plus a redeliver call per
	// failed GUID can run long enough to need a fresh one.
	jwtRefreshAfter = 5 * time.Minute
	// maxPages bounds delivery-list pagination. 100 deliveries per page is
	// already far more than a 72-hour window should ordinarily produce; this
	// exists to guarantee termination if GitHub's Link header ever behaves
	// unexpectedly, not to reflect an expected page count.
	maxPages = 50

	// eventName is the fixed narrate.Line.Event column value for every line
	// this sweep writes, the way hub/poller always narrates as "poll".
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
	// Interval is the gap between sweeps, and the minimum gap this package
	// enforces between two redeliver calls for the same GUID. Zero means
	// DefaultInterval.
	Interval time.Duration
	// Now and Sleep exist so a test drives time instead of waiting for it.
	Now   func() time.Time
	Sleep func(context.Context, time.Duration) error
	// Narrate receives one line per redelivery, one per abandonment, one for
	// a sweep that failed before it could finish, and one per sweep in which
	// any redeliver call went uncounted for lack of reachability evidence.
	// Nil discards every line.
	Narrate func(narrate.Line)
}

// Status is the sweep's most recent completed-pass summary, read by
// `wb daemon status`. LastSweepAt and LastFailureAt are pointers so JSON
// omits them before anything has happened yet, rather than rendering the
// zero time.
type Status struct {
	LastSweepAt *time.Time
	Redelivered int
	Abandoned   int
	// LastFailureAt and LastFailureClass are sticky: a later clean sweep
	// does not clear them. An operator asking "has this ever failed, and
	// how" gets an answer that survives the next successful pass.
	LastFailureAt    *time.Time
	LastFailureClass string
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

// recordCompletion is called exactly once per Sweep, whatever happened
// during it: a sweep that could not even mint a JWT still updates
// LastSweepAt, and a sweep that aborted partway through still reports
// whatever it redelivered or abandoned before the error.
func (sweeper *Sweeper) recordCompletion(at time.Time, redelivered, abandoned int, sweepErr error) {
	sweeper.mu.Lock()
	defer sweeper.mu.Unlock()
	sweptAt := at
	sweeper.last.LastSweepAt = &sweptAt
	sweeper.last.Redelivered = redelivered
	sweeper.last.Abandoned = abandoned
	if sweepErr != nil {
		failedAt := at
		sweeper.last.LastFailureAt = &failedAt
		sweeper.last.LastFailureClass = classify(sweepErr)
	}
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
// the configured interval normally, a GitHub-recommended wait when a 403 or
// 429 carried one, or the current exponential backoff otherwise. It never
// returns an error and never panics on one: a sweep that cannot reach GitHub
// is narrated in one line and retried later, which is the whole point of a
// background recovery loop outliving a transient GitHub outage.
func (sweeper *Sweeper) Sweep(ctx context.Context) time.Duration {
	if sweeper.options.Store == nil || sweeper.options.AppID <= 0 || len(sweeper.options.PrivateKeyPEM) == 0 {
		return sweeper.options.Interval
	}
	err := sweeper.sweepOnce(ctx)
	if err == nil {
		sweeper.backoff = sweeper.options.Interval
		return sweeper.options.Interval
	}
	sweeper.options.Narrate(narrate.Line{At: sweeper.options.Now(), Event: eventName, Subject: "github.com", Action: "sweep failed: " + err.Error()})
	var se *sweepError
	if errors.As(err, &se) && se.wait > 0 {
		// GitHub told us exactly how long to wait; honor that instead of the
		// exponential backoff, and do not let it perturb the backoff series
		// for the next organic failure.
		return se.wait
	}
	return sweeper.transientDelay()
}

// sweepOnce runs exactly one pass. Every branch below still runs prune and
// recordCompletion: pruning touches only the local store, never GitHub, so
// it owes nothing to whether the App API could be reached, and the status
// `wb daemon status` reads must reflect what actually happened even when a
// pass aborted partway through.
func (sweeper *Sweeper) sweepOnce(ctx context.Context) error {
	now := sweeper.options.Now()
	state := &tokenState{}
	var redelivered, abandoned int
	var uncounted bool
	var sweepErr error

	if err := sweeper.ensureToken(ctx, state); err != nil {
		sweepErr = err
	} else {
		cutoff := now.Add(-MaxDeliveryAge)
		failed, newestSuccessAt, listErr := sweeper.listRecentDeliveries(ctx, state, cutoff)
		if listErr != nil {
			sweepErr = listErr
		} else {
			redelivered, abandoned, uncounted, sweepErr = sweeper.actOn(ctx, state, failed, newestSuccessAt)
		}
	}
	if uncounted {
		sweeper.options.Narrate(narrate.Line{At: sweeper.options.Now(), Event: eventName, Subject: "github.com", Action: "endpoint unreachable; not counted"})
	}
	if pruneErr := sweeper.prune(ctx); pruneErr != nil && sweepErr == nil {
		sweepErr = pruneErr
	}
	sweeper.recordCompletion(now, redelivered, abandoned, sweepErr)
	return sweepErr
}

// tokenState is the App JWT a sweep authenticates with, re-minted partway
// through a long pass rather than once at the start.
type tokenState struct {
	value    string
	mintedAt time.Time
}

func (sweeper *Sweeper) ensureToken(_ context.Context, state *tokenState) error {
	now := sweeper.options.Now()
	if state.value != "" && now.Sub(state.mintedAt) < jwtRefreshAfter {
		return nil
	}
	token, err := hub.BuildAppJWT(sweeper.options.AppID, sweeper.options.PrivateKeyPEM, sweeper.options.Now)
	if err != nil {
		return &sweepError{class: "jwt", err: fmt.Errorf("mint github app jwt: %w", err)}
	}
	state.value = token
	state.mintedAt = now
	return nil
}

// deliveryItem is the subset of one GET /app/hook/deliveries entry the sweep
// needs. GitHub lists each attempt separately; a redelivery keeps the GUID
// and marks Redelivery true. It deliberately does not decode
// installation_id or repository_id: repository_events.go's privacy allowlist
// treats an installation ID as never publishable, and a narration line has
// no safe way to spend one on "which repository" without either identifier.
type deliveryItem struct {
	ID          int64     `json:"id"`
	GUID        string    `json:"guid"`
	DeliveredAt time.Time `json:"delivered_at"`
	Redelivery  bool      `json:"redelivery"`
	StatusCode  int       `json:"status_code"`
	Event       string    `json:"event"`
}

func (item deliveryItem) succeeded() bool {
	return item.StatusCode >= 200 && item.StatusCode < 300
}

// subject is the narrate.Line.Subject column: the webhook's own event name
// plus the literal "github.com", following the same shape
// hub.RepositoryEventService's narrationSubject uses for a delivery that
// names no repository. Never an installation or repository ID: neither is
// safe to publish on the console log a detached daemon writes to disk.
func (item deliveryItem) subject() string {
	event := strings.TrimSpace(item.Event)
	if event == "" {
		return "github.com"
	}
	return event + " github.com"
}

// listRecentDeliveries walks the App's delivery list newest-first, following
// cursor pagination via the Link header, and stops as soon as a page's
// deliveries cross the 72-hour boundary: everything after that is even
// older. Because the list is newest-first, the first time a GUID is seen is
// by construction its latest attempt, so grouping needs no sorting.
//
// newestSuccessAt is the most recent DeliveredAt among any GUID's latest
// attempt that succeeded, across the whole listing — the evidence
// spentAttempt uses to tell "the endpoint is reachable" from "it has been
// down the whole window".
func (sweeper *Sweeper) listRecentDeliveries(ctx context.Context, state *tokenState, cutoff time.Time) (failed []deliveryItem, newestSuccessAt time.Time, err error) {
	url := sweeper.options.APIBaseURL + "/app/hook/deliveries?per_page=" + strconv.Itoa(perPage)
	seen := make(map[string]bool)
	seenURLs := make(map[string]bool)
	var latest []deliveryItem
	for url != "" && len(seenURLs) < maxPages {
		if seenURLs[url] {
			// A repeating next URL would otherwise loop forever; the
			// listing so far is everything there is to work with.
			break
		}
		seenURLs[url] = true
		if err := sweeper.ensureToken(ctx, state); err != nil {
			return nil, time.Time{}, err
		}
		var page []deliveryItem
		next, getErr := sweeper.get(ctx, url, state.value, &page)
		if getErr != nil {
			return nil, time.Time{}, getErr
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
	failed = make([]deliveryItem, 0, len(latest))
	for _, item := range latest {
		if item.succeeded() {
			if item.DeliveredAt.After(newestSuccessAt) {
				newestSuccessAt = item.DeliveredAt
			}
			continue
		}
		failed = append(failed, item)
	}
	return failed, newestSuccessAt, nil
}

// spentAttempt reports whether the listing gives evidence the operator's
// endpoint is currently reachable, relative to one GUID's last recorded
// attempt (the zero time for a GUID never attempted before, so any success
// anywhere in the window counts). Without that evidence a redeliver call
// still goes out — the delivery may succeed even though nothing else has —
// but it must not spend one of MaxAttempts, or an outage longer than
// MaxAttempts intervals would abandon a GUID for a reason that has nothing
// to do with that GUID.
func spentAttempt(newestSuccessAt, lastAttemptAt time.Time) bool {
	return newestSuccessAt.After(lastAttemptAt)
}

// actOn redelivers each failed GUID, honoring three rules before it does:
// abandoned GUIDs are skipped for good; a GUID redelivered less than
// Interval ago is left for a later sweep, which is what keeps a crash-loop
// restart from hammering the redeliver endpoint; and a GUID already at
// MaxAttempts is abandoned without another GitHub call. It aborts and
// returns an error only for a systemic failure (401/403/429/5xx, or a
// transport error) — GitHub's own per-delivery rejection of a redeliver
// call (400/404/422) is counted, possibly abandoned, narrated, and the loop
// continues to the next GUID.
func (sweeper *Sweeper) actOn(ctx context.Context, state *tokenState, failed []deliveryItem, newestSuccessAt time.Time) (redelivered, abandoned int, uncounted bool, err error) {
	now := sweeper.options.Now().UTC()
	for _, item := range failed {
		record, found, loadErr := sweeper.options.Store.LoadWebhookRedelivery(ctx, item.GUID)
		if loadErr != nil {
			return redelivered, abandoned, uncounted, storeErr(loadErr)
		}
		if found && record.Abandoned {
			continue
		}
		var lastAttemptAt time.Time
		attempts := 0
		if found {
			lastAttemptAt = record.LastAttemptAt
			attempts = record.Attempts
		}
		if found && now.Sub(lastAttemptAt) < sweeper.options.Interval {
			continue
		}
		if attempts >= MaxAttempts {
			if saveErr := sweeper.options.Store.SaveWebhookRedelivery(ctx, hub.WebhookRedeliveryRecord{GUID: item.GUID, Attempts: attempts, Abandoned: true, LastAttemptAt: now}); saveErr != nil {
				return redelivered, abandoned, uncounted, storeErr(saveErr)
			}
			sweeper.narrate(item, now, fmt.Sprintf("abandoned after %d attempts", attempts))
			abandoned++
			continue
		}

		if !spentAttempt(newestSuccessAt, lastAttemptAt) {
			// No evidence the endpoint is reachable: ask GitHub anyway (the
			// delivery may succeed) but record only the gap-enforcing
			// timestamp, never the spent attempt.
			if saveErr := sweeper.options.Store.SaveWebhookRedelivery(ctx, hub.WebhookRedeliveryRecord{GUID: item.GUID, Attempts: attempts, Abandoned: false, LastAttemptAt: now}); saveErr != nil {
				return redelivered, abandoned, uncounted, storeErr(saveErr)
			}
			if _, callErr := sweeper.redeliver(ctx, state, item.ID); callErr != nil {
				return redelivered, abandoned, uncounted, callErr
			}
			uncounted = true
			continue
		}

		// M2: persist the spent attempt before asking GitHub, so a crash
		// between the save and the response can never grant a fourth real
		// attempt.
		nextAttempts := attempts + 1
		if saveErr := sweeper.options.Store.SaveWebhookRedelivery(ctx, hub.WebhookRedeliveryRecord{GUID: item.GUID, Attempts: nextAttempts, Abandoned: false, LastAttemptAt: now}); saveErr != nil {
			return redelivered, abandoned, uncounted, storeErr(saveErr)
		}
		status, callErr := sweeper.redeliver(ctx, state, item.ID)
		if callErr != nil {
			return redelivered, abandoned, uncounted, callErr
		}
		if status == http.StatusBadRequest || status == http.StatusNotFound || status == http.StatusUnprocessableEntity {
			if nextAttempts >= MaxAttempts {
				if saveErr := sweeper.options.Store.SaveWebhookRedelivery(ctx, hub.WebhookRedeliveryRecord{GUID: item.GUID, Attempts: nextAttempts, Abandoned: true, LastAttemptAt: now}); saveErr != nil {
					return redelivered, abandoned, uncounted, storeErr(saveErr)
				}
				sweeper.narrate(item, now, fmt.Sprintf("abandoned after %d attempts (github rejected redelivery, status %d)", nextAttempts, status))
				abandoned++
			} else {
				sweeper.narrate(item, now, fmt.Sprintf("github rejected redelivery (status %d); counted as attempt %d of %d", status, nextAttempts, MaxAttempts))
			}
			continue
		}
		sweeper.narrate(item, now, fmt.Sprintf("redelivered (attempt %d of %d)", nextAttempts, MaxAttempts))
		redelivered++
	}
	return redelivered, abandoned, uncounted, nil
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
		return storeErr(err)
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
	if err := sweeper.options.Store.DeleteWebhookRedeliveries(ctx, expired); err != nil {
		return storeErr(err)
	}
	return nil
}

// redeliver asks GitHub to redeliver one delivery attempt by its numeric id
// (not its GUID — GitHub keys this route by attempt id). It returns the
// response status for a call GitHub answered at all — 2xx, or a per-delivery
// 4xx the caller decides how to count — and an error only when the call
// itself could not be judged: a transport failure, or a systemic status
// (401/403/429/5xx) that should abort the sweep rather than this one GUID.
func (sweeper *Sweeper) redeliver(ctx context.Context, state *tokenState, deliveryID int64) (int, error) {
	if err := sweeper.ensureToken(ctx, state); err != nil {
		return 0, err
	}
	url := sweeper.options.APIBaseURL + "/app/hook/deliveries/" + strconv.FormatInt(deliveryID, 10) + "/attempts"
	response, err := sweeper.doRequest(ctx, http.MethodPost, url, state.value)
	if err != nil {
		return 0, err
	}
	defer func() { _ = response.Body.Close() }()
	switch response.StatusCode {
	case http.StatusAccepted, http.StatusOK, http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
		return response.StatusCode, nil
	default:
		return 0, sweeper.classifyResponseError(response)
	}
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

// doRequest performs one authenticated GitHub App API request and returns
// the raw response, still open, for the caller to classify and close.
func (sweeper *Sweeper) doRequest(ctx context.Context, method, url, token string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build github request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := sweeper.options.Client.Do(request)
	if err != nil {
		return nil, &sweepError{class: "transport", err: fmt.Errorf("reach github: %w", err)}
	}
	return response, nil
}

// get performs one authenticated GitHub App API request, decodes its JSON
// body into out, and returns the next page URL from the Link header, or "".
// Every non-200 status aborts: a delivery-listing failure has no per-item
// granularity to preserve.
func (sweeper *Sweeper) get(ctx context.Context, url, token string, out any) (string, error) {
	response, err := sweeper.doRequest(ctx, http.MethodGet, url, token)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", sweeper.classifyResponseError(response)
	}
	next := nextPageURL(response.Header.Get("Link"))
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(out); err != nil {
		return "", &sweepError{class: "decode", err: fmt.Errorf("decode github response: %w", err)}
	}
	return next, nil
}

// classifyResponseError turns a non-2xx response this package treats as
// abort-worthy into a classified error, reading Retry-After and
// X-RateLimit-Reset on 403/429 (per B1) so Sweep can honor whichever GitHub
// sent. 401 is classified "auth" too — the App JWT itself can expire
// mid-sweep and comes back this way — but carries no rate-limit wait: GitHub
// does not send one on a plain authentication failure.
func (sweeper *Sweeper) classifyResponseError(response *http.Response) error {
	status := response.StatusCode
	class := "unknown"
	var wait time.Duration
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		class = "auth"
	case status == http.StatusTooManyRequests:
		class = "rate_limited"
	case status >= http.StatusInternalServerError:
		class = "server_error"
	}
	if status == http.StatusForbidden || status == http.StatusTooManyRequests {
		if retryAfter, ok := hub.RetryAfter(response.Header); ok {
			wait = retryAfter
		} else if limit := hub.ReadRateLimit(response.Header); limit.Known {
			if remaining := limit.Reset.Sub(sweeper.options.Now()); remaining > 0 {
				wait = remaining
			}
		}
	}
	return &sweepError{class: class, wait: wait, err: fmt.Errorf("github responded %d", status)}
}

// sweepError classifies why a sweep failed, for LastFailureClass, and
// optionally carries a GitHub-recommended wait, for B1's backoff.
type sweepError struct {
	class string
	wait  time.Duration
	err   error
}

func (e *sweepError) Error() string { return e.err.Error() }
func (e *sweepError) Unwrap() error { return e.err }

func storeErr(err error) error { return &sweepError{class: "store", err: err} }

// classify extracts the short failure class LastFailureClass reports, or
// "unknown" for an error this package did not itself classify.
func classify(err error) string {
	var se *sweepError
	if errors.As(err, &se) {
		return se.class
	}
	return "unknown"
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
