package hooks

import (
	"context"
	"strings"
	"testing"
	"time"
)

func zeroSHA() string { return strings.Repeat("0", 40) }
func fakeSHA(b byte) string {
	return strings.Repeat(string(rune(b)), 40)
}

// fixedPRLookup answers a fixed, known open/unknown status for every branch.
// It never touches the network -- see CachedGHPRLookup's own tests for that
// enrichment path.
type fixedPRLookup struct {
	open  map[string]bool
	known map[string]bool
}

func (f fixedPRLookup) OpenPullRequest(branch string) (open bool, known bool) {
	if f.known != nil && !f.known[branch] {
		return false, false
	}
	return f.open[branch], true
}

// TestClassifyPushTierSixFoundationalScenarios directly exercises the six
// scenarios the founder named: a branch with no open PR, a branch with an
// open PR, the default branch, a tag, a deletion-only push, and a WB
// checkpoint-ref push.
func TestClassifyPushTierSixFoundationalScenarios(t *testing.T) {
	lookup := fixedPRLookup{
		open:  map[string]bool{"has-pr": true, "no-pr": false},
		known: map[string]bool{"has-pr": true, "no-pr": true},
	}
	tests := []struct {
		name          string
		updates       []RefUpdate
		defaultBranch string
		wantTier      pushTier
	}{
		{
			name: "branch with no open PR runs the fast lane",
			updates: []RefUpdate{
				{LocalRef: "refs/heads/no-pr", LocalSHA: fakeSHA('a'), RemoteRef: "refs/heads/no-pr", RemoteSHA: zeroSHA()},
			},
			defaultBranch: "main",
			wantTier:      TierLint,
		},
		{
			name: "branch with an open PR runs the full tier",
			updates: []RefUpdate{
				{LocalRef: "refs/heads/has-pr", LocalSHA: fakeSHA('a'), RemoteRef: "refs/heads/has-pr", RemoteSHA: zeroSHA()},
			},
			defaultBranch: "main",
			wantTier:      TierPublication,
		},
		{
			name: "the default branch is always a publication push",
			updates: []RefUpdate{
				{LocalRef: "refs/heads/main", LocalSHA: fakeSHA('a'), RemoteRef: "refs/heads/main", RemoteSHA: fakeSHA('b')},
			},
			defaultBranch: "main",
			wantTier:      TierPublication,
		},
		{
			name: "a tag is always a publication push",
			updates: []RefUpdate{
				{LocalRef: "refs/tags/v1.2.3", LocalSHA: fakeSHA('a'), RemoteRef: "refs/tags/v1.2.3", RemoteSHA: zeroSHA()},
			},
			defaultBranch: "main",
			wantTier:      TierPublication,
		},
		{
			name: "a pure deletion skips lint and test",
			updates: []RefUpdate{
				{LocalRef: "(delete)", LocalSHA: zeroSHA(), RemoteRef: "refs/heads/no-pr", RemoteSHA: fakeSHA('a')},
			},
			defaultBranch: "main",
			wantTier:      TierSkip,
		},
		{
			name: "a WB checkpoint-ref push skips lint and test",
			updates: []RefUpdate{
				{LocalRef: "refs/heads/task", LocalSHA: fakeSHA('a'), RemoteRef: "refs/wb/checkpoints/task", RemoteSHA: fakeSHA('b')},
			},
			defaultBranch: "main",
			wantTier:      TierSkip,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ClassifyPushTier(test.updates, test.defaultBranch, lookup)
			if got.Tier != test.wantTier {
				t.Fatalf("tier = %d (%s), want %d", got.Tier, got.Reason, test.wantTier)
			}
			if strings.TrimSpace(got.Reason) == "" {
				t.Fatal("classification must always carry a non-empty, explainable reason")
			}
			if got.ExitCode() != int(test.wantTier) {
				t.Fatalf("ExitCode() = %d, want %d", got.ExitCode(), int(test.wantTier))
			}
		})
	}
}

func TestClassifyPushTierUnknownPRStatusStaysAtTheFastLane(t *testing.T) {
	// The founder's explicit constraint: an unresolvable PR status must never
	// silently escalate to the 6-minute full tier. CI remains the real gate.
	lookup := fixedPRLookup{known: map[string]bool{}}
	got := ClassifyPushTier([]RefUpdate{
		{LocalRef: "refs/heads/mystery", LocalSHA: fakeSHA('a'), RemoteRef: "refs/heads/mystery", RemoteSHA: zeroSHA()},
	}, "main", lookup)
	if got.Tier != TierLint {
		t.Fatalf("unknown PR status tier = %d (%s), want %d (fast lane)", got.Tier, got.Reason, TierLint)
	}
}

