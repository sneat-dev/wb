package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// MaxForegroundCheckWaitSlice keeps a single agent-tool call under the common
// ten-minute harness ceiling. Longer CI is observed by explicit re-invocation,
// never a detached worker or a hidden thirty-minute loop.
const MaxForegroundCheckWaitSlice = 9 * time.Minute

// DefaultCheckPollInterval deliberately leaves room for other WB operations
// sharing the authenticated GitHub user budget. A PR receipt still reads every
// dynamic fact on each observation; only static branch policy is cached within
// the bounded slice and is fetched again before a pass is returned.
const DefaultCheckPollInterval = 30 * time.Second

// WaitForCommitChecks observes checks for one exact target commit. PullRequest
// is optional: when present it corroborates that exact PR head and target and
// augments the exact-head check-run/status receipt with GitHub's PR view.
// Every mode observes the exact commit through producer-aware APIs. Pending is
// an intermediate terminal result that callers resume with the same identity,
// not successful completion.
func WaitForCommitChecks(ctx context.Context, options PullRequestWaitOptions) (PullRequestWaitResult, error) {
	if strings.TrimSpace(options.Repository) == "" || strings.TrimSpace(options.Target) == "" || strings.TrimSpace(options.Head) == "" {
		return PullRequestWaitResult{}, fmt.Errorf("repository, target, and exact head are required")
	}
	if options.Slice <= 0 || options.Slice > MaxForegroundCheckWaitSlice {
		return PullRequestWaitResult{}, fmt.Errorf("check wait slice must be positive and at most %s", MaxForegroundCheckWaitSlice)
	}
	if options.CheckPollInterval <= 0 {
		return PullRequestWaitResult{}, fmt.Errorf("check poll interval must be positive")
	}
	if options.CheckPollInterval >= options.Slice {
		return PullRequestWaitResult{}, fmt.Errorf("check poll interval must be shorter than the foreground slice so a terminal snapshot can be reread")
	}
	result := PullRequestWaitResult{
		Repository:  options.Repository,
		PullRequest: options.PullRequest,
		Target:      options.Target,
		Head:        options.Head,
	}
	deadline := time.Now().Add(options.Slice)
	sliceCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	stableFingerprint := ""
	stableObservations := 0
	policyCache := requiredChecksCache{}
	for {
		if err := sliceCtx.Err(); err != nil {
			if err == context.DeadlineExceeded {
				return pendingCommitWaitResult(result), nil
			}
			return failedCommitWaitResult(result, err.Error()), nil
		}
		observedTargetHead := ""
		if options.PullRequest != "" {
			observedHead, observedTarget, reason := pullRequestIdentity(sliceCtx, options.Repository, options.PullRequest)
			result.ObservedHead = observedHead
			if reason != "" {
				if sliceCtx.Err() == context.DeadlineExceeded {
					return pendingCommitWaitResult(result), nil
				}
				return failedCommitWaitResult(result, reason), nil
			}
			if observedHead != options.Head {
				return failedCommitWaitResult(result, fmt.Sprintf("pull request head drifted from %s to %s; start a new exact wait", options.Head, observedHead)), nil
			}
			if observedTarget != options.Target {
				return failedCommitWaitResult(result, fmt.Sprintf("pull request target drifted from %s to %s; start a new exact wait", options.Target, observedTarget)), nil
			}
			observedTargetHead, reason = targetHead(sliceCtx, options.Repository, options.Target)
			result.ObservedTargetHead = observedTargetHead
			if reason != "" {
				if sliceCtx.Err() == context.DeadlineExceeded {
					return pendingCommitWaitResult(result), nil
				}
				return failedCommitWaitResult(result, "read exact pull-request target head: "+reason), nil
			}
			containsTarget, reason := candidateContainsTarget(sliceCtx, options.Repository, observedTargetHead, options.Head)
			if reason != "" {
				if sliceCtx.Err() == context.DeadlineExceeded {
					return pendingCommitWaitResult(result), nil
				}
				return failedCommitWaitResult(result, reason), nil
			}
			result.CandidateContainsTarget = containsTarget
			if !containsTarget {
				return failedCommitWaitResult(result, fmt.Sprintf("pull request head %s does not contain current target %s at %s; rebase or reintegrate before waiting or merging", options.Head, options.Target, observedTargetHead)), nil
			}
		} else {
			observedHead, reason := targetHead(sliceCtx, options.Repository, options.Target)
			result.ObservedHead = observedHead
			if reason != "" {
				if sliceCtx.Err() == context.DeadlineExceeded {
					return pendingCommitWaitResult(result), nil
				}
				return failedCommitWaitResult(result, reason), nil
			}
			if observedHead != options.Head {
				return failedCommitWaitResult(result, fmt.Sprintf("target %s advanced from exact head %s to %s; start a new exact wait", options.Target, options.Head, observedHead)), nil
			}
			observedTargetHead = observedHead
			result.ObservedTargetHead = observedHead
			result.CandidateContainsTarget = true
		}

		checks, pending, reason := commitChecks(sliceCtx, options)
		if reason != "" {
			if sliceCtx.Err() == context.DeadlineExceeded {
				return pendingCommitWaitResult(result), nil
			}
			return failedCommitWaitResult(result, reason), nil
		}
		result.Checks = checks
		failed := false
		for _, check := range checks {
			switch check.Bucket {
			case "pass", "skipping":
			case "fail", "cancel":
				failed = true
			default:
				pending = true
			}
		}
		if failed {
			return failedCommitWaitResult(result, "observed GitHub checks failed or were cancelled"), nil
		}

		requiredChecks, authority, freshnessAuthority, authorityReason := requiredChecksReceipt(sliceCtx, options, &policyCache)
		if authorityReason != "" {
			if sliceCtx.Err() == context.DeadlineExceeded && strings.TrimSpace(result.Reason) != "" {
				return pendingCommitWaitResult(result), nil
			}
			result.Reason = "required-check authority is unavailable; terminal CI evidence is incomplete: " + authorityReason
			return pendingCommitWaitResult(result), nil
		}
		result.RequiredChecks = requiredChecks
		result.RequiredChecksAuthority = authority
		result.TargetFreshnessAuthority = freshnessAuthority
		if options.PullRequest != "" && freshnessAuthority == "" {
			return failedCommitWaitResult(result, "target policy has no nonempty server-enforced strict up-to-date fence; check observations cannot authorize an automatic merge"), nil
		}
		missingRequired := missingRequiredChecks(checks, requiredChecks)
		terminal := len(checks) > 0 && !pending && len(missingRequired) == 0
		if terminal {
			fingerprint := terminalChecksFingerprint(checks, requiredChecks, authority, observedTargetHead, freshnessAuthority)
			if fingerprint == stableFingerprint {
				stableObservations++
			} else {
				stableFingerprint = fingerprint
				stableObservations = 1
			}
			result.StableObservations = stableObservations
		} else {
			stableFingerprint = ""
			stableObservations = 0
			result.StableObservations = 0
			switch {
			case len(missingRequired) > 0:
				result.Reason = "required GitHub checks have not registered for the exact head: " + strings.Join(missingRequired, ", ")
			case len(checks) == 0:
				result.Reason = "no GitHub checks have registered for the exact head"
			default:
				result.Reason = "observed GitHub checks are still pending"
			}
		}
		if terminal && stableObservations >= 2 {
			// Branch protection and active rules are configuration, not a CI
			// observation. Reusing their first receipt eliminates three REST
			// requests from every pending poll, but a pass must still be based on
			// a fresh authority receipt in case policy changed during the slice.
			requiredChecks, authority, freshnessAuthority, authorityReason = requiredChecksReceipt(sliceCtx, options, nil)
			if authorityReason != "" {
				if sliceCtx.Err() == context.DeadlineExceeded && strings.TrimSpace(result.Reason) != "" {
					return pendingCommitWaitResult(result), nil
				}
				result.Reason = "required-check authority is unavailable; terminal CI evidence is incomplete: " + authorityReason
				return pendingCommitWaitResult(result), nil
			}
			result.RequiredChecks = requiredChecks
			result.RequiredChecksAuthority = authority
			result.TargetFreshnessAuthority = freshnessAuthority
			if options.PullRequest != "" && freshnessAuthority == "" {
				return failedCommitWaitResult(result, "target policy has no nonempty server-enforced strict up-to-date fence; check observations cannot authorize an automatic merge"), nil
			}
			if missingRequired = missingRequiredChecks(checks, requiredChecks); len(missingRequired) > 0 {
				result.Reason = "required GitHub checks have not registered for the exact head: " + strings.Join(missingRequired, ", ")
				return pendingCommitWaitResult(result), nil
			}
			// A final identity receipt closes the race between the stable check
			// observation and the reported terminal pass.
			if options.PullRequest != "" {
				observedHead, observedTarget, reason := pullRequestIdentity(sliceCtx, options.Repository, options.PullRequest)
				result.ObservedHead = observedHead
				if reason != "" {
					if sliceCtx.Err() == context.DeadlineExceeded {
						return pendingCommitWaitResult(result), nil
					}
					return failedCommitWaitResult(result, reason), nil
				}
				if observedHead != options.Head || observedTarget != options.Target {
					return failedCommitWaitResult(result, "pull request identity changed after checks passed; start a new exact wait"), nil
				}
				finalTargetHead, targetReason := targetHead(sliceCtx, options.Repository, options.Target)
				if targetReason != "" {
					if sliceCtx.Err() == context.DeadlineExceeded {
						return pendingCommitWaitResult(result), nil
					}
					return failedCommitWaitResult(result, "re-read exact pull-request target head: "+targetReason), nil
				}
				if finalTargetHead != observedTargetHead {
					return failedCommitWaitResult(result, fmt.Sprintf("target %s advanced after checks passed from %s to %s; rebase or reintegrate before merging", options.Target, observedTargetHead, finalTargetHead)), nil
				}
			} else {
				observedHead, reason := targetHead(sliceCtx, options.Repository, options.Target)
				result.ObservedHead = observedHead
				if reason != "" {
					if sliceCtx.Err() == context.DeadlineExceeded {
						return pendingCommitWaitResult(result), nil
					}
					return failedCommitWaitResult(result, reason), nil
				}
				if observedHead != options.Head {
					return failedCommitWaitResult(result, "target advanced after checks passed; start a new exact wait"), nil
				}
			}
			result.Status = PullRequestWaitPassed
			if options.PullRequest != "" {
				result.Reason = "GitHub's required-check policy was enumerated, every required check was present, the candidate contained the exact target, server-side target freshness was enforced, and the observed GitHub check set stayed terminal across a bounded stable reread"
			} else {
				result.Reason = "GitHub's required-check policy was enumerated (possibly empty), every required check was present, and the exact remote target's observed check set stayed terminal across a bounded stable reread"
			}
			return result, nil
		}
		if terminal {
			result.Reason = "terminal checks require one unchanged foreground reread before they form a bounded CI receipt"
		}
		if !time.Now().Add(options.CheckPollInterval).Before(deadline) {
			return pendingCommitWaitResult(result), nil
		}
		timer := time.NewTimer(options.CheckPollInterval)
		select {
		case <-sliceCtx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if sliceCtx.Err() == context.DeadlineExceeded {
				return pendingCommitWaitResult(result), nil
			}
			return failedCommitWaitResult(result, sliceCtx.Err().Error()), nil
		case <-timer.C:
		}
	}
}

