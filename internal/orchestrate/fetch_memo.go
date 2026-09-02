package orchestrate

import "sync"

// FetchMemo memoizes `git fetch origin` per repository within one process-local
// run, so a campaign loop that alternates fleet-wide discovery and per-wave
// mutation (wb deps bump) does not pay one fetch per repository per wave for
// repositories the run itself never wrote to.
//
// The invalidation rule is deliberately "ever touched", not "wrote since the
// last fetch": once this run has pushed a branch to, opened a pull request
// for, or merged into a repository, that repository is permanently
// un-memoizable for the rest of the run. WB merges server-side with `gh pr
// merge`, so the resulting default-branch commit appears on origin with no
// local push at all — an own-writes rule keyed on observed local pushes would
// provably miss it, and a stale origin/<ref> read then either never observes
// the landed manifest (burning waves until --max-waves fails the campaign) or
// cuts a duplicate PR from the stale base. Re-fetching a touched repository on
// every subsequent EnsureCanonical costs one redundant fetch per wave for the
// handful of repositories a wave actually changed; it can never serve a stale
// read.
//
// State is process-local only: a memo lives exactly as long as the run that
// created it, is never persisted, and a fresh invocation always fetches. A
// canonical clone mutated by an external actor mid-run is out of contract,
// exactly as it is for the unmemoized engine. A nil *FetchMemo is valid and
// disables memoization entirely (every method is a no-op / false), which keeps
// every caller that does not thread a memo — deps set, worktree operations,
// all non-campaign engines — byte-identical to the pre-memo behavior.
type FetchMemo struct {
	mu      sync.Mutex
	fetched map[string]bool
	touched map[string]bool
}

// NewFetchMemo returns an empty per-run fetch memo.
func NewFetchMemo() *FetchMemo {
	return &FetchMemo{fetched: map[string]bool{}, touched: map[string]bool{}}
}

// MarkFetched records that this run completed `git fetch origin` for the
// repository. A repository already marked touched is deliberately not
// re-memoized: touched repositories stay un-memoizable for the rest of the
// run regardless of how many times they are fetched again.
func (memo *FetchMemo) MarkFetched(slug string) {
	if memo == nil {
		return
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	if !memo.touched[slug] {
		memo.fetched[slug] = true
	}
}

// MarkTouched permanently disqualifies the repository from fetch memoization
// for the rest of this run. It is called before every stage that publishes
// state for the repository — branch push, pull-request creation, and the
// server-side merge — so a failed or ambiguous publication also invalidates
// (the only cost of over-invalidating is one extra fetch).
func (memo *FetchMemo) MarkTouched(slug string) {
	if memo == nil {
		return
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	memo.touched[slug] = true
	delete(memo.fetched, slug)
}

// SkipFetch reports whether this run may reuse its previous fetch of the
// repository: it was fetched at least once this run and has never been
// touched. A nil memo never skips.
func (memo *FetchMemo) SkipFetch(slug string) bool {
	if memo == nil {
		return false
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	return memo.fetched[slug] && !memo.touched[slug]
}
