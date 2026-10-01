package orchestrate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/testenv"
)

const directCITestHead = "0123456789012345678901234567890123456789"

func installDirectCITestGH(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/bin/sh
case "$2" in
  repos/acme/app/branches/integration) echo '{"protected":false,"protection":{}}' ;;
  'repos/acme/app/rules/branches/integration?per_page=100') echo '[]' ;;
  repos/acme/app/pulls/17) echo "$WB_TEST_PR" ;;
  repos/acme/app/git/ref/heads/integration) echo '{"object":{"sha":"` + directCITestHead + `"}}' ;;
  repos/acme/app/branches/main) echo '{"protected":true,"protection":{"required_status_checks":{"contexts":["Required checks passed"]}}}' ;;
  repos/acme/app/branches/main/protection/required_status_checks) echo '{"strict":true,"contexts":["Required checks passed"],"checks":[]}' ;;
  'repos/acme/app/rules/branches/main?per_page=100') echo '[]' ;;
  repos/acme/app/actions/workflows/go-ci.yml)
    if [ "$WB_TEST_WORKFLOW_ERROR" = 1 ]; then echo 'workflow unavailable' >&2; exit 1; fi
    echo "$WB_TEST_WORKFLOW" ;;
  'repos/acme/app/actions/runs?head_sha=` + directCITestHead + `&per_page=100') echo "$WB_TEST_RUNS" ;;
  'repos/acme/app/commits/` + directCITestHead + `/check-runs?per_page=100') echo "$WB_TEST_CHECK_RUNS" ;;
  'repos/acme/app/commits/` + directCITestHead + `/status?per_page=100') echo '{"total_count":0,"statuses":[]}' ;;
  *) echo "unexpected gh request: $*" >&2; exit 2 ;;