// WaitForPullRequestChecks retains the original internal seam for existing
// orchestrated PR flows while using the exact-commit waiter above.
func WaitForPullRequestChecks(ctx context.Context, options PullRequestWaitOptions) (PullRequestWaitResult, error) {
	if strings.TrimSpace(options.PullRequest) == "" {
		return PullRequestWaitResult{}, fmt.Errorf("pull request is required")
	}
	return WaitForCommitChecks(ctx, options)
}

func pendingCommitWaitResult(result PullRequestWaitResult) PullRequestWaitResult {
	result.Status = PullRequestWaitPending
	if strings.TrimSpace(result.Reason) == "" {
		result.Reason = "observed GitHub checks are still pending"
	}
	result.Reason += "; resume the same exact target identity in another foreground slice"
	return result
}

func failedCommitWaitResult(result PullRequestWaitResult, reason string) PullRequestWaitResult {
	result.Status = PullRequestWaitFailed
	result.Reason = reason
	return result
}

func commitChecks(ctx context.Context, options PullRequestWaitOptions) ([]RemoteCheck, bool, string) {
	checks := make([]RemoteCheck, 0)
	pending := false
	if options.PullRequest != "" {
		output, _, commandErr := runCommand(ctx, 0, 0, "", "gh", "pr", "checks", options.PullRequest, "--repo", options.Repository, "--json", "name,bucket,link")
		prChecks, prPending, err := decodePullRequestChecks(options.PullRequest, output, commandErr)
		if err != nil {
			return nil, false, err.Error()
		}
		checks = append(checks, prChecks...)
		pending = prPending
	}
	runChecks, runPending, reason := commitCheckRuns(ctx, options)
	if reason != "" {
		return nil, false, reason
	}
	statusChecks, statusPending, reason := commitStatuses(ctx, options)
	if reason != "" {
		return nil, false, reason
	}
	checks = append(checks, runChecks...)
	checks = append(checks, statusChecks...)
	pending = pending || runPending || statusPending || len(checks) == 0
	sortRemoteChecks(checks)
	return checks, pending, ""
}

