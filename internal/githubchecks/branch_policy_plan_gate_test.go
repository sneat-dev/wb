package githubchecks

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver"
)

// errPlanGate403 is the error githubobserver returns for GitHub's refusal of a
// branch-policy read on a private repository in a free organisation, as
// reproduced on meaninggraph/meaninggraph on 2026-10-28.
var errPlanGate403 = errors.New("GitHub repos/acme/app/rules/branches/main?per_page=100 returned HTTP 403: gh: " + githubBranchPolicyPlanGateMessage + " (HTTP 403)")

// freePlanBranch is what GitHub reports for main on such a repository: not
// protected, with an empty, unenforced required-status-checks block.
const freePlanBranch = `{"protected":false,"protection":{"enabled":false,"required_status_checks":{"checks":[],"contexts":[],"enforcement_level":"off"}}}`

// branchPolicyReader answers the target-policy reads of a requiredChecksReceipt
// with the given classic branch view and the given refusals of the classic
// detail and rulesets reads.
func branchPolicyReader(t *testing.T, branch string, detailErr, rulesErr error) (context.Context, *int) {
	t.Helper()
	rulesReads := 0
	return githubobserver.WithReader(context.Background(), githubobserver.Reader{
		Get: func(_ context.Context, request githubobserver.GetRequest) (githubobserver.Response, error) {
			switch request.Endpoint {
			case "repos/acme/app/branches/main":
				return githubobserver.Response{Body: []byte(branch)}, nil
			case "repos/acme/app/branches/main/protection/required_status_checks":
				return githubobserver.Response{}, detailErr
			case "repos/acme/app/rules/branches/main?per_page=100":
				rulesReads++
				return githubobserver.Response{}, rulesErr
			case "repos/acme/app/pulls/7":
				return githubobserver.Response{Body: []byte(`{"number":7,"head":{"sha":"head"},"base":{"ref":"main"}}`)}, nil
			}
			t.Fatalf("unexpected GitHub read %s", request.Endpoint)
			return githubobserver.Response{}, nil
		},
	}), &rulesReads
}

func TestOnlyGitHubsPlanGateReadsAsBranchPolicyUnavailableByPlan(t *testing.T) {
	t.Parallel()
	for message, want := range map[string]bool{
		errPlanGate403.Error(): true,
		`GitHub repos/acme/app/rules/branches/main returned HTTP 403: {"message":"` + githubBranchPolicyPlanGateMessage + `","status":"403"}`: true,
		"GitHub repos/acme/app/rules/branches/main returned HTTP 403: gh: Resource not accessible by integration (HTTP 403)":                  false,
		"GitHub repos/acme/app/rules/branches/main returned HTTP 403: gh: Forbidden (HTTP 403)":                                               false,
		"GitHub repos/acme/app/rules/branches/main returned HTTP 403: gh: Upgrade your plan to enable this feature. (HTTP 403)":               false,
		`GitHub repos/acme/app/rules/branches/main returned HTTP 404: {"message":"` + githubBranchPolicyPlanGateMessage + `"}`:                false,
	} {
		if got := IsBranchPolicyUnavailableByPlan(errors.New(message)); got != want {
			t.Errorf("IsBranchPolicyUnavailableByPlan(%q) = %t, want %t", message, got, want)
		}
	}
	if IsBranchPolicyUnavailableByPlan(nil) {
		t.Error("a nil error is not a plan gate")
	}
}

func TestTargetMergeQueueReadsAPlanWithoutRulesetsAsNoQueueAndSaysWhy(t *testing.T) {
	t.Parallel()
	policy, unavailable, err := TargetMergeQueue(mergeQueueRulesReader(t, "", errPlanGate403), "acme/app", "main")
	if err != nil || policy != nil {
		t.Fatalf("policy = %+v, err = %v; want no queue", policy, err)
	}
	if !strings.Contains(unavailable, githubBranchPolicyPlanGateMessage) {
		t.Fatalf("rulesetsUnavailable = %q, want GitHub's refusal recorded", unavailable)
	}
}

