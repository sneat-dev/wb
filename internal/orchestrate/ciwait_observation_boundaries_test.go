package orchestrate

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubobserver"
)

func exactWaitFixture() (PullRequestWaitOptions, commitChecksWaitOps) {
	options := PullRequestWaitOptions{Repository: "acme/app", Target: "main", Head: "head", Slice: 5 * time.Second, CheckPollInterval: 10 * time.Millisecond, StableRereadDelay: time.Millisecond}
	ops := commitChecksWaitOps{
		pullRequestIdentity: func(context.Context, string, string) (string, string, string) { return "head", "main", "" },
		targetHead:          func(context.Context, string, string) (string, string) { return "head", "" },
		containsTarget:      func(context.Context, string, string, string) (bool, string) { return true, "" },
		checks: func(context.Context, PullRequestWaitOptions) ([]RemoteCheck, bool, string) {
			return []RemoteCheck{{Name: "check-run:build", Bucket: "pass"}}, false, ""
		},
		required: func(context.Context, PullRequestWaitOptions, *requiredChecksCache) ([]RequiredRemoteCheck, string, string, string, string) {
			return []RequiredRemoteCheck{{Name: "build"}}, "ruleset", "strict", "", ""
		},
		failureDetails: func(context.Context, string, []RemoteCheck) []CIFailureDetail { return []CIFailureDetail{{}} },
	}
	return options, ops
}

func TestExactCommitWaitRejectsInvalidObservationWindow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*PullRequestWaitOptions)
		want   string
	}{
		{"invalid slice", func(options *PullRequestWaitOptions) { options.Slice = 0 }, "check wait slice must be positive"},
		{"missing exact identity", func(options *PullRequestWaitOptions) { options.Head = " " }, "repository, target, and exact head are required"},
		{"nonpositive interval", func(options *PullRequestWaitOptions) { options.CheckPollInterval = 0 }, "check poll interval must be positive"},
		{"interval consumes slice", func(options *PullRequestWaitOptions) { options.CheckPollInterval = options.Slice }, "shorter than the foreground slice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options, ops := exactWaitFixture()
			tc.change(&options)
			result, err := waitForCommitChecksWith(context.Background(), options, ops)
			if err == nil || !strings.Contains(err.Error(), tc.want) || result.Status != "" {
				t.Fatalf("result=%+v error=%v, want error containing %q", result, err, tc.want)
			}
		})
	}
}

