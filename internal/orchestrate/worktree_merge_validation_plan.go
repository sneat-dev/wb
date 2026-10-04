package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

func ResolveWorktreeMergeRoute(ctx context.Context, repository, target string, requested WorktreeMergeRoute) (WorktreeMergeRouteDecision, error) {
	if requested == "" {
		requested = WorktreeMergeRouteAuto
	}
	if requested != WorktreeMergeRouteAuto && requested != WorktreeMergeRouteDirect && requested != WorktreeMergeRoutePullRequest {
		return WorktreeMergeRouteDecision{}, fmt.Errorf("unsupported merge route %q", requested)
	}
	escapedTarget := url.PathEscape(target)
	branchEndpoint := "repos/" + repository + "/branches/" + escapedTarget
	branchOutput, err := githubGet(ctx, "", repository, target, "", branchEndpoint)
	if err != nil {
		return conservativeWorktreeMergePRRoute(requested, fmt.Sprintf("target branch policy is unavailable: %v", err))
	}
	var branch struct {
		Protected  *bool `json:"protected"`
		Protection struct {
			RequiredPullRequestReviews json.RawMessage `json:"required_pull_request_reviews"`
		} `json:"protection"`
	}
	if err := json.Unmarshal(branchOutput, &branch); err != nil || branch.Protected == nil {
		return conservativeWorktreeMergePRRoute(requested, fmt.Sprintf("authoritative target branch policy for %s is incomplete", target))
	}
	pages, err := activeBranchRules(ctx, repository, target)
	if err != nil {
		return conservativeWorktreeMergePRRoute(requested, fmt.Sprintf("active target rules are unavailable: %v", err))
	}
	requiresPR := len(branch.Protection.RequiredPullRequestReviews) > 0 && string(branch.Protection.RequiredPullRequestReviews) != "null"
	mergeQueue := false
	unknownRule := false
	for _, page := range pages {
		for _, rule := range page {
			switch strings.TrimSpace(rule.Type) {
			case "pull_request":
				requiresPR = true
			case "merge_queue":
				mergeQueue = true
			case "required_status_checks", "creation", "update", "deletion", "non_fast_forward", "required_linear_history", "required_signatures", "commit_author_email_pattern", "commit_message_pattern", "branch_name_pattern", "tag_name_pattern":
			default:
				unknownRule = true
			}
		}
	}
	decision := WorktreeMergeRouteDecision{Requested: requested}
	if mergeQueue {
		decision.Route, decision.Reason = WorktreeMergeRouteUnsupported, "target requires a merge queue, whose merge-group receipt is not implemented"
		return decision, nil
	}
	if requested == WorktreeMergeRouteDirect {
		if *branch.Protected || requiresPR || unknownRule || activeRuleCount(pages) != 0 {
			return decision, fmt.Errorf("direct route is not authoritatively permitted by target policy")
		}
		decision.Route, decision.Reason = WorktreeMergeRouteDirect, "explicit direct route is permitted by authoritative target policy"
		return decision, nil
	}
	if requested == WorktreeMergeRoutePullRequest {
		decision.Route, decision.Reason = WorktreeMergeRoutePullRequest, "explicit pull-request route"
		return decision, nil
	}
	if !*branch.Protected && !requiresPR && !unknownRule && activeRuleCount(pages) == 0 {
		decision.Route, decision.Reason = WorktreeMergeRouteDirect, "target is authoritatively unprotected and has no active rules"
	} else {
		decision.Route, decision.Reason = WorktreeMergeRoutePullRequest, "target protection or conservative policy requires a pull request"
	}
	return decision, nil
}

func conservativeWorktreeMergePRRoute(requested WorktreeMergeRoute, reason string) (WorktreeMergeRouteDecision, error) {
	decision := WorktreeMergeRouteDecision{Requested: requested, Route: WorktreeMergeRoutePullRequest, Reason: reason + "; selecting a pull request conservatively"}
	if requested == WorktreeMergeRouteDirect {
		return decision, fmt.Errorf("direct route is not authoritatively permitted: %s", reason)
	}
	return decision, nil
}

// validateWorktreeMergeCandidate validates the candidate first. A passing
// candidate cannot regress a red target, so the expensive target snapshot is
// evaluated lazily only when candidate failure evidence needs comparison.
// Any new or changed candidate failure remains a hard gate.
// worktreeMergeValidationPlan is resolved exactly once per LandWorktreeMerge
// (and per PrepareWorktreeMerge) call and then threaded through every
// validation site in that call (see applyOrDeferWorktreeMergeValidation),
// instead of each site independently deciding whether to skip local
// validation. Route is also the single decision every publish/landing
// transition in the same call reuses (see requireWorktreeMergePublishedValidation),
// so a route resolved as "pr" and deferred cannot be silently published on a
// route later resolved as "direct" within a different call — see red-team
// finding B1 (sneat-dev/wb#591).
type worktreeMergeValidationPlan struct {
	Route    WorktreeMergeRouteDecision
	Defer    bool
	Reason   string
	DirectCI *worktreeMergeDirectCIContract
}

