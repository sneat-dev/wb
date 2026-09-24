// Package prwatch is the daemon's watcher for herdr-session-transport
// (spec/plans/herdr-session-transport.md, Task 6): it discovers pull
// requests worth watching only through
// worktrees.ListRegisteredPullRequestBindings — the first reader of the
// binding `wb pr create` already records via
// worktrees.RecordClaimPullRequestBinding — and evaluates each one's current
// state through prsnapshot.Observe, the same renamed-required-check-aware
// logic `wb wait pr` and `wb ci wait` already use. It never scans GitHub for
// pull requests WB has no recorded binding for, and it never reimplements
// check-state interpretation.
//
// Each tick takes exactly one observation per registered binding — never a
// poll loop, never a foreground wait — and a Watcher remembers the previous
// tick's classification and head per binding so a checks verdict becomes
// Terminal only once two consecutive ticks agree: the caller's own outer
// cadence supplies the confirming reread `wb ci wait` would otherwise take
// inside one bounded call. A merged or closed pull request needs no such
// confirmation — GitHub's own state is immediately authoritative — and is
// Terminal on its first observation.
//
// This package produces Outcomes only; it does not decide what to do with
// one. Resolving the task's current session and transport identity, the
// at-most-once delivery-intent coalescing, and the record-only/advisory/
// submit decision belong to herdr-session-transport's Task 7, which consumes
// the Outcomes this package produces.
package prwatch

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/prsnapshot"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// Kind classifies one evaluated Outcome so a caller keys its at-most-once
// delivery intent (task, pull request, head, Kind — herdr-session-transport
// Plan Task 7) on a closed set of values, never on parsed Reason text.
type Kind string

const (
	// KindChecksPassed is every observed GitHub check green and the target's
	// required-check policy fully satisfied (prsnapshot.Snapshot.Green),
	// confirmed by two consecutive identical ticks.
	KindChecksPassed Kind = "checks-passed"
	// KindChecksFailed is at least one failed or cancelled GitHub check,
	// confirmed by two consecutive identical ticks.
	KindChecksFailed Kind = "checks-failed"
	// KindChecksPending is an open pull request whose checks have not yet
	// reached a verdict: something is still running, or a required check has
	// not registered at all (prsnapshot.Snapshot.Blocked) — the
	// renamed-workflow trap. Never Terminal.
	KindChecksPending Kind = "checks-pending"
	// KindMerged is GitHub's own merged fact. Terminal on the first
	// observation: nothing about a merge needs a confirming reread.
	KindMerged Kind = "merged"
	// KindClosed is a pull request GitHub reports closed without merging.
	// Terminal on the first observation, for the same reason as KindMerged.
	KindClosed Kind = "closed"
	// KindHeadDrift is an open pull request whose head SHA changed since the
	// Watcher's previous tick for this binding. It overrides whatever the
	// checks verdict for the new head would otherwise have been: a streak
	// toward Terminal must not silently carry over past a new push, and the
	// two-observation count starts over from this tick's real classification.
	// Never Terminal.
	KindHeadDrift Kind = "head-drift"
	// KindUnavailable is an observation that failed for an operational
	// reason (a GitHub read error, transient or not) rather than describing
	// pull-request state at all. Never Terminal; the next tick simply tries
	// again. It never counts as a break in an in-progress checks streak —
	// the Watcher's memory of the last real observation is left untouched.
	KindUnavailable Kind = "unavailable"
)

// Outcome is one evaluated registered pull request, at one tick.
type Outcome struct {
	Task        string
	ClaimID     string
	Repository  string
	PullRequest int
	URL         string
	// Head is the pull request's exact head SHA observed at this tick — part
	// of Task 7's at-most-once delivery-intent key, alongside Task,
	// PullRequest, and Kind. Empty when Kind is KindUnavailable.
	Head string
	// Target is the pull request's exact base branch observed at this tick.
	Target string
	Kind   Kind
	// Terminal is true exactly when this Outcome is ready to act on. See the
	// Kind constants for which values can ever be Terminal.
	Terminal bool
	Reason   string
	// Checks, Failed, Failures, and Blocked are copied from the snapshot
	// unchanged, so Task 8's facts-only template can name a required check
	// (or a count) without re-deriving it from GitHub.
	Checks      map[string]int
	Failed      []string
	Failures    []orchestrate.CIFailureDetail
	Blocked     []string
	EvaluatedAt time.Time
}

// tickMemory is what a Watcher remembers about one binding's previous real
// (non-KindUnavailable) observation, so the current tick can tell a
// confirming reread from a head that moved out from under it.
type tickMemory struct {
	Kind Kind
	Head string
}

