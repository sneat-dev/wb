// Package poller is the ingester a self-hosted bench needs by default.
//
// Without a GitHub App there is no webhook to receive, so the daemon asks
// GitHub instead: once per interval it reads every repository in the local
// machine's published inventory and compares the answer with what it saw last
// time. A moved default branch or a renamed repository produces exactly the
// repositoryevent.Event the webhook path would have produced, enqueued for
// the local machine through the hub's own repository-event store. The daemon
// downstream cannot tell the two apart, which is the point: polling and
// webhooks are two ways to fill one queue.
//
// Event IDs are derived from repository, reason and target, so a replay after
// a crash deduplicates through the store's marker collection rather than
// fast-forwarding a clone twice.
package poller

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

	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/hub/narrate"
)

const (
	// DefaultAPIBaseURL is GitHub's public API. A test points this at an
	// httptest server instead.
	DefaultAPIBaseURL = "https://api.github.com"
	// DefaultInterval matches hub.github.poll_interval's default.
	DefaultInterval = 60 * time.Second
	// MaxBackoff bounds the exponential retreat after a transient GitHub
	// failure. Ten minutes is long enough to ride out an incident and short
	// enough that an operator who fixes their token does not have to restart
	// the daemon to see it work.
	MaxBackoff = 10 * time.Minute
	// eventName is what the narrated lines call the poller, as the feature
	// specification's example output does.
	eventName = "poll"
	// maxResponseBytes bounds what is read from a GitHub response. The two
	// documents the poller reads are small; anything larger is a
	// misconfigured proxy, not GitHub.
	maxResponseBytes = 1 << 20
	// rateLimitHeadroom is the multiple of the polled repository count that
	// must remain in the rate-limit budget for another tick to be safe: one
	// request pair per repository, with the same again in reserve for
	// everything else the operator's token does.
	rateLimitHeadroom = 2
)

// Options configures a Poller. Client, Snapshots, Events, Observations and
// Machine are required; everything else has a working default.
type Options struct {
	// Client performs the GitHub requests.
	Client *http.Client
	// APIBaseURL is GitHub's API origin. Empty means DefaultAPIBaseURL.
	APIBaseURL string
	// Token returns the operator's GitHub token. It is called once per tick
	// so that rotating the token file takes effect without a restart, and its
	// result is never narrated, logged, or stored.
	Token func() (string, error)
	// Snapshots is where the local machine's repository inventory is read
	// from: the poller polls exactly what the machine publishes.
	Snapshots hub.MachineSnapshotStore
	// Events receives the enqueued events.
	Events hub.RepositoryEventStore
	// Observations is the poller's memory of what it last saw.
	Observations hub.PollObservationStore
	// Machine is the local machine every polled event is queued for.
	Machine hub.Machine
	// Interval is the gap between ticks. Zero means DefaultInterval.
	Interval time.Duration
	// Now and Sleep exist so a test drives time instead of waiting for it.
	Now   func() time.Time
	Sleep func(context.Context, time.Duration) error
	// Narrate receives one line per repository observed, plus one line for a
	// failure that ends a tick before any repository is read.
	Narrate func(narrate.Line)
	// Covered reports whether an installed GitHub App already delivers
	// webhooks for a repository, which the poller then skips. Nil means
	// nothing is covered, which is the whole of this feature's default
	// journey; webhook mode supplies it.
	Covered func(repository string) bool
}

// Poller reads GitHub on an interval and enqueues what changed.
type Poller struct {
	options Options

	// backoff is the delay the next tick waits after a transient failure. It
	// starts at the configured interval and doubles, capped at MaxBackoff,
	// until a clean tick resets it.
	backoff time.Duration

	// mu guards repositories, which `wb daemon status` reads from another
	// goroutine through the daemon's health endpoint.
	mu           sync.Mutex
	repositories int
}

