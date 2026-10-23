package worktrees

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
)

// stackedCleanupResult is one eligible worktree of a task, on its own branch,
// recorded against base. judged is the target that proved it, when that is not
// the recorded base.
func stackedCleanupResult(task, repository, base, judged string) CleanupResult {
	result := CleanupResult{ListResult: ListResult{
		Task: task, Repository: "acme/" + repository, Branch: task, Base: base,
		CanonicalDir:  "/projects/acme/" + repository,
		WorktreesRoot: "/wb/worktrees",
		WorktreeDir:   "/wb/worktrees/" + task + "/acme/" + repository,
	}, Eligible: true}
	if judged != "" {
		result.Base, result.RecordedBase = judged, base
	}
	return result
}

func plannedTasks(entries []cleanupApplyEntry) []string {
	tasks := make([]string, 0, len(entries))
	for _, entry := range entries {
		tasks = append(tasks, entry.selection.Task)
	}
	return tasks
}

// One sweep of a repository must be able to retire a whole stack: every leaf
// goes before the task whose branch it is stacked on, because retiring that
// task deletes the branch the leaf's proof may still need.
func TestPlanCleanupApplyRetiresLeavesBeforeTheTasksTheyAreStackedOn(t *testing.T) {
	t.Parallel()
	outcome := CleanupOutcome{Results: []CleanupResult{
		// Walk order is alphabetical, which puts each base ahead of its leaves.
		stackedCleanupResult("a-root", "app", "main", ""),
		stackedCleanupResult("b-middle", "app", "a-root", "main"),
		stackedCleanupResult("c-leaf", "app", "b-middle", ""),
		stackedCleanupResult("d-unrelated", "app", "main", ""),
	}}
	entries := planCleanupApply(outcome)
	if got, want := plannedTasks(entries), []string{"c-leaf", "b-middle", "a-root", "d-unrelated"}; !slices.Equal(got, want) {
		t.Fatalf("apply order = %v, want leaves first: %v", got, want)
	}
	for index, want := range [][]int{nil, {0}, {1}, nil} {
		if !slices.Equal(entries[index].after, want) {
			t.Fatalf("%s waits for %v, want %v", entries[index].selection.Task, entries[index].after, want)
		}
	}
}

func TestPlanCleanupApplyOrdersOnlyWithinOneRepository(t *testing.T) {
	t.Parallel()
	// The same branch name in another clone is another branch.
	outcome := CleanupOutcome{Results: []CleanupResult{
		stackedCleanupResult("a-root", "app", "main", ""),
		stackedCleanupResult("b-leaf", "lib", "a-root", ""),
	}}
	entries := planCleanupApply(outcome)
	if got, want := plannedTasks(entries), []string{"a-root", "b-leaf"}; !slices.Equal(got, want) {
		t.Fatalf("apply order = %v, want the walk order %v", got, want)
	}
	if len(entries[0].after) != 0 || len(entries[1].after) != 0 {
		t.Fatalf("tasks in different repositories wait on each other: %v, %v", entries[0].after, entries[1].after)
	}
}

func TestPlanCleanupApplyBreaksACycleInTheRecordsWithoutLosingATask(t *testing.T) {
	t.Parallel()
	outcome := CleanupOutcome{Results: []CleanupResult{
		stackedCleanupResult("a-task", "app", "b-task", ""),
		stackedCleanupResult("b-task", "app", "a-task", ""),
		// A task recorded against its own branch waits for nothing.
		stackedCleanupResult("c-self", "app", "c-self", ""),
	}}
	entries := planCleanupApply(outcome)
	if got := plannedTasks(entries); len(got) != 3 {
		t.Fatalf("a cycle dropped tasks from the plan: %v", got)
	}
	for index, entry := range entries {
		for _, earlier := range entry.after {
			if earlier >= index {
				t.Fatalf("%s waits for position %d, which is not earlier than its own %d", entry.selection.Task, earlier, index)
			}
		}
	}
	// The plan must still be runnable concurrently without waiting forever.
	errs := runCleanupApply(entries, 3, newCloneLocks(), false, func(cleanupApplyEntry) error { return nil })
	if len(errs) != 3 {
		t.Fatalf("apply returned %d results for 3 tasks", len(errs))
	}
}

