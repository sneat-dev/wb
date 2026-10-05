package orchestrate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver/testfixture"

	"github.com/sneat-dev/wb/internal/quality"
)

func TestDirectCIDeferralNeedsExactOpenHeadPRAndPriorWorkflow(t *testing.T) {
	testfixture.InstallDirectCIGH(t)
	plan, err := resolveWorktreeMergeValidationPlan(context.Background(), "acme/app", "integration", WorktreeMergeRouteDirect, false, false, "17")
	if err != nil || !plan.Defer || plan.DirectCI == nil || plan.DirectCI.PullRequest != "17" || plan.DirectCI.Base != "main" || plan.DirectCI.WorkflowID != 300 {
		t.Fatalf("direct CI plan = %#v, err=%v", plan, err)
	}
	defaultPlan, err := resolveWorktreeMergeValidationPlan(context.Background(), "acme/app", "integration", WorktreeMergeRouteDirect, false, false)
	if err != nil || defaultPlan.Defer {
		t.Fatalf("default direct plan unexpectedly deferred: %#v, err=%v", defaultPlan, err)
	}
	t.Setenv("WB_TEST_PR", `{"number":17,"state":"closed","head":{"ref":"integration","sha":"`+testfixture.DirectCIHead+`","repo":{"full_name":"acme/app"}},"base":{"ref":"main","repo":{"full_name":"acme/app"}}}`)
	if _, err := resolveWorktreeMergeValidationPlan(context.Background(), "acme/app", "integration", WorktreeMergeRouteDirect, false, false, "17"); err == nil || !strings.Contains(err.Error(), "not an open") {
		t.Fatalf("closed PR was accepted: %v", err)
	}
	t.Setenv("WB_TEST_PR", `{"number":17,"state":"open","head":{"ref":"integration","sha":"`+testfixture.DirectCIHead+`","repo":{"full_name":"acme/app"}},"base":{"ref":"main","repo":{"full_name":"acme/app"}}}`)
	t.Setenv("WB_TEST_RUNS", `{"total_count":0,"workflow_runs":[]}`)
	if _, err := resolveWorktreeMergeValidationPlan(context.Background(), "acme/app", "integration", WorktreeMergeRouteDirect, false, false, "17"); err == nil || !strings.Contains(err.Error(), "no pull_request run") {
		t.Fatalf("missing prior workflow was accepted: %v", err)
	}
}

func TestDirectCIDeferralPublishGuardRequiresFreshPlanAndExactCandidate(t *testing.T) {
	t.Parallel()
	receipt := WorktreeMergeReceipt{Status: WorktreeMergePrepared, Route: WorktreeMergeRouteDecision{Route: WorktreeMergeRouteDirect}, Candidate: WorktreeMergeCandidate{SHA: testfixture.DirectCIHead},
		Validation:         quality.VerificationReport{Status: quality.StatusSkipped, Revision: testfixture.DirectCIHead},
		ValidationDeferral: &WorktreeMergeValidationDeferral{Route: WorktreeMergeRouteDirect, CandidateSHA: testfixture.DirectCIHead, DirectCIPullRequest: "17", DirectCIPullRequestNumber: 17, DirectCIBase: "main", DirectCIWorkflowID: 300}}
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
	testfixture.InstallDirectCIGH(t)
	receipt := WorktreeMergeReceipt{Repository: "acme/app", Target: "integration"}
	contract := worktreeMergeDirectCIContract{PullRequest: "17", Base: "main", WorkflowID: 300}
	if err := verifyWorktreeMergeDirectCIPullRequest(context.Background(), receipt, contract, testfixture.DirectCIHead); err != nil {
		t.Fatal(err)
	}
	if err := verifyWorktreeMergeDirectCIPullRequest(context.Background(), receipt, contract, "different"); err == nil {
		t.Fatal("a PR at another head satisfied the landed SHA")
	}
	t.Setenv("WB_TEST_PR", `{"number":17,"state":"closed","head":{"ref":"integration","sha":"`+testfixture.DirectCIHead+`","repo":{"full_name":"acme/app"}},"base":{"ref":"main","repo":{"full_name":"acme/app"}}}`)
	if err := verifyWorktreeMergeDirectCIPullRequest(context.Background(), receipt, contract, testfixture.DirectCIHead); err == nil {
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
