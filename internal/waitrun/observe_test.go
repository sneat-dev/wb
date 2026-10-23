package waitrun

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/githubobserver"
	"strings"
	"testing"
	"time"
)

func waitTestRun(t *testing.T, condition Condition, targets []Reference, observations ...map[string]Target) Output {
	t.Helper()
	call := 0
	clock := time.Unix(0, 0)
	return observeWaitTargets(context.Background(), waitRun{
		Targets:   targets,
		Condition: condition,
		Slice:     time.Minute,
		Interval:  time.Second,
		observe: func(_ context.Context, reference Reference) Target {
			index := call
			if index >= len(observations) {
				index = len(observations) - 1
			}
			observed, ok := observations[index][reference.Selector]
			if !ok {
				t.Fatalf("no observation %d prepared for %s", index, reference.Selector)
			}
			observed.Selector = reference.Selector
			observed.Repository = reference.Repository
			observed.Number = reference.Number
			return observed
		},
		now: func() time.Time { return clock },
		sleep: func(context.Context, time.Duration) error {
			call++
			clock = clock.Add(time.Second)
			if call > len(observations) {
				clock = clock.Add(time.Hour)
			}
			return nil
		},
	})
}

func waitRef(selector string) Reference {
	repository, number, _ := strings.Cut(selector, "#")
	return Reference{Selector: selector, Repository: repository, Number: number}
}

func TestWaitPRSettlesOnlyWhenEveryTargetIsTerminal(t *testing.T) {
	t.Parallel()
	targets := []Reference{waitRef("acme/app#1"), waitRef("acme/app#2")}
	// The first target is terminal from the start. A first-past-the-post wait
	// would return here and starve the second, which is the defect this
	// contract exists to prevent.
	output := waitTestRun(t, ChecksSettled, targets,
		map[string]Target{
			"acme/app#1": {State: "open", Checks: map[string]int{"pass": 3}},
			"acme/app#2": {State: "open", Checks: map[string]int{"pass": 1, "pending": 2}},
		},
		map[string]Target{
			"acme/app#2": {State: "open", Checks: map[string]int{"pass": 3}},
		},
	)
	if output.Status != Settled {
		t.Fatalf("status = %q, want settled; targets = %+v", output.Status, output.Targets)
	}
	if output.Observations < 2 {
		t.Errorf("observations = %d, want at least 2: the second target was still pending", output.Observations)
	}
	for _, target := range output.Targets {
		if target.Status != Settled {
			t.Errorf("%s status = %q, want settled", target.Selector, target.Status)
		}
	}
}

func TestWaitPRPendingRetainsTheUnsettledTargets(t *testing.T) {
	t.Parallel()
	targets := []Reference{waitRef("acme/app#1"), waitRef("acme/app#2")}
	output := waitTestRun(t, ChecksSettled, targets,
		map[string]Target{
			"acme/app#1": {State: "merged", Checks: map[string]int{"pass": 2}},
			"acme/app#2": {State: "open", Checks: map[string]int{"pending": 1}},
		},
	)
	if output.Status != Pending {
		t.Fatalf("status = %q, want pending", output.Status)
	}
	if len(output.Targets) != 2 || output.Targets[0].Status != Settled || output.Targets[1].Status != Pending {
		t.Fatal(output.Targets)
	}
}

func TestWaitPRShortensTheFinalDelayToFitWithinTheRemainingSlice(t *testing.T) {
	t.Parallel()
	targets := []Reference{waitRef("acme/app#1")}
	clock := time.Unix(0, 0)
	var observedDelay time.Duration
	output := observeWaitTargets(context.Background(), waitRun{
		Targets:   targets,
		Condition: ChecksSettled,
		Slice:     2 * time.Second,
		Interval:  10 * time.Second, // longer than the remaining slice below
		observe: func(_ context.Context, reference Reference) Target {
			return Target{Selector: reference.Selector, State: "open", Checks: map[string]int{"pending": 1}}
		},
		now: func() time.Time { return clock },
		sleep: func(_ context.Context, delay time.Duration) error {
			observedDelay = delay
			clock = clock.Add(time.Hour) // ends the loop on the next iteration
			return nil
		},
	})
	if output.Status != Pending {
		t.Fatalf("status = %q, want pending", output.Status)
	}
	if observedDelay != 2*time.Second {
		t.Fatalf("pause delay = %s, want it shortened to the remaining slice (2s), not the full interval (10s)", observedDelay)
	}
}

func TestWaitPRChangedComparesOnlyActionableFacts(t *testing.T) {
	t.Parallel()
	targets := []Reference{waitRef("acme/app#7")}
	// An identical re-observation is not a change; a new head is.
	unchanged := waitTestRun(t, Changed, targets,
		map[string]Target{"acme/app#7": {State: "open", Head: "aaa", Checks: map[string]int{"pass": 1}}},
	)
	if unchanged.Status != Pending {
		t.Errorf("re-reporting the same state settled --until changed: %+v", unchanged.Targets)
	}
	moved := waitTestRun(t, Changed, targets,
		map[string]Target{"acme/app#7": {State: "open", Head: "aaa", Checks: map[string]int{"pending": 1}}},
		map[string]Target{"acme/app#7": {State: "open", Head: "bbb", Checks: map[string]int{"pending": 1}}},
	)
	if moved.Status != Settled {
		t.Errorf("a new head did not settle --until changed: %+v", moved.Targets)
	}
}