func sortRemoteChecks(checks []RemoteCheck) {
	sort.Slice(checks, func(i, j int) bool {
		if checks[i].Name == checks[j].Name {
			if checks[i].Bucket == checks[j].Bucket {
				if checks[i].Link == checks[j].Link {
					return checks[i].AppID < checks[j].AppID
				}
				return checks[i].Link < checks[j].Link
			}
			return checks[i].Bucket < checks[j].Bucket
		}
		return checks[i].Name < checks[j].Name
	})
}

func terminalChecksFingerprint(checks []RemoteCheck, required []RequiredRemoteCheck, authority, targetHead, freshnessAuthority string) string {
	var builder strings.Builder
	builder.WriteString(authority)
	builder.WriteString("\ntarget:")
	builder.WriteString(targetHead)
	builder.WriteString("\nfreshness:")
	builder.WriteString(freshnessAuthority)
	for _, expectation := range required {
		builder.WriteByte('\n')
		builder.WriteString("required:")
		builder.WriteString(expectation.Name)
		builder.WriteByte('@')
		fmt.Fprintf(&builder, "%d", expectation.IntegrationID)
	}
	for _, check := range checks {
		builder.WriteByte('\n')
		builder.WriteString(check.Name)
		builder.WriteByte('\x00')
		builder.WriteString(check.Bucket)
		builder.WriteByte('\x00')
		builder.WriteString(check.Link)
		builder.WriteByte('\x00')
		fmt.Fprintf(&builder, "%d", check.AppID)
	}
	return builder.String()
}