// resolveWorktreeMergeValidationPlan resolves the merge route once and
// decides whether this call may defer local candidate validation to the
// pull-request route's authoritative CI. It never defers when validateLocally
// is set (the --validate-locally escape hatch) or when the resolved route is
// not the pull-request route.
// worktreeMergeValidationPlanHolder memoizes one resolveWorktreeMergeValidationPlan
// call across every validation site and the publish/landing guard in a single
// LandWorktreeMerge invocation, resolved lazily on first use (see the "resolve
// once, lazily" note in LandWorktreeMerge). It is not safe for concurrent use;
// LandWorktreeMerge is not itself concurrent.
type worktreeMergeValidationPlanHolder struct {
	resolved bool
	plan     worktreeMergeValidationPlan
	err      error
}

func (h *worktreeMergeValidationPlanHolder) resolve(ctx context.Context, repository, target string, requestedRoute WorktreeMergeRoute, validateLocally, allowUnfenced bool, directCIPullRequest ...string) (worktreeMergeValidationPlan, error) {
	if !h.resolved {
		h.plan, h.err = resolveWorktreeMergeValidationPlan(ctx, repository, target, requestedRoute, validateLocally, allowUnfenced, directCIPullRequest...)
		h.resolved = true
	}
	return h.plan, h.err
}

// resolveWorktreeMergeValidationPlan never defers when validateLocally or
// allowUnfenced is set for THIS call (red-team finding X1): AllowUnfenced
// tells ciwait it may accept an unfenced or unreadable policy as a merge
// gate, which is exactly the authoritative-fence guarantee deferral relies
// on, so a call made with either flag must always validate locally.
func resolveWorktreeMergeValidationPlan(ctx context.Context, repository, target string, requestedRoute WorktreeMergeRoute, validateLocally, allowUnfenced bool, directCIPullRequest ...string) (worktreeMergeValidationPlan, error) {
	decision, err := ResolveWorktreeMergeRoute(ctx, repository, target, requestedRoute)
	if err != nil {
		return worktreeMergeValidationPlan{}, err
	}
	plan := worktreeMergeValidationPlan{Route: decision}
	if len(directCIPullRequest) > 0 && strings.TrimSpace(directCIPullRequest[0]) != "" {
		if requestedRoute != WorktreeMergeRouteDirect || decision.Route != WorktreeMergeRouteDirect || validateLocally || allowUnfenced {
			return plan, fmt.Errorf("direct CI deferral requires --route direct without --validate-locally or --allow-unfenced")
		}
		contract, contractErr := resolveWorktreeMergeDirectCIContract(ctx, repository, target, directCIPullRequest[0])
		if contractErr != nil {
			return plan, fmt.Errorf("direct CI deferral cannot prove open head PR and workflow: %w", contractErr)
		}
		plan.Defer, plan.DirectCI = true, contract
		plan.Reason = fmt.Sprintf("exact Go CI on open head PR %s into %s will validate the direct target SHA", contract.PullRequest, contract.Base)
		return plan, nil
	}
	if validateLocally || allowUnfenced || decision.Route != WorktreeMergeRoutePullRequest {
		return plan, nil
	}
	eligible, reason := worktreeMergeValidationDeferralEligible(ctx, repository, target)
	plan.Defer, plan.Reason = eligible, reason
	return plan, nil
}

// worktreeMergeValidationDeferralEligible reports whether the pull-request
// route may defer local candidate validation to CI (red-team finding B2). It
// requires the target's required-check policy to have been read
// authoritatively (never under an AllowUnfenced fallback), to be non-empty,
// and to be fenced by a server-enforced strict up-to-date policy. Any of an
// unreadable policy, zero required checks, or an unfenced policy keeps
// validation local.
func worktreeMergeValidationDeferralEligible(ctx context.Context, repository, target string) (bool, string) {
	checks, freshness, reason := targetBranchRequiredChecks(ctx, repository, target, true)
	if reason != "" {
		return false, fmt.Sprintf("required-check policy for %s is unreadable, so validation stays local: %s", target, reason)
	}
	if len(checks) == 0 {
		return false, fmt.Sprintf("target %s has no required status checks, so validation stays local", target)
	}
	if freshness == "" {
		return false, fmt.Sprintf("target %s has no server-enforced strict up-to-date fence, so validation stays local", target)
	}
	return true, fmt.Sprintf("target %s has an authoritative, non-empty, server-fenced required-check policy (%s); the pull-request route defers local validation to it", target, freshness)
}