func TestWaitPRClosedIgnoresChecks(t *testing.T) {
	t.Parallel()
	output := waitTestRun(t, Closed, []Reference{waitRef("acme/app#9")},
		map[string]Target{"acme/app#9": {State: "merged", Checks: map[string]int{"pending": 4}}},
	)
	if output.Status != Settled {
		t.Fatalf("a merged pull request with pending checks did not settle --until closed: %+v", output.Targets)
	}
}

func TestWaitPRTransientReadStaysPendingRatherThanErroring(t *testing.T) {
	t.Parallel()
	transient := waitReadFailure(Target{Selector: "acme/app#3"}, githubobserver.ErrTransientRetriesExhausted)
	if transient.Status != Pending {
		t.Errorf("transient read status = %q, want pending so the wait resumes", transient.Status)
	}
	wrapped := waitReadFailure(Target{Selector: "acme/app#3"}, errors.New("read pull request: "+githubobserver.ErrTransientRetriesExhausted.Error()))
	if wrapped.Status != Pending {
		t.Errorf("wrapped transient read status = %q, want pending", wrapped.Status)
	}
	authoritative := waitReadFailure(Target{Selector: "acme/app#3"}, errors.New("HTTP 404: Not Found"))
	if authoritative.Status != ReadError {
		t.Errorf("authoritative failure status = %q, want error", authoritative.Status)
	}
}

func TestWaitPRErroredTargetNeverSettles(t *testing.T) {
	t.Parallel()
	output := waitTestRun(t, Closed, []Reference{waitRef("acme/app#4")},
		map[string]Target{"acme/app#4": {Status: ReadError, Reason: "HTTP 404"}},
	)
	if output.Status == Settled {
		t.Fatal("a target WB could not read was reported as settled")
	}
}

func TestWaitPendingReasonReportsStillOpenForWaitUntilClosed(t *testing.T) {
	t.Parallel()
	got := waitPendingReason(Target{}, Closed)
	if want := "still open"; got != want {
		t.Errorf("waitPendingReason = %q, want %q", got, want)
	}
}

func TestWaitPendingReasonReportsNoChecksObservedYetWhenChecksIsNil(t *testing.T) {
	t.Parallel()
	got := waitPendingReason(Target{Checks: nil}, ChecksSettled)
	if want := "no checks observed yet"; got != want {
		t.Errorf("waitPendingReason = %q, want %q", got, want)
	}
}

func TestWaitTargetMovedDetectsAMergeabilityChangeAlone(t *testing.T) {
	t.Parallel()
	first := Target{Head: "abc", State: "open", Mergeable: "MERGEABLE"}
	latest := Target{Head: "abc", State: "open", Mergeable: "CONFLICTING"}
	if !targetMoved(first, latest) {
		t.Error("targetMoved = false, want true on a mergeability change")
	}
}

func TestWaitTargetMovedIsFalseWhenNothingObservableChanged(t *testing.T) {
	t.Parallel()
	target := Target{Head: "abc", State: "open", Mergeable: "MERGEABLE", Checks: map[string]int{"pending": 1}}
	if targetMoved(target, target) {
		t.Error("targetMoved = true for two identical observations, want false")
	}
}
func TestDefaultTimerClockCancellationAndLatestOutput(t *testing.T) {
	t.Parallel()
	for _, cancel := range []bool{false, true} {
		ctx, stop := context.WithCancel(t.Context())
		calls := 0
		observer := Observer{Read: func(got context.Context, ref Reference) Target {
			if got != ctx {
				t.Fatal("context not forwarded")
			}
			calls++
			if cancel {
				stop()
				return Target{}
			}
			state := "open"
			if calls == 2 {
				state = "closed"
			}
			return Target{Selector: ref.Selector, State: state, Checks: map[string]int{"pending": 1}}
		}}
		interval := time.Nanosecond
		if cancel {
			interval = time.Second
		}
		out := observer.Wait(ctx, Request{Targets: []Reference{{Selector: "acme/app#1", Repository: "acme/app", Number: "1"}}, Slice: time.Second, Interval: interval})
		stop()
		if out.ObservedAt.Location() != time.UTC || len(out.Targets) != 1 {
			t.Fatal(out)
		}
		if cancel {
			if calls != 1 || out.Status != Pending || out.Targets[0].Selector != "acme/app#1" {
				t.Fatal(out, calls)
			}
		} else if calls != 2 || out.Status != Settled {
			t.Fatal(out, calls)
		}
	}
}

func TestObserverProgressCountsSettledTargetsAndDoesNotReadThemAgain(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0)
	reads := map[string]int{}
	updates := []Update{}
	observer := Observer{Now: func() time.Time { return now }, Sleep: func(_ context.Context, delay time.Duration) error { now = now.Add(delay); return nil }, Read: func(_ context.Context, ref Reference) Target {
		reads[ref.Selector]++
		state := "OPEN"
		if ref.Selector == "first" || reads[ref.Selector] == 2 {
			state = "CLOSED"
		}
		return Target{Selector: ref.Selector, State: state}
	}}
	out := observer.Wait(context.Background(), Request{Targets: []Reference{{Selector: "first"}, {Selector: "second"}}, Condition: Closed, Slice: time.Minute, Interval: time.Second, Progress: func(update Update) { updates = append(updates, update) }})
	if out.Status != Settled || out.Observations != 2 || reads["first"] != 1 || reads["second"] != 2 || len(updates) != 1 || updates[0] != (Update{Observations: 1, Settled: 1, Total: 2, Interval: time.Second}) {
		t.Fatal(out, reads, updates)
	}
}
