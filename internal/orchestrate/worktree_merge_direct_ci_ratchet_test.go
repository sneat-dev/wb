package orchestrate

import (
	"context"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver/testfixture"
)

//nolint:paralleltest // installDirectCITestGH changes process-wide PATH with t.Setenv.
func TestDirectCIContractRejectsUnverifiedServerEvidence(t *testing.T) {
	testfixture.InstallDirectCIGH(t)
	goodPR := `{"number":17,"state":"open","head":{"ref":"integration","sha":"` + testfixture.DirectCIHead + `","repo":{"full_name":"acme/app"}},"base":{"ref":"main","repo":{"full_name":"acme/app"}}}`
	goodWorkflow := `{"id":300,"name":"Go CI","path":".github/workflows/go-ci.yml","state":"active"}`
	goodRuns := `{"total_count":1,"workflow_runs":[{"id":15,"workflow_id":300,"head_sha":"` + testfixture.DirectCIHead + `","head_branch":"integration","event":"pull_request","status":"completed","conclusion":"success","created_at":"2026-09-27T09:00:00Z","check_suite_id":305,"pull_requests":[{"number":17,"base":{"ref":"main"}}]}]}`
	for _, tc := range []struct {
		name, pullRequest, target, pr, workflow, runs, want string
		workflowError                                       bool
	}{
		{name: "missing PR selector", want: "requires an open pull request"},
		{name: "foreign head", pullRequest: "17", pr: strings.Replace(goodPR, `"full_name":"acme/app"`, `"full_name":"other/app"`, 1), want: "not an open same-repository"},
		{name: "remote head unreadable", pullRequest: "17", target: "other", pr: strings.Replace(goodPR, `"ref":"integration"`, `"ref":"other"`, 1), want: "read exact remote"},
		{name: "remote head drift", pullRequest: "17", pr: strings.Replace(goodPR, testfixture.DirectCIHead, strings.Repeat("f", 40), 1), want: "differs from remote"},
		{name: "required CI authority missing", pullRequest: "17", pr: strings.Replace(goodPR, `"ref":"main"`, `"ref":"develop"`, 1), want: "lacks an authoritative strict required Go CI aggregate"},
		{name: "workflow read fails", pullRequest: "17", workflowError: true, want: "read Go CI workflow"},
		{name: "workflow response is invalid", pullRequest: "17", workflow: "{", want: "decode Go CI workflow"},
		{name: "workflow identity is inactive", pullRequest: "17", workflow: strings.Replace(goodWorkflow, `"active"`, `"disabled"`, 1), want: "identity is not active"},
		{name: "prior runs are unreadable", pullRequest: "17", runs: "{", want: "read prior exact-head Actions runs"},
		{name: "prior run belongs to another PR", pullRequest: "17", runs: strings.Replace(goodRuns, `"number":17`, `"number":18`, 1), want: "no pull_request run"},
	} {
		//nolint:paralleltest // Child cases update the fake GitHub response environment.
		t.Run(tc.name, func(t *testing.T) {
			pr := goodPR
			if tc.pr != "" {
				pr = tc.pr
			}
			workflow := goodWorkflow
			if tc.workflow != "" {
				workflow = tc.workflow
			}
			runs := goodRuns
			if tc.runs != "" {
				runs = tc.runs
			}
			t.Setenv("WB_TEST_PR", pr)
			t.Setenv("WB_TEST_WORKFLOW", workflow)
			if tc.workflowError {
				t.Setenv("WB_TEST_WORKFLOW_ERROR", "1")
			} else {
				t.Setenv("WB_TEST_WORKFLOW_ERROR", "0")
			}
			t.Setenv("WB_TEST_RUNS", runs)
			target := tc.target
			if target == "" {
				target = "integration"
			}
			_, err := resolveWorktreeMergeDirectCIContract(context.Background(), "acme/app", target, tc.pullRequest)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("contract error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestDirectCIInputIdentityRequiresExactCommitsAndCheckout(t *testing.T) {
	t.Parallel()
	if err := verifyWorktreeMergeDirectCIInputs(context.Background(), WorktreeMergeReceipt{}); err == nil || !strings.Contains(err.Error(), "exact target and candidate identities") {
		t.Fatalf("missing exact identities error = %v", err)
	}
	broken := WorktreeMergeReceipt{TargetSHA: "target", Candidate: WorktreeMergeCandidate{SHA: "candidate", Worktree: t.TempDir()}}
	if err := verifyWorktreeMergeDirectCIInputs(context.Background(), broken); err == nil || !strings.Contains(err.Error(), "compare candidate CI inputs") {
		t.Fatalf("unreadable commit comparison error = %v", err)
	}
}
