package worktrees

import (
	"path/filepath"
	"sort"
	"sync"
)

// The apply phase, not the inventory walk, is where a fleet sweep spends its
// wall clock once inspection is concurrent: the founder's 2026-08-24 sweep
// inspected 262 candidates in about two minutes and then took ten more to
// remove 86 of them, roughly 7.1 seconds each, strictly one at a time.
//
// The unit that can be parallelised is the repository, not the task. Git
// allows exactly one writer per clone — `worktree remove`, `update-ref -d` and
// the ref updates a push implies all mutate the same .git and fail on each
// other's lock files — so two tasks in `sneat-co/sneat-go` must still take
// turns. Two tasks in different clones need not. That bounds the achievable
// gain to the largest per-repository group: 86 removals over 34 repositories,
// the biggest holding 14, is about a 3x improvement and no more. Workers
// beyond that ceiling idle.

// maxConcurrentRemoteBranchDeletions caps how many `git push --force-with-lease
// origin :refs/heads/<branch>` calls are in flight across the whole sweep.
//
// This is a second, tighter bound than --parallel because it protects a
// different resource. The per-repository locks protect local Git state, of
// which there is one per clone; this protects GitHub's secondary rate limiter,
// of which there is one per account. Eight concurrent branch deletions against
// one account is exactly the burst shape that limiter answers with a 403 and a
// retry-after, which would convert a fast sweep into a slow one with stranded
// transactions. It is not a user-facing knob (--parallel stays the single
// ceiling operators set); it is a var only so tests can pin it.
var maxConcurrentRemoteBranchDeletions = 4

// remoteBranchDeletionGate bounds concurrent remote branch deletions. A nil
// gate is unbounded, which keeps every non-sweep caller free of it.
type remoteBranchDeletionGate struct {
	slots chan struct{}
}

// newRemoteBranchDeletionGate derives the network bound from the same
// --parallel ceiling the operator set, clamped by the account-wide cap above,
// so there is still only one knob.
func newRemoteBranchDeletionGate(workers int) *remoteBranchDeletionGate {
	if workers < 1 {
		workers = 1
	}
	if workers > maxConcurrentRemoteBranchDeletions {
		workers = maxConcurrentRemoteBranchDeletions
	}
	return &remoteBranchDeletionGate{slots: make(chan struct{}, workers)}
}

// enter blocks until a slot is free and returns the release for it.
//
// A gate slot is always taken *after* this task's repository locks and
// released before they are, and nothing holding a slot ever waits for a
// repository lock. A slot holder therefore always makes progress, which is why
// adding this second resource cannot introduce a cycle.
func (gate *remoteBranchDeletionGate) enter() func() {
	if gate == nil {
		return func() {}
	}
	gate.slots <- struct{}{}
	return func() { <-gate.slots }
}

// cleanupApplyEntry is one task's complete, pre-resolved apply plan.
//
// Every index and decision here is computed before the first worker starts.
// That is deliberate: the serial loop this replaces re-scanned all of
// outcome.Results and outcome.Artifacts inside each task, which under
// concurrency would be one goroutine reading the very fields another is
// writing. Resolving the plan up front gives each task a disjoint set of
// slots to write and nothing to read outside them.
type cleanupApplyEntry struct {
	selection cleanupTaskSelection
	// resultIndices are this task's eligible, non-backlog results, in walk
	// order.
	resultIndices []int
	// artifactIndices are this task's eligible lifecycle artifacts.
	artifactIndices []int
	// repositories are the distinct canonical clones this task writes to,
	// sorted. Sorted is the whole deadlock argument: see
	// acquireRepositoryWriteLocks.
	repositories []string
	// canApply and hasEligibleWorktree are the gates the serial loop evaluated
	// inline against the whole outcome.
	canApply            bool
	hasEligibleWorktree bool
}

// planCleanupApply resolves every task's apply plan from the pre-apply outcome.
func planCleanupApply(outcome CleanupOutcome) []cleanupApplyEntry {
	selections := cleanupTaskSelections(outcome)
	entries := make([]cleanupApplyEntry, 0, len(selections))
	for _, selection := range selections {
		key := cleanupTaskKey(selection.WorktreesRoot, selection.Task)
		entry := cleanupApplyEntry{
			selection:           selection,
			canApply:            cleanupTaskCanApply(outcome, key),
			hasEligibleWorktree: cleanupTaskHasEligibleWorktree(outcome, key),
		}
		repositories := make(map[string]bool)
		for index := range outcome.Results {
			result := &outcome.Results[index]
			if !result.Eligible || result.BacklogID != "" ||
				cleanupTaskKey(result.WorktreesRoot, result.Task) != key {
				continue
			}
			entry.resultIndices = append(entry.resultIndices, index)
			repositories[filepath.Clean(result.CanonicalDir)] = true
		}
		for index := range outcome.Artifacts {
			artifact := &outcome.Artifacts[index]
			if !artifact.Eligible || cleanupTaskKey(artifact.WorktreesRoot, artifact.Task) != key {
				continue
			}
			entry.artifactIndices = append(entry.artifactIndices, index)
		}
		for repository := range repositories {
			entry.repositories = append(entry.repositories, repository)
		}
		sort.Strings(entry.repositories)
		entries = append(entries, entry)
	}
	return entries
}

// acquireRepositoryWriteLocks takes every clone one task writes to and returns
// the release for all of them.
//
// The ordering is the entire safety argument for coordinated tasks. A task
// spanning `sneat-co/sneat-go` and `sneat-games/chess` needs both clones for
// its whole transaction, and so may a task spanning the same two. If each took
// them in its own order, one holding sneat-go and waiting for chess against
// one holding chess and waiting for sneat-go is a deadlock that no timeout
// here would resolve. Sorting the set gives every task in the process one
// global acquisition order, which makes a cycle impossible to construct — the
// standard resource-hierarchy argument. planCleanupApply sorts; this asserts
// nothing and simply relies on it, so the sort must never move.
func acquireRepositoryWriteLocks(locks *cloneLocks, repositories []string) func() {
	held := make([]*sync.Mutex, 0, len(repositories))
	for _, repository := range repositories {
		lock := locks.get(repository)
		lock.Lock()
		held = append(held, lock)
	}
	return func() {
		for index := len(held) - 1; index >= 0; index-- {
			held[index].Unlock()
		}
	}
}

// runCleanupApply drives every task's apply, concurrently when asked, and
// records each task's error in its own walk-order slot.
//
// Errors are never appended from a worker. A sweep's report has to read the
// same way whatever order tasks finish in, so the caller folds these slots
// back in walk order afterwards.
func runCleanupApply(
	entries []cleanupApplyEntry,
	workers int,
	locks *cloneLocks,
	stopOnFirstError bool,
	apply func(cleanupApplyEntry) error,
) []error {
	errs := make([]error, len(entries))
	if workers < 1 {
		workers = 1
	}
	if workers > len(entries) {
		workers = len(entries)
	}
	if workers < 2 || stopOnFirstError {
		for index := range entries {
			release := acquireRepositoryWriteLocks(locks, entries[index].repositories)
			errs[index] = apply(entries[index])
			release()
			if errs[index] != nil && stopOnFirstError {
				break
			}
		}
		return errs
	}
	jobs := make(chan int)
	var wait sync.WaitGroup
	go func() {
		defer close(jobs)
		for index := range entries {
			jobs <- index
		}
	}()
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := range jobs {
				release := acquireRepositoryWriteLocks(locks, entries[index].repositories)
				errs[index] = apply(entries[index])
				release()
			}
		}()
	}
	wait.Wait()
	return errs
}