func TestClassifyPushTierNoLookupConfiguredStaysAtTheFastLane(t *testing.T) {
	got := ClassifyPushTier([]RefUpdate{
		{LocalRef: "refs/heads/feature", LocalSHA: fakeSHA('a'), RemoteRef: "refs/heads/feature", RemoteSHA: zeroSHA()},
	}, "main", nil)
	if got.Tier != TierLint {
		t.Fatalf("tier = %d, want %d", got.Tier, TierLint)
	}
}

func TestClassifyPushTierMixedRefsTakeTheHighestRequirement(t *testing.T) {
	lookup := fixedPRLookup{known: map[string]bool{"feature": true}, open: map[string]bool{"feature": false}}
	got := ClassifyPushTier([]RefUpdate{
		{LocalRef: "(delete)", LocalSHA: zeroSHA(), RemoteRef: "refs/heads/old", RemoteSHA: fakeSHA('a')},
		{LocalRef: "refs/heads/feature", LocalSHA: fakeSHA('a'), RemoteRef: "refs/heads/feature", RemoteSHA: zeroSHA()},
		{LocalRef: "refs/heads/main", LocalSHA: fakeSHA('a'), RemoteRef: "refs/heads/main", RemoteSHA: fakeSHA('b')},
	}, "main", lookup)
	if got.Tier != TierPublication {
		t.Fatalf("mixed push tier = %d (%s), want %d (default branch present)", got.Tier, got.Reason, TierPublication)
	}
}

func TestClassifyPushTierEmptyUpdatesDefaultsToFullTier(t *testing.T) {
	got := ClassifyPushTier(nil, "main", nil)
	if got.Tier != TierPublication {
		t.Fatalf("empty update list tier = %d, want %d (safe default)", got.Tier, TierPublication)
	}
}

func TestParseRefUpdatesRejectsMalformedLines(t *testing.T) {
	if _, err := ParseRefUpdates(strings.NewReader("only-one-field\n")); err == nil {
		t.Fatal("expected an error for a malformed pushed-ref line")
	}
}

