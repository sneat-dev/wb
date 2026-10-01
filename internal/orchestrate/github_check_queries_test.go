package orchestrate

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func fakePullRequestCheckOps(t *testing.T) pullRequestCheckOps {
	t.Helper()
	return pullRequestCheckOps{
		read: func(context.Context, string, string) (PullRequestView, error) { return prUpdateView(t, "head"), nil },
		runs: func(context.Context, PullRequestWaitOptions) ([]RemoteCheck, bool, string) {
			return []RemoteCheck{{Name: "check-run:build", Bucket: "fail"}}, false, ""
		},
		statuses: func(context.Context, PullRequestWaitOptions) ([]RemoteCheck, bool, string) {
			return []RemoteCheck{{Name: "status:lint", Bucket: "pass"}}, false, ""
		},
		required: func(context.Context, string, string, bool) ([]RequiredRemoteCheck, string, string) {
			return []RequiredRemoteCheck{{Name: "build"}, {Name: "lint"}}, "", ""
		},
		details: func(_ context.Context, repository string, checks []RemoteCheck) []CIFailureDetail {
			if repository != "acme/app" || len(checks) != 2 || checks[0].Name != "check-run:build" {
				t.Fatalf("failure details received wrong observations: %q %+v", repository, checks)
			}
			return []CIFailureDetail{{}}
		},
	}
}

func TestPullRequestFailureDetailsUsesExactHeadObservations(t *testing.T) {
	t.Parallel()
	ops := fakePullRequestCheckOps(t)
	oldRuns := ops.runs
	ops.runs = func(ctx context.Context, options PullRequestWaitOptions) ([]RemoteCheck, bool, string) {
		if options.Repository != "acme/app" || options.Target != "main" || options.Head != "head" {
			t.Fatalf("wrong check identity: %+v", options)
		}
		return oldRuns(ctx, options)
	}
	details, err := pullRequestFailureDetailsWith(context.Background(), "acme/app", "7", ops)
	if err != nil || len(details) != 1 {
		t.Fatalf("details=%+v error=%v", details, err)
	}
}

func TestPublicCheckQueriesRejectMissingPullRequestIdentity(t *testing.T) {
	t.Parallel()
	if _, err := PullRequestFailureDetails(context.Background(), "acme/app", ""); err == nil {
		t.Fatal("failure details accepted an empty PR selector")
	}
}

