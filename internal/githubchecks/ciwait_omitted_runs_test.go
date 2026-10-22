package githubchecks

import (
	"strings"
	"testing"
)

// Under an explicit contract, a suite the head_sha listing omitted is still
// recovered: a newer run of this PR replaces the older listed one, while a
// recovered run of another PR on the same SHA stays filtered out (#877).
func TestExpectedActionChecksRecoverOmittedRunsAndStillExcludeOtherPullRequests(t *testing.T) {
	t.Parallel()
	const listed = `{"total_count":1,"workflow_runs":[{"id":15,"workflow_id":300,"head_sha":"head","head_branch":"integration","event":"pull_request","status":"completed","conclusion":"success","created_at":"2026-09-27T09:00:00Z","check_suite_id":305,"pull_requests":[{"number":17,"base":{"ref":"main"}}]}]}`
	const checkRuns = `{"total_count":2,"check_runs":[{"id":90,"name":"build","status":"completed","conclusion":"success","app":{"slug":"github-actions"},"check_suite":{"id":305}},{"id":91,"name":"build","status":"completed","conclusion":"failure","app":{"slug":"github-actions"},"check_suite":{"id":306}}]}`
	for _, tc := range []struct {
		name, pullRequest string
		wantRunID         int64
		wantConclusion    string
	}{
		{"newer run of this PR replaces the listed one", "17", 16, "failure"},
		{"run of another PR stays excluded", "18", 15, "success"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := contractReadContext(t, map[string]string{
				"repos/acme/app/commits/head/check-runs?per_page=100":    checkRuns,
				"repos/acme/app/actions/runs?head_sha=head&per_page=100": listed,
				"repos/acme/app/actions/jobs/91":                         `{"run_id":16}`,
				"repos/acme/app/actions/runs/16":                         `{"id":16,"workflow_id":300,"head_sha":"head","head_branch":"integration","event":"pull_request","status":"completed","conclusion":"failure","created_at":"2026-09-27T09:05:00Z","check_suite_id":306,"pull_requests":[{"number":` + tc.pullRequest + `,"base":{"ref":"main"}}]}`,
			}, nil)
			options := PullRequestWaitOptions{Repository: "acme/app", Target: "integration", Head: "head",
				ExpectedActionChecks: &ExpectedActionChecks{WorkflowID: 300, Event: "pull_request", PullRequestNumber: 17, PullRequestBase: "main", Names: []string{"build"}}}
			checks, pending, reason := commitCheckRuns(ctx, options)
			if reason != "" || pending || len(checks) != 1 {
				t.Fatalf("checks=%+v pending=%t reason=%q", checks, pending, reason)
			}
			if checks[0].Name != "check-run:build" || checks[0].WorkflowRunID != tc.wantRunID || checks[0].Conclusion != tc.wantConclusion {
				t.Fatalf("check=%+v, want run %d %s", checks[0], tc.wantRunID, tc.wantConclusion)
			}
		})
	}
}

// ActionsRunsForHead backs the direct-CI deferral check, so a newer run the
// listing omitted must become the latest run rather than leave an older
// listed success standing.
func TestActionsRunsForHeadRecoversANewerRunTheListingOmitted(t *testing.T) {
	t.Parallel()
	ctx := contractReadContext(t, map[string]string{
		"repos/acme/app/commits/head/check-runs?per_page=100":    `{"total_count":1,"check_runs":[{"id":91,"name":"build","app":{"slug":"github-actions"},"check_suite":{"id":12}}]}`,
		"repos/acme/app/actions/runs?head_sha=head&per_page=100": `{"total_count":1,"workflow_runs":[{"id":1,"workflow_id":300,"head_sha":"head","check_suite_id":11,"created_at":"2026-09-27T09:00:00Z","event":"push","conclusion":"success"}]}`,
		"repos/acme/app/actions/jobs/91":                         `{"run_id":2}`,
		"repos/acme/app/actions/runs/2":                          `{"id":2,"workflow_id":300,"head_sha":"head","check_suite_id":12,"created_at":"2026-09-27T09:05:00Z","event":"push","conclusion":"failure"}`,
	}, nil)
	bySuite, runs, reason := ActionsRunsForHead(ctx, PullRequestWaitOptions{Repository: "acme/app", Head: "head"})
	if reason != "" || len(bySuite) != 2 || len(runs) != 1 || runs[0].ID != 2 || runs[0].Conclusion != "failure" {
		t.Fatalf("suites=%v runs=%+v reason=%q", bySuite, runs, reason)
	}
}

