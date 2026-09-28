package quality

import (
	"testing"
)

// TestClockSeamSitesHaveNoDirectTimeCalls is task-10's static check
// (spec/plans/coverage-to-100): every function ClockSeamSites names
// (clock_seam.go's own doc comment explains what belongs on that list and
// why it, not an open-ended repository-wide scan, is "retry, timeout or
// backoff code path") must call time.Sleep, time.Now, time.After,
// time.NewTimer and time.Tick zero times directly. Each of these functions
// owned exactly such a direct call before task-10 gave it a clock/sleep
// seam (a function parameter or struct field, defaulting to the real time
// functions, that a test replaces with a fake); this test keeps it that
// way, mechanically, so a future edit that reaches back for time.Sleep
// instead of the seam fails here rather than only in review.
func TestClockSeamSitesHaveNoDirectTimeCalls(t *testing.T) {
	t.Parallel()

	root, err := ParallelGuardModuleRoot()
	if err != nil {
		t.Fatal(err)
	}

	matches, err := FindClockSeamViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		for _, match := range matches {
			t.Logf("clock seam violation: %s", match)
		}
		t.Fatalf("%d direct time.Sleep/time.Now/time.After/time.NewTimer/time.Tick call(s) found in retry/timeout/backoff functions, want 0", len(matches))
	}
}

// TestClockSeamSitesResolveEveryNamedFunction proves every ClockSeamSites
// entry names a function FindClockSeamViolations can actually find, so a
// stale or mistyped entry (which would otherwise silently check nothing)
// surfaces as a test failure instead of a false "clean".
func TestClockSeamSitesResolveEveryNamedFunction(t *testing.T) {
	t.Parallel()

	if len(ClockSeamSites) == 0 {
		t.Fatal("ClockSeamSites is empty; task-10's guard has nothing to check")
	}

	root, err := ParallelGuardModuleRoot()
	if err != nil {
		t.Fatal(err)
	}

	// FindClockSeamViolations itself errors on an unresolved site (see its
	// own doc comment); running it end to end here is enough to prove every
	// entry resolved, in addition to TestClockSeamSitesHaveNoDirectTimeCalls
	// above already exercising the same path.
	if _, err := FindClockSeamViolations(root); err != nil {
		t.Fatalf("a ClockSeamSites entry did not resolve: %v", err)
	}
}