// New returns a Poller with every optional field defaulted.
func New(options Options) *Poller {
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
	if options.Covered == nil {
		options.Covered = func(string) bool { return false }
	}
	return &Poller{options: options, backoff: options.Interval}
}

// Repositories is how many repositories the last tick actually polled, which
// `wb daemon status` reports. It is read from another goroutine.
func (poller *Poller) Repositories() int {
	poller.mu.Lock()
	defer poller.mu.Unlock()
	return poller.repositories
}

func (poller *Poller) setRepositories(count int) {
	poller.mu.Lock()
	poller.repositories = count
	poller.mu.Unlock()
}

// Run ticks until ctx ends. It returns nil on a cancelled context: a stopped
// poller is a normal shutdown, not a failure the daemon should report.
func (poller *Poller) Run(ctx context.Context) error {
	for {
		delay := poller.Tick(ctx)
		if err := poller.options.Sleep(ctx, delay); err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return nil
		}
	}
}

// Tick performs one pass over the inventory and returns how long to wait
// before the next one: the configured interval normally, the remaining
// rate-limit window when GitHub's budget is nearly spent, or the current
// backoff after a transient failure.
func (poller *Poller) Tick(ctx context.Context) time.Duration {
	token, err := poller.options.Token()
	if err != nil {
		// The error carries a path, never the file's contents.
		poller.narrate("github.com", "error: "+err.Error())
		return poller.transientDelay()
	}
	repositories, err := poller.inventory(ctx)
	if err != nil {
		poller.narrate("github.com", "error: "+err.Error())
		return poller.transientDelay()
	}
	poller.setRepositories(len(repositories))

	transient := false
	var budget rateLimit
	for _, repository := range repositories {
		observed, limit, observeErr := poller.observe(ctx, repository, token)
		if limit.known {
			budget = limit
		}
		if observeErr != nil {
			poller.narrate(repository, "error: "+observeErr.Error())
			if isTransient(observeErr) {
				transient = true
			}
			continue
		}
		poller.narrate(repository, observed)
	}
	if transient {
		return poller.transientDelay()
	}
	poller.backoff = poller.options.Interval
	// One request pair per repository per tick is the contract; when fewer
	// than two ticks' worth of budget remains, wait for the window to reset
	// rather than spend the operator's remaining calls on the next tick.
	if budget.known && budget.remaining < rateLimitHeadroom*len(repositories) {
		wait := budget.reset.Sub(poller.options.Now())
		if wait < 0 {
			wait = 0
		}
		poller.narrate("github.com", "rate limited; next poll at "+budget.reset.Format("15:04:05"))
		return wait
	}
	return poller.options.Interval
}

// transientDelay returns the current backoff and doubles it for next time.
func (poller *Poller) transientDelay() time.Duration {
	delay := poller.backoff
	if delay <= 0 {
		delay = poller.options.Interval
	}
	poller.backoff = min(delay*2, MaxBackoff)
	return delay
}

func (poller *Poller) narrate(subject, action string) {
	poller.options.Narrate(narrate.Line{At: poller.options.Now(), Event: eventName, Subject: subject, Action: action})
}

// inventory is the sorted, canonical repository list the local machine last
// published, minus anything a GitHub App already covers.
func (poller *Poller) inventory(ctx context.Context) ([]string, error) {
	if poller.options.Snapshots == nil {
		return nil, errors.New("machine snapshot store is unavailable")
	}
	records, err := poller.options.Snapshots.ListLatest(ctx)
	if err != nil {
		return nil, errors.New("list machine snapshots")
	}
	repositories := make([]string, 0)
	for _, record := range records {
		if record.MachineID != poller.options.Machine.ID {
			continue
		}
		for _, repository := range record.Snapshot.Repositories {
			if poller.options.Covered(repository) {
				continue
			}
			repositories = append(repositories, repository)
		}
	}
	return repositories, nil
}

