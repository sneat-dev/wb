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
	if _, err := UnsatisfiedRequiredChecks(context.Background(), "acme/app", ""); err == nil {
		t.Fatal("required check query accepted an empty PR selector")
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

func TestUnsatisfiedRequiredChecksNamesOnlyMissingOrFailedProducers(t *testing.T) {
	t.Parallel()
	ops := fakePullRequestCheckOps(t)
	ops.required = func(context.Context, string, string, bool) ([]RequiredRemoteCheck, string, string) {
		return []RequiredRemoteCheck{{Name: "missing"}, {Name: "lint"}, {Name: "build"}}, "", ""
	}
	gaps, err := unsatisfiedRequiredChecksWith(context.Background(), "acme/app", "7", ops)
	if err != nil || !reflect.DeepEqual(gaps, []string{"build", "missing"}) {
		t.Fatalf("gaps=%v error=%v", gaps, err)
	}
	ops.required = func(context.Context, string, string, bool) ([]RequiredRemoteCheck, string, string) {
		return nil, "", ""
	}
	gaps, err = unsatisfiedRequiredChecksWith(context.Background(), "acme/app", "7", ops)
	if err != nil || len(gaps) != 0 {
		t.Fatalf("unruled branch gaps=%v error=%v", gaps, err)
	}
}

func TestUnsatisfiedRequiredChecksRefusesIncompleteReads(t *testing.T) {
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
			_, err := unsatisfiedRequiredChecksWith(context.Background(), "acme/app", "7", ops)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
}
