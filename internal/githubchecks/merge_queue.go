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
// queue": a caller that guessed wrong would merge around the queue.
func TargetMergeQueue(ctx context.Context, repository, target string) (*MergeQueuePolicy, error) {
	pages, err := ActiveRules(ctx, repository, target)
	if err != nil {
		return nil, fmt.Errorf("read active branch rules for target %s: %w", target, err)
	}
	return mergeQueueFromRules(pages, target)
}

// IsPolicyUnavailableByPlan reports a target-policy read GitHub refused with
// a 403, the same class requiredChecksReceipt accepts as unavailable policy
// under an explicit --allow-unfenced. That flag then also gives up merge-queue
// detection: the receipt records the gap, and a caller that must never merge
// around a queue does not pass it.
func IsPolicyUnavailableByPlan(err error) bool {
	return err != nil && isGitHubPolicyPlan403(err.Error())
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
