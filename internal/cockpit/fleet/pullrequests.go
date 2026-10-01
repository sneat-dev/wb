package fleet

import (
	"context"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/prwatch"
	"github.com/sneat-dev/wb/internal/worktrees"
)

const (
	// DefaultPullRequestLimit is the most pull requests observed per pass when
	// cockpit.pull_request_limit is not set.
	DefaultPullRequestLimit = 10
	// DefaultPullRequestHourlyBudget is the most observations in any rolling
	// hour when cockpit.pull_request_hourly_budget is not set. An observation
	// is prsnapshot.ReadsPerSettledObservation GitHub reads, 5 more for a head
	// that is neither green, pending nor failed, so the default is at most 1200
	// reads an hour; a read answered 304 by the ETag cache is not charged to
	// the rate limit.
	DefaultPullRequestHourlyBudget = 120
	// pullWindow is the rolling window the budget is counted over.
	pullWindow = time.Hour
	// pullPendingEvery is how soon a pull request still waiting for its checks
	// (or whose mergeability GitHub is still computing) is observed again, and
	// pullSettledEvery how soon one whose verdict is known.
	pullPendingEvery = 90 * time.Second
	pullSettledEvery = 10 * time.Minute
	// pullBackoffFrom and pullBackoffTo bound the wait after a failed read,
	// which doubles with each failure in a row.
	pullBackoffFrom = 2 * time.Minute
	pullBackoffTo   = 30 * time.Minute
	// defaultPullRequestTimeout bounds one pull request's observation.
	defaultPullRequestTimeout = 30 * time.Second
	// pullRequestWorkers is how many observations of one pass run at once.
	pullRequestWorkers = 4
	// maxFailedCheckText caps the failing check's name.
	maxFailedCheckText = 100
)

// PullRequestObserver is the watcher the snapshotter runs for the pull
// requests recorded locally. *prwatch.Watcher is the production one; a test
// supplies a fake, so no test reaches GitHub. Evaluate takes one observation
// of a binding; Forget and Retain drop what the watcher remembers of one
// binding and of every binding not listed.
type PullRequestObserver interface {
	Evaluate(ctx context.Context, binding worktrees.RegisteredPullRequestBinding) (prwatch.Outcome, error)
	Forget(binding worktrees.RegisteredPullRequestBinding)
	Retain(bindings []worktrees.RegisteredPullRequestBinding)
}

var _ PullRequestObserver = (*prwatch.Watcher)(nil)

// mergeableStates is the closed set of merge states a pull request entry may
// carry; any other value GitHub (or a hostile source) gives is dropped.
var mergeableStates = []string{"clean", "blocked", "dirty", "behind", "unstable", "has_hooks", "draft", "unknown"}

// pullObservation is what the last successful observation of one pull request
// said, with when it was taken. done marks a pull request merged or closed,
// which has left the watch set; waiting one whose checks have not reached a
// verdict.
type pullObservation struct {
	state, mergeable, failedCheck           string
	total, passed, failed, skipped, pending int
	green, done, waiting                    bool
	checkedAt                               time.Time
}

// pullKey identifies a pull request by its repository slug and number.
func pullKey(repository string, number int) string {
	return repository + "#" + strconv.Itoa(number)
}

// observationOf maps a watcher outcome to an observation taken at at. The
// verdict `green` is the snapshot's own, never derived from the counts; the
// counts are the snapshot's check buckets, failed and cancelled together and
// skipped apart from passed. It reports false for an outcome that describes no
// pull request state (a failed read).
func observationOf(outcome prwatch.Outcome, at time.Time) (pullObservation, bool) {
	snapshot := outcome.Snapshot
	if outcome.Kind == prwatch.KindUnavailable || snapshot.Err != nil {
		return pullObservation{}, false
	}
	observation := pullObservation{
		state: "open", green: snapshot.Green, checkedAt: at, done: outcome.Kind == prwatch.KindMerged || outcome.Kind == prwatch.KindClosed,
		passed: snapshot.Checks["pass"], failed: snapshot.Checks["fail"] + snapshot.Checks["cancel"],
		skipped: snapshot.Checks["skipping"], pending: snapshot.Checks["pending"],
	}
	switch {
	case snapshot.Merged:
		observation.state = "merged"
	case snapshot.State != "" && snapshot.State != "open":
		observation.state = "closed"
	case snapshot.Draft:
		observation.state = "draft"
	}
	for _, count := range snapshot.Checks {
		observation.total += count
	}
	if slices.Contains(mergeableStates, snapshot.Mergeable) {
		observation.mergeable = snapshot.Mergeable
	}
	if len(snapshot.Failed) > 0 {
		observation.failedCheck = plainTextMax(snapshot.Failed[0], maxFailedCheckText)
	}
	// Checks are awaited while something runs, while GitHub still computes the
	// merge state, and while an open pull request has no check at all, no
	// verdict and no required check named missing (CI has not registered yet).
	// A missing required check, a failure, a draft and a green head are settled.
	observation.waiting = observation.pending > 0 || snapshot.Mergeable == "unknown" ||
		(observation.state == "open" && observation.total == 0 && !snapshot.Green && len(snapshot.Blocked) == 0)
	return observation, true
}

