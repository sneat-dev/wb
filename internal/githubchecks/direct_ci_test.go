package githubchecks

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubobserver/testfixture"
)

func TestDirectCIWaitCannotPassOnJoblessWorkflowOrSkippedCoverage(t *testing.T) {
	testfixture.InstallDirectCIGH(t)
	options := PullRequestWaitOptions{Repository: "acme/app", Target: "integration", Head: testfixture.DirectCIHead,
		ExpectedActionChecks: &ExpectedActionChecks{WorkflowID: 300, Event: "pull_request", PullRequestNumber: 17, PullRequestBase: "main", Names: []string{"Required checks passed", "Tests and coverage (8 shards)"}},
		Slice:                20 * time.Second, CheckPollInterval: 200 * time.Millisecond,
		Progress: func(progress PullRequestWaitProgress) {
			if progress.Result.Status != PullRequestWaitPending || progress.NextPoll == 0 {
				return
			}
			if progress.Result.Head != testfixture.DirectCIHead || progress.Result.StableObservations != 0 || len(progress.Result.Checks) != 0 ||
				!strings.Contains(progress.Result.Reason, "expected Actions jobs have not registered for the exact head") {
				t.Errorf("jobless workflow yielded an unsafe pending observation: %#v", progress)
			}
		}}
	result, err := WaitForCommitChecks(context.Background(), options)
	if err != nil || result.Status != PullRequestWaitPending || !strings.Contains(result.Reason, "expected Actions jobs") {
		t.Fatalf("jobless workflow wait = %#v, err=%v", result, err)
	}
	options.Progress = nil
	t.Setenv("WB_TEST_CHECK_RUNS", `{"total_count":2,"check_runs":[{"id":1,"name":"Required checks passed","status":"completed","conclusion":"success","app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":305}},{"id":2,"name":"Tests and coverage (8 shards)","status":"completed","conclusion":"skipped","app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":305}}]}`)
	result, err = WaitForCommitChecks(context.Background(), options)
	if err != nil || result.Status != PullRequestWaitFailed || !strings.Contains(result.Reason, "did not execute successfully") {
		t.Fatalf("skipped coverage wait = %#v, err=%v", result, err)
	}
	t.Setenv("WB_TEST_CHECK_RUNS", `{"total_count":2,"check_runs":[{"id":1,"name":"Required checks passed","status":"completed","conclusion":"success","app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":305}},{"id":2,"name":"Tests and coverage (8 shards)","status":"completed","conclusion":"success","app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":305}}]}`)
	result, err = WaitForCommitChecks(context.Background(), options)
	if err != nil || result.Status != PullRequestWaitPassed || result.Head != testfixture.DirectCIHead || result.StableObservations < 2 {
		t.Fatalf("successful exact Go CI wait = %#v, err=%v", result, err)
	}
}

func TestDirectCIWaitIgnoresUnrelatedSameSHAWorkflows(t *testing.T) {
	testfixture.InstallDirectCIGH(t)
	options := PullRequestWaitOptions{Repository: "acme/app", Target: "integration", Head: testfixture.DirectCIHead,
		ExpectedActionChecks: &ExpectedActionChecks{WorkflowID: 300, Event: "pull_request", PullRequestNumber: 17, PullRequestBase: "main", Names: []string{"Required checks passed", "Tests and coverage (8 shards)"}},
		Slice:                20 * time.Second, CheckPollInterval: 200 * time.Millisecond}
	t.Setenv("WB_TEST_RUNS", `{"total_count":3,"workflow_runs":[{"id":15,"workflow_id":300,"head_sha":"`+testfixture.DirectCIHead+`","head_branch":"integration","event":"pull_request","status":"completed","conclusion":"success","created_at":"2026-09-27T09:00:00Z","check_suite_id":305,"pull_requests":[{"number":17,"base":{"ref":"main"}}]},{"id":16,"workflow_id":301,"head_sha":"`+testfixture.DirectCIHead+`","head_branch":"integration","event":"workflow_dispatch","status":"completed","conclusion":"failure","created_at":"2026-09-27T09:00:01Z","check_suite_id":306},{"id":17,"workflow_id":302,"head_sha":"`+testfixture.DirectCIHead+`","head_branch":"integration","event":"workflow_dispatch","status":"in_progress","conclusion":null,"created_at":"2026-09-27T09:00:02Z","check_suite_id":307}]}`)
	t.Setenv("WB_TEST_CHECK_RUNS", `{"total_count":4,"check_runs":[{"id":1,"name":"Required checks passed","status":"completed","conclusion":"success","app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":305}},{"id":2,"name":"Tests and coverage (8 shards)","status":"completed","conclusion":"success","app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":305}},{"id":3,"name":"Unrelated failed job","status":"completed","conclusion":"failure","app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":306}},{"id":4,"name":"Unrelated pending job","status":"in_progress","conclusion":null,"app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":307}}]}`)
	result, err := WaitForCommitChecks(context.Background(), options)
	if err != nil || result.Status != PullRequestWaitPassed || result.StableObservations < 2 {
		t.Fatalf("unrelated same-SHA workflows affected explicit wait: %#v, err=%v", result, err)
	}
	for _, check := range result.Checks {
		if strings.Contains(check.Name, "Unrelated") || check.WorkflowID == 301 || check.WorkflowID == 302 {
			t.Fatalf("unrelated workflow leaked into explicit receipt: %#v", check)
		}
	}
}