func TestPullRequestFailureDetailsRefusesIncompleteReads(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		change func(*pullRequestCheckOps)
		want   string
	}{
		{"PR read", func(ops *pullRequestCheckOps) {
			ops.read = func(context.Context, string, string) (PullRequestView, error) {
				return PullRequestView{}, errors.New("PR unavailable")
			}
		}, "PR unavailable"},
		{"check runs", func(ops *pullRequestCheckOps) {
			ops.runs = func(context.Context, PullRequestWaitOptions) ([]RemoteCheck, bool, string) {
				return nil, false, "runs unavailable"
			}
		}, "runs unavailable"},
		{"commit statuses", func(ops *pullRequestCheckOps) {
			ops.statuses = func(context.Context, PullRequestWaitOptions) ([]RemoteCheck, bool, string) {
				return nil, false, "statuses unavailable"
			}
		}, "statuses unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ops := fakePullRequestCheckOps(t)
			ops.details = func(context.Context, string, []RemoteCheck) []CIFailureDetail {
				t.Fatal("incomplete observations reached details")
				return nil
			}
			tc.change(&ops)
			_, err := pullRequestFailureDetailsWith(context.Background(), "acme/app", "7", ops)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestObservePullRequestHeadNamesOnlyMissingOrFailedProducersFromTheSameReads(t *testing.T) {
	t.Parallel()
	ops := fakePullRequestCheckOps(t)
	reads := map[string]int{}
	countRuns, countStatuses := ops.runs, ops.statuses
	ops.runs = func(ctx context.Context, options PullRequestWaitOptions) ([]RemoteCheck, bool, string) {
		reads["runs"]++
		return countRuns(ctx, options)
	}
	ops.statuses = func(ctx context.Context, options PullRequestWaitOptions) ([]RemoteCheck, bool, string) {
		reads["statuses"]++
		return countStatuses(ctx, options)
	}
	ops.required = func(ctx context.Context, repository, target string, fresh bool) ([]RequiredRemoteCheck, string, string) {
		reads["required"]++
		return []RequiredRemoteCheck{{Name: "missing"}, {Name: "lint"}, {Name: "build"}}, "", ""
	}
	view := prUpdateView(t, "head")
	observation, err := observePullRequestHeadWith(context.Background(), "acme/app", view, ops)
	if err != nil || observation.Green || !reflect.DeepEqual(observation.Blocked, []string{"build", "missing"}) || len(observation.Checks) != 2 {
		t.Fatalf("observation=%+v error=%v", observation, err)
	}
	if !reflect.DeepEqual(reads, map[string]int{"runs": 1, "statuses": 1, "required": 1}) {
		t.Fatalf("naming the gap re-read: %v", reads)
	}
	ops.required = func(context.Context, string, string, bool) ([]RequiredRemoteCheck, string, string) {
		return nil, "", ""
	}
	observation, err = observePullRequestHeadWith(context.Background(), "acme/app", view, ops)
	if err != nil || len(observation.Blocked) != 0 || observation.Green {
		t.Fatalf("unruled branch with a failed check: %+v error=%v", observation, err)
	}
}

func headOps(t *testing.T, runs []RemoteCheck, required []RequiredRemoteCheck) pullRequestCheckOps {
	t.Helper()
	ops := fakePullRequestCheckOps(t)
	ops.runs = func(context.Context, PullRequestWaitOptions) ([]RemoteCheck, bool, string) { return runs, false, "" }
	ops.statuses = func(context.Context, PullRequestWaitOptions) ([]RemoteCheck, bool, string) { return nil, false, "" }
	ops.required = func(context.Context, string, string, bool) ([]RequiredRemoteCheck, string, string) {
		return required, "", ""
	}
	return ops
}

// TestObservePullRequestHeadWeighsRequiredChecksAgainstTheObservedOnes pins the
// verdict rules: a required check with no passing observation blocks even when
// every observed run passed; with two observations under one name the sorted
// order decides (a `fail` sorts before a `pass`, and the first one counts); and
// a check pinned to one GitHub App is satisfied only by that app.
func TestObservePullRequestHeadWeighsRequiredChecksAgainstTheObservedOnes(t *testing.T) {
	t.Parallel()
	view := prUpdateView(t, "head")
	for name, test := range map[string]struct {
		runs     []RemoteCheck
		required []RequiredRemoteCheck
		green    bool
		blocked  []string
	}{
		"all passed but a required one is missing": {
			runs:     []RemoteCheck{{Name: "check-run:present", Bucket: "pass"}},
			required: []RequiredRemoteCheck{{Name: "present"}, {Name: "missing"}},
			blocked:  []string{"missing"},
		},
		"a fail and a pass under one name: the fail sorts first": {
			runs:     []RemoteCheck{{Name: "check-run:build", Bucket: "pass"}, {Name: "check-run:build", Bucket: "fail"}},
			required: []RequiredRemoteCheck{{Name: "build"}},
			blocked:  []string{"build"},
		},
		"a skipped and a pass under one name: the pass sorts first": {
			runs:     []RemoteCheck{{Name: "check-run:build", Bucket: "skipping"}, {Name: "check-run:build", Bucket: "pass"}},
			required: []RequiredRemoteCheck{{Name: "build"}},
			green:    true,
		},
		"a pinned check passed by its app": {
			runs:     []RemoteCheck{{Name: "check-run:build", Bucket: "pass", AppID: 7}},
			required: []RequiredRemoteCheck{{Name: "build", IntegrationID: 7}},
			green:    true,
		},
		"a pinned check passed by another app": {
			runs:     []RemoteCheck{{Name: "check-run:build", Bucket: "pass", AppID: 8}},
			required: []RequiredRemoteCheck{{Name: "build", IntegrationID: 7}},
			blocked:  []string{"build"},
		},
	} {
		observation, err := observePullRequestHeadWith(context.Background(), "acme/app", view, headOps(t, test.runs, test.required))
		if err != nil || observation.Green != test.green || !reflect.DeepEqual(observation.Blocked, test.blocked) {
			t.Errorf("%s: green=%v blocked=%v error=%v, want %v %v", name, observation.Green, observation.Blocked, err, test.green, test.blocked)
		}
	}
}

func TestObservePullRequestHeadRefusesIncompleteReads(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		change func(*pullRequestCheckOps)
		want   string
	}{
		{"required checks", func(ops *pullRequestCheckOps) {
			ops.required = func(context.Context, string, string, bool) ([]RequiredRemoteCheck, string, string) {
				return nil, "", "policy unavailable"
			}
		}, "policy unavailable"},
		{"check runs", func(ops *pullRequestCheckOps) {
			ops.runs = func(context.Context, PullRequestWaitOptions) ([]RemoteCheck, bool, string) {
				return nil, false, "runs unavailable"
			}
		}, "runs unavailable"},
		{"commit statuses", func(ops *pullRequestCheckOps) {
			ops.statuses = func(context.Context, PullRequestWaitOptions) ([]RemoteCheck, bool, string) {
				return nil, false, "statuses unavailable"
			}
		}, "statuses unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ops := fakePullRequestCheckOps(t)
			tc.change(&ops)
			_, err := observePullRequestHeadWith(context.Background(), "acme/app", prUpdateView(t, "head"), ops)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
}