func TestExactCommitWaitRefusesDriftAndUnreadableObservations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		pr         bool
		change     func(*PullRequestWaitOptions, *commitChecksWaitOps)
		wantStatus PullRequestWaitStatus
		wantReason string
	}{
		{"target read failed", false, func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			ops.targetHead = func(context.Context, string, string) (string, string) { return "", "fatal target read" }
		}, PullRequestWaitFailed, "fatal target read"},
		{"target advanced", false, func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			ops.targetHead = func(context.Context, string, string) (string, string) { return "other", "" }
		}, PullRequestWaitFailed, "target main advanced"},
		{"descendant ancestry read failed", false, func(options *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			options.AllowTargetDescendant = true
			ops.targetHead = func(context.Context, string, string) (string, string) { return "other", "" }
			ops.containsTarget = func(context.Context, string, string, string) (bool, string) { return false, "fatal ancestry read" }
		}, PullRequestWaitFailed, "fatal ancestry read"},
		{"descendant excludes head", false, func(options *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			options.AllowTargetDescendant = true
			ops.targetHead = func(context.Context, string, string) (string, string) { return "other", "" }
			ops.containsTarget = func(context.Context, string, string, string) (bool, string) { return false, "" }
		}, PullRequestWaitFailed, "does not contain exact landed head"},
		{"PR identity read failed", true, func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			ops.pullRequestIdentity = func(context.Context, string, string) (string, string, string) { return "", "", "fatal PR read" }
		}, PullRequestWaitFailed, "fatal PR read"},
		{"PR head moved", true, func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			ops.pullRequestIdentity = func(context.Context, string, string) (string, string, string) { return "other", "main", "" }
		}, PullRequestWaitFailed, "pull request head drifted"},
		{"PR base moved", true, func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			ops.pullRequestIdentity = func(context.Context, string, string) (string, string, string) { return "head", "release", "" }
		}, PullRequestWaitFailed, "pull request target drifted"},
		{"PR target read failed", true, func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			ops.targetHead = func(context.Context, string, string) (string, string) { return "", "fatal target read" }
		}, PullRequestWaitFailed, "read exact pull-request target head"},
		{"PR ancestry read failed", true, func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			ops.containsTarget = func(context.Context, string, string, string) (bool, string) { return false, "fatal ancestry read" }
		}, PullRequestWaitFailed, "fatal ancestry read"},
		{"PR excludes target", true, func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			ops.containsTarget = func(context.Context, string, string, string) (bool, string) { return false, "" }
		}, PullRequestWaitFailed, "does not contain current target"},
		{"check read failed", false, func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			ops.checks = func(context.Context, PullRequestWaitOptions) ([]RemoteCheck, bool, string) {
				return nil, false, "fatal checks read"
			}
		}, PullRequestWaitFailed, "fatal checks read"},
		{"policy read failed", false, func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			ops.required = func(context.Context, PullRequestWaitOptions, *requiredChecksCache) ([]RequiredRemoteCheck, string, string, string, string) {
				return nil, "", "", "", "fatal policy read"
			}
		}, PullRequestWaitPending, "authority is unavailable"},
		{"PR lacks strict freshness", true, func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			ops.required = func(context.Context, PullRequestWaitOptions, *requiredChecksCache) ([]RequiredRemoteCheck, string, string, string, string) {
				return nil, "ruleset", "", "", ""
			}
		}, PullRequestWaitFailed, "no nonempty server-enforced"},
		{
			"transient PR identity", true, func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
				ops.pullRequestIdentity = func(context.Context, string, string) (string, string, string) {
					return "", "", githubobserver.ErrTransientRetriesExhausted.Error()
				}
			}, PullRequestWaitPending, "pending"},
		{
			"transient PR target", true, func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
				ops.targetHead = func(context.Context, string, string) (string, string) {
					return "", githubobserver.ErrTransientRetriesExhausted.Error()
				}
			}, PullRequestWaitPending, "pending"},
		{
			"transient PR containment", true, func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
				ops.containsTarget = func(context.Context, string, string, string) (bool, string) {
					return false, githubobserver.ErrTransientRetriesExhausted.Error()
				}
			}, PullRequestWaitPending, "pending"},
		{
			"transient direct target", false, func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
				ops.targetHead = func(context.Context, string, string) (string, string) {
					return "", githubobserver.ErrTransientRetriesExhausted.Error()
				}
			}, PullRequestWaitPending, "pending"},
		{
			"transient checks", false, func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
				ops.checks = func(context.Context, PullRequestWaitOptions) ([]RemoteCheck, bool, string) {
					return nil, false, githubobserver.ErrTransientRetriesExhausted.Error()
				}
			}, PullRequestWaitPending, "pending"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options, ops := exactWaitFixture()
			if tc.pr {
				options.PullRequest = "7"
			}
			tc.change(&options, &ops)
			result, err := waitForCommitChecksWith(context.Background(), options, ops)
			if err != nil || result.Status != tc.wantStatus || !strings.Contains(result.Reason, tc.wantReason) {
				t.Fatalf("result=%+v error=%v, want %s reason %q", result, err, tc.wantStatus, tc.wantReason)
			}
		})
	}
}