// Watcher is the daemon's own PR-outcome watcher. It remembers each
// registered binding's previous tick's classification and head so a checks
// verdict becomes Terminal only once two consecutive ticks agree, and so a
// head that changed between ticks is reported honestly as KindHeadDrift
// instead of silently restarting the confirmation count on an unconfirmed
// new observation. It holds no durable state across process restarts: a
// restarted daemon simply starts its two-observation count over, which is
// safe because starting over only delays a Terminal verdict by one tick, it
// never fabricates one.
type Watcher struct {
	mu   sync.Mutex
	seen map[string]tickMemory
	// Now stamps Outcome.EvaluatedAt. Tests inject a fixed function so they
	// never depend on a real clock; Evaluate itself takes exactly one
	// observation and makes no timing decision of its own — there is no
	// timer to inject because there is nothing here that waits.
	Now func() time.Time
}

// NewWatcher returns a Watcher with no remembered ticks.
func NewWatcher() *Watcher {
	return &Watcher{seen: map[string]tickMemory{}, Now: func() time.Time { return time.Now().UTC() }}
}

// bindingKey identifies a watched pull request by task, repository, and pull
// request number — never by claim ID (round 3 review, minor 1). A claim ID
// can change under a binding that still names the same task and the same
// pull request (a successor claim after `/move`, or a re-recorded binding);
// keying on it would silently reset an in-progress two-observation streak on
// every such change even though nothing about the watched pull request
// itself moved. Task-scoping still keeps two different tasks that happen to
// share a pull request (a rare but possible fan-in) from colliding.
func bindingKey(binding worktrees.RegisteredPullRequestBinding) string {
	return binding.Task + "\x00" + binding.Repository + "\x00" + strconv.Itoa(binding.PullRequest)
}

// Evaluate takes exactly one observation of binding's pull request through
// prsnapshot.Observe and classifies it, comparing it against this Watcher's
// memory of binding's previous tick. It never polls, waits, or takes a
// second observation itself: the caller's own outer tick cadence supplies
// the second observation Terminal's two-observation rule needs for a checks
// verdict.
func (w *Watcher) Evaluate(ctx context.Context, binding worktrees.RegisteredPullRequestBinding) (Outcome, error) {
	repository := strings.TrimSpace(binding.Repository)
	if repository == "" || binding.PullRequest <= 0 {
		return Outcome{}, fmt.Errorf("registered pull-request binding for task %q claim %q is missing a repository or pull-request number", binding.Task, binding.ClaimID)
	}
	number := strconv.Itoa(binding.PullRequest)
	snapshot := prsnapshot.Observe(ctx, repository, number)

	now := time.Now().UTC()
	if w.Now != nil {
		now = w.Now()
	}
	outcome := Outcome{
		Task: binding.Task, ClaimID: binding.ClaimID, Repository: repository,
		PullRequest: binding.PullRequest, URL: binding.URL,
		Head: snapshot.Head, Target: snapshot.Base,
		Checks: snapshot.Checks, Failed: snapshot.Failed, Failures: snapshot.Failures, Blocked: snapshot.Blocked,
		EvaluatedAt: now,
	}

	if snapshot.Err != nil {
		// An operational read failure describes nothing about pull-request
		// state, so it must never overwrite the Watcher's memory of the last
		// real observation — doing so would let a transient GitHub blip look
		// like a head drift, or reset an in-progress checks streak, on the
		// very next successful tick.
		outcome.Kind = KindUnavailable
		outcome.Reason = snapshot.Err.Error()
		return outcome, nil
	}

	// GitHub's own merged/closed fact decides first and is immediately
	// authoritative: neither condition can flip back to "checks in
	// progress", so neither needs a confirming reread, and a merged or
	// closed pull request must never be reported through the checks
	// classification below (herdr-session-transport Plan Task 6 review round
	// 2, point 2 — this must never surface as checks-failed).
	switch {
	case snapshot.Merged:
		outcome.Kind = KindMerged
		outcome.Terminal = true
		outcome.Reason = "pull request merged"
		w.remember(binding, outcome.Kind, outcome.Head)
		return outcome, nil
	case snapshot.State != "" && !strings.EqualFold(snapshot.State, "open"):
		outcome.Kind = KindClosed
		outcome.Terminal = true
		outcome.Reason = "pull request closed without merging"
		w.remember(binding, outcome.Kind, outcome.Head)
		return outcome, nil
	}

	rawKind, reason := classifyChecks(snapshot)

	key := bindingKey(binding)
	w.mu.Lock()
	previous, seenBefore := w.seen[key]
	w.mu.Unlock()

	reportKind := rawKind
	reportReason := reason
	switch {
	case seenBefore && previous.Head != snapshot.Head:
		// The head moved since the last tick: whatever streak was building
		// no longer describes this commit. Report the drift itself rather
		// than silently starting an unconfirmed pass/fail verdict on the new
		// head — the next tick's comparison is against rawKind/snapshot.Head
		// below (via remember), so a real second observation still confirms
		// normally one tick later.
		reportKind = KindHeadDrift
		reportReason = fmt.Sprintf("head moved from %s to %s since the previous tick", previous.Head, snapshot.Head)
	case rawKind == KindChecksPassed || rawKind == KindChecksFailed:
		outcome.Terminal = seenBefore && previous.Kind == rawKind && previous.Head == snapshot.Head
	}
	outcome.Kind = reportKind
	outcome.Reason = reportReason
	w.remember(binding, rawKind, snapshot.Head)
	return outcome, nil
}

