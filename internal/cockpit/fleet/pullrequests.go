package fleet

import (
	"context"
	"slices"
	"sort"
	"strconv"
	"strings"
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
	// is prsnapshot.ReadsPerObservation (6) GitHub reads for an open pull
	// request, whatever its checks say, and prsnapshot.ReadsPerInactiveObservation
	// (1) for a merged or closed one, so the default is at most 720 reads an
	// hour and the ceiling of 400 at most 2,400; a read answered 304 by the
	// ETag cache is not charged to the rate limit.
	DefaultPullRequestHourlyBudget = 120
	// pullWindow is the rolling window the budget is counted over.
	pullWindow = time.Hour
	// pullPendingEvery is how soon a pull request still waiting for its checks
	// (or whose mergeability GitHub is still computing) is observed again, and
	// pullSettledEvery how soon one whose verdict is known.
	pullPendingEvery = 90 * time.Second
	pullSettledEvery = 10 * time.Minute
	// pullClosedEvery is how soon a pull request closed without merging is
	// observed again, in case it is reopened; a merged one is not.
	pullClosedEvery = time.Hour
	// maxUnknownMergeable is how many observations in a row may report an
	// unknown merge state before it is treated as settled: GitHub that never
	// computes it must not keep a pull request on the pending cadence.
	maxUnknownMergeable = 5
	// pullPendingShare is the part of the hourly budget the pending cadence may
	// use, so that settled pull requests keep their share.
	pullPendingShare = 0.7
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
// said, with when it was taken. done marks a pull request merged, which has
// left the watch set. checksAwaited says the checks have not reached a verdict;
// mergeUnknown that GitHub still reports no merge state, and unknowns in how
// many observations in a row it did.
type pullObservation struct {
	state, mergeable, failedCheck           string
	total, passed, failed, skipped, pending int
	green, done, checksAwaited              bool
	mergeUnknown                            bool
	unknowns                                int
	checkedAt                               time.Time
}

// waiting is whether the pull request is on the pending cadence.
func (o pullObservation) waiting() bool {
	return o.checksAwaited || (o.mergeUnknown && o.unknowns < maxUnknownMergeable)
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
		state: "open", green: snapshot.Green, checkedAt: at, done: outcome.Kind == prwatch.KindMerged,
		passed: snapshot.Checks["pass"], failed: snapshot.Checks["fail"] + snapshot.Checks["cancel"],
		skipped: snapshot.Checks["skipping"], pending: snapshot.Checks["pending"],
	}
	switch {
	case snapshot.Merged:
		observation.state = "merged"
	case snapshot.State != "" && !strings.EqualFold(snapshot.State, "open"):
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
		observation.failedCheck = plainTextMax(failedCheckName(snapshot.Failed), maxFailedCheckText)
	}
	// Checks are awaited while something runs and while an open pull request
	// has no check at all, no verdict and no required check named missing (CI
	// has not registered yet). A missing required check, a failure, a draft and
	// a green head are settled; so is a merge state GitHub keeps not computing.
	observation.checksAwaited = observation.pending > 0 ||
		(observation.state == "open" && observation.total == 0 && !snapshot.Green && len(snapshot.Blocked) == 0)
	if snapshot.Mergeable == "unknown" {
		observation.mergeUnknown, observation.unknowns = true, 1
	}
	return observation, true
}

