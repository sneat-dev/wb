package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFetchMemoMemoizesOnlyUntouchedRepositories(t *testing.T) {
	t.Parallel()
	memo := NewFetchMemo()
	if memo.SkipFetch("acme/app") {
		t.Fatal("an unfetched repository must not be skipped")
	}
	memo.MarkFetched("acme/app")
	if !memo.SkipFetch("acme/app") {
		t.Fatal("a fetched, untouched repository must be skipped")
	}
	if memo.SkipFetch("acme/other") {
		t.Fatal("memoization must be per-repository")
	}
	memo.MarkTouched("acme/app")
	if memo.SkipFetch("acme/app") {
		t.Fatal("a touched repository must be refetched")
	}
	// Permanently un-memoizable: even a later completed fetch must not
	// re-enable skipping for a repository this run has written to — the next
	// write can again be a server-side merge the memo cannot observe.
	memo.MarkFetched("acme/app")
	if memo.SkipFetch("acme/app") {
		t.Fatal("a touched repository must stay un-memoizable for the rest of the run")
	}
}

func TestFetchMemoTouchBeforeFirstFetchStaysUnmemoizable(t *testing.T) {
	t.Parallel()
	memo := NewFetchMemo()
	memo.MarkTouched("acme/app")
	memo.MarkFetched("acme/app")
	if memo.SkipFetch("acme/app") {
		t.Fatal("touch order must not matter: touched-then-fetched stays un-memoizable")
	}
}

func TestNilFetchMemoDisablesMemoization(t *testing.T) {
	t.Parallel()
	var memo *FetchMemo
	memo.MarkFetched("acme/app")
	memo.MarkTouched("acme/app")
	if memo.SkipFetch("acme/app") {
		t.Fatal("a nil memo must never skip a fetch")
	}
}

// TestEnsureCanonicalSkipsRefetchOnlyForMemoizedUntouchedRepository proves the
// memoized skip and the touched-repository refetch without shims: after the
// first memoized fetch the bare remote is removed, so a second EnsureCanonical
// can only succeed if it genuinely skipped `git fetch origin` — and after
// MarkTouched it must fail because the refetch is genuinely attempted again.
func TestEnsureCanonicalSkipsRefetchOnlyForMemoizedUntouchedRepository(t *testing.T) {
	fixture := newEngineFixture(t)
	memo := NewFetchMemo()
	options := Options{GitHubDir: fixture.githubDir, Ref: "main", Timeout: time.Minute, FetchMemo: memo}
	if _, err := EnsureCanonical(context.Background(), fixture.repository, fixture.canonical, options); err != nil {
		t.Fatalf("first EnsureCanonical must fetch and memoize: %v", err)
	}
	if !memo.SkipFetch(fixture.repository.Slug) {
		t.Fatal("first EnsureCanonical did not memoize the completed fetch")
	}
	// Remove the bare remote: any further real fetch now fails loudly.
	if err := os.RemoveAll(fixture.repository.CloneURL); err != nil {
		t.Fatal(err)
	}
	resolved, err := EnsureCanonical(context.Background(), fixture.repository, fixture.canonical, options)
	if err != nil {
		t.Fatalf("memoized EnsureCanonical must skip the origin fetch entirely: %v", err)
	}
	if resolved.Ref != "main" || resolved.Fallback {
		t.Fatalf("resolved = %+v, want ref=main fallback=false", resolved)
	}
	memo.MarkTouched(fixture.repository.Slug)
	if _, err := EnsureCanonical(context.Background(), fixture.repository, fixture.canonical, options); err == nil {
		t.Fatal("a touched repository must be refetched, and the removed remote must make that refetch fail")
	}
}

// TestEnsureCanonicalWithoutMemoAlwaysFetches pins the opt-out default: with
// no memo threaded (every caller today), EnsureCanonical fetches origin every
// time — the removed remote makes the very next call fail.
func TestEnsureCanonicalWithoutMemoAlwaysFetches(t *testing.T) {
	fixture := newEngineFixture(t)
	options := Options{GitHubDir: fixture.githubDir, Ref: "main", Timeout: time.Minute}
	if _, err := EnsureCanonical(context.Background(), fixture.repository, fixture.canonical, options); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(fixture.repository.CloneURL); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureCanonical(context.Background(), fixture.repository, fixture.canonical, options); err == nil {
		t.Fatal("without a memo, EnsureCanonical must fetch origin unconditionally")
	}
	if _, err := os.Stat(filepath.Join(fixture.canonical, ".git")); err != nil {
		t.Fatalf("canonical clone must remain intact: %v", err)
	}
}