// observe reads one repository and returns the plain-words action taken.
func (poller *Poller) observe(ctx context.Context, repository, token string) (string, rateLimit, error) {
	owner, name, found := strings.Cut(strings.TrimPrefix(repository, "github.com/"), "/")
	if !found || owner == "" || name == "" {
		return "", rateLimit{}, fmt.Errorf("%q is not an owner/repository name", repository)
	}
	var view struct {
		ID            int64  `json:"id"`
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
	}
	limit, err := poller.get(ctx, "/repos/"+owner+"/"+name, token, &view)
	if err != nil {
		return "", limit, err
	}
	if strings.TrimSpace(view.FullName) == "" || strings.TrimSpace(view.DefaultBranch) == "" {
		return "", limit, errors.New("github returned no full_name or default_branch")
	}
	var commit struct {
		SHA string `json:"sha"`
	}
	commitLimit, err := poller.get(ctx, "/repos/"+owner+"/"+name+"/commits/"+view.DefaultBranch, token, &commit)
	if commitLimit.known {
		limit = commitLimit
	}
	if err != nil {
		return "", limit, err
	}
	if strings.TrimSpace(commit.SHA) == "" {
		return "", limit, errors.New("github returned no head commit sha")
	}
	action, err := poller.reconcile(ctx, repository, canonicalRepository(view.FullName), view.FullName, view.DefaultBranch, commit.SHA)
	return action, limit, err
}

// reconcile compares one reading against the stored observation and enqueues
// whatever the difference implies.
func (poller *Poller) reconcile(ctx context.Context, key, current, fullName, branch, sha string) (string, error) {
	if poller.options.Observations == nil || poller.options.Events == nil {
		return "", errors.New("poll observation store is unavailable")
	}
	previous, found, err := poller.options.Observations.LoadPollObservation(ctx, key)
	if err != nil {
		return "", err
	}
	next := hub.PollObservation{Repository: current, FullName: fullName, DefaultBranch: branch, SHA: sha, ObservedAt: poller.options.Now().UTC()}

	// First sight of a repository is recorded and nothing else: the head it
	// happens to be at now was not pushed by anything the daemon missed, and
	// enqueueing it would fast-forward a clone that is already correct.
	if !found {
		if err := poller.options.Observations.SavePollObservation(ctx, next); err != nil {
			return "", err
		}
		return "no change", nil
	}

	if current != previous.Repository {
		event := repositoryevent.Event{
			Version:            repositoryevent.ContractVersion,
			ID:                 eventID(current, repositoryevent.ReasonRepositoryRenamed, current),
			Repository:         current,
			PreviousRepository: previous.Repository,
			Ref:                "refs/heads/" + branch,
			Reason:             repositoryevent.ReasonRepositoryRenamed,
		}
		if err := poller.enqueue(ctx, event); err != nil {
			return "", err
		}
		if err := poller.options.Observations.SavePollObservation(ctx, next); err != nil {
			return "", err
		}
		// The observation is re-keyed: the old name will never be read again,
		// and leaving it behind would make a repository renamed back to it
		// look unchanged.
		if err := poller.options.Observations.DeletePollObservation(ctx, previous.Repository); err != nil {
			return "", err
		}
		return "renamed from " + strings.TrimPrefix(previous.Repository, "github.com/") + "; queued for " + poller.options.Machine.Name, nil
	}

	if sha == previous.SHA && branch == previous.DefaultBranch {
		return "no change", nil
	}
	occurredAt := poller.options.Now().UTC()
	event := repositoryevent.Event{
		Version:    repositoryevent.ContractVersion,
		ID:         eventID(current, repositoryevent.ReasonDefaultBranchUpdated, sha),
		Repository: current,
		Ref:        "refs/heads/" + branch,
		Reason:     repositoryevent.ReasonDefaultBranchUpdated,
		TargetSHA:  sha,
		OccurredAt: &occurredAt,
	}
	if err := poller.enqueue(ctx, event); err != nil {
		return "", err
	}
	if err := poller.options.Observations.SavePollObservation(ctx, next); err != nil {
		return "", err
	}
	return "default branch " + branch + " -> " + shortSHA(sha) + "; queued for " + poller.options.Machine.Name, nil
}

