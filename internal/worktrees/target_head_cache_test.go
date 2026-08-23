package worktrees

import (
	"context"
	"errors"
	"testing"
)

// The measured shape on a real fleet: 262 worktrees across 71 repositories, so
// 73% of the exact-target fetches re-learned a SHA already in hand. One
// repository alone (sneat-co/chessraiders) held 51 worktrees and produced
// exactly one distinct answer across its 51 fetches.
func TestTargetHeadCacheFetchesOncePerRepositoryAndBase(t *testing.T) {
	cache := &targetHeadCache{entries: map[string]targetHeadEntry{}}
	calls := 0
	fetch := func() (string, error) { calls++; return "780916c29da3", nil }

	for i := 0; i < 51; i++ {
		sha, err := cache.resolve("/repos/chessraiders", "main", fetch)
		if err != nil || sha != "780916c29da3" {
			t.Fatalf("resolve %d = %q, %v", i, sha, err)
		}
	}
	if calls != 1 {
		t.Fatalf("51 worktrees in one repository issued %d fetches, want 1", calls)
	}
}

// A different base is a different question and must not be served the answer
// to the first one.
func TestTargetHeadCacheSeparatesRepositoryAndBase(t *testing.T) {
	cache := &targetHeadCache{entries: map[string]targetHeadEntry{}}
	calls := 0
	for _, tc := range []struct{ repository, base, want string }{
		{"/repos/a", "main", "aaa"},
		{"/repos/a", "release", "bbb"},
		{"/repos/b", "main", "ccc"},
		{"/repos/a", "main", "aaa"},
		{"/repos/a", "release", "bbb"},
		{"/repos/b", "main", "ccc"},
	} {
		want := tc.want
		sha, err := cache.resolve(tc.repository, tc.base, func() (string, error) {
			calls++
			return want, nil
		})
		if err != nil || sha != tc.want {
			t.Fatalf("resolve(%s, %s) = %q, %v; want %q", tc.repository, tc.base, sha, err, tc.want)
		}
	}
	if calls != 3 {
		t.Fatalf("three distinct (repository, base) pairs issued %d fetches, want 3", calls)
	}
}

// An unreachable remote is a property of the repository, not of whichever
// worktree asked first, so the fleet pays for it once rather than once per
// task — which is exactly the hang that cost a live sweep 38 minutes.
func TestTargetHeadCacheMemoisesFailureSoOneBadRemoteCostsOneAttempt(t *testing.T) {
	cache := &targetHeadCache{entries: map[string]targetHeadEntry{}}
	unreachable := errors.New("fetch exact origin/main target: connection timed out")
	calls := 0
	for i := 0; i < 20; i++ {
		_, err := cache.resolve("/repos/sneat-libs", "main", func() (string, error) {
			calls++
			return "", unreachable
		})
		if !errors.Is(err, unreachable) {
			t.Fatalf("resolve %d returned %v, want the memoised failure", i, err)
		}
	}
	if calls != 1 {
		t.Fatalf("20 worktrees behind one unreachable remote issued %d attempts, want 1", calls)
	}
}

// The safety property this cache must never weaken: a context with no cache
// installed always performs a live fetch. preflightCleanupRepository re-inspects
// on the caller's own context precisely so the pre-deletion recheck is real.
func TestNoCacheInstalledMeansNoMemoisation(t *testing.T) {
	if targetHeadCacheFrom(context.Background()) != nil {
		t.Fatal("a bare context must carry no target cache")
	}
	if targetHeadCacheFrom(withTargetHeadCache(context.Background())) == nil {
		t.Fatal("an installed cache must be retrievable")
	}
}
