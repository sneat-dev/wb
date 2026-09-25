package main

import (
	"strings"
	"testing"
	"time"
)

// AC: cov-rwi-03 unit03 seam list, cmd/wb/wait.go validateWaitBounds.
// A non-positive --interval is rejected independently of --slice, with a
// message naming the flag that is wrong.
func TestValidateWaitBoundsRejectsANonPositiveInterval(t *testing.T) {
	t.Parallel()
	err := validateWaitBounds(time.Minute, 0)
	if err == nil || !strings.Contains(err.Error(), "--interval must be positive") {
		t.Fatalf("err = %v, want a --interval-must-be-positive error", err)
	}
}

// AC: cov-rwi-03 unit03 seam list, cmd/wb/wait.go waitPendingReason.
// waitUntilClosed reports a fixed "still open" reason regardless of the
// observed target's own fields.
func TestWaitPendingReasonReportsStillOpenForWaitUntilClosed(t *testing.T) {
	t.Parallel()
	got := waitPendingReason(waitTarget{}, waitUntilClosed)
	if want := "still open"; got != want {
		t.Errorf("waitPendingReason = %q, want %q", got, want)
	}
}

// The checks-settled default falls back to a placeholder when no check
// counts have been observed at all yet, rather than reporting "0 check(s)
// still running" -- a claim it cannot back up.
func TestWaitPendingReasonReportsNoChecksObservedYetWhenChecksIsNil(t *testing.T) {
	t.Parallel()
	got := waitPendingReason(waitTarget{Checks: nil}, waitUntilChecksSettled)
	if want := "no checks observed yet"; got != want {
		t.Errorf("waitPendingReason = %q, want %q", got, want)
	}
}

// AC: cov-rwi-03 unit03 seam list, cmd/wb/wait.go waitTargetMoved.
// A mergeability flip alone -- head, state, draft and check-set otherwise
// unchanged -- must still count as the target having moved.
func TestWaitTargetMovedDetectsAMergeabilityChangeAlone(t *testing.T) {
	t.Parallel()
	first := waitTarget{Head: "abc", State: "open", Mergeable: "MERGEABLE"}
	latest := waitTarget{Head: "abc", State: "open", Mergeable: "CONFLICTING"}
	if !waitTargetMoved(first, latest) {
		t.Error("waitTargetMoved = false, want true on a mergeability change")
	}
}

func TestWaitTargetMovedIsFalseWhenNothingObservableChanged(t *testing.T) {
	t.Parallel()
	target := waitTarget{Head: "abc", State: "open", Mergeable: "MERGEABLE", Checks: map[string]int{"pending": 1}}
	if waitTargetMoved(target, target) {
		t.Error("waitTargetMoved = true for two identical observations, want false")
	}
}