// The plan order alone is not enough once tasks run concurrently: a base must
// not start until the leaves stacked on it have finished.
func TestRunCleanupApplyStartsABaseOnlyAfterItsLeavesFinished(t *testing.T) {
	t.Parallel()
	entries := planCleanupApply(CleanupOutcome{Results: []CleanupResult{
		stackedCleanupResult("a-root", "app", "main", ""),
		stackedCleanupResult("b-leaf", "app", "a-root", "main"),
		stackedCleanupResult("c-elsewhere", "lib", "main", ""),
	}})
	var mu sync.Mutex
	var events []string
	record := func(event string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	}
	elsewhereStarted := make(chan struct{})
	errs := runCleanupApply(entries, 3, newCloneLocks(), false, func(entry cleanupApplyEntry) error {
		task := entry.selection.Task
		record("start " + task)
		switch task {
		case "c-elsewhere":
			close(elsewhereStarted)
		case "b-leaf":
			// Hold the leaf open until an unrelated repository's task is running,
			// which proves the apply is concurrent while the base still waits.
			<-elsewhereStarted
		}
		record("end " + task)
		return nil
	})
	for index, err := range errs {
		if err != nil {
			t.Fatalf("task %d: %v", index, err)
		}
	}
	if slices.Index(events, "start a-root") < slices.Index(events, "end b-leaf") {
		t.Fatalf("the base started before its leaf finished: %v", events)
	}
	if slices.Index(events, "start c-elsewhere") > slices.Index(events, "end b-leaf") {
		t.Fatalf("an unrelated repository waited for the stack: %v", events)
	}
}

func refusedStackedResult(task, repository, base, reason string) CleanupResult {
	result := stackedCleanupResult(task, repository, base, "")
	result.Eligible, result.Reason = false, reason
	return result
}

// Retiring a base deletes its branch, locally and on origin, and GitHub closes
// a pull request whose base branch is deleted. A base is therefore held while
// any listed task recorded against it is refused.
func TestCleanupHoldsABaseWhileATaskStackedOnItIsRefused(t *testing.T) {
	t.Parallel()
	judgedElsewhere := stackedCleanupResult("d-leaf-judged-on-main", "app", "c-other-root", "main")
	judgedElsewhere.Eligible, judgedElsewhere.Reason = false, "worktree has local changes"
	sibling := stackedCleanupResult("b-middle", "lib", "main", "")
	results := []CleanupResult{
		stackedCleanupResult("a-root", "app", "main", ""),
		stackedCleanupResult("b-middle", "app", "a-root", ""),
		sibling,
		refusedStackedResult("c-leaf", "app", "b-middle", "branch still has an open pull request: https://example.test/pull/7"),
		stackedCleanupResult("c-other-root", "app", "main", ""),
		judgedElsewhere,
		// The same branch name in another clone is another branch.
		stackedCleanupResult("e-unrelated", "lib", "main", ""),
		refusedStackedResult("f-leaf-elsewhere", "docs", "e-unrelated", "worktree has local changes"),
		// A durable backlog row has no checkout left to hold.
		{ListResult: ListResult{Task: "g-backlog", Repository: "acme/app", Branch: "g-backlog", CanonicalDir: "/projects/acme/app"}, Eligible: true, BacklogID: "backlog-1"},
		refusedStackedResult("h-leaf-of-backlog", "app", "g-backlog", "worktree has local changes"),
	}
	holdBasesOfIneligibleDependants(results)

	reasons := map[string]string{}
	for _, result := range results {
		if !result.Eligible {
			reasons[result.Task+"@"+result.Repository] = result.Reason
		}
	}
	for held, want := range map[string]string{
		"b-middle@acme/app":     "held: branch b-middle is the recorded base of c-leaf (acme/app), which is not eligible: branch still has an open pull request: https://example.test/pull/7",
		"a-root@acme/app":       "held: branch a-root is the recorded base of b-middle (acme/app), which is not eligible: held: branch b-middle is the recorded base of c-leaf",
		"b-middle@acme/lib":     "coordinated task blocked by acme/app: held: branch b-middle",
		"c-other-root@acme/app": "held: branch c-other-root is the recorded base of d-leaf-judged-on-main (acme/app), which is not eligible: worktree has local changes",
	} {
		if !strings.HasPrefix(reasons[held], want) {
			t.Fatalf("%s reason = %q, want it held with %q", held, reasons[held], want)
		}
	}
	for _, result := range results {
		if (result.Task == "e-unrelated" || result.Task == "g-backlog") && !result.Eligible {
			t.Fatalf("%s was held although nothing in its repository is stacked on a live branch of it: %s", result.Task, result.Reason)
		}
	}
}