esac
`
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WB_TEST_PR", `{"number":17,"state":"open","head":{"ref":"integration","sha":"`+directCITestHead+`","repo":{"full_name":"acme/app"}},"base":{"ref":"main","repo":{"full_name":"acme/app"}}}`)
	t.Setenv("WB_TEST_WORKFLOW", `{"id":300,"name":"Go CI","path":".github/workflows/go-ci.yml","state":"active"}`)
	t.Setenv("WB_TEST_RUNS", `{"total_count":1,"workflow_runs":[{"id":15,"workflow_id":300,"head_sha":"`+directCITestHead+`","head_branch":"integration","event":"pull_request","status":"completed","conclusion":"success","created_at":"2026-09-27T09:00:00Z","check_suite_id":305,"pull_requests":[{"number":17,"base":{"ref":"main"}}]}]}`)
	t.Setenv("WB_TEST_CHECK_RUNS", `{"total_count":0,"check_runs":[]}`)
}

func TestDirectCIWaitCannotPassOnJoblessWorkflowOrSkippedCoverage(t *testing.T) {
	installDirectCITestGH(t)
	options := PullRequestWaitOptions{Repository: "acme/app", Target: "integration", Head: directCITestHead,
		ExpectedActionChecks: &ExpectedActionChecks{WorkflowID: 300, Event: "pull_request", PullRequestNumber: 17, PullRequestBase: "main", Names: directCIGoChecks},
		Slice:                20 * time.Second, CheckPollInterval: 200 * time.Millisecond,
		Progress: func(progress PullRequestWaitProgress) {
			if progress.Result.Status != PullRequestWaitPending || progress.NextPoll == 0 {
				return
			}
			if progress.Result.Head != directCITestHead || progress.Result.StableObservations != 0 || len(progress.Result.Checks) != 0 ||
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
	if err != nil || result.Status != PullRequestWaitPassed || result.Head != directCITestHead || result.StableObservations < 2 {
		t.Fatalf("successful exact Go CI wait = %#v, err=%v", result, err)
	}
}

func TestDirectCIWaitIgnoresUnrelatedSameSHAWorkflows(t *testing.T) {
	installDirectCITestGH(t)
	options := PullRequestWaitOptions{Repository: "acme/app", Target: "integration", Head: directCITestHead,
		ExpectedActionChecks: &ExpectedActionChecks{WorkflowID: 300, Event: "pull_request", PullRequestNumber: 17, PullRequestBase: "main", Names: directCIGoChecks},
		Slice:                20 * time.Second, CheckPollInterval: 200 * time.Millisecond}
	t.Setenv("WB_TEST_RUNS", `{"total_count":3,"workflow_runs":[{"id":15,"workflow_id":300,"head_sha":"`+directCITestHead+`","head_branch":"integration","event":"pull_request","status":"completed","conclusion":"success","created_at":"2026-09-27T09:00:00Z","check_suite_id":305,"pull_requests":[{"number":17,"base":{"ref":"main"}}]},{"id":16,"workflow_id":301,"head_sha":"`+directCITestHead+`","head_branch":"integration","event":"workflow_dispatch","status":"completed","conclusion":"failure","created_at":"2026-09-27T09:00:01Z","check_suite_id":306},{"id":17,"workflow_id":302,"head_sha":"`+directCITestHead+`","head_branch":"integration","event":"workflow_dispatch","status":"in_progress","conclusion":null,"created_at":"2026-09-27T09:00:02Z","check_suite_id":307}]}`)
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
	expected := &ExpectedActionChecks{WorkflowID: 300, Event: "pull_request", PullRequestNumber: 17, PullRequestBase: "main", Names: directCIGoChecks}
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

func TestDirectCIDeferralNeedsExactOpenHeadPRAndPriorWorkflow(t *testing.T) {
	installDirectCITestGH(t)
	plan, err := resolveWorktreeMergeValidationPlan(context.Background(), "acme/app", "integration", WorktreeMergeRouteDirect, false, false, "17")
	if err != nil || !plan.Defer || plan.DirectCI == nil || plan.DirectCI.PullRequest != "17" || plan.DirectCI.Base != "main" || plan.DirectCI.WorkflowID != 300 {
		t.Fatalf("direct CI plan = %#v, err=%v", plan, err)
	}
	defaultPlan, err := resolveWorktreeMergeValidationPlan(context.Background(), "acme/app", "integration", WorktreeMergeRouteDirect, false, false)
	if err != nil || defaultPlan.Defer {
		t.Fatalf("default direct plan unexpectedly deferred: %#v, err=%v", defaultPlan, err)
	}
	t.Setenv("WB_TEST_PR", `{"number":17,"state":"closed","head":{"ref":"integration","sha":"`+directCITestHead+`","repo":{"full_name":"acme/app"}},"base":{"ref":"main","repo":{"full_name":"acme/app"}}}`)
	if _, err := resolveWorktreeMergeValidationPlan(context.Background(), "acme/app", "integration", WorktreeMergeRouteDirect, false, false, "17"); err == nil || !strings.Contains(err.Error(), "not an open") {
		t.Fatalf("closed PR was accepted: %v", err)
	}
	t.Setenv("WB_TEST_PR", `{"number":17,"state":"open","head":{"ref":"integration","sha":"`+directCITestHead+`","repo":{"full_name":"acme/app"}},"base":{"ref":"main","repo":{"full_name":"acme/app"}}}`)
	t.Setenv("WB_TEST_RUNS", `{"total_count":0,"workflow_runs":[]}`)
	if _, err := resolveWorktreeMergeValidationPlan(context.Background(), "acme/app", "integration", WorktreeMergeRouteDirect, false, false, "17"); err == nil || !strings.Contains(err.Error(), "no pull_request run") {
		t.Fatalf("missing prior workflow was accepted: %v", err)
	}
}

func TestDirectCIExpectedJobsRequireExecutedMatchingWorkflow(t *testing.T) {
	t.Parallel()
	expected := &ExpectedActionChecks{WorkflowID: 300, Event: "pull_request", PullRequestNumber: 17, PullRequestBase: "main", Names: directCIGoChecks}
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

func TestDirectCIDeferralPublishGuardRequiresFreshPlanAndExactCandidate(t *testing.T) {
	t.Parallel()
	receipt := WorktreeMergeReceipt{Status: WorktreeMergePrepared, Route: WorktreeMergeRouteDecision{Route: WorktreeMergeRouteDirect}, Candidate: WorktreeMergeCandidate{SHA: directCITestHead},
		Validation:         quality.VerificationReport{Status: quality.StatusSkipped, Revision: directCITestHead},
		ValidationDeferral: &WorktreeMergeValidationDeferral{Route: WorktreeMergeRouteDirect, CandidateSHA: directCITestHead, DirectCIPullRequest: "17", DirectCIPullRequestNumber: 17, DirectCIBase: "main", DirectCIWorkflowID: 300}}
	plan := worktreeMergeValidationPlan{Defer: true, DirectCI: &worktreeMergeDirectCIContract{PullRequest: "17", PullRequestNumber: 17, Base: "main", WorkflowID: 300}}
	if err := requireWorktreeMergePublishedValidationContext(context.Background(), receipt, plan, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []WorktreeMergeReceipt{
		func() WorktreeMergeReceipt { copy := receipt; copy.Candidate.SHA = "different"; return copy }(),
		func() WorktreeMergeReceipt { copy := receipt; copy.ValidationDeferral = nil; return copy }(),
	} {
		if err := requireWorktreeMergePublishedValidationContext(context.Background(), changed, plan, 0, 0, 0); err == nil {
			t.Fatalf("stale direct CI deferral was accepted: %#v", changed)
		}
	}
	if err := requireWorktreeMergePublishedValidationContext(context.Background(), receipt, worktreeMergeValidationPlan{}, 0, 0, 0); err == nil {
		t.Fatal("recorded deferral without a fresh eligible plan was accepted")
	}
}

func TestDirectCIPullRequestMustStillMatchExactLandedHead(t *testing.T) {
	installDirectCITestGH(t)
	receipt := WorktreeMergeReceipt{Repository: "acme/app", Target: "integration"}
	contract := worktreeMergeDirectCIContract{PullRequest: "17", Base: "main", WorkflowID: 300}
	if err := verifyWorktreeMergeDirectCIPullRequest(context.Background(), receipt, contract, directCITestHead); err != nil {
		t.Fatal(err)
	}
	if err := verifyWorktreeMergeDirectCIPullRequest(context.Background(), receipt, contract, "different"); err == nil {
		t.Fatal("a PR at another head satisfied the landed SHA")
	}
	t.Setenv("WB_TEST_PR", `{"number":17,"state":"closed","head":{"ref":"integration","sha":"`+directCITestHead+`","repo":{"full_name":"acme/app"}},"base":{"ref":"main","repo":{"full_name":"acme/app"}}}`)
	if err := verifyWorktreeMergeDirectCIPullRequest(context.Background(), receipt, contract, directCITestHead); err == nil {
		t.Fatal("closed PR satisfied the post-push gate")
	}
}

func TestDirectCIInputsRejectWorkflowChangesButAllowProductChanges(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "-q")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(dir, "product.go"), []byte("package test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "base")
	base := git("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "product.go"), []byte("package test\n// changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "product")
	receipt := WorktreeMergeReceipt{TargetSHA: base, Candidate: WorktreeMergeCandidate{SHA: git("rev-parse", "HEAD"), Worktree: dir}}
	if err := verifyWorktreeMergeDirectCIInputs(context.Background(), receipt); err != nil {
		t.Fatalf("product-only candidate rejected: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".github", "workflows", "go-ci.yml"), []byte("name: changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "workflow")
	receipt.Candidate.SHA = git("rev-parse", "HEAD")
	if err := verifyWorktreeMergeDirectCIInputs(context.Background(), receipt); err == nil || !strings.Contains(err.Error(), "changed CI inputs") {
		t.Fatalf("workflow-changing candidate accepted: %v", err)
	}
}
