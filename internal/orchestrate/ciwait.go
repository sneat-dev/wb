package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// MaxForegroundCheckWaitSlice keeps a single agent-tool call under the common
// ten-minute harness ceiling. Longer CI is observed by explicit re-invocation,
// never a detached worker or a hidden thirty-minute loop.
const MaxForegroundCheckWaitSlice = 9 * time.Minute

// WaitForCommitChecks observes checks for one exact target commit. PullRequest
// is optional: when present it corroborates that exact PR head and target;
// without it WB observes the target branch's exact direct-push commit through
// the GitHub check-runs API. Pending is an intermediate terminal result that
// callers resume with the same identity, not successful completion.
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
	result := PullRequestWaitResult{
		Repository:  options.Repository,
		PullRequest: options.PullRequest,
		Target:      options.Target,
		Head:        options.Head,
	}
	deadline := time.Now().Add(options.Slice)
	sliceCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	for {
		if err := sliceCtx.Err(); err != nil {
			if err == context.DeadlineExceeded {
				return pendingCommitWaitResult(result), nil
			}
			return failedCommitWaitResult(result, err.Error()), nil
		}
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
		if len(checks) > 0 && !pending {
			// One final receipt closes the race between check observation and the
			// reported terminal pass.
			if options.PullRequest != "" {
				observedHead, observedTarget, reason := pullRequestIdentity(sliceCtx, options.Repository, options.PullRequest)
				result.ObservedHead = observedHead
				if reason != "" {
					return failedCommitWaitResult(result, reason), nil
				}
				if observedHead != options.Head || observedTarget != options.Target {
					return failedCommitWaitResult(result, "pull request identity changed after checks passed; start a new exact wait"), nil
				}
			} else {
				observedHead, reason := targetHead(sliceCtx, options.Repository, options.Target)
				result.ObservedHead = observedHead
				if reason != "" {
					return failedCommitWaitResult(result, reason), nil
				}
				if observedHead != options.Head {
					return failedCommitWaitResult(result, "target advanced after checks passed; start a new exact wait"), nil
				}
			}
			result.Status = PullRequestWaitPassed
			result.Reason = "all observed GitHub checks passed or were skipped for the exact target identity"
			return result, nil
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
	result.Reason = "observed GitHub checks are still pending; resume the same exact target identity in another foreground slice"
	return result
}

func failedCommitWaitResult(result PullRequestWaitResult, reason string) PullRequestWaitResult {
	result.Status = PullRequestWaitFailed
	result.Reason = reason
	return result
}

func commitChecks(ctx context.Context, options PullRequestWaitOptions) ([]RemoteCheck, bool, string) {
	if options.PullRequest != "" {
		output, _, commandErr := runCommand(ctx, 0, 0, "", "gh", "pr", "checks", options.PullRequest, "--repo", options.Repository, "--json", "name,bucket,link")
		checks, pending, err := decodePullRequestChecks(options.PullRequest, output, commandErr)
		if err != nil {
			return nil, false, err.Error()
		}
		return checks, pending, ""
	}
	runChecks, runPending, reason := commitCheckRuns(ctx, options)
	if reason != "" {
		return nil, false, reason
	}
	statusChecks, statusPending, reason := commitStatuses(ctx, options)
	if reason != "" {
		return nil, false, reason
	}
	checks := append(runChecks, statusChecks...)
	pending := runPending || statusPending || len(checks) == 0
	sort.Slice(checks, func(i, j int) bool {
		if checks[i].Name == checks[j].Name {
			return checks[i].Link < checks[j].Link
		}
		return checks[i].Name < checks[j].Name
	})
	return checks, pending, ""
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
		checks = append(checks, RemoteCheck{Name: "check-run:" + check.Name, Bucket: bucket, Link: check.HTMLURL})
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
