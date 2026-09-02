package orchestrate

import (
	"sync"
	"time"
)

// FetchMemoMaxAge bounds how long one memoized `git fetch origin` may stand in
// for a new one within a run. Discovery over a large fleet plus a full wave of
// CI waits can hold a campaign open for hours; without a bound, one early
// fetch of an untouched repository would be trusted for that whole span. With
// the bound, an external writer's mid-campaign push to an untouched
// repository is observed within this window at the latest, while nearly all
// of the per-wave discovery saving is kept (waves separated by less than the
// bound still skip).
const FetchMemoMaxAge = 15 * time.Minute

// FetchMemo memoizes `git fetch origin` per canonical clone within one
// process-local run, so a campaign loop that alternates fleet-wide discovery
// and per-wave mutation (wb deps bump) does not pay one fetch per repository
// per wave for repositories the run itself never wrote to.
//
// Only read-only graph discovery may consume the memo (see
// Options.FetchMemoDiscovery): the mutation engine always re-fetches before
// cutting a branch, so a wave's base ref is never a snapshot up to one
// discovery pass old. The engine still records its fetches here and drives
// the touch-invalidation below.
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
// Honest staleness window: this memo widens exposure to EXTERNAL writers. A
// teammate's merge, a sibling campaign, or a bot landing on an untouched
// repository's default branch mid-run is observed within one wave WITHOUT the
// memo, but only within FetchMemoMaxAge WITH it. That is the flag's real
// trade — which is why it is opt-in, why the age bound exists, and why the
// skill docs say not to use --fetch-cache when anything other than this run
// may land on main mid-campaign.
//
// State is process-local only: a memo lives exactly as long as the run that
// created it, is never persisted, and a fresh invocation always fetches. Keys
// are canonical clone directories (not owner/repo slugs), so two directories
// that happen to claim one slug can never satisfy each other's fetches. A nil
// *FetchMemo is valid and disables memoization entirely (every method is a
// no-op / zero), which keeps every caller that does not thread a memo — deps
// set, worktree operations, all non-campaign engines — byte-identical to the
// pre-memo behavior.
type FetchMemo struct {
	mu      sync.Mutex
	fetched map[string]time.Time
	touched map[string]bool
	skips   int
	// now is the clock SkipFetch ages entries against; tests may replace it.
	now func() time.Time
}

// NewFetchMemo returns an empty per-run fetch memo.
func NewFetchMemo() *FetchMemo {
	return &FetchMemo{fetched: map[string]time.Time{}, touched: map[string]bool{}, now: time.Now}
}

// MarkFetched records that this run completed `git fetch origin` for the
// canonical clone, refreshing its age. A clone already marked touched is
// deliberately not re-memoized: touched repositories stay un-memoizable for
// the rest of the run regardless of how many times they are fetched again.
func (memo *FetchMemo) MarkFetched(canonical string) {
	if memo == nil {
		return
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	if !memo.touched[canonical] {
		memo.fetched[canonical] = memo.now()
	}
}

// MarkTouched permanently disqualifies the canonical clone from fetch
// memoization for the rest of this run. It is called before every stage that
// publishes state for the repository — branch push, pull-request creation,
// and the server-side merge — so a failed or ambiguous publication also
// invalidates (the only cost of over-invalidating is one extra fetch).
func (memo *FetchMemo) MarkTouched(canonical string) {
	if memo == nil {
		return
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	memo.touched[canonical] = true
	delete(memo.fetched, canonical)
}

// SkipFetch reports whether a discovery pass may reuse this run's previous
// fetch of the canonical clone: it was fetched no longer than FetchMemoMaxAge
// ago and has never been touched. A nil memo never skips. Each true result is
// counted for the campaign report (see Skips).
func (memo *FetchMemo) SkipFetch(canonical string) bool {
	if memo == nil {
		return false
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	fetchedAt, fetched := memo.fetched[canonical]
	if !fetched || memo.touched[canonical] || memo.now().Sub(fetchedAt) > FetchMemoMaxAge {
		return false
	}
	memo.skips++
	return true
}

// Skips returns how many fetches this memo has skipped so far. Campaign
// reports use deltas of this counter to attribute per-wave savings.
func (memo *FetchMemo) Skips() int {
	if memo == nil {
		return 0
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	return memo.skips
}