func TestTargetMergeQueueStillFailsClosedOnAnyOther403(t *testing.T) {
	t.Parallel()
	forbidden := errors.New("GitHub repos/acme/app/rules/branches/main?per_page=100 returned HTTP 403: gh: Resource not accessible by integration (HTTP 403)")
	policy, unavailable, err := TargetMergeQueue(mergeQueueRulesReader(t, "", forbidden), "acme/app", "main")
	if err == nil || policy != nil || unavailable != "" || !strings.Contains(err.Error(), "read active branch rules for target main") {
		t.Fatalf("policy = %+v, unavailable = %q, err = %v; want a fail-closed error", policy, unavailable, err)
	}
}

func TestRequiredChecksReceiptOnAFreePlanPrivateRepositoryRecordsNoRulesetsInsteadOfFailing(t *testing.T) {
	t.Parallel()
	ctx, rulesReads := branchPolicyReader(t, freePlanBranch, errPlanGate403, errPlanGate403)
	options := PullRequestWaitOptions{Repository: "acme/app", PullRequest: "7", Target: "main"}
	cache := &requiredChecksCache{}
	checks, authority, freshness, unavailable, reason := requiredChecksReceipt(ctx, options, cache)
	if reason != "" {
		t.Fatalf("reason = %q, want the plan gate read as no rulesets", reason)
	}
	if len(checks) != 0 || freshness != "" || unavailable != "" {
		t.Fatalf("checks = %v, freshness = %q, policyUnavailable = %q; want no checks and no fence", checks, freshness, unavailable)
	}
	if authority != "github-branch-protection+rulesets-unavailable-by-plan+pr-base-verified" {
		t.Fatalf("authority = %q, want the plan gate recorded in the receipt", authority)
	}
	// A later observation reuses the cached policy, gap included.
	_, cachedAuthority, _, _, reason := requiredChecksReceipt(ctx, options, cache)
	if reason != "" || cachedAuthority != authority || *rulesReads != 1 {
		t.Fatalf("cached authority = %q, reason = %q, rules reads = %d", cachedAuthority, reason, *rulesReads)
	}
}

func TestRequiredChecksStillFailClosedOnAnyOther403FromTheRulesetsRead(t *testing.T) {
	t.Parallel()
	forbidden := errors.New("GitHub repos/acme/app/rules/branches/main?per_page=100 returned HTTP 403: gh: Resource not accessible by integration (HTTP 403)")
	ctx, _ := branchPolicyReader(t, `{"protected":false,"protection":{}}`, nil, forbidden)
	_, _, reason := RequiredChecks(ctx, "acme/app", "main", true)
	if !strings.Contains(reason, "read active branch rules for target main") || !strings.Contains(reason, "Resource not accessible") {
		t.Fatalf("reason = %q, want a fail-closed refusal", reason)
	}
}

func TestRequiredChecksRefuseAPlanGateThatClassicProtectionContradicts(t *testing.T) {
	t.Parallel()
	ctx, _ := branchPolicyReader(t, `{"protected":true,"protection":{}}`, nil, errPlanGate403)
	_, _, reason := RequiredChecks(ctx, "acme/app", "main", true)
	if !strings.Contains(reason, "classic protection still protects main, so the plan gate is not corroborated") {
		t.Fatalf("reason = %q, want a fail-closed refusal", reason)
	}
}

func TestRequiredChecksStillFailClosedWhenAProtectedBranchsDetailIsPlanGated(t *testing.T) {
	t.Parallel()
	ctx, _ := branchPolicyReader(t, `{"protected":true,"protection":{"required_status_checks":{"checks":[],"contexts":[]}}}`, errPlanGate403, nil)
	_, _, reason := RequiredChecks(ctx, "acme/app", "main", true)
	if !strings.Contains(reason, "read authoritative required-status-check policy for main") {
		t.Fatalf("reason = %q, want a fail-closed refusal", reason)
	}
}
