package worktrees

import (
	"context"
	"sync"
	"testing"
)

// A sweep must tell a heartbeat how many candidates it has found and which
// step each in-flight inspection is in, so a piped caller can see where the
// time goes. Every candidate reaches the local git step, the GitHub step when
// evidence is requested, and the post-walk phase counts every listed result.
func TestCleanupSweepReportsFoundCountsStepsAndPhases(t *testing.T) {
	fixture := newGitFixture(t)
	createTasks(t, fixture, 3)
	installPerCommitPullRequestFixture(t, nil)

	var (
		mu     sync.Mutex
		found  int
		steps  = map[string]map[string]bool{}
		phases []ListProgressDetail
	)
	outcome, err := GC(context.Background(), GCOptions{
		ProjectsRoot: fixture.projectsRoot, SkipSizes: true, Workers: 2,
		ProgressDetail: func(event ListProgressDetail) {
			mu.Lock()
			defer mu.Unlock()
			if event.Found > found {
				found = event.Found
			}
			if event.Step != "" {
				if steps[event.Path] == nil {
					steps[event.Path] = map[string]bool{}
				}
				steps[event.Path][event.Step] = true
			}
			if event.Phase != "" {
				phases = append(phases, event)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Inventoried) == 0 {
		t.Fatal("fixture produced no candidates")
	}
	if found != len(outcome.Inventoried) {
		t.Fatalf("found %d, inventoried %d", found, len(outcome.Inventoried))
	}
	for _, path := range outcome.Inventoried {
		if !steps[path]["git"] || !steps[path]["pull requests"] {
			t.Fatalf("candidate %s steps = %v, want git and pull requests", path, steps[path])
		}
	}
	if len(phases) != len(outcome.Inventoried) || phases[0].PhaseTotal != len(outcome.Inventoried) || phases[0].Phase != "work log and size" {
		t.Fatalf("phases = %#v", phases)
	}
}

// Without a detail sink nothing is reported and nothing breaks; a nil reporter
// and an empty batch are inert.
func TestProgressDetailIsInertWithoutASink(t *testing.T) {
	t.Parallel()
	var nilReporter *listProgressReporter
	nilReporter.discovered(3)
	nilReporter.step(progressToken{}, "t", "/p", "git")
	(&listProgressReporter{}).discovered(3)
	(&listProgressReporter{}).step(progressToken{}, "t", "/p", "git")

	calls := 0
	reporter := &listProgressReporter{detail: func(ListProgressDetail) { calls++ }}
	reporter.discovered(0)
	markInspectStep(context.Background(), "git")
	if calls != 0 {
		t.Fatalf("empty batch reported %d events", calls)
	}
}
