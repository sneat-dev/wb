package orchestrate

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/progress"
)

func TestCheckObservationOwnerWaitBudgetAndPhase(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		options WorktreeMergeLandOptions
		want    time.Duration
	}{
		{"default", WorktreeMergeLandOptions{}, 8 * time.Minute},
		{"timeout", WorktreeMergeLandOptions{Timeout: time.Second}, time.Second},
		{"slice", WorktreeMergeLandOptions{Timeout: time.Second, WaitSlice: 2 * time.Second}, 2 * time.Second},
		{"capped timeout", WorktreeMergeLandOptions{Timeout: time.Hour}, 8 * time.Minute},
		{"capped slice", WorktreeMergeLandOptions{WaitSlice: time.Hour}, 8 * time.Minute},
		{"negative timeout", WorktreeMergeLandOptions{Timeout: -time.Second}, 8 * time.Minute},
		{"negative slice falls back", WorktreeMergeLandOptions{Timeout: time.Second, WaitSlice: -time.Second}, time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.options.checkWaitSlice(); got != tc.want {
				t.Fatalf("wait budget = %s, want %s", got, tc.want)
			}
		})
	}
	if worktreeMergeCheckPhase("") != "target_checks" || worktreeMergeCheckPhase("17") != "candidate_checks" || worktreeMergeCheckPhase(" ") != "candidate_checks" {
		t.Fatal("check phase changed empty versus supplied PR distinction")
	}
	// Nonpositive intervals use the real default before comparing the wait
	// budget. These options-only refusals do not attest native or hosted CI.
	for _, tc := range []struct {
		name     string
		interval time.Duration
	}{
		{"zero interval defaults before refusal", 0},
		{"negative interval defaults before refusal", -time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options := WorktreeMergeLandOptions{WaitSlice: DefaultCheckPollInterval, CheckPollInterval: tc.interval,
				Progress: func(progress.Event) { t.Fatal("invalid poll budget started CI observation") }}
			got, err := waitForWorktreeMergeChecks(t.Context(), WorktreeMergeReceipt{}, options, "", "", false)
			want := "CI poll interval " + DefaultCheckPollInterval.String() + " must be shorter than wait slice " + DefaultCheckPollInterval.String()
			if err == nil || err.Error() != want || !reflect.DeepEqual(got, PullRequestWaitResult{}) {
				t.Fatalf("default poll refusal = %+v, %v; want empty result and %q", got, err, want)
			}
		})
	}
}

func TestCheckObservationOwnerProgressPreservesObservedBucketsAndCadence(t *testing.T) {
	t.Parallel()
	if reportWorktreeMergeCheckProgress(nil, "candidate_checks") != nil {
		t.Fatal("nil progress reporter must remain nil")
	}
	for _, tc := range []struct {
		name   string
		status PullRequestWaitStatus
		want   progress.State
	}{
		{"pending", PullRequestWaitPending, progress.Waiting},
		{"passed", PullRequestWaitPassed, progress.Completed},
		{"failed", PullRequestWaitFailed, progress.Failed},
		{"unknown", PullRequestWaitStatus("unknown"), progress.Running},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var events []progress.Event
			report := reportWorktreeMergeCheckProgress(func(e progress.Event) { events = append(events, e) }, "target_checks")
			observed := PullRequestWaitProgress{Observation: 4, NextPoll: 100 * time.Millisecond, Result: PullRequestWaitResult{
				Status: tc.status, StableObservations: 1, Reason: "provider reason",
				Checks: []RemoteCheck{{Bucket: "pass"}, {Bucket: "skipping"}, {Bucket: "fail"}, {Bucket: "cancel"}, {Bucket: "pending"}, {Bucket: "unknown"}},
			}}
			report(observed)
			if len(events) != 1 || events[0].State != tc.want || events[0].Phase != "target_checks" || events[0].Operation != "worktree_merge" || events[0].Detail != "poll 4: 2 passed, 2 pending, 2 failed; stable 1/2; next poll in 100ms" {
				t.Fatalf("observed progress = %+v", events)
			}
			observed.Result.Checks, observed.Result.StableObservations, observed.NextPoll = nil, 0, 0
			report(observed)
			if events[1].Detail != "poll 4: 0 passed, 0 pending, 0 failed; provider reason" {
				t.Fatalf("reason fallback = %+v", events[1])
			}
			observed.Result.Reason = " \t "
			report(observed)
			if events[2].Detail != "poll 4: 0 passed, 0 pending, 0 failed" {
				t.Fatalf("blank reason = %+v", events[2])
			}
		})
	}
}

// Result policy is tested with supplied records; this does not attest hosted CI.
func TestCheckObservationOwnerTerminalResultPolicy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		status   PullRequestWaitStatus
		reason   string
		unfenced bool
		want     string
	}{
		{"passed", PullRequestWaitPassed, "stable", false, ""},
		{"pending", PullRequestWaitPending, "jobs registering", false, "exact-head checks remain pending: jobs registering; resume with wb worktree merge resume /private/receipt.json"},
		{"strict fence", PullRequestWaitFailed, "missing strict up-to-date fence", false, "exact-head checks failed: missing strict up-to-date fence; resume with wb worktree merge resume /private/receipt.json --allow-unfenced"},
		{"allowed fence", PullRequestWaitFailed, "missing strict up-to-date fence", true, "exact-head checks failed: missing strict up-to-date fence"},
		{"ordinary failure", PullRequestWaitFailed, "required job failed", false, "exact-head checks failed: required job failed"},
		{"unknown", PullRequestWaitStatus("unrecognized"), "unknown outcome", false, "exact-head checks failed: unknown outcome"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := PullRequestWaitResult{Status: tc.status, Reason: tc.reason, Repository: "acme/app", Head: "exact", StableObservations: 2,
				Checks: []RemoteCheck{{Name: "build", Bucket: "pass"}}, Evidence: map[string]string{"authority": "supplied record"}}
			before := result
			before.Checks = append([]RemoteCheck(nil), result.Checks...)
			before.Evidence = map[string]string{"authority": "supplied record"}
			err := worktreeMergeCheckResultError("/private/receipt.json", tc.unfenced, result)
			if (err == nil) != (tc.want == "") || err != nil && err.Error() != tc.want {
				t.Fatalf("terminal error = %v, want %q", err, tc.want)
			}
			if !reflect.DeepEqual(before, result) {
				t.Fatal("diagnostic phase mutated observed result")
			}
		})
	}
	result := PullRequestWaitResult{Status: PullRequestWaitFailed, Reason: "required check failed", FailureDetails: []CIFailureDetail{{Check: "unit", Excerpt: "compile failed"}}}
	if err := worktreeMergeCheckResultError("receipt", false, result); err == nil || !strings.Contains(err.Error(), "required check failed") || !strings.Contains(err.Error(), "unit") || !strings.Contains(err.Error(), "compile failed") {
		t.Fatalf("first actual finding omitted: %v", err)
	}
}
