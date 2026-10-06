package githubchecks

import (
	"context"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver/testfixture"
)

func TestExpectedActionWaitRejectsIncompleteContractAndFailedJob(t *testing.T) {
	t.Parallel()
	options, ops := exactWaitFixture()
	options.ExpectedActionChecks = &ExpectedActionChecks{WorkflowID: 300, Event: "pull_request", PullRequestNumber: 7, PullRequestBase: "main"}
	if _, err := waitForCommitChecksWith(context.Background(), options, ops); err == nil || !strings.Contains(err.Error(), "expected Actions checks require") {
		t.Fatalf("incomplete contract error = %v", err)
	}
	options.ExpectedActionChecks.Names = []string{"build"}
	ops.checks = func(context.Context, PullRequestWaitOptions) ([]RemoteCheck, bool, string) {
		return []RemoteCheck{
			{Name: "check-run:build", Bucket: "pass", Conclusion: "success", WorkflowID: 300, WorkflowEvent: "pull_request", WorkflowRunID: 15, PullRequestNumber: 7, PullRequestBase: "main"},
			{Name: "check-run:security", Bucket: "fail", Conclusion: "failure"},
		}, false, ""
	}
	ops.required = func(context.Context, PullRequestWaitOptions, *requiredChecksCache) ([]RequiredRemoteCheck, string, string, string, string) {
		return []RequiredRemoteCheck{{Name: "security"}}, "ruleset", "strict", "", ""
	}
	result, err := waitForCommitChecksWith(context.Background(), options, ops)
	if err != nil || result.Status != PullRequestWaitFailed || !strings.Contains(result.Reason, "expected GitHub checks failed") {
		t.Fatalf("failed expected job result = %+v, err = %v", result, err)
	}
}

func TestExpectedActionJobPendingRemainsMissing(t *testing.T) {
	t.Parallel()
	expected := &ExpectedActionChecks{WorkflowID: 300, Event: "pull_request", PullRequestNumber: 7, PullRequestBase: "main", Names: []string{"build"}}
	checks := []RemoteCheck{{Name: "check-run:build", Bucket: "pending", WorkflowID: 300, WorkflowEvent: "pull_request", WorkflowRunID: 15, PullRequestNumber: 7, PullRequestBase: "main"}}
	missing, rejected := expectedActionCheckState(checks, expected)
	if len(missing) != 1 || missing[0] != "build" || len(rejected) != 0 {
		t.Fatalf("pending job missing=%v rejected=%v", missing, rejected)
	}
}

//nolint:paralleltest // installDirectCITestGH changes the process-wide PATH.
func TestExpectedActionRunExcludesOtherPullRequestOnSameSHA(t *testing.T) {
	testfixture.InstallDirectCIGH(t)
	t.Setenv("WB_TEST_RUNS", `{"total_count":1,"workflow_runs":[{"id":15,"workflow_id":300,"head_sha":"`+testfixture.DirectCIHead+`","head_branch":"integration","event":"pull_request","status":"completed","conclusion":"success","created_at":"2026-09-27T09:00:00Z","check_suite_id":305,"pull_requests":[{"number":18,"base":{"ref":"main"}}]}]}`)
	options := PullRequestWaitOptions{Repository: "acme/app", Target: "integration", Head: testfixture.DirectCIHead,
		ExpectedActionChecks: &ExpectedActionChecks{WorkflowID: 300, Event: "pull_request", PullRequestNumber: 17, PullRequestBase: "main", Names: []string{"build"}}}
	bySuite, runs, reason := ActionsRunsForHead(context.Background(), options)
	if reason != "" || len(bySuite) != 0 || len(runs) != 0 {
		t.Fatalf("other PR run remained: suites=%v runs=%v reason=%q", bySuite, runs, reason)
	}
}

//nolint:paralleltest // installDirectCITestGH changes the process-wide PATH.
func TestExpectedActionChecksExcludeUnknownWorkflowSuite(t *testing.T) {
	testfixture.InstallDirectCIGH(t)
	t.Setenv("WB_TEST_CHECK_RUNS", `{"total_count":1,"check_runs":[{"id":91,"name":"Unrelated workflow job","status":"completed","conclusion":"failure","app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":999}}]}`)
	options := PullRequestWaitOptions{Repository: "acme/app", Target: "integration", Head: testfixture.DirectCIHead,
		ExpectedActionChecks: &ExpectedActionChecks{WorkflowID: 300, Event: "pull_request", PullRequestNumber: 17, PullRequestBase: "main", Names: []string{"build"}}}
	checks, pending, reason := commitCheckRuns(context.Background(), options)
	if reason != "" || pending {
		t.Fatalf("unrelated suite observation: checks=%+v pending=%t reason=%q", checks, pending, reason)
	}
	for _, check := range checks {
		if check.Name == "check-run:Unrelated workflow job" {
			t.Fatalf("unrelated suite's failing job entered explicit PR receipt: %+v", checks)
		}
	}
}