func missingRequiredChecks(checks []RemoteCheck, required []RequiredRemoteCheck) []string {
	observed := make(map[string][]RemoteCheck, len(checks))
	for _, check := range checks {
		name := strings.TrimSpace(check.Name)
		name = strings.TrimPrefix(name, "check-run:")
		name = strings.TrimPrefix(name, "status:")
		if name != "" {
			observed[name] = append(observed[name], check)
		}
	}
	missing := make([]string, 0)
	for _, expectation := range required {
		matched := false
		for _, check := range observed[expectation.Name] {
			if expectation.IntegrationID == 0 || check.AppID == expectation.IntegrationID {
				matched = true
				break
			}
		}
		if !matched {
			label := expectation.Name
			if expectation.IntegrationID != 0 {
				label += fmt.Sprintf(" (GitHub App %d)", expectation.IntegrationID)
			}
			missing = append(missing, label)
		}
	}
	return missing
}

// requiredChecksReceipt combines classic branch protection, every active
// repository/organization ruleset GitHub says applies to the target, and the
// PR's effective required-check view when one exists. A terminal result is not
// allowed when any authority cannot be read: an observed green snapshot can
// otherwise precede registration of a required workflow on the same SHA.
type requiredChecksCache struct {
	valid              bool
	branchChecks       []RequiredRemoteCheck
	freshnessAuthority string
}

