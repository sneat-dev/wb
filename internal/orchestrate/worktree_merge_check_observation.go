package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/githubchecks"

	"github.com/sneat-dev/wb/internal/progress"
)

// checkWaitSlice is the budget of one exact-head check wait: WaitSlice when
// set, otherwise Timeout, capped at 8 minutes either way.
func (options WorktreeMergeLandOptions) checkWaitSlice() time.Duration {
	slice := options.Timeout
	if options.WaitSlice > 0 {
		slice = options.WaitSlice
	}
	if slice <= 0 || slice > 8*time.Minute {
		slice = 8 * time.Minute
	}
	return slice
}

func waitForWorktreeMergeChecks(ctx context.Context, receipt WorktreeMergeReceipt, options WorktreeMergeLandOptions, pullRequest, head string, allowTargetDescendant bool) (githubchecks.PullRequestWaitResult, error) {
	slice := options.checkWaitSlice()
	interval := options.CheckPollInterval
	if interval <= 0 {
		interval = githubchecks.DefaultCheckPollInterval
	}
	if interval >= slice {
		return githubchecks.PullRequestWaitResult{}, fmt.Errorf("CI poll interval %s must be shorter than wait slice %s", interval, slice)
	}
	// One slice can still run several minutes of CI observation: keep the
	// lane's heartbeat fresh throughout so it never goes stale out from under
	// this still-live session. See startLandingLaneHeartbeat. A no-op when
	// options.Lane was never populated (no guard running for this call).
	stopLaneHeartbeat := startLandingLaneHeartbeat(options.ProjectsRoot, receipt.Repository, receipt.Target, options.Lane.Owner.WBSessionID, 0)
	waitOptions := githubchecks.PullRequestWaitOptions{
		Repository: receipt.Repository, PullRequest: pullRequest, Target: receipt.Target, Head: head, AllowTargetDescendant: allowTargetDescendant,
		// --allow-unfenced is durable because a private repository can expose
		// neither PR nor post-target branch-policy authority. Both phases still
		// require stable exact-head check observations.
		AllowUnfenced: options.AllowUnfenced,
		Slice:         slice, CheckPollInterval: interval, Progress: reportWorktreeMergeCheckProgress(options.Progress, worktreeMergeCheckPhase(pullRequest)),
		OperationProgress: options.Progress,
	}
	var directCI *worktreeMergeDirectCIContract
	if deferral := receipt.ValidationDeferral; pullRequest == "" && deferral != nil && deferral.Route == WorktreeMergeRouteDirect {
		if deferral.CandidateSHA != head || deferral.DirectCIPullRequest == "" || deferral.DirectCIPullRequestNumber <= 0 || deferral.DirectCIBase == "" || deferral.DirectCIWorkflowID <= 0 {
			stopLaneHeartbeat()
			return githubchecks.PullRequestWaitResult{Status: githubchecks.PullRequestWaitFailed}, fmt.Errorf("direct CI deferral is not pinned to exact landed head %s", head)
		}
		directCI = &worktreeMergeDirectCIContract{PullRequest: deferral.DirectCIPullRequest, PullRequestNumber: deferral.DirectCIPullRequestNumber, Base: deferral.DirectCIBase, WorkflowID: deferral.DirectCIWorkflowID}
		waitOptions.AllowTargetDescendant = false
		waitOptions.ExpectedActionChecks = &githubchecks.ExpectedActionChecks{WorkflowID: directCI.WorkflowID, Event: "pull_request", PullRequestNumber: directCI.PullRequestNumber, PullRequestBase: directCI.Base, Names: directCIGoChecks}
	}
	result, err := githubchecks.WaitForCommitChecks(ctx, waitOptions)
	stopLaneHeartbeat()
	if err != nil {
		return result, err
	}
	if result.Status == githubchecks.PullRequestWaitPassed && directCI != nil {
		if err := verifyWorktreeMergeDirectCIPullRequest(ctx, receipt, *directCI, head); err != nil {
			result.Status = githubchecks.PullRequestWaitFailed
			result.Reason = "direct CI pull request identity changed: " + err.Error()
			return result, fmt.Errorf("%s", result.Reason)
		}
	}
	return result, worktreeMergeCheckResultError(receipt.ReceiptPath, options.AllowUnfenced, result)
}

func worktreeMergeCheckPhase(pullRequest string) string {
	if pullRequest != "" {
		return "candidate_checks"
	}
	return "target_checks"
}

func reportWorktreeMergeCheckProgress(reporter progress.Reporter, phase string) func(githubchecks.PullRequestWaitProgress) {
	if reporter == nil {
		return nil
	}
	return func(event githubchecks.PullRequestWaitProgress) {
		passed, pending, failed := 0, 0, 0
		for _, check := range event.Result.Checks {
			switch check.Bucket {
			case "pass", "skipping":
				passed++
			case "fail", "cancel":
				failed++
			default:
				pending++
			}
		}
		var state progress.State
		switch event.Result.Status {
		case githubchecks.PullRequestWaitPending:
			state = progress.Waiting
		case githubchecks.PullRequestWaitPassed:
			state = progress.Completed
		case githubchecks.PullRequestWaitFailed:
			state = progress.Failed
		default:
			state = progress.Running
		}
		detail := fmt.Sprintf("poll %d: %d passed, %d pending, %d failed", event.Observation, passed, pending, failed)
		if event.Result.StableObservations > 0 {
			detail += fmt.Sprintf("; stable %d/2", event.Result.StableObservations)
		}
		if event.NextPoll > 0 {
			detail += "; next poll in " + event.NextPoll.String()
		} else if strings.TrimSpace(event.Result.Reason) != "" {
			detail += "; " + event.Result.Reason
		}
		progress.Report(reporter, progress.Event{Operation: "worktree_merge", Phase: phase, State: state, Detail: detail})
	}
}

