package worktrees

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// createTasks seeds n WB tasks over the same canonical repository, which is the
// shape that makes the inventory walk worth parallelising and also the shape
// that makes it dangerous: every one of them inspects the same clone.
func createTasks(t *testing.T, fixture *gitFixture, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
			ProjectsRoot: fixture.projectsRoot,
			Operation:    fmt.Sprintf("task-%02d", i),
			WorkLog:      WorkLogOptions{Model: "unknown"},
		}); err != nil {
			t.Fatalf("create task %d: %v", i, err)
		}
	}
}

// listedKeys reduces an outcome to a comparable identity per candidate.
func listedKeys(outcome ListOutcome) []string {
	keys := make([]string, 0, len(outcome.Results))
	for _, r := range outcome.Results {
		keys = append(keys, r.Task+"|"+r.Repository+"|"+r.WorktreeDir+"|"+r.Branch)
	}
	return keys
}

// The inspection phase runs concurrently, so its output order is not the walk
// order. ListWithDiagnostics sorts before returning precisely so that does not
// matter — this pins that guarantee, because a parallel inventory that returned
// a different answer than the serial one would silently change which worktrees
// cleanup considers eligible to delete.
func TestParallelInventoryMatchesSerialInventory(t *testing.T) {
	fixture := newGitFixture(t)
	createTasks(t, fixture, 6)

	serial, err := ListWithDiagnostics(context.Background(), ListOptions{
		ProjectsRoot: fixture.projectsRoot, Workers: 1,
	})
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	parallel, err := ListWithDiagnostics(context.Background(), ListOptions{
		ProjectsRoot: fixture.projectsRoot, Workers: 8,
	})
	if err != nil {
		t.Fatalf("parallel: %v", err)
	}

	serialKeys, parallelKeys := listedKeys(serial), listedKeys(parallel)
	if len(serialKeys) == 0 {
		t.Fatal("fixture produced no candidates; the comparison would be vacuous")
	}
	if len(serialKeys) != len(parallelKeys) {
		t.Fatalf("candidate count differs: serial %d, parallel %d", len(serialKeys), len(parallelKeys))
	}
	for i := range serialKeys {
		if serialKeys[i] != parallelKeys[i] {
			t.Fatalf("candidate %d differs:\n  serial   %s\n  parallel %s", i, serialKeys[i], parallelKeys[i])
		}
	}
	if len(serial.Diagnostics) != len(parallel.Diagnostics) {
		t.Fatalf("diagnostic count differs: serial %d, parallel %d",
			len(serial.Diagnostics), len(parallel.Diagnostics))
	}
}

// Every candidate must be announced before it is inspected and reported after,
// because the gap between the two is the only thing that identifies a stuck
// candidate. A walk that only reported completions would go silent for exactly
// as long as the problem lasted.
func TestInventoryReportsProgressForEveryCandidate(t *testing.T) {
	fixture := newGitFixture(t)
	createTasks(t, fixture, 4)

	var (
		mu       sync.Mutex
		starts   = map[string]int{}
		finishes = map[string]int{}
		indexes  = map[int]bool{}
	)
	outcome, err := ListWithDiagnostics(context.Background(), ListOptions{
		ProjectsRoot: fixture.projectsRoot,
		Workers:      4,
		Progress: func(event ListProgress) {
			mu.Lock()
			defer mu.Unlock()
			if event.Done {
				finishes[event.Path]++
				return
			}
			starts[event.Path]++
			indexes[event.Index] = true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) == 0 {
		t.Fatal("fixture produced no candidates")
	}
	if len(starts) != len(outcome.Results) {
		t.Fatalf("progress announced %d candidates, inventory returned %d", len(starts), len(outcome.Results))
	}
	for path, n := range starts {
		if n != 1 {
			t.Fatalf("candidate %s announced %d times, want once", path, n)
		}
		if finishes[path] != 1 {
			t.Fatalf("candidate %s finished %d times, want once — an unfinished candidate is the stuck one", path, finishes[path])
		}
	}
	if len(indexes) != len(starts) {
		t.Fatalf("progress indexes collided: %d distinct for %d candidates", len(indexes), len(starts))
	}
}

// A nil Progress is the normal case and must not cost or crash anything.
func TestInventoryWithoutProgressHook(t *testing.T) {
	fixture := newGitFixture(t)
	createTasks(t, fixture, 2)

	if _, err := ListWithDiagnostics(context.Background(), ListOptions{
		ProjectsRoot: fixture.projectsRoot, Workers: 4,
	}); err != nil {
		t.Fatal(err)
	}
}
