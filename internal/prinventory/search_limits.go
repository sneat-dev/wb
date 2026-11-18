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
// GitHub's search window is one minute, so each wait is at most about a minute
// and an owner query never stalls for longer than searchRateLimitWaits minutes.
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

func defaultSleep(ctx context.Context, d time.Duration) error {
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

// searchOwner runs the owner search. A search rate-limit response waits for
// the reset GitHub reports and retries; an HTTP 422 is accepted as "no results"
// only when the owner is proven to exist and to have no repositories.
func searchOwner(ctx context.Context, options Options, owner Owner, args []string) ([]byte, error) {
	for waits := 0; ; waits++ {
		if err := options.gate.wait(ctx, options.now, options.sleep); err != nil {
			return nil, err
		}
		out, err := options.Runner.Run(ctx, args...)
		if err == nil {
			return out, nil
		}
		message := strings.ToLower(err.Error())
		switch {
		case strings.Contains(message, "rate limit"):
			if waits >= searchRateLimitWaits {
				return nil, err
			}
			delay := searchResetDelay(ctx, options)
			options.gate.holdUntil(options.now().Add(delay))
			options.notify(fmt.Sprintf("GitHub search rate limit reached; waiting %s for the reset before querying %s (wait %d/%d)", delay, owner, waits+1, searchRateLimitWaits))
		case strings.Contains(message, "http 422") && ownerHasNoRepositories(ctx, options.Runner, owner):
			return []byte(`[{"total_count":0,"items":[]}]`), nil
		default:
			return nil, err
		}
	}
}

// searchResetDelay reads the search quota reset from the core-API rate_limit
// endpoint (which does not consume search quota). Without a usable reset, or
// when quota remains (a secondary limit), it waits one full search window.
func searchResetDelay(ctx context.Context, options Options) time.Duration {
	out, err := options.Runner.Run(ctx, "api", "rate_limit")
	if err != nil {
		return time.Minute
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
		return time.Minute
	}
	if wait := time.Unix(info.Resources.Search.Reset, 0).Sub(options.now()); wait > 0 {
		return wait + time.Second
	}
	return time.Second
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
