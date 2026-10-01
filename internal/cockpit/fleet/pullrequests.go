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
	// cockpit.pull_request_limit is not set. One observation costs a handful
	// of GitHub reads (the pull request, its check runs, its commit statuses
	// and the target's required-check policy, plus the annotations of a red
	// head), so with the default pass interval this stays far below GitHub's
	// hourly allowance for an authenticated user.
	DefaultPullRequestLimit = 10
	// defaultPullRequestInterval is the shortest time between two observation
	// passes. The refresh interval is a minute by default, so a pass runs on
	// every other refresh; a longer refresh interval runs one on each.
	defaultPullRequestInterval = 90 * time.Second
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
// said, with when it was taken. merged marks a pull request that has left the
// watch set.
type pullObservation struct {
	state, mergeable, failedCheck           string
	total, passed, failed, skipped, pending int
	green, merged                           bool
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
		state: "open", green: snapshot.Green, checkedAt: at, merged: outcome.Kind == prwatch.KindMerged,
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
	return observation, true
}

// applyTo puts the observation's fields on a pull request entry.
func (o pullObservation) applyTo(pull *PullRequest) {
	pull.State, pull.Mergeable, pull.FailedCheck, pull.CheckedAt = o.state, o.mergeable, o.failedCheck, o.checkedAt
	pull.ChecksTotal, pull.ChecksPassed, pull.ChecksFailed, pull.ChecksSkipped, pull.ChecksPending = &o.total, &o.passed, &o.failed, &o.skipped, &o.pending
	pull.ChecksGreen = &o.green
}

// pullBatch picks the bindings one pass observes: those not yet confirmed
// merged, one for each pull request, the oldest checked_at first (a pull
// request never observed first), at most limit of them.
func pullBatch(bindings []worktrees.RegisteredPullRequestBinding, observed map[string]pullObservation, limit int) []worktrees.RegisteredPullRequestBinding {
	seen := map[string]bool{}
	var batch []worktrees.RegisteredPullRequestBinding
	for _, binding := range bindings {
		key := pullKey(binding.Repository, binding.PullRequest)
		if binding.Repository == "" || binding.PullRequest <= 0 || seen[key] || observed[key].merged {
			continue
		}
		seen[key] = true
		batch = append(batch, binding)
	}
	sort.SliceStable(batch, func(i, j int) bool {
		left, right := observed[pullKey(batch[i].Repository, batch[i].PullRequest)].checkedAt, observed[pullKey(batch[j].Repository, batch[j].PullRequest)].checkedAt
		return left.Before(right)
	})
	if len(batch) > limit {
		batch = batch[:limit]
	}
	return batch
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
	s.mu.Unlock()
	if s.pullObserver != nil {
		s.pullObserver.Retain(bindings)
	}
}

// startPullRequests starts an observation pass in a goroutine of its own, under
// its own timeouts, unless the interval since the last pass has not passed or
// one is still running. The refresh does not wait for it: what it learns is
// published when it arrives, and a failure keeps the previous values.
func (s *Snapshotter) startPullRequests(ctx context.Context) {
	if s.pullObserver == nil || !s.pullBusy.CompareAndSwap(false, true) {
		return
	}
	s.mu.Lock()
	now := s.now()
	var batch []worktrees.RegisteredPullRequestBinding
	if !s.pullStarted || now.Sub(s.lastPull) >= s.pullInterval {
		batch = pullBatch(s.bindings, s.observed, s.pullLimit)
	}
	if len(batch) > 0 {
		s.pullStarted, s.lastPull = true, now
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
	for _, result := range results {
		key := pullKey(result.binding.Repository, result.binding.PullRequest)
		if !result.ok || !listed[key] {
			continue
		}
		s.observed[key] = result.observation
		if result.observation.merged {
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
