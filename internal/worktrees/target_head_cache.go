package worktrees

import (
	"context"
	"sync"
)

// targetHeadCache memoises one exact origin target resolution per
// (canonical repository, base branch) for the lifetime of a single inventory
// walk.
//
// It is deliberately *not* a process-wide cache and never outlives the walk
// that installs it. Cleanup's safety argument rests on a freshly fetched
// target, and preflightCleanupRepository re-inspects on the caller's own
// context before any destructive write — a context with no cache in it — so
// the memo informs planning while the actual deletion still proves itself
// against a live fetch.
//
// A failed resolution is memoised too. An unreachable remote is a property of
// the repository, not of the worktree that happened to ask first, so a fleet
// walk should pay for it once rather than once per task in that repository.
type targetHeadCache struct {
	mu      sync.Mutex
	entries map[string]targetHeadEntry
}

type targetHeadEntry struct {
	sha string
	err error
}

type targetHeadCacheKey struct{}

// withTargetHeadCache returns a context whose exact-target resolutions are
// shared. Callers that must see a live fetch simply do not install one.
func withTargetHeadCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, targetHeadCacheKey{}, &targetHeadCache{
		entries: make(map[string]targetHeadEntry),
	})
}

func targetHeadCacheFrom(ctx context.Context) *targetHeadCache {
	cache, _ := ctx.Value(targetHeadCacheKey{}).(*targetHeadCache)
	return cache
}

// resolve returns the memoised target for this repository and branch, calling
// fetch at most once per key. The lock is held across fetch so that a later
// bounded-parallel walk cannot start N identical fetches for one repository
// before the first records its answer — the single-flight property is the
// whole point, and without it concurrency would reintroduce the duplication
// this cache exists to remove.
func (c *targetHeadCache) resolve(repository, branch string, fetch func() (string, error)) (string, error) {
	key := repository + "\x00" + branch
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.entries[key]; ok {
		return entry.sha, entry.err
	}
	sha, err := fetch()
	c.entries[key] = targetHeadEntry{sha: sha, err: err}
	return sha, err
}