// applyTo puts the observation's fields on a pull request entry.
func (o pullObservation) applyTo(pull *PullRequest) {
	pull.State, pull.Mergeable, pull.FailedCheck, pull.CheckedAt = o.state, o.mergeable, o.failedCheck, o.checkedAt
	pull.ChecksTotal, pull.ChecksPassed, pull.ChecksFailed, pull.ChecksSkipped, pull.ChecksPending = &o.total, &o.passed, &o.failed, &o.skipped, &o.pending
	pull.ChecksGreen = &o.green
}

// pullAttempt is what the snapshotter remembers of the last time it asked
// about one pull request: when, and how many reads in a row have failed. The
// zero value is a pull request not yet asked about.
type pullAttempt struct {
	at       time.Time
	failures int
}

// pullBackoff is the wait after failures failed reads in a row: two minutes,
// doubling, at most thirty.
func pullBackoff(failures int) time.Duration {
	wait := pullBackoffFrom
	for range failures - 1 {
		wait *= 2
		if wait >= pullBackoffTo {
			return pullBackoffTo
		}
	}
	return wait
}

// pullDue is when a pull request is next to be observed, and whether it is
// watched at all. It is a pure function of the history: a pull request never
// asked about is due at once; after a failed read it is due after the backoff;
// otherwise after 90 seconds while its checks are awaited and 10 minutes once
// they are settled; a merged or closed one is not watched.
func pullDue(observation pullObservation, observed bool, attempt pullAttempt) (due time.Time, watched bool) {
	switch {
	case observed && observation.done:
		return time.Time{}, false
	case attempt.at.IsZero():
		return time.Time{}, true
	case attempt.failures > 0:
		return attempt.at.Add(pullBackoff(attempt.failures)), true
	case !observed || observation.waiting:
		return attempt.at.Add(pullPendingEvery), true
	}
	return attempt.at.Add(pullSettledEvery), true
}

// pullBatch picks the bindings one pass observes at now: those watched and due,
// one for each pull request, the oldest due first, at most limit of them and at
// most allowed (what the hourly budget leaves). It also says whether the budget
// cut the batch short, which is what throttled means.
func pullBatch(bindings []worktrees.RegisteredPullRequestBinding, observed map[string]pullObservation, attempts map[string]pullAttempt, now time.Time, limit, allowed int) (batch []worktrees.RegisteredPullRequestBinding, throttled bool) {
	seen := map[string]bool{}
	dues := map[string]time.Time{}
	var due []worktrees.RegisteredPullRequestBinding
	for _, binding := range bindings {
		key := pullKey(binding.Repository, binding.PullRequest)
		if binding.Repository == "" || binding.PullRequest <= 0 || seen[key] {
			continue
		}
		seen[key] = true
		observation, ok := observed[key]
		when, watched := pullDue(observation, ok, attempts[key])
		if watched && !when.After(now) {
			dues[key] = when
			due = append(due, binding)
		}
	}
	sort.SliceStable(due, func(i, j int) bool {
		return dues[pullKey(due[i].Repository, due[i].PullRequest)].Before(dues[pullKey(due[j].Repository, due[j].PullRequest)])
	})
	if len(due) > limit {
		due = due[:limit]
	}
	if len(due) > allowed {
		due, throttled = due[:max(allowed, 0)], true
	}
	return due, throttled
}

// budgetLeft drops the observations that have left the rolling window and says
// how many of budget remain.
func budgetLeft(spent []time.Time, now time.Time, budget int) (kept []time.Time, left int) {
	for _, at := range spent {
		if now.Sub(at) < pullWindow {
			kept = append(kept, at)
		}
	}
	return kept, budget - len(kept)
}

