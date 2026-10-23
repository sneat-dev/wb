package githubchecks

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver"
)

func contractReadContext(t *testing.T, answers map[string]string, command *githubobserver.CommandResponse) context.Context {
	t.Helper()
	return githubobserver.WithReader(context.Background(), githubobserver.Reader{
		Get: func(_ context.Context, request githubobserver.GetRequest) (githubobserver.Response, error) {
			body, ok := answers[request.Endpoint]
			if !ok {
				t.Fatalf("unexpected read: %+v", request)
			}
			if request.Dir != "" || request.Repository != "acme/app" || request.FreshWindow != 0 {
				t.Fatalf("read identity: %+v", request)
			}
			return githubobserver.Response{Body: []byte(body)}, nil
		},
		Read: func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("unexpected Read")
			return nil, errors.New("unexpected Read")
		},
		Execute: func(_ context.Context, dir string, args ...string) githubobserver.CommandResponse {
			if command == nil || dir != "" || !reflect.DeepEqual(args, []string{"run", "view", "7", "--repo", "acme/app", "--job", "9", "--log-failed"}) {
				t.Fatalf("unexpected Execute %q %v", dir, args)
			}
			return *command
		},
	})
}

func TestSkippedRequiredChecksRespectProducerAndFirstMatchingReceipt(t *testing.T) {
	t.Parallel()
	checks := []RemoteCheck{{Name: " "}, {Name: "check-run:build", AppID: 1, Conclusion: "success"}, {Name: "check-run:build", AppID: 2, Conclusion: "skipped"}, {Name: "status:scan", Conclusion: "neutral"}, {Name: "status:publish"}}
	got := SkippedOrNeutralRequiredChecks(checks, []RequiredRemoteCheck{{Name: "build", IntegrationID: 2}, {Name: "scan"}, {Name: "publish"}, {Name: "absent"}})
	if !reflect.DeepEqual(got, []string{"build", "scan"}) {
		t.Fatalf("skipped required checks=%v", got)
	}
}

func TestPullRequestReceiptRefusesHeadAndBaseDrift(t *testing.T) {
	t.Parallel()
	ctx := contractReadContext(t, map[string]string{"repos/acme/app/pulls/7": `{"number":7,"head":{"sha":"other"},"base":{"ref":"release"}}`}, nil)
	if checks, pending, reason := commitChecks(ctx, PullRequestWaitOptions{Repository: "acme/app", PullRequest: "7", Head: "head"}); checks != nil || pending || !strings.Contains(reason, "now points at other") {
		t.Fatalf("head drift: %v %v %q", checks, pending, reason)
	}
	if reason := pullRequestTargetsBase(ctx, "acme/app", "7", "main"); !strings.Contains(reason, "targets release, not main") {
		t.Fatalf("base drift: %q", reason)
	}
}

func TestRequiredPolicyRejectsIncompleteAuthority(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, branch, detail, rules, want string }{
		{"protected receipt", `{}`, `{}`, `[]`, "omitted the protected"},
		{"strict JSON", `{"protected":true,"protection":{"required_status_checks":{"contexts":["build"]}}}`, `{`, `[]`, "decode authoritative"},
		{"empty classic context", `{"protected":true,"protection":{"required_status_checks":{"contexts":[" "]}}}`, `{"strict":true,"contexts":[" "]}`, `[]`, "no context"},
		{"empty producer context", `{"protected":true,"protection":{"required_status_checks":{"checks":[{"context":" ","app_id":2}]}}}`, `{"strict":true,"checks":[{"context":" ","app_id":2}]}`, `[]`, "no context"},
		{"rule type", `{"protected":false}`, `{}`, `[{}]`, "omitted its type"},
		{"rule context", `{"protected":false}`, `{}`, `[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":" "}]}}]`, "no context"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := contractReadContext(t, map[string]string{"repos/acme/app/branches/main": tc.branch, "repos/acme/app/branches/main/protection/required_status_checks": tc.detail, "repos/acme/app/rules/branches/main?per_page=100": tc.rules}, nil)
			checks, freshness, reason := RequiredChecks(ctx, "acme/app", "main", true)
			if checks != nil || freshness != "" || !strings.Contains(reason, tc.want) {
				t.Fatalf("checks=%v freshness=%q reason=%q", checks, freshness, reason)
			}
		})
	}
}

