package orchestrate

import (
	"testing"
	"time"
)

// TestWorktreeMergeLandOptionsCheckWaitSliceBoundsOnlyTheWait pins the budget
// of one exact-head check wait: WaitSlice wins when set, Timeout is the
// fallback, and either is capped at 8 minutes.
func TestWorktreeMergeLandOptionsCheckWaitSliceBoundsOnlyTheWait(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		options WorktreeMergeLandOptions
		want    time.Duration
	}{
		{"timeout is the wait when no wait slice is set", WorktreeMergeLandOptions{Timeout: 30 * time.Second}, 30 * time.Second},
		{"wait slice overrides a longer timeout", WorktreeMergeLandOptions{Timeout: 15 * time.Second, WaitSlice: 200 * time.Millisecond}, 200 * time.Millisecond},
		{"no limits means the 8 minute cap", WorktreeMergeLandOptions{}, 8 * time.Minute},
		{"a timeout above the cap is capped", WorktreeMergeLandOptions{Timeout: time.Hour}, 8 * time.Minute},
		{"a wait slice above the cap is capped", WorktreeMergeLandOptions{Timeout: time.Second, WaitSlice: time.Hour}, 8 * time.Minute},
	}
	for _, tc := range cases {
		if got := tc.options.checkWaitSlice(); got != tc.want {
			t.Errorf("%s: checkWaitSlice() = %s, want %s", tc.name, got, tc.want)
		}
	}
}