// classifyChecks maps an open, unmerged pull request's snapshot onto a
// checks Kind. The pull request's own required checks decide pass or fail —
// Green already is that verdict (prsnapshot.Snapshot.Green) — so nothing
// here re-derives it from a target-branch freshness fence or a
// candidate-contains-target ancestry check: this watcher never merges, so a
// target simply advancing past this pull request's base is not its concern
// (herdr-session-transport Plan Task 6 review round 2, point 3).
func classifyChecks(snapshot prsnapshot.Snapshot) (Kind, string) {
	if len(snapshot.Failed) > 0 {
		return KindChecksFailed, fmt.Sprintf("%d GitHub check(s) failed or were cancelled: %s", len(snapshot.Failed), strings.Join(snapshot.Failed, ", "))
	}
	if snapshot.Green {
		return KindChecksPassed, "every observed GitHub check passed and the target's required-check policy is satisfied"
	}
	if snapshot.Checks["pending"] > 0 {
		return KindChecksPending, fmt.Sprintf("%d GitHub check(s) still running", snapshot.Checks["pending"])
	}
	if len(snapshot.Blocked) > 0 {
		return KindChecksPending, "required check(s) have not registered for this head: " + strings.Join(snapshot.Blocked, ", ")
	}
	return KindChecksPending, "no GitHub checks have registered for this head yet"
}

func (w *Watcher) remember(binding worktrees.RegisteredPullRequestBinding, kind Kind, head string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.seen == nil {
		// A zero-value Watcher{} (round 3 review, minor 3) has never gone
		// through NewWatcher, so seen is still nil: writing to it directly
		// here, rather than only in NewWatcher, is what makes the zero value
		// usable at all instead of panicking on its first remembered tick.
		w.seen = map[string]tickMemory{}
	}
	w.seen[bindingKey(binding)] = tickMemory{Kind: kind, Head: head}
}

// PollResult pairs one registered binding with the Outcome its evaluation
// produced, or the error that evaluation hit. One unreadable or unreachable
// pull request never stops Tick from evaluating the rest. Err is reserved for
// a binding-level problem (a malformed binding); an ordinary GitHub read
// failure is reported as an Outcome with Kind KindUnavailable, never as Err.
type PollResult struct {
	Binding worktrees.RegisteredPullRequestBinding
	Outcome Outcome
	Err     error
}

// Tick runs one evaluation pass — one observation per binding, no poll loop
// of its own — over every pull request WB has a recorded binding for:
// worktrees.ListRegisteredPullRequestBindings, never a fleet-wide GitHub
// scan. The caller decides the outer cadence; Tick itself runs exactly one
// pass and returns. Call Tick repeatedly on the same Watcher so its
// two-observation rule (see the Kind constants) can confirm a checks verdict
// across calls.
func (w *Watcher) Tick(ctx context.Context, projectsRoot string) ([]PollResult, error) {
	bindings, err := worktrees.ListRegisteredPullRequestBindings(projectsRoot)
	if err != nil {
		return nil, fmt.Errorf("list registered pull-request bindings: %w", err)
	}
	// Pruning (round 3 review, minor 2): a binding this tick never lists at
	// all — the claim finished, moved on, or lost its binding — has no
	// future tick to confirm or drift against, so its memory would only ever
	// grow the map. A merged or closed entry is pruned once its terminal
	// outcome has been emitted below, for the same reason: neither
	// classification depends on remembered state (see Evaluate), so nothing
	// is lost by forgetting it, and the daemon may still be polling a
	// binding whose claim has not yet been sealed.
	listed := make(map[string]bool, len(bindings))
	for _, binding := range bindings {
		listed[bindingKey(binding)] = true
	}
	w.pruneUnlisted(listed)

	results := make([]PollResult, 0, len(bindings))
	for _, binding := range bindings {
		outcome, evalErr := w.Evaluate(ctx, binding)
		results = append(results, PollResult{Binding: binding, Outcome: outcome, Err: evalErr})
		if outcome.Kind == KindMerged || outcome.Kind == KindClosed {
			w.forget(binding)
		}
	}
	return results, nil
}

// pruneUnlisted drops every remembered key not present in listed.
func (w *Watcher) pruneUnlisted(listed map[string]bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for key := range w.seen {
		if !listed[key] {
			delete(w.seen, key)
		}
	}
}

// forget drops binding's remembered tick, if any.
func (w *Watcher) forget(binding worktrees.RegisteredPullRequestBinding) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.seen != nil {
		delete(w.seen, bindingKey(binding))
	}
}