func TestExactCommitWaitRecordsFailedCheckDetails(t *testing.T) {
	t.Parallel()
	for _, expectedMode := range []bool{false, true} {
		t.Run(fmt.Sprintf("expected=%t", expectedMode), func(t *testing.T) {
			t.Parallel()
			options, ops := exactWaitFixture()
			trace := []string{}
			checks := []RemoteCheck{{Name: "check-run:security", Bucket: "fail", Conclusion: "failure"}}
			if expectedMode {
				options.PullRequest = "7"
				options.ExpectedActionChecks = &ExpectedActionChecks{WorkflowID: 300, Event: "pull_request", PullRequestNumber: 7, PullRequestBase: "main", Names: []string{"build"}}
				checks = append(checks,
					RemoteCheck{Name: "check-run:build", Bucket: "pass", Conclusion: "success", WorkflowID: 300, WorkflowEvent: "pull_request", WorkflowRunID: 15, PullRequestNumber: 7, PullRequestBase: "main"},
					RemoteCheck{Name: "check-run:unrelated", Bucket: "fail"})
				ops.required = func(context.Context, PullRequestWaitOptions, *requiredChecksCache) ([]RequiredRemoteCheck, string, string, string, string) {
					trace = append(trace, "policy")
					// Failure diagnosis precedes freshness refusal even when this fence is missing.
					return []RequiredRemoteCheck{{Name: "security"}}, "ruleset", "", "", ""
				}
			}
			ops.checks = func(context.Context, PullRequestWaitOptions) ([]RemoteCheck, bool, string) { return checks, false, "" }
			ops.failureDetails = func(_ context.Context, _ string, selected []RemoteCheck) []CIFailureDetail {
				trace = append(trace, "details")
				for _, check := range selected {
					if check.Name == "check-run:unrelated" {
						t.Fatal("unrelated failure reached diagnosis")
					}
				}
				return []CIFailureDetail{{Check: "security", Reason: "diagnosed"}}
			}
			options.Progress = func(event PullRequestWaitProgress) {
				if event.Result.Status == PullRequestWaitFailed {
					trace = append(trace, "failed-progress")
				} else {
					trace = append(trace, "progress")
				}
			}
			result, err := waitForCommitChecksWith(context.Background(), options, ops)
			wantTrace := "progress,details,failed-progress"
			if expectedMode {
				wantTrace = "progress,policy,details,failed-progress"
			}
			if err != nil || result.Status != PullRequestWaitFailed || len(result.FailureDetails) != 1 || result.FailureDetails[0].Reason != "diagnosed" || !strings.Contains(result.Reason, "failed or were cancelled") || strings.Join(trace, ",") != wantTrace {
				t.Fatalf("result=%+v error=%v trace=%v, want %s", result, err, trace, wantTrace)
			}
			if expectedMode && (result.RequiredChecksAuthority != "ruleset" || len(result.RequiredChecks) != 1 || result.RequiredChecks[0].Name != "security") {
				t.Fatalf("policy not adopted before diagnosis: %+v", result)
			}
		})
	}
}

func TestExactCommitWaitRechecksPolicyAndIdentityBeforePassing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		pr     bool
		change func(*commitChecksWaitOps)
		status PullRequestWaitStatus
		want   string
	}{
		{"stable direct target", false, func(*commitChecksWaitOps) {}, PullRequestWaitPassed, "stayed terminal"},
		{"required policy changed", false, func(ops *commitChecksWaitOps) {
			ops.required = func(_ context.Context, _ PullRequestWaitOptions, cache *requiredChecksCache) ([]RequiredRemoteCheck, string, string, string, string) {
				if cache == nil {
					return []RequiredRemoteCheck{{Name: "new-check"}}, "ruleset", "strict", "", ""
				}
				return []RequiredRemoteCheck{{Name: "build"}}, "ruleset", "strict", "", ""
			}
		}, PullRequestWaitPending, "have not registered"},
		{"final PR head moved", true, func(ops *commitChecksWaitOps) {
			calls := 0
			ops.pullRequestIdentity = func(context.Context, string, string) (string, string, string) {
				calls++
				if calls >= 3 {
					return "other", "main", ""
				}
				return "head", "main", ""
			}
		}, PullRequestWaitFailed, "identity changed after checks passed"},
		{"final PR target moved", true, func(ops *commitChecksWaitOps) {
			calls := 0
			ops.targetHead = func(context.Context, string, string) (string, string) {
				calls++
				if calls >= 3 {
					return "other", ""
				}
				return "head", ""
			}
		}, PullRequestWaitFailed, "advanced after checks passed"},
		{"final direct target moved", false, func(ops *commitChecksWaitOps) {
			calls := 0
			ops.targetHead = func(context.Context, string, string) (string, string) {
				calls++
				if calls >= 3 {
					return "other", ""
				}
				return "head", ""
			}
		}, PullRequestWaitFailed, "target advanced after checks passed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options, ops := exactWaitFixture()
			if tc.pr {
				options.PullRequest = "7"
			}
			tc.change(&ops)
			result, err := waitForCommitChecksWith(context.Background(), options, ops)
			if err != nil || result.Status != tc.status || !strings.Contains(result.Reason, tc.want) {
				t.Fatalf("result=%+v error=%v, want %s reason %q", result, err, tc.status, tc.want)
			}
		})
	}
}

