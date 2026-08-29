package hooks

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/console"
)

// defaultPRStatusCacheTTL bounds how long a cached positive open-PR answer is trusted
// before push-tier will pay for one more bounded gh lookup. It is a
// durability/latency trade-off. Negative answers are never trusted from the
// cache because a pull request can be created immediately after a push.
const defaultPRStatusCacheTTL = 10 * time.Minute

// defaultPRStatusLookupTimeout is the hard ceiling on the one optional
// network call this package ever makes. A git hook that can hang on a
// network round trip is strictly worse than the 6-minute tax it replaces, so
// this timeout is non-negotiable and always applied via context, never left
// to gh's own defaults.
const defaultPRStatusLookupTimeout = 3 * time.Second

// runGHFunc runs `gh` with the given arguments, rooted at dir, and returns
// its stdout. It exists so tests can substitute a fake without touching the
// real network or requiring gh on PATH.
type runGHFunc func(ctx context.Context, dir string, args ...string) ([]byte, error)

// CachedGHPRLookup is the enrichment path in push-tier's PR-status decision.
// It is deliberately the SECOND thing consulted, never the first: a fresh
// positive cache entry (a signal WB itself already wrote) wins over asking
// GitHub again. Negative answers are revalidated because PR creation makes
// them stale immediately. Every gh call has a hard timeout.
type CachedGHPRLookup struct {
	RepoRoot  string
	RepoSlug  string
	CachePath string
	TTL       time.Duration
	Timeout   time.Duration
	Now       func() time.Time
	RunGH     runGHFunc
}

// NewCachedGHPRLookup builds the production lookup for repoRoot: cache under
// the user's XDG state directory (matching hook-metrics' own convention),
// repository slug from the configured origin remote, gh invoked through
// console.Env() so it can never block on a prompt or a pager.
func NewCachedGHPRLookup(repoRoot string) *CachedGHPRLookup {
	return &CachedGHPRLookup{
		RepoRoot:  repoRoot,
		RepoSlug:  originSlug(repoRoot),
		CachePath: defaultPRStatusCachePath(),
		TTL:       defaultPRStatusCacheTTL,
		Timeout:   defaultPRStatusLookupTimeout,
		Now:       time.Now,
		RunGH:     runGH,
	}
}

func defaultPRStatusCachePath() string {
	if stateHome := os.Getenv("XDG_STATE_HOME"); stateHome != "" {
		return filepath.Join(stateHome, "wb", "pr-status-cache.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".wb", "pr-status-cache.json")
	}
	return filepath.Join(home, ".local", "state", "wb", "pr-status-cache.json")
}

type prStatusCacheEntry struct {
	Open      bool      `json:"open"`
	CheckedAt time.Time `json:"checked_at"`
}

// OpenPullRequest answers whether branch has an open pull request in
// RepoSlug. known is false whenever the answer cannot be established without
// exceeding the bounded, offline-safe budget above: no cache entry, an
// expired one, gh missing, gh erroring, or the timeout firing. A failed or
// unknown lookup is never cached, so the next push tries again rather than
// freezing on a bad answer.
func (l *CachedGHPRLookup) OpenPullRequest(branch string) (open bool, known bool) {
	if strings.TrimSpace(l.RepoSlug) == "" || strings.TrimSpace(branch) == "" {
		return false, false
	}
	now := time.Now
	if l.Now != nil {
		now = l.Now
	}
	cache := loadPRStatusCache(l.CachePath)
	key := l.RepoSlug + "#" + branch
	if entry, found := cache[key]; found && entry.Open && now().Sub(entry.CheckedAt) < l.ttl() {
		return entry.Open, true
	}

	runner := l.RunGH
	if runner == nil {
		runner = runGH
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return false, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout())
	defer cancel()
	output, err := runner(ctx, l.RepoRoot, "pr", "list",
		"--repo", l.RepoSlug, "--head", branch, "--state", "open",
		"--json", "number", "--limit", "1")
	if err != nil {
		return false, false
	}
	var pulls []struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(output, &pulls); err != nil {
		return false, false
	}
	open = len(pulls) > 0
	if open {
		cache[key] = prStatusCacheEntry{Open: true, CheckedAt: now()}
		savePRStatusCache(l.CachePath, cache)
	}
	return open, true
}

func (l *CachedGHPRLookup) ttl() time.Duration {
	if l.TTL > 0 {
		return l.TTL
	}
	return defaultPRStatusCacheTTL
}

func (l *CachedGHPRLookup) timeout() time.Duration {
	if l.Timeout > 0 {
		return l.Timeout
	}
	return defaultPRStatusLookupTimeout
}

func runGH(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	cmd.Env = console.Env()
	return cmd.Output()
}

func loadPRStatusCache(path string) map[string]prStatusCacheEntry {
	cache := map[string]prStatusCacheEntry{}
	if strings.TrimSpace(path) == "" {
		return cache
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return cache
	}
	// A corrupt or foreign-format cache file is never fatal: push-tier
	// degrades to "unknown" for every branch until the file self-heals on
	// the next successful lookup, which is strictly better than a hook
	// failing on its own diagnostic cache.
	_ = json.Unmarshal(content, &cache)
	if cache == nil {
		cache = map[string]prStatusCacheEntry{}
	}
	return cache
}

func savePRStatusCache(path string, cache map[string]prStatusCacheEntry) {
	if strings.TrimSpace(path) == "" {
		return
	}
	encoded, err := json.Marshal(cache)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	// Best-effort: a cache write failure must never fail the push-tier
	// decision that is already in hand.
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o644); err != nil {
		return
	}
	_ = os.Rename(temporary, path)
}