func requiredChecksReceipt(ctx context.Context, options PullRequestWaitOptions, cache *requiredChecksCache) ([]RequiredRemoteCheck, string, string, string) {
	required := map[string]RequiredRemoteCheck{}
	branchChecks := []RequiredRemoteCheck(nil)
	freshnessAuthority := ""
	if cache != nil && cache.valid {
		branchChecks = cache.branchChecks
		freshnessAuthority = cache.freshnessAuthority
	} else {
		var reason string
		branchChecks, freshnessAuthority, reason = targetBranchRequiredChecks(ctx, options.Repository, options.Target, options.PullRequest != "")
		if reason != "" {
			return nil, "", "", reason
		}
		if cache != nil {
			cache.valid = true
			cache.branchChecks = branchChecks
			cache.freshnessAuthority = freshnessAuthority
		}
	}
	for _, expectation := range branchChecks {
		addRequiredExpectation(required, expectation)
	}
	authority := "github-branch-protection+active-branch-rules"
	if options.PullRequest != "" {
		prChecks, prReason := pullRequestRequiredChecks(ctx, options.Repository, options.PullRequest)
		if prReason != "" {
			return nil, "", "", prReason
		}
		for _, expectation := range prChecks {
			addRequiredExpectation(required, expectation)
		}
		authority += "+pr-required-checks"
	}
	checks := make([]RequiredRemoteCheck, 0, len(required))
	for _, expectation := range required {
		checks = append(checks, expectation)
	}
	sortRequiredChecks(checks)
	return checks, authority, freshnessAuthority, ""
}

func pullRequestRequiredChecks(ctx context.Context, repository, pullRequest string) ([]RequiredRemoteCheck, string) {
	output, _, commandErr := runCommand(
		ctx, 0, 0, "", "gh", "pr", "checks", pullRequest,
		"--repo", repository, "--required", "--json", "name,bucket,link",
	)
	checks, _, err := decodePullRequestChecks(pullRequest, output, commandErr)
	if err != nil {
		return nil, fmt.Sprintf("read effective required checks for pull request %s: %v", pullRequest, err)
	}
	required := make([]RequiredRemoteCheck, 0, len(checks))
	seen := map[string]bool{}
	for _, check := range checks {
		name := strings.TrimSpace(check.Name)
		if name == "" {
			return nil, fmt.Sprintf("pull request %s returned a required check with no name", pullRequest)
		}
		if !seen[name] {
			seen[name] = true
			required = append(required, RequiredRemoteCheck{Name: name})
		}
	}
	sortRequiredChecks(required)
	return required, ""
}

type githubBranchPolicyView struct {
	Protected  *bool `json:"protected"`
	Protection struct {
		RequiredStatusChecks *struct {
			Contexts []string `json:"contexts"`
			Checks   []struct {
				Context string `json:"context"`
				AppID   int64  `json:"app_id"`
			} `json:"checks"`
		} `json:"required_status_checks"`
	} `json:"protection"`
}

type githubRequiredStatusChecksPolicy struct {
	Strict   *bool    `json:"strict"`
	Contexts []string `json:"contexts"`
	Checks   []struct {
		Context string `json:"context"`
		AppID   int64  `json:"app_id"`
	} `json:"checks"`
}

type githubActiveBranchRule struct {
	Type              string `json:"type"`
	RulesetSourceType string `json:"ruleset_source_type"`
	RulesetSource     string `json:"ruleset_source"`
	RulesetID         int64  `json:"ruleset_id"`
	Parameters        struct {
		StrictRequiredStatusChecksPolicy *bool `json:"strict_required_status_checks_policy"`
		RequiredStatusChecks             []struct {
			Context       string `json:"context"`
			IntegrationID int64  `json:"integration_id"`
		} `json:"required_status_checks"`
	} `json:"parameters"`
}

