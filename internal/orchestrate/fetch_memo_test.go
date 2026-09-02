package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestFetchMemoMemoizesOnlyUntouchedRepositories(t *testing.T) {
	t.Parallel()
	memo := NewFetchMemo()
	if memo.SkipFetch("/clones/acme/app") {
		t.Fatal("an unfetched clone must not be skipped")
	}
	memo.MarkFetched("/clones/acme/app")
	if !memo.SkipFetch("/clones/acme/app") {
		t.Fatal("a fetched, untouched clone must be skipped")
	}
	if memo.SkipFetch("/clones/acme/other") {
		t.Fatal("memoization must be per-clone")
	}
	memo.MarkTouched("/clones/acme/app")
	if memo.SkipFetch("/clones/acme/app") {
		t.Fatal("a touched clone must be refetched")
	}
	// Permanently un-memoizable: even a later completed fetch must not
	// re-enable skipping for a clone this run has written to — the next
	// write can again be a server-side merge the memo cannot observe.
	memo.MarkFetched("/clones/acme/app")
	if memo.SkipFetch("/clones/acme/app") {
		t.Fatal("a touched clone must stay un-memoizable for the rest of the run")
	}
	if memo.Skips() != 1 {
		t.Fatalf("skips = %d, want exactly the one granted skip", memo.Skips())
	}
}

func TestFetchMemoTouchBeforeFirstFetchStaysUnmemoizable(t *testing.T) {
	t.Parallel()
	memo := NewFetchMemo()
	memo.MarkTouched("/clones/acme/app")
	memo.MarkFetched("/clones/acme/app")
	if memo.SkipFetch("/clones/acme/app") {
		t.Fatal("touch order must not matter: touched-then-fetched stays un-memoizable")
	}
}

// TestFetchMemoExpiresEntriesAfterMaxAge pins the bounded staleness window: a
// memoized fetch older than FetchMemoMaxAge no longer stands in for a new
// one, and a completed re-fetch re-arms the memo with a fresh age.
func TestFetchMemoExpiresEntriesAfterMaxAge(t *testing.T) {
	t.Parallel()
	memo := NewFetchMemo()
	current := time.Now()
	memo.now = func() time.Time { return current }
	memo.MarkFetched("/clones/acme/app")
	current = current.Add(FetchMemoMaxAge)
	if !memo.SkipFetch("/clones/acme/app") {
		t.Fatal("a fetch exactly at the age bound must still be reusable")
	}
	current = current.Add(time.Second)
	if memo.SkipFetch("/clones/acme/app") {
		t.Fatal("a fetch older than FetchMemoMaxAge must not be reused")
	}
	memo.MarkFetched("/clones/acme/app")
	if !memo.SkipFetch("/clones/acme/app") {
		t.Fatal("a completed re-fetch must re-arm the memo")
	}
}

func TestNilFetchMemoDisablesMemoization(t *testing.T) {
	t.Parallel()
	var memo *FetchMemo
	memo.MarkFetched("/clones/acme/app")
	memo.MarkTouched("/clones/acme/app")
	if memo.SkipFetch("/clones/acme/app") {
		t.Fatal("a nil memo must never skip a fetch")
	}
	if memo.Skips() != 0 {
		t.Fatal("a nil memo must report zero skips")
	}
}

// TestFetchMemoIsSafeUnderConcurrentUse hammers every memo operation from
// concurrent goroutines over overlapping keys. It asserts nothing beyond
// termination: its job is to give the CI -race shard a genuinely concurrent
// workload over the memo, which production only exercises at discovery
// parallelism.
func TestFetchMemoIsSafeUnderConcurrentUse(t *testing.T) {
	t.Parallel()
	memo := NewFetchMemo()
	keys := []string{"/clones/a", "/clones/b", "/clones/c", "/clones/d"}
	var group sync.WaitGroup
	for worker := range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			for iteration := range 500 {
				key := keys[(worker+iteration)%len(keys)]
				memo.MarkFetched(key)
				memo.SkipFetch(key)
				if iteration%3 == 0 {
					memo.MarkTouched(key)
				}
				memo.Skips()
			}
		}()
	}
	group.Wait()
}

// TestEnsureCanonicalSkipsRefetchOnlyForMemoizedUntouchedRepository proves the
// discovery-lifecycle skip and the touched-repository refetch without shims:
// after the first memoized fetch the bare remote is removed, so a second
// discovery EnsureCanonical can only succeed if it genuinely skipped `git
// fetch origin` — and after MarkTouched it must fail because the refetch is
// genuinely attempted again.
func TestEnsureCanonicalSkipsRefetchOnlyForMemoizedUntouchedRepository(t *testing.T) {
	fixture := newEngineFixture(t)
	memo := NewFetchMemo()
	discovery := Options{GitHubDir: fixture.githubDir, Ref: "main", Timeout: time.Minute, FetchMemo: memo, FetchMemoDiscovery: true}
	if _, err := EnsureCanonical(context.Background(), fixture.repository, fixture.canonical, discovery); err != nil {
		t.Fatalf("first EnsureCanonical must fetch and memoize: %v", err)
	}
	if !memo.SkipFetch(fixture.canonical) {
		t.Fatal("first EnsureCanonical did not memoize the completed fetch")
	}
	// Remove the bare remote: any further real fetch now fails loudly.
	if err := os.RemoveAll(fixture.repository.CloneURL); err != nil {
		t.Fatal(err)
	}
	resolved, err := EnsureCanonical(context.Background(), fixture.repository, fixture.canonical, discovery)
	if err != nil {
		t.Fatalf("memoized discovery EnsureCanonical must skip the origin fetch entirely: %v", err)
	}
	if resolved.Ref != "main" || resolved.Fallback {
		t.Fatalf("resolved = %+v, want ref=main fallback=false", resolved)
	}
	memo.MarkTouched(fixture.canonical)
	if _, err := EnsureCanonical(context.Background(), fixture.repository, fixture.canonical, discovery); err == nil {
		t.Fatal("a touched repository must be refetched, and the removed remote must make that refetch fail")
	}
}