func TestParseRefUpdatesSkipsBlankLines(t *testing.T) {
	updates, err := ParseRefUpdates(strings.NewReader("\nrefs/heads/a " + zeroSHA() + " refs/heads/a " + fakeSHA('a') + "\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 {
		t.Fatalf("updates = %#v, want exactly 1", updates)
	}
}

// TestCachedGHPRLookupPrefersFreshCacheOverAskingGH proves the local signal
// wb already owns wins over a network round trip: with a fresh cache entry in
// place, RunGH must never be invoked.
func TestCachedGHPRLookupPrefersFreshCacheOverAskingGH(t *testing.T) {
	called := false
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	lookup := &CachedGHPRLookup{
		RepoRoot: t.TempDir(), RepoSlug: "acme/app",
		CachePath: t.TempDir() + "/cache.json",
		TTL:       time.Hour, Timeout: time.Second,
		Now: func() time.Time { return now },
		RunGH: func(ctx context.Context, dir string, args ...string) ([]byte, error) {
			called = true
			return []byte(`[]`), nil
		},
	}
	savePRStatusCache(lookup.CachePath, map[string]prStatusCacheEntry{
		"acme/app#feature": {Open: true, CheckedAt: now.Add(-time.Minute)},
	})
	open, known := lookup.OpenPullRequest("feature")
	if !known || !open {
		t.Fatalf("open=%v known=%v, want a fresh cache hit reporting open", open, known)
	}
	if called {
		t.Fatal("a fresh cache entry must never be bypassed to ask gh")
	}
}

// TestCachedGHPRLookupRevalidatesFreshNegative proves a mutable negative
// answer cannot hide a pull request created after the previous push. Positive
// answers are monotonic enough to cache, but "no PR" must ask GitHub again.
func TestCachedGHPRLookupRevalidatesFreshNegative(t *testing.T) {
	calls := 0
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	lookup := &CachedGHPRLookup{
		RepoRoot: t.TempDir(), RepoSlug: "acme/app",
		CachePath: t.TempDir() + "/cache.json",
		TTL:       time.Hour, Timeout: time.Second,
		Now: func() time.Time { return now },
		RunGH: func(ctx context.Context, dir string, args ...string) ([]byte, error) {
			calls++
			return []byte(`[{"number":42}]`), nil
		},
	}
	savePRStatusCache(lookup.CachePath, map[string]prStatusCacheEntry{
		"acme/app#feature": {Open: false, CheckedAt: now.Add(-time.Minute)},
	})

	open, known := lookup.OpenPullRequest("feature")
	if !known || !open || calls != 1 {
		t.Fatalf("open=%v known=%v calls=%d, want a revalidated open PR after exactly one gh call", open, known, calls)
	}
}

// TestCachedGHPRLookupFallsBackToGHOnCacheMiss proves the bounded enrichment
// path: a cache miss triggers exactly one gh call, whose result is then
// cached for next time.
func TestCachedGHPRLookupFallsBackToGHOnCacheMiss(t *testing.T) {
	calls := 0
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	lookup := &CachedGHPRLookup{
		RepoRoot: t.TempDir(), RepoSlug: "acme/app",
		CachePath: t.TempDir() + "/cache.json",
		TTL:       time.Hour, Timeout: time.Second,
		Now: func() time.Time { return now },
		RunGH: func(ctx context.Context, dir string, args ...string) ([]byte, error) {
			calls++
			return []byte(`[{"number":42}]`), nil
		},
	}
	open, known := lookup.OpenPullRequest("feature")
	if !known || !open || calls != 1 {
		t.Fatalf("open=%v known=%v calls=%d, want open known after exactly one gh call", open, known, calls)
	}
	// The second call within the TTL window must be served from cache.
	open, known = lookup.OpenPullRequest("feature")
	if !known || !open || calls != 1 {
		t.Fatalf("second lookup made %d gh calls, want the cached answer reused", calls)
	}
}

// TestCachedGHPRLookupNeverCachesAFailureOrTimeout proves an unresolvable
// answer is reported as unknown, and is never poisoned into the cache, so the
// very next push tries again instead of freezing on a bad answer.
func TestCachedGHPRLookupNeverCachesAFailureOrTimeout(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	calls := 0
	lookup := &CachedGHPRLookup{
		RepoRoot: t.TempDir(), RepoSlug: "acme/app",
		CachePath: t.TempDir() + "/cache.json",
		TTL:       time.Hour, Timeout: time.Second,
		Now: func() time.Time { return now },
		RunGH: func(ctx context.Context, dir string, args ...string) ([]byte, error) {
			calls++
			return nil, context.DeadlineExceeded
		},
	}
	open, known := lookup.OpenPullRequest("feature")
	if known || open {
		t.Fatalf("open=%v known=%v, want unknown on a failed lookup", open, known)
	}
	if _, known = lookup.OpenPullRequest("feature"); known {
		t.Fatal("a failed lookup must not be cached")
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want exactly one retry per uncached lookup", calls)
	}
}

func TestDetectDefaultBranchHonoursExplicitOverride(t *testing.T) {
	t.Setenv(DefaultBranchEnv, "trunk")
	if got := detectDefaultBranch(t.TempDir()); got != "trunk" {
		t.Fatalf("detectDefaultBranch = %q, want %q", got, "trunk")
	}
}

func TestDetectDefaultBranchReturnsEmptyWhenUnresolvable(t *testing.T) {
	repo := initRepo(t)
	if got := detectDefaultBranch(repo); got != "" {
		t.Fatalf("detectDefaultBranch = %q, want empty (no origin configured)", got)
	}
}

// TestClassifyPendingPushWithEmptyNonTerminalStdinDefaultsToFullTier covers
// the (unusual, but possible) case of a real pipe that carries no ref-update
// lines at all. This is deliberately distinct from the interactive-terminal
// path: a terminal has a known, explainable reason to run the fast lane
// (there is no git invocation at all), while an empty pipe is unexplained and
// gets the conservative full-tier default. console.IsTerminal only ever
// returns true for a live *os.File terminal device, which a test cannot
// safely fabricate without a real pty, so the interactive branch itself is
// exercised only by inspection and by the CLI-level integration test in
// cmd/wb, not here.
func TestClassifyPendingPushWithEmptyNonTerminalStdinDefaultsToFullTier(t *testing.T) {
	got, err := ClassifyPendingPush(strings.NewReader(""), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got.Tier != TierPublication {
		t.Fatalf("empty non-terminal stdin tier = %d, want %d (no ref lines observed)", got.Tier, TierPublication)
	}
}