func TestCleanupDoesNotHoldABaseWhoseDependantsAreAllEligible(t *testing.T) {
	t.Parallel()
	results := []CleanupResult{
		stackedCleanupResult("a-root", "app", "main", ""),
		stackedCleanupResult("b-leaf", "app", "a-root", "main"),
	}
	holdBasesOfIneligibleDependants(results)
	for _, result := range results {
		if !result.Eligible {
			t.Fatalf("%s was held: %s", result.Task, result.Reason)
		}
	}
}

// A leaf can still fail under its own lock after the plan called it eligible.
// Its base, and whatever that base is stacked on, is then not retired in this
// run, in a serial and in a concurrent apply alike.
func TestRunCleanupApplyHoldsEveryBaseAboveALeafThatFailed(t *testing.T) {
	t.Parallel()
	leafFailure := errors.New("cleanup safety changed for acme/app: branch head moved")
	for name, workers := range map[string]int{"serial": 1, "concurrent": 4} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			entries := planCleanupApply(CleanupOutcome{Results: []CleanupResult{
				stackedCleanupResult("a-root", "app", "main", ""),
				stackedCleanupResult("b-middle", "app", "a-root", "main"),
				stackedCleanupResult("c-leaf", "app", "b-middle", "main"),
				stackedCleanupResult("d-other-leaf", "app", "a-root", "main"),
				stackedCleanupResult("e-elsewhere", "lib", "main", ""),
			}})
			var mu sync.Mutex
			var applied []string
			errs := runCleanupApply(entries, workers, newCloneLocks(), false, func(entry cleanupApplyEntry) error {
				mu.Lock()
				defer mu.Unlock()
				applied = append(applied, entry.selection.Task)
				if entry.selection.Task == "c-leaf" {
					return leafFailure
				}
				return nil
			})
			slices.Sort(applied)
			if want := []string{"c-leaf", "d-other-leaf", "e-elsewhere"}; !slices.Equal(applied, want) {
				t.Fatalf("applied tasks = %v, want %v: a base above the failed leaf must not start", applied, want)
			}
			for index, entry := range entries {
				task, err := entry.selection.Task, errs[index]
				switch task {
				case "b-middle":
					if err == nil || !errors.Is(err, leafFailure) || !strings.HasPrefix(err.Error(), "held: c-leaf is stacked on a branch of b-middle and was not retired in this run: ") {
						t.Fatalf("b-middle error = %v", err)
					}
				case "a-root":
					if err == nil || !errors.Is(err, leafFailure) || !strings.HasPrefix(err.Error(), "held: b-middle is stacked on a branch of a-root and was not retired in this run: held: c-leaf") {
						t.Fatalf("a-root error = %v", err)
					}
				case "c-leaf":
					if !errors.Is(err, leafFailure) {
						t.Fatalf("c-leaf error = %v", err)
					}
				default:
					if err != nil {
						t.Fatalf("%s error = %v", task, err)
					}
				}
			}
		})
	}
}
