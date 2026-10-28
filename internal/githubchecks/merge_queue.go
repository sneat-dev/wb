package githubchecks

import (
	"context"
	"fmt"
	"strings"
)

// MergeQueueFreshnessAuthority names GitHub's merge queue as the freshness
// authority of a candidate receipt taken with PullRequestWaitOptions.MergeQueue.
const MergeQueueFreshnessAuthority = "github merge queue"

// MergeQueuePolicy is the active merge_queue ruleset rule on one target
// branch. Its presence means GitHub expects every pull request into the target
// to be merged by the queue, never by a direct merge call.
type MergeQueuePolicy struct {
	RulesetID     int64  `json:"ruleset_id"`
	RulesetSource string `json:"ruleset_source,omitempty"`
	// MergeMethod is the queue's own method, lower case: merge, squash or
	// rebase. The queue uses it whatever the caller asks for.
	MergeMethod      string `json:"merge_method"`
	GroupingStrategy string `json:"grouping_strategy,omitempty"`
}

// TargetMergeQueue reports the merge queue the target's active rules require,
// or nil when none does. An unreadable rule set is an error, never "no
// queue": a caller that guessed wrong would merge around the queue. The one
// exception is a corroborated plan gate (activeRulesCorroborated): no queue can
// apply there, so it returns no queue and GitHub's refusal as
// rulesetsUnavailable for the caller's receipt.
func TargetMergeQueue(ctx context.Context, repository, target string) (policy *MergeQueuePolicy, rulesetsUnavailable string, err error) {
	pages, rulesetsUnavailable, err := activeRulesCorroborated(ctx, repository, target, func() (bool, error) {
		branch, err := readTargetBranchPolicy(ctx, repository, target)
		if err != nil {
			return false, err
		}
		return *branch.Protected, nil
	})
	if err != nil {
		return nil, "", err
	}
	policy, err = mergeQueueFromRules(pages, target)
	return policy, rulesetsUnavailable, err
}

// IsTargetPolicyRead403 reports any target-policy read GitHub refused with a
// 403 — a plan gate, a missing scope, an integration without access alike —
// the class requiredChecksReceipt accepts as unavailable policy under an
// explicit --allow-unfenced. That flag then also gives up merge-queue
// detection: the receipt records the gap, and a caller that must never merge
// around a queue does not pass it. It is deliberately broader than
// IsBranchPolicyUnavailableByPlan, the one 403 that means "no rules".
func IsTargetPolicyRead403(err error) bool {
	return err != nil && isTargetPolicyRead403(err.Error())
}

// activeRulesCorroborated reads the target's active rules, and is the one
// place that decides what GitHub's plan-gate refusal of that read means, so a
// merge-queue read and a required-checks read of the same target cannot
// disagree. A plan that offers no rulesets offers no classic protection
// either, so the refusal means "no ruleset applies" only while classic
// protection agrees the target is unprotected; protected is consulted only
// then. A target classic protection still protects contradicts the gate, and
// the read fails closed rather than skip rules that may exist.
func activeRulesCorroborated(ctx context.Context, repository, target string, protected func() (bool, error)) (pages [][]ActiveBranchRule, rulesetsUnavailable string, err error) {
	pages, err = ActiveRules(ctx, repository, target)
	if !IsBranchPolicyUnavailableByPlan(err) {
		if err != nil {
			return nil, "", fmt.Errorf("read active branch rules for target %s: %w", target, err)
		}
		return pages, "", nil
	}
	isProtected, protectedErr := protected()
	if protectedErr != nil {
		return nil, "", protectedErr
	}
	if isProtected {
		return nil, "", fmt.Errorf("read active branch rules for target %s: %w; classic protection still protects %s, so the plan gate is not corroborated", target, err, target)
	}
	return nil, err.Error(), nil
}

// githubBranchPolicyPlanGateMessage is GitHub's exact refusal of a rulesets
// or classic branch-protection read on a private repository whose plan offers
// neither (a free organisation or personal account).
const githubBranchPolicyPlanGateMessage = "Upgrade to GitHub Pro or make this repository public to enable this feature."

// IsBranchPolicyUnavailableByPlan reports the one 403 GitHub answers a
// branch-policy read with when the repository's plan cannot have that policy.
// None of it can then apply to the target, so it is "no rules", not an
// unreadable policy. Every other 403 — a missing scope, an integration without
// access — leaves policy that may exist unread and must still fail closed.
func IsBranchPolicyUnavailableByPlan(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "gh: "+githubBranchPolicyPlanGateMessage+" (HTTP 403)") ||
		(strings.Contains(message, "HTTP 403") && strings.Contains(message, `"message":"`+githubBranchPolicyPlanGateMessage+`"`))
}

func mergeQueueFromRules(pages [][]ActiveBranchRule, target string) (*MergeQueuePolicy, error) {
	for _, page := range pages {
		for _, rule := range page {
			if strings.TrimSpace(rule.Type) != "merge_queue" {
				continue
			}
			method := strings.ToLower(strings.TrimSpace(rule.Parameters.MergeMethod))
			switch method {
			case "merge", "squash", "rebase":
			default:
				return nil, fmt.Errorf("merge queue ruleset %d on %s names merge method %q, which WB does not recognise", rule.RulesetID, target, rule.Parameters.MergeMethod)
			}
			return &MergeQueuePolicy{
				RulesetID:        rule.RulesetID,
				RulesetSource:    strings.TrimSpace(rule.RulesetSource),
				MergeMethod:      method,
				GroupingStrategy: strings.TrimSpace(rule.Parameters.GroupingStrategy),
			}, nil
		}
	}
	return nil, nil
}