func TestActionsRunsForHeadRefusesAnIncompleteCheckRunReceipt(t *testing.T) {
	t.Parallel()
	ctx := contractReadContext(t, map[string]string{"repos/acme/app/commits/head/check-runs?per_page=100": `{"total_count":2,"check_runs":[]}`}, nil)
	if bySuite, runs, reason := ActionsRunsForHead(ctx, PullRequestWaitOptions{Repository: "acme/app", Head: "head"}); bySuite != nil || runs != nil || !strings.Contains(reason, "only 0 of 2 observed check runs") {
		t.Fatalf("suites=%v runs=%v reason=%q", bySuite, runs, reason)
	}
}

// Another workflow's omitted suite neither enters an explicit contract's
// receipt nor stalls it: a recovered failing job is filtered out, and a suite
// whose recovered run cannot be proved is skipped rather than refused, while
// an unprovable suite of the contract's own workflow still refuses.
func TestExpectedActionChecksIgnoreOtherWorkflowsOmittedSuites(t *testing.T) {
	t.Parallel()
	const listed = `{"total_count":1,"workflow_runs":[{"id":15,"workflow_id":300,"head_sha":"head","head_branch":"integration","event":"pull_request","status":"completed","conclusion":"success","created_at":"2026-09-27T09:00:00Z","check_suite_id":305,"pull_requests":[{"number":17,"base":{"ref":"main"}}]}]}`
	const checkRuns = `{"total_count":2,"check_runs":[{"id":90,"name":"build","status":"completed","conclusion":"success","app":{"slug":"github-actions"},"check_suite":{"id":305}},{"id":91,"name":"Unrelated workflow job","status":"completed","conclusion":"failure","app":{"slug":"github-actions"},"check_suite":{"id":999}}]}`
	for _, tc := range []struct{ name, recovered, want string }{
		{"recovered failing job is filtered out", `{"id":16,"workflow_id":301,"head_sha":"head","event":"push","status":"completed","conclusion":"failure","created_at":"2026-09-27T09:05:00Z","check_suite_id":999}`, ""},
		{"unprovable suite is skipped", `{"id":16,"workflow_id":301,"head_sha":"head","event":"push","created_at":"2026-09-27T09:05:00Z","check_suite_id":1000}`, ""},
		{"unprovable contract suite still refuses", `{"id":16,"workflow_id":300,"head_sha":"head","event":"pull_request","created_at":"2026-09-27T09:05:00Z","check_suite_id":1000}`, "exact-head workflow-run receipt omitted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := contractReadContext(t, map[string]string{
				"repos/acme/app/commits/head/check-runs?per_page=100":    checkRuns,
				"repos/acme/app/actions/runs?head_sha=head&per_page=100": listed,
				"repos/acme/app/actions/jobs/91":                         `{"run_id":16}`,
				"repos/acme/app/actions/runs/16":                         tc.recovered,
			}, nil)
			expected := &ExpectedActionChecks{WorkflowID: 300, Event: "pull_request", PullRequestNumber: 17, PullRequestBase: "main", Names: []string{"build"}}
			checks, pending, reason := commitCheckRuns(ctx, PullRequestWaitOptions{Repository: "acme/app", Target: "integration", Head: "head", ExpectedActionChecks: expected})
			if tc.want != "" {
				if checks != nil || pending || !strings.Contains(reason, tc.want) {
					t.Fatalf("checks=%+v pending=%t reason=%q", checks, pending, reason)
				}
				return
			}
			if reason != "" || pending {
				t.Fatalf("checks=%+v pending=%t reason=%q", checks, pending, reason)
			}
			relevant := relevantExpectedActionChecks(checks, expected, nil)
			if len(relevant) != 1 || relevant[0].Name != "check-run:build" || relevant[0].Conclusion != "success" {
				t.Fatalf("relevant=%+v from checks=%+v", relevant, checks)
			}
		})
	}
}