func TestExactCommitWaitNamesChecksThatCannotYetAuthorizeLanding(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*PullRequestWaitOptions, *commitChecksWaitOps)
		want   string
	}{
		{"required producer absent", func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			ops.required = func(context.Context, PullRequestWaitOptions, *requiredChecksCache) ([]RequiredRemoteCheck, string, string, string, string) {
				return []RequiredRemoteCheck{{Name: "build"}, {Name: "security"}}, "ruleset", "strict", "", ""
			}
		}, "security"},
		{"no checks registered", func(options *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			options.PullRequest = "7"
			ops.checks = func(context.Context, PullRequestWaitOptions) ([]RemoteCheck, bool, string) { return nil, false, "" }
			ops.required = func(context.Context, PullRequestWaitOptions, *requiredChecksCache) ([]RequiredRemoteCheck, string, string, string, string) {
				return nil, "ruleset", "strict", "", ""
			}
		}, "no GitHub checks have registered"},
		{"producer still running", func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			ops.checks = func(context.Context, PullRequestWaitOptions) ([]RemoteCheck, bool, string) {
				return []RemoteCheck{{Name: "check-run:build", Bucket: "pending"}}, true, ""
			}
		}, "still pending"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options, ops := exactWaitFixture()
			options.Slice, options.CheckPollInterval = time.Second, 5*time.Millisecond
			tc.change(&options, &ops)
			result, err := waitForCommitChecksWith(context.Background(), options, ops)
			if err != nil || result.Status != PullRequestWaitPending || !strings.Contains(result.Reason, tc.want) {
				t.Fatalf("result=%+v error=%v, want pending reason %q", result, err, tc.want)
			}
		})
	}
}

func TestExactCommitWaitRefusesLateAuthorityAndIdentityLoss(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		pr     bool
		change func(*PullRequestWaitOptions, *commitChecksWaitOps)
		status PullRequestWaitStatus
		want   string
	}{
		{"final freshness lost", true, func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			ops.required = func(_ context.Context, _ PullRequestWaitOptions, cache *requiredChecksCache) ([]RequiredRemoteCheck, string, string, string, string) {
				freshness := "strict"
				if cache == nil {
					freshness = ""
				}
				return []RequiredRemoteCheck{{Name: "build"}}, "ruleset", freshness, "", ""
			}
		}, PullRequestWaitFailed, "no nonempty server-enforced"},
		{"final transient PR identity", true, func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			calls := 0
			ops.pullRequestIdentity = func(context.Context, string, string) (string, string, string) {
				calls++
				if calls >= 3 {
					return "", "", githubobserver.ErrTransientRetriesExhausted.Error()
				}
				return "head", "main", ""
			}
		}, PullRequestWaitPending, "terminal checks require"},
		{"final transient PR target", true, func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			calls := 0
			ops.targetHead = func(context.Context, string, string) (string, string) {
				calls++
				if calls >= 3 {
					return "", githubobserver.ErrTransientRetriesExhausted.Error()
				}
				return "head", ""
			}
		}, PullRequestWaitPending, "terminal checks require"},
		{"final transient direct target", false, func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			calls := 0
			ops.targetHead = func(context.Context, string, string) (string, string) {
				calls++
				if calls >= 3 {
					return "", githubobserver.ErrTransientRetriesExhausted.Error()
				}
				return "head", ""
			}
		}, PullRequestWaitPending, "terminal checks require"},
		{"final policy unavailable", false, func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			ops.required = func(_ context.Context, _ PullRequestWaitOptions, cache *requiredChecksCache) ([]RequiredRemoteCheck, string, string, string, string) {
				if cache == nil {
					return nil, "", "", "", "final policy read failed"
				}
				return []RequiredRemoteCheck{{Name: "build"}}, "ruleset", "strict", "", ""
			}
		}, PullRequestWaitPending, "authority is unavailable"},
		{"final PR read failed", true, func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			calls := 0
			ops.pullRequestIdentity = func(context.Context, string, string) (string, string, string) {
				calls++
				if calls >= 3 {
					return "", "", "final PR read failed"
				}
				return "head", "main", ""
			}
		}, PullRequestWaitFailed, "final PR read failed"},
		{"final PR target read failed", true, func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			calls := 0
			ops.targetHead = func(context.Context, string, string) (string, string) {
				calls++
				if calls >= 3 {
					return "", "final target read failed"
				}
				return "head", ""
			}
		}, PullRequestWaitFailed, "re-read exact pull-request target head"},
		{"final direct target read failed", false, func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			calls := 0
			ops.targetHead = func(context.Context, string, string) (string, string) {
				calls++
				if calls >= 3 {
					return "", "final target read failed"
				}
				return "head", ""
			}
		}, PullRequestWaitFailed, "final target read failed"},
		{"final descendant excludes head", false, func(options *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			options.AllowTargetDescendant = true
			calls := 0
			ops.targetHead = func(context.Context, string, string) (string, string) {
				calls++
				if calls >= 3 {
					return "other", ""
				}
				return "head", ""
			}
			ops.containsTarget = func(context.Context, string, string, string) (bool, string) { return false, "" }
		}, PullRequestWaitFailed, "does not contain exact landed head"},
		{"final descendant ancestry read fails", false, func(options *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			options.AllowTargetDescendant = true
			calls := 0
			ops.targetHead = func(context.Context, string, string) (string, string) {
				calls++
				if calls >= 3 {
					return "other", ""
				}
				return "head", ""
			}
			ops.containsTarget = func(context.Context, string, string, string) (bool, string) {
				return false, "final ancestry read failed"
			}
		}, PullRequestWaitFailed, "final ancestry read failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options, ops := exactWaitFixture()
			if tc.pr {
				options.PullRequest = "7"
			}
			tc.change(&options, &ops)
			result, err := waitForCommitChecksWith(context.Background(), options, ops)
			if err != nil || result.Status != tc.status || !strings.Contains(result.Reason, tc.want) {
				t.Fatalf("result=%+v error=%v, want %s reason %q", result, err, tc.status, tc.want)
			}
		})
	}
}