// bindingsRead stores the pull-request records just read and drops what is
// remembered of a pull request that is no longer recorded.
func (s *Snapshotter) bindingsRead(bindings []worktrees.RegisteredPullRequestBinding, at time.Time) {
	s.mu.Lock()
	s.bindings, s.boundAt = bindings, at
	listed := make(map[string]bool, len(bindings))
	for _, binding := range bindings {
		listed[pullKey(binding.Repository, binding.PullRequest)] = true
	}
	for key := range s.observed {
		if !listed[key] {
			delete(s.observed, key)
		}
	}
	for key := range s.attempts {
		if !listed[key] {
			delete(s.attempts, key)
		}
	}
	s.mu.Unlock()
	if s.pullObserver != nil {
		s.pullObserver.Retain(bindings)
	}
}

// startPullRequests starts an observation pass in a goroutine of its own, under
// its own timeouts, for the pull requests that are due, unless one is still
// running. The refresh does not wait for it: what it learns is published when
// it arrives, and a failure keeps the previous values. The hourly budget is
// spent when the pass starts; when it cuts the pass short the document says
// observation is throttled.
func (s *Snapshotter) startPullRequests(ctx context.Context) {
	if s.pullObserver == nil || !s.pullBusy.CompareAndSwap(false, true) {
		return
	}
	s.mu.Lock()
	now := s.now()
	var left int
	s.spent, left = budgetLeft(s.spent, now, s.pullBudget)
	batch, throttled := pullBatch(s.bindings, s.observed, s.attempts, now, s.pullLimit, left)
	s.throttled = throttled
	for range batch {
		s.spent = append(s.spent, now)
	}
	s.mu.Unlock()
	if len(batch) == 0 {
		s.pullBusy.Store(false)
		return
	}
	s.side.Add(1)
	go func() {
		defer s.side.Done()
		defer s.pullBusy.Store(false)
		s.observePullRequests(ctx, batch)
	}()
}

// pullResult is one observation of a pass.
type pullResult struct {
	binding     worktrees.RegisteredPullRequestBinding
	observation pullObservation
	ok          bool
}

// observePullRequests observes the batch with a small pool of workers, each
// observation under its own timeout, then stores what succeeded and publishes.
func (s *Snapshotter) observePullRequests(ctx context.Context, batch []worktrees.RegisteredPullRequestBinding) {
	results := make([]pullResult, len(batch))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(pullRequestWorkers, len(batch)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				results[index] = s.observeOne(ctx, batch[index])
			}
		}()
	}
	for index := range batch {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	listed := map[string]bool{}
	for _, binding := range s.bindings {
		listed[pullKey(binding.Repository, binding.PullRequest)] = true
	}
	at := s.now()
	for _, result := range results {
		key := pullKey(result.binding.Repository, result.binding.PullRequest)
		if !listed[key] {
			continue
		}
		attempt := s.attempts[key]
		attempt.at = at
		if !result.ok {
			attempt.failures++
			s.attempts[key] = attempt
			continue
		}
		attempt.failures = 0
		s.attempts[key] = attempt
		s.observed[key] = result.observation
		if result.observation.done {
			s.pullObserver.Forget(result.binding)
		}
	}
	s.publishMaybeLocked()
}

// observeOne takes one observation under the per-call timeout. A panic in the
// watcher, an error or an unavailable outcome is a failed observation, which
// is logged and changes nothing.
func (s *Snapshotter) observeOne(parent context.Context, binding worktrees.RegisteredPullRequestBinding) pullResult {
	ctx, cancel := context.WithTimeout(parent, s.pullTimeout)
	defer cancel()
	var outcome prwatch.Outcome
	if err := catch(func() (err error) {
		outcome, err = s.pullObserver.Evaluate(ctx, binding)
		return err
	}); err != nil {
		s.logf("cockpit fleet: observe pull request %s: %v", pullKey(binding.Repository, binding.PullRequest), err)
		return pullResult{binding: binding}
	}
	observation, ok := observationOf(outcome, s.now())
	if !ok {
		s.logf("cockpit fleet: pull request %s is unavailable", pullKey(binding.Repository, binding.PullRequest))
	}
	return pullResult{binding: binding, observation: observation, ok: ok}
}
