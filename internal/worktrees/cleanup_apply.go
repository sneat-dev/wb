package worktrees

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"
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
	// after are the positions, in the plan, of the tasks stacked on one of this
	// task's branches. Each of them comes earlier in the plan and must finish
	// before this task starts. See orderLeavesBeforeBases.
	after []int
}

// planCleanupApply resolves every task's apply plan from the pre-apply outcome.
func planCleanupApply(outcome CleanupOutcome, homes ...string) []cleanupApplyEntry {
	home := ""
	if len(homes) > 0 {
		home = homes[0]
	}
	selections := cleanupTaskSelections(outcome, home)
	entries := make([]cleanupApplyEntry, 0, len(selections))
	for _, selection := range selections {
		key := cleanupTaskKey(selection.WorktreesRoot, selection.Task)
		entry := cleanupApplyEntry{
			selection:           selection,
			canApply:            cleanupTaskCanApply(outcome, key, home),
			hasEligibleWorktree: cleanupTaskHasEligibleWorktree(outcome, key, home),
		}
		repositories := make(map[string]bool)
		for index := range outcome.Results {
			result := &outcome.Results[index]
			if !result.Eligible || result.BacklogID != "" ||
				logicalCleanupTaskKey(result.ListResult, home) != key {
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
	return orderLeavesBeforeBases(entries, outcome)
}

// orderLeavesBeforeBases puts a task stacked on another task's branch ahead of
// the task that owns that branch, and records the dependency so a concurrent
// apply honours it too.
//
// Cleaning a base retires its branch on origin. A leaf whose recorded base is
// that branch is re-inspected under its own lock immediately before removal,
// and by then the target it was proved against would be gone. Whenever the
// base had landed in the default branch the leaf is still provable there, but
// a base that landed somewhere else leaves the leaf with nothing to be judged
// against. Retiring leaves first keeps every proof standing for as long as
// something still needs it, so one sweep of a repository can retire a whole
// stack.
//
// The order is otherwise the walk order the plan already had. A dependency
// cycle cannot be built from real branches; if the records describe one
// anyway, it is broken where the walk first meets it, every task stays in the
// plan, and nothing waits on itself or on a later task.
func orderLeavesBeforeBases(entries []cleanupApplyEntry, outcome CleanupOutcome) []cleanupApplyEntry {
	ownerOfBranch := make(map[string]int)
	for index, entry := range entries {
		for _, resultIndex := range entry.resultIndices {
			result := outcome.Results[resultIndex]
			if result.Branch != "" {
				ownerOfBranch[cleanupBranchKey(result.CanonicalDir, result.Branch)] = index
			}
		}
	}
	// leaves[base] are the entries stacked on one of base's branches.
	leaves := make([][]int, len(entries))
	for index, entry := range entries {
		for _, resultIndex := range entry.resultIndices {
			result := outcome.Results[resultIndex]
			base := result.RecordedBase
			if base == "" {
				base = result.Base
			}
			if owner, stacked := ownerOfBranch[cleanupBranchKey(result.CanonicalDir, base)]; stacked && owner != index {
				leaves[owner] = append(leaves[owner], index)
			}
		}
	}
	position := make([]int, len(entries))
	for index := range position {
		position[index] = -1
	}
	ordered := make([]cleanupApplyEntry, 0, len(entries))
	var place func(index int, visiting map[int]bool)
	place = func(index int, visiting map[int]bool) {
		if position[index] >= 0 || visiting[index] {
			return
		}
		visiting[index] = true
		entry := entries[index]
		for _, leaf := range leaves[index] {
			place(leaf, visiting)
			if position[leaf] >= 0 && !slices.Contains(entry.after, position[leaf]) {
				entry.after = append(entry.after, position[leaf])
			}
		}
		position[index] = len(ordered)
		ordered = append(ordered, entry)
	}
	for index := range entries {
		place(index, map[int]bool{})
	}
	return ordered
}

func cleanupBranchKey(canonical, branch string) string {
	return filepath.Clean(canonical) + "\x00" + branch
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
	// A task whose stacked leaf was not retired in this run is not retired
	// either: its branch is still the recorded base of work that is still here.
	applyUnlessHeld := func(index int) {
		if errs[index] = heldByUnretiredLeaf(entries, errs, index); errs[index] != nil {
			return
		}
		release := acquireRepositoryWriteLocks(locks, entries[index].repositories)
		errs[index] = apply(entries[index])
		release()
	}
	if workers < 2 || stopOnFirstError {
		for index := range entries {
			applyUnlessHeld(index)
			if errs[index] != nil && stopOnFirstError {
				break
			}
		}
		return errs
	}
	// A task that must follow others waits for them before it takes any
	// repository lock. Those others always sit earlier in the plan and jobs are
	// handed out in plan order, so whatever a worker waits for is already in
	// another worker's hands and none of them can wait in a circle.
	done := make([]chan struct{}, len(entries))
	for index := range done {
		done[index] = make(chan struct{})
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
				for _, earlier := range entries[index].after {
					<-done[earlier]
				}
				applyUnlessHeld(index)
				close(done[index])
			}
		}()
	}
	wait.Wait()
	return errs
}

// heldByUnretiredLeaf is the refusal for a task whose stacked leaf failed to
// apply, or was itself held, earlier in this run. It names the leaf.
func heldByUnretiredLeaf(entries []cleanupApplyEntry, errs []error, index int) error {
	for _, earlier := range entries[index].after {
		if errs[earlier] != nil {
			return fmt.Errorf("held: %s is stacked on a branch of %s and was not retired in this run: %w",
				entries[earlier].selection.Task, entries[index].selection.Task, errs[earlier])
		}
	}
	return nil
}

// cleanupTaskApplicationPorts keep the task lock and artifact custody here
// while letting one locked task's preflight and member phases be fault-tested
// without creating a Git repository for every failure boundary.
type cleanupTaskApplicationPorts struct {
	Inventory        func(context.Context, ListOptions) (ListOutcome, error)
	Preflight        func(context.Context, CleanupOptions, time.Time, *cleanupTaskHandle, CleanupResult, string) (ListResult, error)
	PrepareArtifacts func(string, *cleanupTaskHandle, []int, []LifecycleArtifact) (*os.File, string, []cleanupLifecycleArtifactHandle, error)
	CloseArtifacts   func([]cleanupLifecycleArtifactHandle)
	ApplyMember      func(*cleanupRun, *cleanupTaskHandle, int, *remoteBranchDeletionGate, bool, *int) error
	ArchiveArtifacts func(*cleanupTaskHandle, *os.File, string, []cleanupLifecycleArtifactHandle, []LifecycleArtifact) error
}

func productionCleanupTaskApplicationPorts() cleanupTaskApplicationPorts {
	return cleanupTaskApplicationPorts{
		Inventory:        ListWithDiagnostics,
		Preflight:        preflightCleanupRepository,
		PrepareArtifacts: prepareCleanupLifecycleArtifacts,
		CloseArtifacts:   closeCleanupLifecycleArtifacts,
		ApplyMember:      (*cleanupRun).applyCleanupMember,
		ArchiveArtifacts: archiveCleanupLifecycleArtifacts,
	}
}