// TestEnsureCanonicalMutationLifecycleAlwaysFetchesDespiteMemo pins the
// mutation-base freshness rule: a lifecycle WITHOUT FetchMemoDiscovery — the
// wave engine's processRepository — must fetch unconditionally even when the
// shared memo holds a fresh entry, so a branch base can never be a snapshot
// up to one discovery pass old.
func TestEnsureCanonicalMutationLifecycleAlwaysFetchesDespiteMemo(t *testing.T) {
	fixture := newEngineFixture(t)
	memo := NewFetchMemo()
	discovery := Options{GitHubDir: fixture.githubDir, Ref: "main", Timeout: time.Minute, FetchMemo: memo, FetchMemoDiscovery: true}
	if _, err := EnsureCanonical(context.Background(), fixture.repository, fixture.canonical, discovery); err != nil {
		t.Fatal(err)
	}
	if !memo.SkipFetch(fixture.canonical) {
		t.Fatal("discovery fetch was not memoized")
	}
	if err := os.RemoveAll(fixture.repository.CloneURL); err != nil {
		t.Fatal(err)
	}
	mutation := discovery
	mutation.FetchMemoDiscovery = false
	if _, err := EnsureCanonical(context.Background(), fixture.repository, fixture.canonical, mutation); err == nil {
		t.Fatal("a mutation lifecycle must fetch unconditionally; the removed remote must make that fetch fail")
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

// TestRunWithSharedFetchMemoInvalidatesPullRequestRepository is the
// wave-level hook coverage the memo's safety story rests on: a repository a
// PR-publishing (non-merging) run touched must come out of the shared memo
// un-memoizable, so a later discovery pass genuinely fetches it again.
// Deleting the push and PR-stage MarkTouched hooks makes this test fail (the
// engine's own MarkFetched would otherwise re-memoize the clone), so the
// hooks cannot silently regress.
func TestRunWithSharedFetchMemoInvalidatesPullRequestRepository(t *testing.T) {
	fixture := newEngineFixture(t)
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeEngineFile(t, filepath.Join(bin, "gh"), `#!/bin/sh
if [ "$1" = pr ] && [ "$2" = list ]; then exit 0; fi
if [ "$1" = pr ] && [ "$2" = create ]; then echo 'https://github.test/acme/app/pull/1'; exit 0; fi
echo "unexpected gh args: $*" >&2
exit 2
`)
	if err := os.Chmod(filepath.Join(bin, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	memo := NewFetchMemo()
	discovery := Options{GitHubDir: fixture.githubDir, Ref: "main", Timeout: time.Minute, FetchMemo: memo, FetchMemoDiscovery: true}
	if _, err := EnsureCanonical(context.Background(), fixture.repository, fixture.canonical, discovery); err != nil {
		t.Fatal(err)
	}
	if !memo.SkipFetch(fixture.canonical) {
		t.Fatal("discovery fetch was not memoized")
	}
	options := fixture.options()
	options.PR = true
	options.FetchMemo = memo
	results, err := Run(context.Background(), []Repository{fixture.repository}, textHandler{}, options)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Status != "pr_open" || results[0].PR == "" {
		t.Fatalf("result = %+v, want an opened pull request", results[0])
	}
	if memo.SkipFetch(fixture.canonical) {
		t.Fatal("a repository this run opened a PR for must be un-memoizable for the rest of the run")
	}
	// Behavioral proof, not just state: a later discovery pass genuinely
	// attempts the fetch again, so removing the remote makes it fail.
	if err := os.RemoveAll(fixture.repository.CloneURL); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureCanonical(context.Background(), fixture.repository, fixture.canonical, discovery); err == nil {
		t.Fatal("post-PR discovery must refetch the touched repository")
	}
}

// TestRunMarksRepositoryTouchedEvenWhenPushFails pins the stated fail-safe
// direction of the push hook: invalidation happens BEFORE the push attempt,
// so a push the remote rejected (leaving origin state ambiguous from this
// run's perspective) still permanently disqualifies the clone from the memo.
func TestRunMarksRepositoryTouchedEvenWhenPushFails(t *testing.T) {
	fixture := newEngineFixture(t)
	memo := NewFetchMemo()
	discovery := Options{GitHubDir: fixture.githubDir, Ref: "main", Timeout: time.Minute, FetchMemo: memo, FetchMemoDiscovery: true}
	if _, err := EnsureCanonical(context.Background(), fixture.repository, fixture.canonical, discovery); err != nil {
		t.Fatal(err)
	}
	if !memo.SkipFetch(fixture.canonical) {
		t.Fatal("discovery fetch was not memoized")
	}
	// Make the bare remote reject every push while keeping fetches healthy.
	hook := filepath.Join(fixture.repository.CloneURL, "hooks", "pre-receive")
	writeEngineFile(t, hook, "#!/bin/sh\necho 'pushes are rejected by this test remote' >&2\nexit 1\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	options := fixture.options()
	options.Push = true
	options.FetchMemo = memo
	results, err := Run(context.Background(), []Repository{fixture.repository}, textHandler{}, options)
	if err == nil || results[0].Status != "failed" {
		t.Fatalf("push against the rejecting remote must fail: err=%v result=%+v", err, results[0])
	}
	if memo.SkipFetch(fixture.canonical) {
		t.Fatal("a FAILED push must still permanently invalidate the repository in the memo")
	}
}