func targetBranchRequiredChecks(ctx context.Context, repository, target string, requireServerFreshness bool) ([]RequiredRemoteCheck, string, string) {
	branchEndpoint := "repos/" + repository + "/branches/" + url.PathEscape(target)
	branchOutput, _, branchErr := runCommand(ctx, 0, 0, "", "gh", "api", branchEndpoint)
	if branchErr != nil {
		return nil, "", fmt.Sprintf("read target branch protection for %s: %v", target, branchErr)
	}
	var branch githubBranchPolicyView
	if err := json.Unmarshal([]byte(branchOutput), &branch); err != nil {
		return nil, "", fmt.Sprintf("decode target branch protection for %s: %v", target, err)
	}
	if branch.Protected == nil {
		return nil, "", fmt.Sprintf("target branch protection for %s omitted the protected policy receipt", target)
	}
	required := map[string]RequiredRemoteCheck{}
	freshnessAuthorities := make([]string, 0)
	if branch.Protection.RequiredStatusChecks != nil {
		contexts := branch.Protection.RequiredStatusChecks.Contexts
		checks := branch.Protection.RequiredStatusChecks.Checks
		classicStrict := false
		if requireServerFreshness {
			detailOutput, _, detailErr := runCommand(ctx, 0, 0, "", "gh", "api", branchEndpoint+"/protection/required_status_checks")
			if detailErr != nil {
				if len(contexts) != 0 || len(checks) != 0 || !strings.Contains(strings.ToLower(detailErr.Error()), "http 404") {
					return nil, "", fmt.Sprintf("read authoritative required-status-check policy for %s: %v", target, detailErr)
				}
			} else {
				var detail githubRequiredStatusChecksPolicy
				if err := json.Unmarshal([]byte(detailOutput), &detail); err != nil {
					return nil, "", fmt.Sprintf("decode authoritative required-status-check policy for %s: %v", target, err)
				}
				if detail.Strict == nil {
					return nil, "", fmt.Sprintf("authoritative required-status-check policy for %s omitted strict", target)
				}
				classicStrict = *detail.Strict
				contexts = detail.Contexts
				checks = detail.Checks
			}
		}
		for _, name := range contexts {
			if reason := addRequiredCheck(required, name, 0); reason != "" {
				return nil, "", "target branch protection " + reason
			}
		}
		for _, check := range checks {
			if reason := addRequiredCheck(required, check.Context, check.AppID); reason != "" {
				return nil, "", "target branch protection " + reason
			}
		}
		if classicStrict && len(contexts)+len(checks) > 0 {
			freshnessAuthorities = append(freshnessAuthorities, "classic strict required-status-check policy")
		}
	}

	rulesEndpoint := "repos/" + repository + "/rules/branches/" + url.PathEscape(target) + "?per_page=100"
	rulesOutput, _, rulesErr := runCommand(ctx, 0, 0, "", "gh", "api", "--paginate", "--slurp", rulesEndpoint)
	if rulesErr != nil {
		return nil, "", fmt.Sprintf("read active branch rules for target %s: %v", target, rulesErr)
	}
	var pages [][]githubActiveBranchRule
	if err := json.Unmarshal([]byte(rulesOutput), &pages); err != nil {
		return nil, "", fmt.Sprintf("decode paginated active branch rules for target %s: %v", target, err)
	}
	if len(pages) == 0 {
		return nil, "", fmt.Sprintf("paginated active branch rules for target %s returned no page receipt", target)
	}
	for _, page := range pages {
		for _, rule := range page {
			ruleType := strings.TrimSpace(rule.Type)
			if ruleType == "" {
				return nil, "", fmt.Sprintf("active branch rule for target %s omitted its type", target)
			}
			source := fmt.Sprintf("ruleset %d (%s %s)", rule.RulesetID, rule.RulesetSourceType, rule.RulesetSource)
			switch {
			case ruleType == "required_status_checks":
				for _, check := range rule.Parameters.RequiredStatusChecks {
					if reason := addRequiredCheck(required, check.Context, check.IntegrationID); reason != "" {
						return nil, "", source + " " + reason
					}
				}
				if rule.Parameters.StrictRequiredStatusChecksPolicy != nil && *rule.Parameters.StrictRequiredStatusChecksPolicy && len(rule.Parameters.RequiredStatusChecks) > 0 {
					freshnessAuthorities = append(freshnessAuthorities, fmt.Sprintf("strict required-status-check ruleset %d", rule.RulesetID))
				}
			case ruleType == "merge_queue":
				if requireServerFreshness {
					return nil, "", fmt.Sprintf("%s requires merge-group check observation, which this source-head waiter does not yet implement", source)
				}
			case strings.Contains(ruleType, "workflow"):
				return nil, "", fmt.Sprintf("%s contains %s, whose expected check names GitHub does not expose in this receipt", source, ruleType)
			}
		}
	}

	checks := make([]RequiredRemoteCheck, 0, len(required))
	for _, expectation := range required {
		checks = append(checks, expectation)
	}
	sortRequiredChecks(checks)
	sort.Strings(freshnessAuthorities)
	return checks, strings.Join(freshnessAuthorities, "+"), ""
}

