package worktrees

import (
	"slices"
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
