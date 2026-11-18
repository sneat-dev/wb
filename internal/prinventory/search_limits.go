package prinventory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// searchRateLimitWaits bounds how many reset waits one owner query may take.
// Each wait is capped by maxSearchWait.
const searchRateLimitWaits = 5

// searchGate holds every worker while GitHub's search quota is exhausted, so
// one rate-limit response pauses the whole run rather than each worker
// discovering the limit separately.
type searchGate struct {
	mu    sync.Mutex
	until time.Time
}

func (g *searchGate) holdUntil(t time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if t.After(g.until) {
		g.until = t
	}
}

func (g *searchGate) wait(ctx context.Context, now func() time.Time, sleep func(context.Context, time.Duration) error) error {
	g.mu.Lock()
	until := g.until
	g.mu.Unlock()
	if delay := until.Sub(now()); delay > 0 {
		return sleep(ctx, delay)
	}
	return nil
}

// RealSleep is the production sleeper. It is deliberately not a default inside
// Inventory: a caller that supplies no Sleep never waits, so a test cannot reach
// a real timer by accident.
func RealSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// maxSearchWait caps one wait, so a local clock that is behind GitHub's cannot
// produce an arbitrarily long pause. A search window is one minute.
const maxSearchWait = 90 * time.Second

// searchOwner runs the owner search. A failed search that GitHub's own quota
// shows to be rate limited waits for the reset and retries; an HTTP 422 is
// accepted as "no results" only when the owner is proven to exist and to have
// no repositories. Neither decision reads the error text beyond the status.
func searchOwner(ctx context.Context, options Options, owner Owner, args []string) ([]byte, error) {
	for waits := 0; ; waits++ {
		if err := options.gate.wait(ctx, options.now, options.sleep); err != nil {
			return nil, err
		}
		out, err := options.Runner.Run(ctx, args...)
		if err == nil {
			return out, nil
		}
		if strings.Contains(strings.ToLower(err.Error()), "(http 422)") && ownerHasNoRepositories(ctx, options.Runner, owner) {
			return []byte(`[{"total_count":0,"items":[]}]`), nil
		}
		if options.sleep == nil || waits >= searchRateLimitWaits {
			return nil, err
		}
		delay, limited := searchLimitDelay(ctx, options)
		if !limited {
			return nil, err
		}
		note := ""
		if delay > maxSearchWait {
			delay, note = maxSearchWait, fmt.Sprintf(" (capped at %s)", maxSearchWait)
		}
		options.gate.holdUntil(options.now().Add(delay))
		options.notify(fmt.Sprintf("GitHub search rate limit reached; waiting %s%s for the reset before querying %s (wait %d/%d)", delay, note, owner, waits+1, searchRateLimitWaits))
	}
}

// searchLimitDelay asks the core-API rate_limit endpoint, which does not use
// search quota, whether the search quota is exhausted and when it resets. The
// error text of the failed search is never consulted: the production reader
// appends stdout to it, which can carry arbitrary pull-request titles.
func searchLimitDelay(ctx context.Context, options Options) (time.Duration, bool) {
	out, err := options.Runner.Run(ctx, "api", "rate_limit")
	if err != nil {
		return 0, false
	}
	var info struct {
		Resources struct {
			Search struct {
				Remaining int   `json:"remaining"`
				Reset     int64 `json:"reset"`
			} `json:"search"`
		} `json:"resources"`
	}
	if json.Unmarshal(out, &info) != nil || info.Resources.Search.Remaining > 0 {
		return 0, false
	}
	if wait := time.Unix(info.Resources.Search.Reset, 0).Sub(options.now()); wait > 0 {
		return wait + time.Second, true
	}
	return time.Second, true
}

// ownerHasNoRepositories confirms by evidence that a 422 from the search API
// means "nothing to search": the owner exists (the lookup succeeds) and
// reports zero public and zero private repositories. A private count that is
// hidden from the caller is accepted only for users, whose private repositories
// are never visible to a search anyway.
func ownerHasNoRepositories(ctx context.Context, runner Runner, owner Owner) bool {
	collection := "users/"
	if owner.Qualifier == "org" {
		collection = "orgs/"
	}
	out, err := runner.Run(ctx, "api", collection+owner.Login)
	if err != nil {
		return false
	}
	var info struct {
		PublicRepos       *int `json:"public_repos"`
		TotalPrivateRepos *int `json:"total_private_repos"`
	}
	if json.Unmarshal(out, &info) != nil || info.PublicRepos == nil || *info.PublicRepos != 0 {
		return false
	}
	if info.TotalPrivateRepos == nil {
		return owner.Qualifier == "user"
	}
	return *info.TotalPrivateRepos == 0
}
