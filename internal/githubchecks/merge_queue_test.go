package githubchecks

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver"
)

// mergeQueueRulesReader answers the target-policy reads a merge-queue target
// produces: classic branch protection, its strict detail, active rules and
// the pull request itself.
func mergeQueueRulesReader(t *testing.T, rules string, rulesErr error) context.Context {
	t.Helper()
	return githubobserver.WithReader(context.Background(), githubobserver.Reader{
		Get: func(_ context.Context, request githubobserver.GetRequest) (githubobserver.Response, error) {
			switch request.Endpoint {
			case "repos/acme/app/rules/branches/main?per_page=100":
				if rulesErr != nil {
					return githubobserver.Response{}, rulesErr
				}
				return githubobserver.Response{Body: []byte(rules)}, nil
			case "repos/acme/app/branches/main":
				return githubobserver.Response{Body: []byte(`{"protected":true,"protection":{}}`)}, nil
			case "repos/acme/app/pulls/7":
				return githubobserver.Response{Body: []byte(`{"number":7,"head":{"sha":"head"},"base":{"ref":"main"}}`)}, nil
			}
			t.Fatalf("unexpected GitHub read %s", request.Endpoint)
			return githubobserver.Response{}, nil
		},
	})
}

const datatugShapedRules = `[{"type":"required_status_checks","ruleset_id":25200937,"ruleset_source":"acme/app","parameters":{"strict_required_status_checks_policy":false,"required_status_checks":[{"context":"test","integration_id":15368}]}},` +
	`{"type":"merge_queue","ruleset_id":25200937,"ruleset_source":"acme/app","parameters":{"merge_method":"MERGE","grouping_strategy":"ALLGREEN"}}]`

func TestTargetMergeQueueReadsTheQueueRuleItsMethodAndGrouping(t *testing.T) {
	t.Parallel()
	policy, _, err := TargetMergeQueue(mergeQueueRulesReader(t, datatugShapedRules, nil), "acme/app", "main")
	if err != nil {
		t.Fatal(err)
	}
	want := MergeQueuePolicy{RulesetID: 25200937, RulesetSource: "acme/app", MergeMethod: "merge", GroupingStrategy: "ALLGREEN"}
	if policy == nil || *policy != want {
		t.Fatalf("policy = %+v, want %+v", policy, want)
	}
}

func TestTargetMergeQueueReportsNoQueueWhenNoRuleRequiresOne(t *testing.T) {
	t.Parallel()
	policy, _, err := TargetMergeQueue(mergeQueueRulesReader(t, `[{"type":"pull_request"}]`, nil), "acme/app", "main")
	if err != nil || policy != nil {
		t.Fatalf("policy = %+v, err = %v; want no queue", policy, err)
	}
}

func TestTargetMergeQueueNeverReadsAnUnreadableRuleSetAsNoQueue(t *testing.T) {
	t.Parallel()
	_, _, err := TargetMergeQueue(mergeQueueRulesReader(t, "", errors.New("HTTP 502")), "acme/app", "main")
	if err == nil || !strings.Contains(err.Error(), "read active branch rules for target main: HTTP 502") {
		t.Fatalf("err = %v", err)
	}
}

func TestTargetMergeQueueRefusesAMergeMethodItDoesNotRecognise(t *testing.T) {
	t.Parallel()
	_, _, err := TargetMergeQueue(mergeQueueRulesReader(t, `[{"type":"merge_queue","ruleset_id":4,"parameters":{"merge_method":"FAST_FORWARD"}}]`, nil), "acme/app", "main")
	if err == nil || !strings.Contains(err.Error(), `merge queue ruleset 4 on main names merge method "FAST_FORWARD"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestMergeQueueReceiptNamesTheQueueAsFreshnessAuthorityWithoutTheStrictPolicy(t *testing.T) {
	t.Parallel()
	ctx := mergeQueueRulesReader(t, datatugShapedRules, nil)
	_, _, freshness, _, reason := requiredChecksReceipt(ctx, PullRequestWaitOptions{Repository: "acme/app", PullRequest: "7", Target: "main", MergeQueue: true}, nil)
	if reason != "" || freshness != MergeQueueFreshnessAuthority {
		t.Fatalf("freshness = %q, reason = %q", freshness, reason)
	}
	// The same target without the merge-queue landing keeps failing closed:
	// a source-head receipt cannot authorize what the queue will merge.
	_, _, _, _, reason = requiredChecksReceipt(ctx, PullRequestWaitOptions{Repository: "acme/app", PullRequest: "7", Target: "main"}, nil)
	if !strings.Contains(reason, "requires GitHub's merge queue, so no source-head receipt can authorize a merge") || !strings.Contains(reason, "wb pr land") {
		t.Fatalf("reason = %q", reason)
	}
}

func TestMergeQueueWaitAcceptsACandidateBehindAMovingTarget(t *testing.T) {
	t.Parallel()
	options, ops := exactWaitFixture()
	options.PullRequest, options.MergeQueue = "7", true
	targetReads := 0
	ops.targetHead = func(context.Context, string, string) (string, string) {
		targetReads++
		return "main-" + strings.Repeat("x", targetReads), ""
	}
	ops.containsTarget = func(context.Context, string, string, string) (bool, string) { return false, "" }
	result, err := waitForCommitChecksWith(context.Background(), options, ops)
	if err != nil || result.Status != PullRequestWaitPassed {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if result.CandidateContainsTarget || !strings.Contains(result.Reason, "delegated to GitHub's merge queue") {
		t.Fatalf("receipt must record the candidate as behind and the queue as the fence: %+v", result)
	}
}

func TestSourceHeadWaitStillRejectsACandidateBehindTheTargetWithoutTheQueue(t *testing.T) {
	t.Parallel()
	options, ops := exactWaitFixture()
	options.PullRequest = "7"
	ops.containsTarget = func(context.Context, string, string, string) (bool, string) { return false, "" }
	result, err := waitForCommitChecksWith(context.Background(), options, ops)
	if err != nil || result.Status != PullRequestWaitFailed || !strings.Contains(result.Reason, "does not contain current target") {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

func TestOnlyARefusedPolicyReadIsAnAcceptedMergeQueuePolicyGap(t *testing.T) {
	t.Parallel()
	for message, want := range map[string]bool{
		"read active branch rules for target main: Upgrade to GitHub Pro or make this repository public to enable this feature. (HTTP 403)": true,
		"read active branch rules for target main: Resource not accessible by integration (HTTP 403)":                                       true,
		"read active branch rules for target main: HTTP 502":                                                                                false,
		`merge queue ruleset 4 on main names merge method "OCTOPUS", which WB does not recognise`:                                           false,
	} {
		if got := IsPolicyUnavailableByPlan(errors.New(message)); got != want {
			t.Errorf("IsPolicyUnavailableByPlan(%q) = %t, want %t", message, got, want)
		}
	}
	if IsPolicyUnavailableByPlan(nil) {
		t.Error("a nil error is not a policy gap")
	}
}