// enqueue writes the event for the local machine. Entitlements are not
// consulted: a loopback hub has one identity and one machine, and the
// operator's own token already decided which repositories they can read.
func (poller *Poller) enqueue(ctx context.Context, event repositoryevent.Event) error {
	if _, err := poller.options.Events.EnqueueForMachines(ctx, event, []hub.Machine{poller.options.Machine}); err != nil {
		return fmt.Errorf("enqueue %s: %w", event.Reason, err)
	}
	return nil
}

// rateLimit is what GitHub's headers said about the remaining budget.
type rateLimit struct {
	known     bool
	remaining int
	reset     time.Time
}

// transientError marks a status GitHub is expected to recover from, so the
// poller retreats instead of giving up. It mirrors hub's own
// transientGitHubStatus classification.
type transientError struct{ status int }

func (err transientError) Error() string { return "github responded " + strconv.Itoa(err.status) }

func isTransient(err error) bool {
	var transient transientError
	if errors.As(err, &transient) {
		return true
	}
	// A request that never reached GitHub is as transient as a 503.
	var urlErr *transportError
	return errors.As(err, &urlErr)
}

// transportError wraps a failure to reach GitHub at all.
type transportError struct{ err error }

func (err *transportError) Error() string { return "reach github: " + err.err.Error() }
func (err *transportError) Unwrap() error { return err.err }

// get performs one authenticated GitHub request and decodes its body.
func (poller *Poller) get(ctx context.Context, path, token string, out any) (rateLimit, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, poller.options.APIBaseURL+path, nil)
	if err != nil {
		return rateLimit{}, fmt.Errorf("build github request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := poller.options.Client.Do(request)
	if err != nil {
		return rateLimit{}, &transportError{err: err}
	}
	defer func() { _ = response.Body.Close() }()
	limit := readRateLimit(response.Header)
	if response.StatusCode != http.StatusOK {
		if transientGitHubStatus(response.StatusCode) {
			return limit, transientError{status: response.StatusCode}
		}
		return limit, fmt.Errorf("github responded %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(out); err != nil {
		return limit, fmt.Errorf("decode github response: %w", err)
	}
	return limit, nil
}

// transientGitHubStatus is the same classification hub/github_app.go uses: a
// status the caller should retreat from rather than treat as a permanent
// answer.
func transientGitHubStatus(status int) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	}
	return status >= 500
}

func readRateLimit(header http.Header) rateLimit {
	remaining, remainingErr := strconv.Atoi(strings.TrimSpace(header.Get("X-RateLimit-Remaining")))
	reset, resetErr := strconv.ParseInt(strings.TrimSpace(header.Get("X-RateLimit-Reset")), 10, 64)
	if remainingErr != nil || resetErr != nil {
		return rateLimit{}
	}
	return rateLimit{known: true, remaining: remaining, reset: time.Unix(reset, 0)}
}

// eventID derives a stable, contract-legal identifier from repository, reason
// and target, so the same observation made twice deduplicates in the store's
// marker collection.
//
// The contract's identifiers admit no "/", so the repository is flattened
// rather than spelled: `github.com/sneat-dev/wb` becomes `sneat-dev_wb`. The
// mapping is injective over repository names, which is all deduplication
// needs.
func eventID(repository string, reason repositoryevent.Reason, target string) string {
	return "poll:" + identifierPart(repository) + ":" + string(reason) + ":" + identifierPart(target)
}

func identifierPart(value string) string {
	return strings.ReplaceAll(strings.TrimPrefix(value, "github.com/"), "/", "_")
}

func canonicalRepository(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if strings.HasPrefix(value, "github.com/") {
		return value
	}
	return "github.com/" + value
}

func shortSHA(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