func TestActionsReceiptRetainsCoLatestRunsAndRejectsMissingSuite(t *testing.T) {
	t.Parallel()
	ctx := contractReadContext(t, map[string]string{"repos/acme/app/commits/head/check-runs?per_page=100": `{"total_count":0,"check_runs":[]}`, "repos/acme/app/actions/runs?head_sha=head&per_page=100": `{"total_count":2,"workflow_runs":[{"id":1,"workflow_id":300,"check_suite_id":11,"created_at":"2026-09-27T09:00:00Z","event":"push"},{"id":2,"workflow_id":300,"check_suite_id":12,"created_at":"2026-09-27T09:00:00Z","event":"push"}]}`}, nil)
	options := PullRequestWaitOptions{Repository: "acme/app", Head: "head"}
	bySuite, runs, reason := ActionsRunsForHead(ctx, options)
	if reason != "" || len(bySuite) != 2 || len(runs) != 2 || runs[0].ID != 1 || runs[1].ID != 2 {
		t.Fatalf("co-latest runs: suites=%v runs=%v reason=%q", bySuite, runs, reason)
	}
	ctx = contractReadContext(t, map[string]string{"repos/acme/app/actions/runs?head_sha=head&per_page=100": `{"total_count":0,"workflow_runs":[]}`, "repos/acme/app/commits/head/check-runs?per_page=100": `{"total_count":1,"check_runs":[{"id":1,"name":"build","app":{"slug":"github-actions"},"check_suite":{"id":11}}]}`, "repos/acme/app/actions/jobs/1": `{"run_id":7}`, "repos/acme/app/actions/runs/7": `{"id":7,"workflow_id":300,"head_sha":"head","check_suite_id":12,"created_at":"2026-09-27T09:00:00Z","event":"push"}`}, nil)
	if _, _, reason := commitCheckRuns(ctx, options); !strings.Contains(reason, "exact-head workflow-run receipt omitted") {
		t.Fatalf("missing suite=%q", reason)
	}
}

// GitHub's head_sha run listing is eventually consistent: it can list no runs
// while the head's check runs already exist (#877). The suite each check run
// names is then resolved through the direct job and run reads.
func TestCommitCheckRunsRecoversSuitesTheRunListingOmitted(t *testing.T) {
	t.Parallel()
	const run = `{"id":7,"workflow_id":300,"head_sha":"head","check_suite_id":11,"created_at":"2026-09-27T09:00:00Z","event":"pull_request","status":"completed","conclusion":"success"}`
	for _, tc := range []struct {
		name, checkRuns string
		wantBuckets     []string
		wantPending     bool
	}{
		{"skipped jobs pass", `[{"id":1,"name":"build","status":"completed","conclusion":"success","app":{"slug":"github-actions"},"check_suite":{"id":11}},{"id":2,"name":"bump","status":"completed","conclusion":"skipped","app":{"slug":"github-actions"},"check_suite":{"id":11}},{"id":3,"name":"scan","status":"completed","conclusion":"success","app":{"slug":"scanner"},"check_suite":{"id":99}}]`, []string{"pass", "skipping", "pass"}, false},
		{"failing job still fails", `[{"id":1,"name":"build","status":"completed","conclusion":"failure","app":{"slug":"github-actions"},"check_suite":{"id":11}}]`, []string{"fail"}, false},
		{"pending job still pends", `[{"id":1,"name":"build","status":"in_progress","app":{"slug":"github-actions"},"check_suite":{"id":11}}]`, []string{"pending"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := contractReadContext(t, map[string]string{
				"repos/acme/app/actions/runs?head_sha=head&per_page=100": `{"total_count":0,"workflow_runs":[]}`,
				"repos/acme/app/commits/head/check-runs?per_page=100":    `{"total_count":0,"check_runs":` + tc.checkRuns + `}`,
				"repos/acme/app/actions/jobs/1":                          `{"run_id":7}`,
				"repos/acme/app/actions/runs/7":                          run,
			}, nil)
			checks, pending, reason := commitCheckRuns(ctx, PullRequestWaitOptions{Repository: "acme/app", Head: "head"})
			if reason != "" || pending != tc.wantPending {
				t.Fatalf("checks=%+v pending=%t reason=%q", checks, pending, reason)
			}
			buckets := make([]string, 0, len(checks))
			for _, check := range checks {
				if check.Name == "workflow-run:300:pull_request" {
					continue
				}
				buckets = append(buckets, check.Bucket)
			}
			if !reflect.DeepEqual(buckets, tc.wantBuckets) {
				t.Fatalf("buckets=%v want %v (checks=%+v)", buckets, tc.wantBuckets, checks)
			}
		})
	}
}