func TestExactCommitWaitExplainsEachTerminalReceiptMode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*PullRequestWaitOptions, *commitChecksWaitOps)
		want   string
	}{
		{"fenced PR", func(options *PullRequestWaitOptions, _ *commitChecksWaitOps) {
			options.PullRequest = "7"
		}, "server-side target freshness was enforced"},
		{"unfenced PR with unavailable policy", func(options *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			options.PullRequest, options.AllowUnfenced = "7", true
			ops.required = func(context.Context, PullRequestWaitOptions, *requiredChecksCache) ([]RequiredRemoteCheck, string, string, string, string) {
				return []RequiredRemoteCheck{{Name: "build"}}, "ruleset", "", "policy unavailable", ""
			}
		}, "policy authority was unavailable under explicit --allow-unfenced"},
		{"empty direct-target policy", func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			ops.checks = func(context.Context, PullRequestWaitOptions) ([]RemoteCheck, bool, string) { return nil, false, "" }
			ops.required = func(context.Context, PullRequestWaitOptions, *requiredChecksCache) ([]RequiredRemoteCheck, string, string, string, string) {
				return nil, "ruleset", "", "", ""
			}
		}, "enumerated as empty"},
		{"unfenced validation PR", func(options *PullRequestWaitOptions, _ *commitChecksWaitOps) {
			options.PullRequest, options.AllowUnfenced = "7", true
		}, "validation-only publication"},
		{"direct target with unavailable policy", func(_ *PullRequestWaitOptions, ops *commitChecksWaitOps) {
			ops.required = func(context.Context, PullRequestWaitOptions, *requiredChecksCache) ([]RequiredRemoteCheck, string, string, string, string) {
				return []RequiredRemoteCheck{{Name: "build"}}, "ruleset", "", "policy unavailable", ""
			}
		}, "policy authority was unavailable under explicit --allow-unfenced"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options, ops := exactWaitFixture()
			tc.change(&options, &ops)
			result, err := waitForCommitChecksWith(context.Background(), options, ops)
			if err != nil || result.Status != PullRequestWaitPassed || !strings.Contains(result.Reason, tc.want) {
				t.Fatalf("result=%+v error=%v, want passed reason containing %q", result, err, tc.want)
			}
		})
	}
}

