package orchestrate

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/progress"
)

// Landing refusals specific to bringing a candidate up to date.
const (
	// LandRefusalUpdateConflict is a candidate whose update against the target
	// conflicts. Resolving someone else's conflict is a judgement WB does not
	// make on their behalf.
	LandRefusalUpdateConflict = "update-branch-conflict"
)

// updateBranchSettleTimeout bounds the wait for GitHub to publish the updated
// head. The update is asynchronous: the API accepts it and the new commit
// appears shortly after, so a caller that read the head immediately would
// observe the pre-update SHA and wait for checks on a head that is about to be
// replaced.
const updateBranchSettleTimeout = 90 * time.Second

// updateBranchSettlePoll is deliberately short. This is one local API read
// against a head that is already changing, not a CI observation.
const updateBranchSettlePoll = 3 * time.Second

// candidateIsBehindTarget reports whether the pull request head does not
// contain the target's current head, which is what a strict up-to-date policy
// refuses to merge.
//
// The reason string is returned rather than an error to match the convention
// every other read in this package uses, so a transient failure stays
// resumable instead of ending the landing.
func candidateIsBehindTarget(ctx context.Context, repository, target, head string) (bool, string) {
	targetSHA, reason := targetHead(ctx, repository, target)
	if reason != "" {
		return false, reason
	}
	contains, reason := candidateContainsTarget(ctx, repository, targetSHA, head)
	if reason != "" {
		return false, reason
	}
	return !contains, ""
}

// updatePullRequestBranch asks GitHub to merge the base into the pull request
// branch, server side, and returns the new head once it is observable.
//
// It exists so a landing does not have to refuse a candidate merely for being
// behind. The evidence contract is unchanged by it: the caller re-reads the
// pull request afterwards and waits for checks on the NEW head, so the checks
// that authorize the merge are always the checks for the commit being merged.
func updatePullRequestBranch(ctx context.Context, repository, number, expectedHead string, reporter progress.Reporter) (string, string) {
	endpoint := "repos/" + repository + "/pulls/" + number + "/update-branch"
	// expected_head_sha makes this a compare-and-swap: if the head moved since
	// it was read, GitHub refuses rather than updating a branch this caller
	// never observed.
	response := githubExecute(ctx, "", "api", "--method", "PUT", endpoint,
		"-f", "expected_head_sha="+expectedHead)
	if response.ExitCode != 0 {
		message := strings.TrimSpace(string(response.Stderr))
		if message == "" {
			message = strings.TrimSpace(string(response.Stdout))
		}
		return "", fmt.Sprintf("update pull request branch: %s", message)
	}
	return waitForUpdatedHead(ctx, repository, number, expectedHead, reporter)
}

// waitForUpdatedHead polls until the pull request reports a head other than the
// one that was updated. GitHub accepts the update asynchronously, so reading
// the head immediately would return the commit that is about to be replaced.
func waitForUpdatedHead(ctx context.Context, repository, number, previousHead string, reporter progress.Reporter) (string, string) {
	deadline := time.Now().Add(updateBranchSettleTimeout)
	for {
		view, err := ReadPullRequest(ctx, repository, number)
		if err == nil && !strings.EqualFold(view.Head.SHA, previousHead) && strings.TrimSpace(view.Head.SHA) != "" {
			return view.Head.SHA, ""
		}
		if time.Now().After(deadline) {
			return "", fmt.Sprintf("updated head for %s#%s did not appear within %s", repository, number, updateBranchSettleTimeout)
		}
		reportPullRequestLandProgress(reporter, "update_branch", progress.Waiting, "waiting for the updated head", 0, 0)
		select {
		case <-ctx.Done():
			return "", ctx.Err().Error()
		case <-time.After(updateBranchSettlePoll):
		}
	}
}

// updateBranchConflict reports whether a failed update was a merge conflict
// rather than a transient or permission failure. A conflict is the caller's to
// resolve; everything else is WB's to report as it found it.
func updateBranchConflict(reason string) bool {
	lowered := strings.ToLower(reason)
	return strings.Contains(lowered, "merge conflict") ||
		strings.Contains(lowered, "not mergeable") ||
		strings.Contains(lowered, "conflict")
}

// waitDeadline is the wall-clock end of this landing's total check-wait budget.
// It is computed once so that updating a candidate mid-wait spends the same
// budget rather than restarting it: a target that keeps advancing must not be
// able to extend one landing indefinitely.
func waitDeadline(options PullRequestLandOptions) time.Time {
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	budget := options.Slice
	if budget <= 0 {
		budget = MaxForegroundCheckWaitSlice
	}
	return now().Add(budget)
}