func TestCommitCheckRunsRefusesIncompleteOrMalformedReceipts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, checkRuns, runs, want string }{
		{"truncated check runs", `{"total_count":2,"check_runs":[]}`, `{"total_count":0,"workflow_runs":[]}`, "only 0 of 2 observed check runs"},
		{"malformed listed run", `{"total_count":0,"check_runs":[]}`, `{"total_count":1,"workflow_runs":[{"id":7}]}`, "malformed exact-head workflow-run identity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			answers := map[string]string{"repos/acme/app/commits/head/check-runs?per_page=100": tc.checkRuns, "repos/acme/app/actions/runs?head_sha=head&per_page=100": tc.runs}
			ctx := contractReadContext(t, answers, nil)
			if checks, pending, reason := commitCheckRuns(ctx, PullRequestWaitOptions{Repository: "acme/app", Head: "head"}); checks != nil || pending || !strings.Contains(reason, tc.want) {
				t.Fatalf("checks=%+v pending=%t reason=%q", checks, pending, reason)
			}
		})
	}
}

func TestCommitCheckRunsRefusesAnOmittedSuiteItCannotProve(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, job, run, want string }{
		{"job read fails", "", "", "lost job"},
		{"job undecodable", `{`, "", "decode repos/acme/app/actions/jobs/1"},
		{"job names no run", `{"run_id":0}`, "", "named no workflow run"},
		{"run undecodable", `{"run_id":7}`, `{`, "decode repos/acme/app/actions/runs/7"},
		{"run on another head", `{"run_id":7}`, `{"id":7,"head_sha":"other","check_suite_id":11}`, `names head "other" and suite 11`},
		{"run in another suite", `{"run_id":7}`, `{"id":7,"head_sha":"head","check_suite_id":12}`, `names head "head" and suite 12`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			answers := map[string]string{
				"repos/acme/app/actions/runs?head_sha=head&per_page=100": `{"total_count":0,"workflow_runs":[]}`,
				"repos/acme/app/commits/head/check-runs?per_page=100":    `{"total_count":1,"check_runs":[{"id":1,"name":"bump","status":"completed","conclusion":"skipped","app":{"slug":"github-actions"},"check_suite":{"id":11}}]}`,
				"repos/acme/app/actions/jobs/1":                          tc.job,
				"repos/acme/app/actions/runs/7":                          tc.run,
			}
			ctx := githubobserver.WithReader(context.Background(), githubobserver.Reader{Get: func(_ context.Context, request githubobserver.GetRequest) (githubobserver.Response, error) {
				body := answers[request.Endpoint]
				if body == "" {
					return githubobserver.Response{}, errors.New("lost job")
				}
				return githubobserver.Response{Body: []byte(body)}, nil
			}})
			checks, pending, reason := commitCheckRuns(ctx, PullRequestWaitOptions{Repository: "acme/app", Head: "head"})
			if checks != nil || pending || !strings.Contains(reason, "exact-head workflow-run receipt omitted") || !strings.Contains(reason, tc.want) {
				t.Fatalf("checks=%+v pending=%t reason=%q", checks, pending, reason)
			}
		})
	}
}

func TestCommitCheckRunsRequiresChecksFromEveryCoLatestRun(t *testing.T) {
	t.Parallel()
	ctx := contractReadContext(t, map[string]string{
		"repos/acme/app/actions/runs?head_sha=head&per_page=100": `{"total_count":3,"workflow_runs":[{"id":1,"workflow_id":300,"check_suite_id":10,"created_at":"2026-09-27T08:59:59Z","event":"push","status":"completed","conclusion":"failure"},{"id":2,"workflow_id":300,"check_suite_id":11,"created_at":"2026-09-27T09:00:00Z","event":"push","status":"completed","conclusion":"success"},{"id":3,"workflow_id":300,"check_suite_id":12,"created_at":"2026-09-27T09:00:00Z","event":"push","status":"completed","conclusion":"success"}]}`,
		"repos/acme/app/commits/head/check-runs?per_page=100":    `{"total_count":3,"check_runs":[{"id":1001,"name":"build","status":"completed","conclusion":"failure","app":{"slug":"github-actions"},"check_suite":{"id":10}},{"id":1002,"name":"build","status":"completed","conclusion":"success","app":{"slug":"github-actions"},"check_suite":{"id":11}},{"id":1003,"name":"build","status":"completed","conclusion":"failure","app":{"slug":"github-actions"},"check_suite":{"id":12}}]}`,
	}, nil)
	checks, pending, reason := commitCheckRuns(ctx, PullRequestWaitOptions{Repository: "acme/app", Head: "head"})
	if reason != "" || pending || len(checks) != 2 {
		t.Fatalf("checks=%+v pending=%t reason=%q", checks, pending, reason)
	}
	if checks[0].CheckRunID != 1002 || checks[0].Conclusion != "success" || checks[1].CheckRunID != 1003 || checks[1].Conclusion != "failure" {
		t.Fatalf("co-latest checks=%+v; older run should be excluded and both newest runs must be retained", checks)
	}
}