// failedCheckName is the name of the first failing check as a person knows it.
// The snapshot names its failures with the source prefixed and sorted by that
// name (`check-run:build`, `status:ci/lint`, `workflow-run:<id>:<event>`): the
// prefix is dropped, and a real check is preferred to a workflow run, which
// has only an id and an event to be called by.
func failedCheckName(failed []string) string {
	for _, name := range failed {
		if !strings.HasPrefix(name, "workflow-run:") {
			return strings.TrimPrefix(strings.TrimPrefix(name, "check-run:"), "status:")
		}
	}
	event := "workflow run"
	if parts := strings.SplitN(failed[0], ":", 3); len(parts) == 3 {
		event = parts[2] + " workflow run"
	}
	return event
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
// a closed one after an hour (it may be reopened); one whose checks are awaited
// after pendingEvery; otherwise after 10 minutes. A merged one is not watched.
func pullDue(observation pullObservation, observed bool, attempt pullAttempt, pendingEvery time.Duration) (due time.Time, watched bool) {
	switch {
	case observed && observation.done:
		return time.Time{}, false
	case attempt.at.IsZero():
		return time.Time{}, true
	case attempt.failures > 0:
		return attempt.at.Add(pullBackoff(attempt.failures)), true
	case observed && observation.state == "closed":
		return attempt.at.Add(pullClosedEvery), true
	case !observed || observation.waiting():
		return attempt.at.Add(pendingEvery), true
	}
	return attempt.at.Add(pullSettledEvery), true
}

// pendingInterval is how soon a pull request whose checks are awaited is
// observed again: 90 seconds, stretched when many are awaited so that together
// they use at most 70% of the hourly budget and the settled ones keep the rest.
func pendingInterval(waiting, budget int) time.Duration {
	stretched := time.Duration(float64(waiting) * float64(pullWindow) / (float64(budget) * pullPendingShare))
	return max(pullPendingEvery, stretched)
}

// pullPlan is what one pass is planned with: the clock, the most to observe
// (limit, and allowed by the budget), the pending interval and whether a
// binding belongs to a worktree this machine has.
type pullPlan struct {
	now            time.Time
	limit, allowed int
	budget         int
	active         func(worktrees.RegisteredPullRequestBinding) bool
}

// pullBatch picks the bindings one pass observes: those watched and due, one
// for each pull request, the longest due first (those never observed first, and
// among equals the bindings of a worktree this machine has before the others,
// so many lingering records of merged pull requests cannot starve the open
// ones), at most plan.limit of them and at most plan.allowed (what the hourly
// budget leaves). It also says whether the budget cut the batch short, which is
// what throttled means.
func pullBatch(bindings []worktrees.RegisteredPullRequestBinding, observed map[string]pullObservation, attempts map[string]pullAttempt, plan pullPlan) (batch []worktrees.RegisteredPullRequestBinding, throttled bool) {
	first := map[string]worktrees.RegisteredPullRequestBinding{}
	active := map[string]bool{}
	var keys []string
	for _, binding := range bindings {
		key := pullKey(binding.Repository, binding.PullRequest)
		if binding.Repository == "" || binding.PullRequest <= 0 {
			continue
		}
		if _, seen := first[key]; !seen {
			first[key] = binding
			keys = append(keys, key)
		}
		active[key] = active[key] || plan.active(binding)
	}
	waiting := 0
	for _, key := range keys {
		if observation, ok := observed[key]; ok && !observation.done && observation.waiting() && attempts[key].failures == 0 {
			waiting++
		}
	}
	pendingEvery := pendingInterval(waiting, plan.budget)
	dues := map[string]time.Time{}
	var due []string
	for _, key := range keys {
		observation, ok := observed[key]
		when, watched := pullDue(observation, ok, attempts[key], pendingEvery)
		if watched && !when.After(plan.now) {
			dues[key] = when
			due = append(due, key)
		}
	}
	sort.SliceStable(due, func(i, j int) bool {
		if !dues[due[i]].Equal(dues[due[j]]) {
			return dues[due[i]].Before(dues[due[j]])
		}
		return active[due[i]] && !active[due[j]]
	})
	if len(due) > plan.limit {
		due = due[:plan.limit]
	}
	if len(due) > plan.allowed {
		due, throttled = due[:max(plan.allowed, 0)], true
	}
	for _, key := range due {
		batch = append(batch, first[key])
	}
	return batch, throttled
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

// activeTasks is the set of tasks this machine has a worktree for, by
// repository slug, which is what makes a pull request record look open.
func (s *Snapshotter) activeTasks() map[string]bool {
	active := map[string]bool{}
	for _, state := range s.repos {
		for _, worktree := range state.entries.worktrees {
			active[state.repo.Slug()+"\x00"+worktree.Task] = true
		}
	}
	return active
}

// startPullRequests starts an observation pass in a goroutine of its own, under
// its own timeouts, for the pull requests that are due, unless one is still
// running. The refresh does not wait for it: what it learns is published when
// it arrives, and a failure keeps the previous values. The hourly budget is
// spent when the pass starts (and refunded for what a cancelled pass did not
// observe); when it cuts the pass short the document says observation is
// throttled.
func (s *Snapshotter) startPullRequests(ctx context.Context) {
	if s.pullObserver == nil || !s.pullBusy.CompareAndSwap(false, true) {
		return
	}
	s.mu.Lock()
	now := s.now()
	var left int
	s.spent, left = budgetLeft(s.spent, now, s.pullBudget)
	tasks := s.activeTasks()
	batch, throttled := pullBatch(s.bindings, s.observed, s.attempts, pullPlan{
		now: now, limit: s.pullLimit, allowed: left, budget: s.pullBudget,
		active: func(binding worktrees.RegisteredPullRequestBinding) bool {
			return tasks[binding.Repository+"\x00"+binding.Task]
		},
	})
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
		s.observePullRequests(ctx, batch, now)
	}()
}

// pullResult is one observation of a pass. cancelled says the pass was stopped
// before this one was observed, which is neither a success nor a failure.
type pullResult struct {
	binding     worktrees.RegisteredPullRequestBinding
	observation pullObservation
	ok          bool
	cancelled   bool
}

// observePullRequests observes the batch, begun at began, with a small pool of
// workers, each observation under its own timeout, then stores what succeeded
// and publishes. A pass whose context ended records nothing for what it did not
// observe (no failure, no backoff) and gives those budget units back.
func (s *Snapshotter) observePullRequests(ctx context.Context, batch []worktrees.RegisteredPullRequestBinding, began time.Time) {
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
		if result.cancelled {
			s.refund(began)
			continue
		}
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
		observation := result.observation
		if previous, ok := s.observed[key]; ok && previous.mergeUnknown && observation.mergeUnknown {
			observation.unknowns = previous.unknowns + 1
		}
		s.observed[key] = observation
		if observation.done {
			s.pullObserver.Forget(result.binding)
		}
	}
	s.publishMaybeLocked()
}