func addRequiredCheck(required map[string]RequiredRemoteCheck, value string, integrationID int64) string {
	name := strings.TrimSpace(value)
	if name == "" {
		return "contains a required status check with no context"
	}
	addRequiredExpectation(required, RequiredRemoteCheck{Name: name, IntegrationID: integrationID})
	return ""
}

func addRequiredExpectation(required map[string]RequiredRemoteCheck, expectation RequiredRemoteCheck) {
	key := expectation.Name
	if expectation.IntegrationID != 0 {
		key += fmt.Sprintf("\x00%d", expectation.IntegrationID)
		// A producer-pinned rule is stronger than an unpinned duplicate from
		// the branch summary or PR effective view.
		delete(required, expectation.Name)
	} else {
		for _, existing := range required {
			if existing.Name == expectation.Name && existing.IntegrationID != 0 {
				return
			}
		}
	}
	required[key] = expectation
}

func sortRequiredChecks(checks []RequiredRemoteCheck) {
	sort.Slice(checks, func(i, j int) bool {
		if checks[i].Name == checks[j].Name {
			return checks[i].IntegrationID < checks[j].IntegrationID
		}
		return checks[i].Name < checks[j].Name
	})
}

func commitCheckRuns(ctx context.Context, options PullRequestWaitOptions) ([]RemoteCheck, bool, string) {
	output, _, err := runCommand(ctx, 0, 0, "", "gh", "api", "repos/"+options.Repository+"/commits/"+options.Head+"/check-runs?per_page=100")
	if err != nil {
		return nil, false, err.Error()
	}
	var response githubCheckRunsResponse
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		return nil, false, fmt.Sprintf("decode GitHub check runs for %s: %v", options.Head, err)
	}
	if response.TotalCount > len(response.CheckRuns) {
		return nil, false, fmt.Sprintf("GitHub returned only %d of %d observed check runs for %s; refusing an incomplete CI receipt", len(response.CheckRuns), response.TotalCount, options.Head)
	}
	checks := make([]RemoteCheck, 0, len(response.CheckRuns))
	pending := false
	for _, check := range response.CheckRuns {
		bucket := checkRunBucket(check.Status, check.Conclusion)
		checks = append(checks, RemoteCheck{Name: "check-run:" + check.Name, Bucket: bucket, Link: check.HTMLURL, AppID: check.App.ID})
		if bucket != "pass" && bucket != "skipping" && bucket != "fail" && bucket != "cancel" {
			pending = true
		}
	}
	return checks, pending, ""
}

func commitStatuses(ctx context.Context, options PullRequestWaitOptions) ([]RemoteCheck, bool, string) {
	output, _, err := runCommand(ctx, 0, 0, "", "gh", "api", "repos/"+options.Repository+"/commits/"+options.Head+"/status?per_page=100")
	if err != nil {
		return nil, false, err.Error()
	}
	var response githubCommitStatusResponse
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		return nil, false, fmt.Sprintf("decode GitHub commit statuses for %s: %v", options.Head, err)
	}
	if response.TotalCount > len(response.Statuses) {
		return nil, false, fmt.Sprintf("GitHub returned only %d of %d commit statuses for %s; refusing an incomplete CI receipt", len(response.Statuses), response.TotalCount, options.Head)
	}
	// GitHub returns the latest status first. Retain one exact receipt per
	// status context; an older duplicate must not overrule its replacement.
	seen := make(map[string]bool, len(response.Statuses))
	checks := make([]RemoteCheck, 0, len(response.Statuses))
	pending := false
	for _, status := range response.Statuses {
		name := "status:" + strings.TrimSpace(status.Context)
		if name == "status:" {
			return nil, false, "GitHub commit status has no context"
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		bucket := commitStatusBucket(status.State)
		checks = append(checks, RemoteCheck{Name: name, Bucket: bucket, Link: status.TargetURL})
		if bucket == "pending" {
			pending = true
		}
	}
	return checks, pending, ""
}

type pullRequestIdentityView struct {
	HeadRefOID  string `json:"headRefOid"`
	BaseRefName string `json:"baseRefName"`
}