func TestExactCommitWaitStopsCancelledObservationAndPoll(t *testing.T) {
	t.Parallel()
	for _, atPoll := range []bool{false, true} {
		t.Run(fmt.Sprintf("poll=%t", atPoll), func(t *testing.T) {
			t.Parallel()
			options, ops := exactWaitFixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if atPoll {
				options.Progress = func(event PullRequestWaitProgress) {
					if event.NextPoll > 0 {
						cancel()
					}
				}
			} else {
				cancel()
			}
			result, err := waitForCommitChecksWith(ctx, options, ops)
			if err != nil || result.Status != PullRequestWaitFailed || result.Reason != context.Canceled.Error() {
				t.Fatalf("result=%+v error=%v", result, err)
			}
		})
	}
}

func TestExactCommitWaitReturnsPendingForExpiredObservation(t *testing.T) {
	t.Parallel()
	options, ops := exactWaitFixture()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	t.Cleanup(cancel)
	result, err := waitForCommitChecksWith(ctx, options, ops)
	if err != nil || result.Status != PullRequestWaitPending || result.ObservedHead != "" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestExactCommitWaitNamesExpectedJobsThatAreMissingOrRejected(t *testing.T) {
	t.Parallel()
	for _, rejected := range []bool{false, true} {
		t.Run(fmt.Sprintf("rejected=%t", rejected), func(t *testing.T) {
			t.Parallel()
			options, ops := exactWaitFixture()
			options.Slice, options.CheckPollInterval = time.Second, 900*time.Millisecond
			options.ExpectedActionChecks = &ExpectedActionChecks{WorkflowID: 300, Event: "pull_request", PullRequestNumber: 7, PullRequestBase: "main", Names: []string{"build"}}
			ops.required = func(context.Context, PullRequestWaitOptions, *requiredChecksCache) ([]RequiredRemoteCheck, string, string, string, string) {
				return nil, "ruleset", "", "", ""
			}
			ops.checks = func(context.Context, PullRequestWaitOptions) ([]RemoteCheck, bool, string) {
				if rejected {
					return []RemoteCheck{{Name: "check-run:build", Bucket: "fail", Conclusion: "failure", WorkflowID: 300, WorkflowEvent: "pull_request", WorkflowRunID: 15, PullRequestNumber: 7, PullRequestBase: "main"}}, false, ""
				}
				return nil, false, ""
			}
			result, err := waitForCommitChecksWith(context.Background(), options, ops)
			wantStatus, wantReason := PullRequestWaitPending, "expected Actions jobs have not registered"
			if rejected {
				wantStatus, wantReason = PullRequestWaitFailed, "expected CI jobs did not execute successfully"
			}
			if err != nil || result.Status != wantStatus || !strings.Contains(result.Reason, wantReason) {
				t.Fatalf("result=%+v error=%v", result, err)
			}
		})
	}
}

func TestExactCommitWaitAdoptsFreshPolicyBeforeFinalIdentity(t *testing.T) {
	t.Parallel()
	for _, pr := range []bool{false, true} {
		t.Run(fmt.Sprintf("pr=%t", pr), func(t *testing.T) {
			t.Parallel()
			options, ops := exactWaitFixture()
			if pr {
				options.PullRequest = "7"
			}
			trace := []string{}
			ops.required = func(_ context.Context, _ PullRequestWaitOptions, cache *requiredChecksCache) ([]RequiredRemoteCheck, string, string, string, string) {
				if cache == nil {
					trace = append(trace, "fresh-policy")
				} else {
					trace = append(trace, "cached-policy")
				}
				return []RequiredRemoteCheck{{Name: "build"}}, "ruleset", "strict", "", ""
			}
			ops.pullRequestIdentity = func(context.Context, string, string) (string, string, string) {
				trace = append(trace, "pr")
				return "head", "main", ""
			}
			ops.targetHead = func(context.Context, string, string) (string, string) {
				trace = append(trace, "target")
				return "head", ""
			}
			result, err := waitForCommitChecksWith(context.Background(), options, ops)
			wantSuffix := "fresh-policy,target"
			if pr {
				wantSuffix = "fresh-policy,pr,target"
			}
			if err != nil || result.Status != PullRequestWaitPassed || !strings.HasSuffix(strings.Join(trace, ","), wantSuffix) || result.RequiredChecksAuthority != "ruleset" || result.TargetFreshnessAuthority != "strict" {
				t.Fatalf("result=%+v error=%v trace=%v", result, err, trace)
			}
		})
	}
}