func TestCommitCheckRunsRetainsPendingCoLatestRunWithoutCheckRuns(t *testing.T) {
	t.Parallel()
	ctx := contractReadContext(t, map[string]string{
		"repos/acme/app/actions/runs?head_sha=head&per_page=100": `{"total_count":2,"workflow_runs":[{"id":1,"workflow_id":300,"check_suite_id":11,"created_at":"2026-09-27T09:00:00Z","event":"push","status":"completed","conclusion":"success"},{"id":2,"workflow_id":300,"check_suite_id":12,"created_at":"2026-09-27T09:00:00Z","event":"push","status":"in_progress"}]}`,
		"repos/acme/app/commits/head/check-runs?per_page=100":    `{"total_count":1,"check_runs":[{"id":1001,"name":"build","status":"completed","conclusion":"success","app":{"slug":"github-actions"},"check_suite":{"id":11}}]}`,
	}, nil)
	checks, pending, reason := commitCheckRuns(ctx, PullRequestWaitOptions{Repository: "acme/app", Head: "head"})
	if reason != "" || !pending || len(checks) != 2 {
		t.Fatalf("checks=%+v pending=%t reason=%q", checks, pending, reason)
	}
	if checks[0].CheckRunID != 1001 || checks[0].Bucket != "pass" || checks[1].Name != "workflow-run:300:push" || checks[1].Bucket != "pending" {
		t.Fatalf("co-latest checks=%+v; pending run without check-runs must remain visible", checks)
	}
}

func TestActionsReceiptSortsDistinctEventsOfOneWorkflow(t *testing.T) {
	t.Parallel()
	ctx := contractReadContext(t, map[string]string{"repos/acme/app/commits/head/check-runs?per_page=100": `{"total_count":0,"check_runs":[]}`, "repos/acme/app/actions/runs?head_sha=head&per_page=100": `{"total_count":2,"workflow_runs":[{"id":1,"workflow_id":300,"check_suite_id":11,"created_at":"2026-09-27T09:00:00Z","event":"push"},{"id":2,"workflow_id":300,"check_suite_id":12,"created_at":"2026-09-27T09:00:00Z","event":"pull_request"}]}`}, nil)
	_, runs, reason := ActionsRunsForHead(ctx, PullRequestWaitOptions{Repository: "acme/app", Head: "head"})
	if reason != "" || len(runs) != 2 || runs[0].Event != "pull_request" || runs[1].Event != "push" {
		t.Fatalf("runs=%v reason=%q", runs, reason)
	}
}

func TestFailedJobWithoutStderrRetainsTransportOrExitReason(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		response githubobserver.CommandResponse
		want     string
	}{
		{"transport", githubobserver.CommandResponse{Err: errors.New("lost response")}, "lost response"},
		{"exit", githubobserver.CommandResponse{ExitCode: 1}, "GitHub command exited non-zero"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := contractReadContext(t, nil, &tc.response)
			details := failedCheckDetails(ctx, "acme/app", []RemoteCheck{{Name: "build", Bucket: "fail", Link: "https://github.com/acme/app/actions/runs/7/job/9"}})
			if len(details) != 1 || !strings.Contains(details[0].Reason, tc.want) {
				t.Fatalf("details=%+v", details)
			}
		})
	}
}

func TestFailureAnnotationsDiscardInvalidRowsAndRetainWarnings(t *testing.T) {
	t.Parallel()
	ctx := contractReadContext(t, map[string]string{"repos/acme/app/check-runs/9/annotations?per_page=100": `[{"path":"a.go","start_line":1,"message":"notice detail","annotation_level":"notice"},{"path":"","start_line":1,"message":"missing path","annotation_level":"failure"},{"path":"a.go","start_line":0,"message":"invalid line","annotation_level":"failure"},{"path":"a.go","start_line":2,"message":" ","annotation_level":"failure"},{"path":"a.go","start_line":3,"message":"warning detail","annotation_level":"warning"}]`}, nil)
	annotations, err := failedCheckAnnotations(ctx, "acme/app", 9, map[string]bool{})
	if err != nil || len(annotations) != 1 || annotations[0].Message != "warning detail" {
		t.Fatalf("annotations=%+v err=%v", annotations, err)
	}
}
