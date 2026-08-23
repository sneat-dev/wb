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
	entries map[string]*targetHeadEntry
}

// targetHeadEntry carries its own once so single-flight is per repository. A
// single cache-wide lock held across the fetch single-flights correctly but
// serialises every repository behind whichever one is currently fetching, which
// silently neuters concurrent inspection: three distinct repositories with a
// ceiling of three peaked at one concurrent fetch.
type targetHeadEntry struct {
	once sync.Once
	sha  string
	err  error
}

type targetHeadCacheKey struct{}

// withTargetHeadCache returns a context whose exact-target resolutions are
// shared. Callers that must see a live fetch simply do not install one.
func withTargetHeadCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, targetHeadCacheKey{}, &targetHeadCache{
		entries: make(map[string]*targetHeadEntry),
	})
}

func targetHeadCacheFrom(ctx context.Context) *targetHeadCache {
	cache, _ := ctx.Value(targetHeadCacheKey{}).(*targetHeadCache)
	return cache
}

// resolve returns the memoised target for this repository and branch, calling
// fetch at most once per key even when many workers ask at the same moment.
// The cache lock is held only long enough to find or create the entry; the
// fetch happens under that entry's own once, so one repository single-flights
// while other repositories proceed concurrently. Holding the cache lock across
// the fetch instead would preserve single-flight and destroy the concurrency.
func (c *targetHeadCache) resolve(repository, branch string, fetch func() (string, error)) (string, error) {
	key := repository + "\x00" + branch
	c.mu.Lock()
	entry, ok := c.entries[key]
	if !ok {
		entry = &targetHeadEntry{}
		c.entries[key] = entry
	}
	c.mu.Unlock()
	entry.once.Do(func() { entry.sha, entry.err = fetch() })
	return entry.sha, entry.err
}
