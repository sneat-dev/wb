package orchestrate

import (
	"context"
	"strings"
	"testing"
	"time"
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
	options, ops := exactWaitFixture()
	ops.checks = func(context.Context, PullRequestWaitOptions) ([]RemoteCheck, bool, string) {
		return []RemoteCheck{{Name: "check-run:build", Bucket: "fail"}}, false, ""
	}
	result, err := waitForCommitChecksWith(context.Background(), options, ops)
	if err != nil || result.Status != PullRequestWaitFailed || len(result.FailureDetails) != 1 || !strings.Contains(result.Reason, "failed or were cancelled") {
		t.Fatalf("result=%+v error=%v", result, err)
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