// refund gives back one budget unit spent at began. The caller holds s.mu.
func (s *Snapshotter) refund(began time.Time) {
	for index := len(s.spent) - 1; index >= 0; index-- {
		if s.spent[index].Equal(began) {
			s.spent = slices.Delete(s.spent, index, index+1)
			return
		}
	}
}

// observeOne takes one observation under the per-call timeout. A panic in the
// watcher, an error or an unavailable outcome is a failed observation, which
// is logged and changes nothing; an observation that the pass's own context cut
// off is cancelled, not failed.
func (s *Snapshotter) observeOne(parent context.Context, binding worktrees.RegisteredPullRequestBinding) pullResult {
	if parent.Err() != nil {
		return pullResult{binding: binding, cancelled: true}
	}
	ctx, cancel := context.WithTimeout(parent, s.pullTimeout)
	defer cancel()
	var outcome prwatch.Outcome
	err := catch(func() (err error) {
		outcome, err = s.pullObserver.Evaluate(ctx, binding)
		return err
	})
	if parent.Err() != nil {
		return pullResult{binding: binding, cancelled: true}
	}
	if err != nil {
		s.logf("cockpit fleet: observe pull request %s: %v", pullKey(binding.Repository, binding.PullRequest), err)
		return pullResult{binding: binding}
	}
	observation, ok := observationOf(outcome, s.now())
	if !ok {
		s.logf("cockpit fleet: pull request %s is unavailable", pullKey(binding.Repository, binding.PullRequest))
	}
	return pullResult{binding: binding, observation: observation, ok: ok}
}