// verifyPublishedWorktreeMergePullRequest proves the exact remote handoff
// identity before an intentional stop. An open pull request at the candidate
// SHA and target has one unambiguous remote diff; verifying just a local
// branch, or a PR number without its current head, is insufficient.
func verifyPublishedWorktreeMergePullRequest(ctx context.Context, receipt WorktreeMergeReceipt, options WorktreeMergeLandOptions) error {
	if receipt.PullRequest == "" {
		return errors.New("published handoff has no pull request")
	}
	remote, _, err := runCommand(ctx, options.resolveRunner(), options.Timeout, options.Retry, receipt.Candidate.Worktree,
		"git", "ls-remote", "--heads", "origin", "refs/heads/"+receipt.Candidate.Branch)
	if err != nil {
		return fmt.Errorf("read published candidate ref: %w", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(remote), receipt.Candidate.SHA+"\t") {
		return fmt.Errorf("published candidate ref %s does not match preserved candidate %s", receipt.Candidate.Branch, receipt.Candidate.SHA)
	}

	const maxHeadObservations = 4
	delay := 2 * time.Second
	if options.CheckPollInterval > 0 && options.CheckPollInterval < delay {
		delay = options.CheckPollInterval
	}
	for observation := 1; ; observation++ {
		viewOutput, readErr := githubRead(ctx, "", "pr", "view", receipt.PullRequest,
			"--repo", receipt.Repository, "--json", "state,headRefOid,baseRefName")
		if readErr != nil {
			return fmt.Errorf("read published pull-request identity: %w", readErr)
		}
		var view struct {
			State       string `json:"state"`
			HeadRefOID  string `json:"headRefOid"`
			BaseRefName string `json:"baseRefName"`
		}
		if unmarshalErr := json.Unmarshal([]byte(viewOutput), &view); unmarshalErr != nil {
			return fmt.Errorf("decode published pull-request identity: %w", unmarshalErr)
		}
		if view.State != "OPEN" {
			return fmt.Errorf("published pull request %s is %s, not open", receipt.PullRequest, view.State)
		}
		if view.BaseRefName != receipt.Target {
			return fmt.Errorf("published pull-request base %s does not match target %s", view.BaseRefName, receipt.Target)
		}
		if view.HeadRefOID == receipt.Candidate.SHA {
			return nil
		}
		stalePredecessor := receipt.PushGate != nil && receipt.PushGate.Status == "passed" &&
			receipt.PushGate.LocalSHA == receipt.Candidate.SHA && receipt.PushGate.PreviousRemoteSHA != "" &&
			receipt.PushGate.PreviousRemoteSHA != receipt.Candidate.SHA && view.HeadRefOID == receipt.PushGate.PreviousRemoteSHA
		if !stalePredecessor || observation == maxHeadObservations {
			return fmt.Errorf("published pull-request head %s does not match preserved candidate %s", view.HeadRefOID, receipt.Candidate.SHA)
		}
		reportWorktreeMergeProgress(options.Progress, "verify_pull_request_head", progress.Waiting,
			fmt.Sprintf("GitHub still reports published predecessor %s; observation %d/%d, retrying in %s",
				shortMergeRevision(view.HeadRefOID), observation, maxHeadObservations, delay))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("wait for published pull-request head %s: %w", receipt.Candidate.SHA, ctx.Err())
		case <-timer.C:
		}
	}
}

// worktreeMergeCheckResultError formats an already-observed result. It neither
// observes checks nor grants direct-CI identity authority.
func worktreeMergeCheckResultError(receiptPath string, allowUnfenced bool, result githubchecks.PullRequestWaitResult) error {
	switch result.Status {
	case githubchecks.PullRequestWaitPassed:
		return nil
	case githubchecks.PullRequestWaitPending:
		return fmt.Errorf("exact-head checks remain pending: %s; resume with wb worktree merge resume %s", result.Reason, receiptPath)
	default:
		if !allowUnfenced && strings.Contains(result.Reason, "strict up-to-date fence") {
			return fmt.Errorf("exact-head checks failed: %s; resume with wb worktree merge resume %s --allow-unfenced", result.Reason, receiptPath)
		}
		// #600: name each failing check and its first error line rather than
		// leaving the caller to hand-roll the same log scraping WB already
		// did while observing the checks.
		if summary := githubchecks.SummarizeFailures(result.FailureDetails); summary != "" {
			return fmt.Errorf("exact-head checks failed: %s; %s", result.Reason, summary)
		}
		return fmt.Errorf("exact-head checks failed: %s", result.Reason)
	}
}