func pullRequestIdentity(ctx context.Context, repository, pullRequest string) (string, string, string) {
	output, _, err := runCommand(ctx, 0, 0, "", "gh", "pr", "view", pullRequest, "--repo", repository, "--json", "headRefOid,baseRefName")
	if err != nil {
		return "", "", err.Error()
	}
	var view pullRequestIdentityView
	if err := json.Unmarshal([]byte(output), &view); err != nil || strings.TrimSpace(view.HeadRefOID) == "" || strings.TrimSpace(view.BaseRefName) == "" {
		if err != nil {
			return "", "", fmt.Sprintf("decode pull request identity: %v", err)
		}
		return "", "", "GitHub pull request view returned no exact head or target"
	}
	return strings.TrimSpace(view.HeadRefOID), strings.TrimSpace(view.BaseRefName), ""
}

type githubReference struct {
	Object struct {
		SHA string `json:"sha"`
	} `json:"object"`
}

func targetHead(ctx context.Context, repository, target string) (string, string) {
	output, _, err := runCommand(ctx, 0, 0, "", "gh", "api", "repos/"+repository+"/git/ref/heads/"+target)
	if err != nil {
		return "", err.Error()
	}
	var reference githubReference
	if err := json.Unmarshal([]byte(output), &reference); err != nil || strings.TrimSpace(reference.Object.SHA) == "" {
		if err != nil {
			return "", fmt.Sprintf("decode target ref %s: %v", target, err)
		}
		return "", "GitHub target ref returned no SHA"
	}
	return strings.TrimSpace(reference.Object.SHA), ""
}

type githubCompareView struct {
	Status     string `json:"status"`
	BaseCommit struct {
		SHA string `json:"sha"`
	} `json:"base_commit"`
	MergeBaseCommit struct {
		SHA string `json:"sha"`
	} `json:"merge_base_commit"`
}

func candidateContainsTarget(ctx context.Context, repository, target, candidate string) (bool, string) {
	endpoint := "repos/" + repository + "/compare/" + target + "..." + candidate
	output, _, err := runCommand(ctx, 0, 0, "", "gh", "api", endpoint)
	if err != nil {
		return false, fmt.Sprintf("prove candidate ancestry against target %s: %v", target, err)
	}
	var comparison githubCompareView
	if err := json.Unmarshal([]byte(output), &comparison); err != nil {
		return false, fmt.Sprintf("decode candidate ancestry against target %s: %v", target, err)
	}
	status := strings.TrimSpace(comparison.Status)
	base := strings.TrimSpace(comparison.BaseCommit.SHA)
	mergeBase := strings.TrimSpace(comparison.MergeBaseCommit.SHA)
	if base == "" || mergeBase == "" {
		return false, fmt.Sprintf("GitHub comparison omitted base or merge-base SHA for target %s", target)
	}
	if base != target {
		return false, fmt.Sprintf("GitHub comparison returned base %s, want exact target %s", base, target)
	}
	switch status {
	case "ahead", "identical":
		if mergeBase != target {
			return false, fmt.Sprintf("GitHub comparison status %s did not use target %s as merge base (got %s)", status, target, mergeBase)
		}
		return true, ""
	case "behind", "diverged":
		return false, ""
	default:
		return false, fmt.Sprintf("GitHub comparison returned unsupported status %q for target %s and candidate %s", status, target, candidate)
	}
}

type githubCheckRunsResponse struct {
	TotalCount int              `json:"total_count"`
	CheckRuns  []githubCheckRun `json:"check_runs"`
}

type githubCommitStatusResponse struct {
	TotalCount int                  `json:"total_count"`
	Statuses   []githubCommitStatus `json:"statuses"`
}

type githubCommitStatus struct {
	Context   string `json:"context"`
	State     string `json:"state"`
	TargetURL string `json:"target_url"`
}

type githubCheckRun struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
	App        struct {
		ID int64 `json:"id"`
	} `json:"app"`
}

func checkRunBucket(status, conclusion string) string {
	if status != "completed" {
		return "pending"
	}
	switch conclusion {
	case "success", "neutral":
		return "pass"
	case "skipped":
		return "skipping"
	case "cancelled", "timed_out", "action_required":
		return "cancel"
	case "failure", "startup_failure", "stale":
		return "fail"
	default:
		return "pending"
	}
}

func commitStatusBucket(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "success":
		return "pass"
	case "pending":
		return "pending"
	case "failure", "error":
		return "fail"
	default:
		return "pending"
	}
}
