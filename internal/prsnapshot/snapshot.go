// Package prsnapshot is one bounded, single-observation read of a pull
// request's current state and check verdict: no poll loop, no confirming
// reread, no target-branch strict-freshness fence. It exists so `wb wait
// pr`'s own per-poll observation (cmd/wb/wait.go's former observePullRequest)
// and the herdr-session-transport daemon watcher (internal/prwatch) share
// exactly one implementation of "what does this pull request look like right
// now" — both already needed the identical renamed-required-check-aware logic
// (orchestrate.ObservePullRequestHead),
// and a caller keying a decision on that verdict must see the same one
// whichever command took the observation.
//
// It never enforces the strict-freshness / candidate-contains-target fence
// orchestrate.WaitForPullRequestChecks (the merge-oriented logic behind `wb
// ci wait` and `wb pr land`) enforces: neither caller here merges anything,
// so a target branch simply advancing past this pull request's base is not
// this package's concern (herdr-session-transport Plan Task 6 review round
// 2, point 3).
package prsnapshot

import (
	"context"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/githubchecks"
)

// Snapshot is one observation of a pull request's current state: GitHub's
// own state/merged fact first, then — only while the pull request is still
// open — its head's check verdict. Err is set when the observation itself
// failed (a GitHub read error); every other field is then zero and must not
// be read.
type Snapshot struct {
	Repository string
	Number     string
	// State is GitHub's raw pull-request state, exactly as the API reports
	// it: "open" or "closed". It is never rewritten to "merged" here — Merged
	// carries that fact instead, so a caller decides for itself how to
	// present the two together (cmd/wb/wait.go's waitTarget.State keeps its
	// own pre-existing "merged" overlay for backward compatibility; a new
	// caller such as internal/prwatch reads Merged directly).
	State     string
	Merged    bool
	Draft     bool
	Head      string
	Base      string
	Mergeable string
	URL       string
	// Checks and Failed are populated only while the pull request is open —
	// a closed pull request's head checks are no longer read at all.
	Checks   map[string]int
	Failed   []string
	Failures []githubchecks.CIFailureDetail
	// Blocked names a required check with no passing observation on this
	// head (orchestrate.HeadObservation.Blocked) — the renamed-workflow
	// trap. It is fetched only when nothing is pending or failed and Green
	// is still false, matching the same reserved-reads discipline the rest
	// of this observation follows.
	Blocked []string
	// Green is orchestrate.ObservePullRequestHead's own verdict: every
	// observed check passed or was skipped, AND the target's required-check
	// policy is fully satisfied. It is the one fact that decides pass vs.
	// fail; nothing in this package re-derives it from the check counts.
	Green bool
	Err   error
}

// Reads of one observation, each a conditional (ETag) read of which a 304
// answer is not charged to the rate limit. A test counts every one:
//
//   - ReadsPerObservation: an open pull request, green, red or blocked: the pull
//     request, its check runs, its Actions workflow runs, its commit statuses, the
//     target's branch policy and its active rules. Naming a blocked required check
//     reuses these reads.
//   - ReadsPerInactiveObservation: a merged or closed pull request, lean only:
//     the pull request alone, since the checks of a head that is no longer open
//     are not read. Observe reads them as before.
//
// Observe adds to the first the reads that explain a red head (see ObserveLean).
const (
	ReadsPerObservation         = 6
	ReadsPerInactiveObservation = 1
)

// Observe takes exactly one observation of repository's pull request
// selector (a number or URL, as orchestrate.ReadPullRequest accepts it) — an
// exact head's checks, never a poll loop, never a confirming reread. A caller
// that needs a stable, confirmed terminal verdict takes a second observation
// itself, on its own outer cadence (`wb wait pr`'s bounded poll slice, or the
// daemon watcher's next tick), and compares the two; this package holds no
// state across calls.
func Observe(ctx context.Context, repository, selector string) Snapshot {
	return observe(ctx, repository, selector, false)
}

// ObserveLean is Observe without the failure details: Failures stays empty. A
// caller that shows only that a check failed and its name (the cockpit fleet)
// saves the pull request, check, status and annotation reads that explaining a
// red head costs.
func ObserveLean(ctx context.Context, repository, selector string) Snapshot {
	return observe(ctx, repository, selector, true)
}

func observe(ctx context.Context, repository, selector string, lean bool) Snapshot {
	snapshot := Snapshot{Repository: repository, Number: selector}
	view, err := githubchecks.ReadPullRequest(ctx, repository, selector)
	if err != nil {
		snapshot.Err = err
		return snapshot
	}
	snapshot.State = view.State
	snapshot.Merged = view.Merged
	snapshot.Draft = view.Draft
	snapshot.Head = view.Head.SHA
	snapshot.Base = view.Base.Ref
	snapshot.URL = view.HTMLURL
	snapshot.Mergeable = view.MergeableState

	// The Cockpit's lean observation does not read the checks of a head that is
	// no longer open: reporting the merge or the closure costs the one read
	// already made. Observe still reads them, as `wb wait pr` always has.
	inactive := snapshot.State != "" && !strings.EqualFold(snapshot.State, "open")
	if lean && inactive {
		snapshot.Checks = map[string]int{}
		return snapshot
	}
	observation, err := githubchecks.ObservePullRequestHead(ctx, repository, view)
	if err != nil {
		// A closed pull request no longer needs its checks read; reporting
		// the closure is more useful than failing on a head that may be gone.
		if inactive {
			snapshot.Checks = map[string]int{}
			return snapshot
		}
		snapshot.Err = err
		return snapshot
	}
	snapshot.Green = observation.Green
	snapshot.Checks = map[string]int{}
	for _, check := range observation.Checks {
		snapshot.Checks[check.Bucket]++
		if check.Bucket == "fail" || check.Bucket == "cancel" {
			snapshot.Failed = append(snapshot.Failed, check.Name)
		}
	}
	sort.Strings(snapshot.Failed)
	// A required check that nobody produces is ABSENT from the observed set,
	// not pending in it — the renamed-workflow trap. Counting pending checks
	// alone would call that head settled and green when it can never merge,
	// so Green is what decides, and the gap is named here only for a caller
	// that wants to explain why. It comes from the reads already made.
	if !observation.Green && snapshot.Checks["pending"] == 0 && len(snapshot.Failed) == 0 {
		snapshot.Blocked = observation.Blocked
	}
	// Why it is red is only fetched once the head is terminal, and only when
	// something actually failed. Annotations cost extra reads, and a check
	// that is still running has nothing to explain yet.
	if !lean && len(snapshot.Failed) > 0 && snapshot.Checks["pending"] == 0 {
		if failures, failErr := githubchecks.PullRequestFailureDetails(ctx, repository, selector); failErr == nil {
			snapshot.Failures = failures
		}
	}
	return snapshot
}