func TestDirectCIWaitRetainsOtherRequiredTargetChecks(t *testing.T) {
	t.Parallel()
	expected := &ExpectedActionChecks{WorkflowID: 300, Event: "pull_request", PullRequestNumber: 17, PullRequestBase: "main", Names: []string{"Required checks passed", "Tests and coverage (8 shards)"}}
	checks := []RemoteCheck{
		{Name: "check-run:Tests and coverage (8 shards)", Bucket: "pass", WorkflowID: 300, WorkflowEvent: "pull_request", WorkflowRunID: 15, PullRequestNumber: 17, PullRequestBase: "main", AppID: 15368},
		{Name: "check-run:Security audit", Bucket: "fail", WorkflowID: 301, WorkflowEvent: "workflow_dispatch", WorkflowRunID: 16, AppID: 42},
		{Name: "check-run:Optional audit", Bucket: "fail", WorkflowID: 302, WorkflowEvent: "workflow_dispatch", WorkflowRunID: 17, AppID: 42},
	}
	relevant := relevantExpectedActionChecks(checks, expected, []RequiredRemoteCheck{{Name: "Security audit", IntegrationID: 42}})
	if len(relevant) != 2 || relevant[0].Name != checks[0].Name || relevant[1].Name != checks[1].Name {
		t.Fatalf("explicit wait filtered a required target check: %#v", relevant)
	}
	pending := false
	if !failedObservedChecks(relevant, &pending) {
		t.Fatal("required failure did not fail explicit wait")
	}
}

func TestDirectCIExpectedJobsRequireExecutedMatchingWorkflow(t *testing.T) {
	t.Parallel()
	expected := &ExpectedActionChecks{WorkflowID: 300, Event: "pull_request", PullRequestNumber: 17, PullRequestBase: "main", Names: []string{"Required checks passed", "Tests and coverage (8 shards)"}}
	good := []RemoteCheck{
		{Name: "check-run:Required checks passed", Bucket: "pass", Conclusion: "success", WorkflowID: 300, WorkflowEvent: "pull_request", WorkflowRunID: 15, PullRequestNumber: 17, PullRequestBase: "main"},
		{Name: "check-run:Tests and coverage (8 shards)", Bucket: "pass", Conclusion: "success", WorkflowID: 300, WorkflowEvent: "pull_request", WorkflowRunID: 15, PullRequestNumber: 17, PullRequestBase: "main"},
	}
	if missing, rejected := expectedActionCheckState(good, expected); len(missing)+len(rejected) != 0 {
		t.Fatalf("valid exact jobs missing=%v rejected=%v", missing, rejected)
	}
	for _, test := range []struct {
		name                      string
		checks                    []RemoteCheck
		wantMissing, wantRejected string
	}{
		{name: "empty", wantMissing: "Required checks passed"},
		{name: "wrong workflow", checks: []RemoteCheck{{Name: good[0].Name, Bucket: "pass", Conclusion: "success", WorkflowID: 301, WorkflowEvent: "pull_request", WorkflowRunID: 15}}, wantMissing: "Required checks passed"},
		{name: "status spoof", checks: []RemoteCheck{{Name: "status:Required checks passed", Bucket: "pass"}}, wantMissing: "Required checks passed"},
		{name: "skipped", checks: []RemoteCheck{{Name: good[0].Name, Bucket: "skipping", Conclusion: "skipped", WorkflowID: 300, WorkflowEvent: "pull_request", WorkflowRunID: 15, PullRequestNumber: 17, PullRequestBase: "main"}}, wantRejected: "Required checks passed"},
		{name: "neutral", checks: []RemoteCheck{{Name: good[0].Name, Bucket: "pass", Conclusion: "neutral", WorkflowID: 300, WorkflowEvent: "pull_request", WorkflowRunID: 15, PullRequestNumber: 17, PullRequestBase: "main"}}, wantRejected: "Required checks passed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			missing, rejected := expectedActionCheckState(test.checks, expected)
			if test.wantMissing != "" && !strings.Contains(strings.Join(missing, ","), test.wantMissing) {
				t.Fatalf("missing=%v, want %s", missing, test.wantMissing)
			}
			if test.wantRejected != "" && !strings.Contains(strings.Join(rejected, ","), test.wantRejected) {
				t.Fatalf("rejected=%v, want %s", rejected, test.wantRejected)
			}
		})
	}
}
